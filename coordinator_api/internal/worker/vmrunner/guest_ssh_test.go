//go:build !windows

package vmrunner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testSSHUser/testSSHPassword are obviously-fake test-only fixtures for an
// in-process SSH server that never leaves this process -- not real
// credentials for any system.
const (
	testSSHUser     = "reactorcide-test-user"
	testSSHPassword = "reactorcide-test-password-not-real"
)

// testSSHServer is a minimal, in-process sshd used to exercise SSHTransport
// without depending on a system sshd. It authenticates a single
// user/password pair and runs each "exec" request via the local shell,
// reporting exit-status/exit-signal like a real server would -- enough
// surface for SSHTransport's Start/Wait/Signal contract.
type testSSHServer struct {
	listener net.Listener
	config   *ssh.ServerConfig
	addr     GuestAddr
}

// startTestSSHServer binds a loopback listener and starts serving in the
// background. It skips the test (rather than failing) if this sandbox
// can't create a loopback listener, matching the task's "skip gracefully"
// requirement.
func startTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("build test host signer: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == testSSHUser && string(pass) == testSSHPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("test sshd: authentication rejected")
		},
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping SSH transport test: could not create a loopback listener: %v", err)
	}

	tcpAddr := ln.Addr().(*net.TCPAddr)
	s := &testSSHServer{
		listener: ln,
		config:   config,
		addr:     GuestAddr{Host: "127.0.0.1", Port: tcpAddr.Port},
	}

	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *testSSHServer) serve() {
	for {
		nConn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(nConn)
	}
}

func (s *testSSHServer) handleConn(nConn net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(nConn, s.config)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		go handleSessionChannel(newCh)
	}
}

func handleSessionChannel(newCh ssh.NewChannel) {
	ch, requests, err := newCh.Accept()
	if err != nil {
		return
	}

	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			runExecOverChannel(ch, payload.Command)
			return
		case "env":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// runExecOverChannel runs command via the local shell (a real sh -c, since
// the test server's whole point is to exercise SSHTransport's remote
// command construction -- pidfile capture, exports, exec-replace -- the
// same way a real guest's sshd would) and reports its outcome the way RFC
// 4254 expects: exit-status for a normal exit, exit-signal when the
// process was killed by a signal.
func runExecOverChannel(ch ssh.Channel, command string) {
	defer ch.Close()

	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()

	if err := cmd.Start(); err != nil {
		sendExitStatus(ch, 127)
		return
	}

	waitErr := cmd.Wait()
	_ = ch.CloseWrite()

	if waitErr == nil {
		sendExitStatus(ch, 0)
		return
	}

	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		sendExitStatus(ch, 1)
		return
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		sendExitSignal(ch, signalRFCName(ws.Signal()))
		return
	}
	sendExitStatus(ch, exitErr.ExitCode())
}

func sendExitStatus(ch ssh.Channel, code int) {
	type exitStatusMsg struct{ Status uint32 }
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&exitStatusMsg{Status: uint32(code)}))
}

func sendExitSignal(ch ssh.Channel, sig string) {
	type exitSignalMsg struct {
		Signal     string
		CoreDumped bool
		Error      string
		Lang       string
	}
	_, _ = ch.SendRequest("exit-signal", false, ssh.Marshal(&exitSignalMsg{Signal: sig}))
}

func signalRFCName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGINT:
		return "INT"
	default:
		return "TERM"
	}
}

func testCreds() GuestCreds {
	return GuestCreds{User: testSSHUser, Password: testSSHPassword}
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestSSHTransport_RunsCommandAndReportsExitCode(t *testing.T) {
	server := startTestSSHServer(t)
	transport := NewSSHTransport()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := transport.Start(ctx, server.addr, testCreds(), GuestCommand{
		Platform: GuestPlatformPOSIX,
		Args:     []string{"sh", "-c", "echo out-line; echo err-line >&2; exit 7"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	stdout := readAll(t, session.Stdout())
	stderr := readAll(t, session.Stderr())

	code, err := session.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if !strings.Contains(stdout, "out-line") {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, "out-line")
	}
	if !strings.Contains(stderr, "err-line") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, "err-line")
	}
}

func TestSSHTransport_InjectsEnv(t *testing.T) {
	server := startTestSSHServer(t)
	transport := NewSSHTransport()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	env := map[string]string{"REACTORCIDE_TEST_VAR": "test-value-not-a-secret"}
	session, err := transport.Start(ctx, server.addr, testCreds(), GuestCommand{
		Platform: GuestPlatformPOSIX,
		Args:     []string{"sh", "-c", "echo $REACTORCIDE_TEST_VAR"},
		Env:      env,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	stdout := readAll(t, session.Stdout())
	code, err := session.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != "test-value-not-a-secret" {
		t.Fatalf("stdout = %q, want the injected env value", stdout)
	}
}

func TestSSHTransport_CreatesSensitiveFilesAndUsesWorkingDirectory(t *testing.T) {
	server := startTestSSHServer(t)
	transport := NewSSHTransport()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workDir := t.TempDir()
	credentialPath := filepath.Join(workDir, "auth", "credentials")
	session, err := transport.Start(ctx, server.addr, testCreds(), GuestCommand{
		Platform:   GuestPlatformPOSIX,
		Args:       []string{"sh", "-c", "pwd; cat auth/credentials; stat -c %a auth/credentials"},
		WorkingDir: workDir,
		Files: []GuestFile{{
			Path: credentialPath,
			Data: []byte("test-credential-data"),
			Mode: 0o600,
		}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	stdout := readAll(t, session.Stdout())
	if code, err := session.Wait(); err != nil || code != 0 {
		t.Fatalf("Wait: code=%d err=%v stderr=%s", code, err, readAll(t, session.Stderr()))
	}
	if !strings.Contains(stdout, workDir) || !strings.Contains(stdout, "test-credential-data") || !strings.Contains(stdout, "600") {
		t.Fatalf("stdout = %q, want working directory, file data, and mode", stdout)
	}
}

func TestSSHTransport_StreamsSourceTree(t *testing.T) {
	server := startTestSSHServer(t)
	transport := NewSSHTransport()

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "source.txt"), []byte("source-tree-data"), 0o640); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "job", "src")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := transport.Start(ctx, server.addr, testCreds(), GuestCommand{
		Platform: GuestPlatformPOSIX,
		Args:     []string{"cat", filepath.Join(destination, "source.txt")},
		Trees: []GuestTree{{
			SourcePath:  sourceDir,
			Destination: destination,
		}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	stdout := readAll(t, session.Stdout())
	if code, err := session.Wait(); err != nil || code != 0 {
		t.Fatalf("Wait: code=%d err=%v stderr=%s", code, err, readAll(t, session.Stderr()))
	}
	if strings.TrimSpace(stdout) != "source-tree-data" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestSSHClientConfigRejectsInvalidHostKey(t *testing.T) {
	_, err := sshClientConfig(GuestCreds{
		User:          testSSHUser,
		Password:      testSSHPassword,
		HostPublicKey: []byte("not-a-public-key"),
	}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "parse guest host public key") {
		t.Fatalf("error = %v", err)
	}
}

func TestWindowsBootstrap_QuotesInputsAndCreatesFiles(t *testing.T) {
	script, err := windowsBootstrap(GuestCommand{
		Platform:   GuestPlatformWindows,
		Args:       []string{"cmd", "/c", "echo it's ready"},
		Env:        map[string]string{"TOKEN": "value'with-quote"},
		WorkingDir: `C:/reactorcide/job`,
		Files: []GuestFile{{
			Path: `C:/reactorcide/job/.reactorcide/vcs-auth/credentials`,
			Data: []byte("credential-data"),
		}},
	}, `$env:TEMP/.reactorcide-test.pid`)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"[IO.File]::WriteAllBytes",
		"$env:TOKEN='value''with-quote'",
		"Set-Location -LiteralPath 'C:/reactorcide/job'",
		"& 'cmd' '/c' 'echo it''s ready'",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("bootstrap is missing %q:\n%s", marker, script)
		}
	}
}

func TestSSHTransport_SignalTerminatesLongRunningCommand(t *testing.T) {
	server := startTestSSHServer(t)
	transport := NewSSHTransport()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := transport.Start(ctx, server.addr, testCreds(), GuestCommand{
		Platform: GuestPlatformPOSIX,
		Args:     []string{"sleep", "100"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	// Drain stdout/stderr concurrently so the exec channel isn't backed up
	// (there won't be output from `sleep`, but Wait's SSH request loop
	// still needs the channel to make progress towards EOF/exit).
	go io.Copy(io.Discard, session.Stdout())
	go io.Copy(io.Discard, session.Stderr())

	if err := session.Signal("TERM"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	waitDone := make(chan struct{})
	var code int
	var waitErr error
	go func() {
		code, waitErr = session.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return within 5s of Signal(\"TERM\")")
	}

	if waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}
	// 128+15: sh was replaced by `sleep` via exec, so the kill-fallback's
	// `kill -TERM <pid>` (via the pidfile captured at Start) hits `sleep`
	// directly; the test server reports that as exit-signal TERM, which
	// golang.org/x/crypto/ssh's client maps to 128+SIGTERM.
	if code != 143 {
		t.Fatalf("exit code = %d, want 143 (killed by SIGTERM)", code)
	}
}

func TestSSHTransport_WaitIsSafeToCallMultipleTimes(t *testing.T) {
	server := startTestSSHServer(t)
	transport := NewSSHTransport()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := transport.Start(ctx, server.addr, testCreds(), GuestCommand{Platform: GuestPlatformPOSIX, Args: []string{"true"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	_, _ = io.ReadAll(session.Stdout())
	_, _ = io.ReadAll(session.Stderr())

	code1, err1 := session.Wait()
	code2, err2 := session.Wait()
	if code1 != code2 || err1 != err2 {
		t.Fatalf("Wait not idempotent: (%d, %v) vs (%d, %v)", code1, err1, code2, err2)
	}
}
