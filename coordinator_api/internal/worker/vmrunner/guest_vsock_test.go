//go:build !windows

package vmrunner

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

// pipeGuest returns a GuestAddr whose Dial connects an in-memory pipe to a
// real guest daemon server. net.Pipe has no buffer, so a protocol step that
// waits for the wrong peer deadlocks the test instead of passing by luck.
func pipeGuest(t *testing.T) GuestAddr {
	t.Helper()
	srv := &vmguest.Server{}
	return GuestAddr{Dial: func(ctx context.Context) (net.Conn, error) {
		host, guest := net.Pipe()
		go srv.ServeConn(guest)
		return host, nil
	}}
}

type sessionOutput struct {
	stdout, stderr string
	code           int
	err            error
}

func runSession(t *testing.T, s GuestSession) sessionOutput {
	t.Helper()
	var out sessionOutput
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b, _ := io.ReadAll(s.Stdout()); out.stdout = string(b) }()
	go func() { defer wg.Done(); b, _ := io.ReadAll(s.Stderr()); out.stderr = string(b) }()
	done := make(chan struct{})
	go func() {
		out.code, out.err = s.Wait()
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the guest command did not finish")
	}
	return out
}

func TestVsockTransportRunsCommandWithEnvAndExitCode(t *testing.T) {
	// macOS: the temporary directory is below /var, a link to /private/var,
	// and the shell reports the resolved path in $PWD.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "work", "dir")
	tr := NewVsockTransport()
	s, err := tr.Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args:       []string{"/bin/sh", "-c", `printf '%s|' "$SECRET_VALUE" "$PWD"; echo to-stderr >&2; exit 3`},
		Env:        map[string]string{"SECRET_VALUE": "it's $(not) `run`\n"},
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := runSession(t, s)
	if out.err != nil || out.code != 3 {
		t.Fatalf("exit = %d, %v", out.code, out.err)
	}
	if want := "it's $(not) `run`\n|" + dir + "|"; out.stdout != want {
		t.Fatalf("stdout = %q, want %q", out.stdout, want)
	}
	if out.stderr != "to-stderr\n" {
		t.Fatalf("stderr = %q", out.stderr)
	}
	// Wait is safe to call again.
	if code, err := s.Wait(); code != 3 || err != nil {
		t.Fatalf("second Wait = %d, %v", code, err)
	}
}

func TestVsockTransportLooksUpProgramInRequestPath(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "rc-test-tool"), []byte("#!/bin/sh\necho tool-ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args: []string{"rc-test-tool"},
		Env:  map[string]string{"PATH": bin + ":/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out := runSession(t, s); out.code != 0 || out.stdout != "tool-ran\n" {
		t.Fatalf("out = %+v", out)
	}
}

func TestVsockTransportUnknownProgramExits127(t *testing.T) {
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args: []string{"/nonexistent/program"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := runSession(t, s)
	if out.code != 127 || !strings.Contains(out.stderr, "start command") {
		t.Fatalf("out = %+v", out)
	}
}

func TestVsockTransportFilesTreesAndResults(t *testing.T) {
	guestRoot := t.TempDir()
	hostWorkspace := t.TempDir()
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "pkg", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pkg/main.go", filepath.Join(source, "entry")); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 200<<10) // several frames
	if err := os.WriteFile(filepath.Join(source, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(guestRoot, "job")
	cred := filepath.Join(guestRoot, "vcs", "credentials")
	script := `set -eu
test "$(cat ` + cred + `)" = "token-value"
test "$(stat -c %a ` + cred + ` 2>/dev/null || stat -f %Lp ` + cred + `)" = 600
test "$(cat ` + job + `/src/entry)" = "package main"
test "$(wc -c < ` + job + `/src/big.txt | tr -d ' ')" = 204800
printf '{"ok":true}' > ` + job + `/workflow-output.json
head -c 2048 /dev/zero > ` + job + `/too-big.bin
`
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args:  []string{"/bin/sh", "-c", script},
		Files: []GuestFile{{Path: cred, Data: []byte("token-value"), Mode: 0o600}},
		Trees: []GuestTree{{SourcePath: source, Destination: job + "/src"}},
		Results: []GuestResultFile{
			{SourcePath: job + "/workflow-output.json", DestinationPath: filepath.Join(hostWorkspace, "workflow-output.json"), MaxBytes: 1 << 20, Optional: true},
			{SourcePath: job + "/triggers.json", DestinationPath: filepath.Join(hostWorkspace, "triggers.json"), MaxBytes: 1 << 20, Optional: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := runSession(t, s)
	if out.err != nil || out.code != 0 {
		t.Fatalf("out = %+v", out)
	}
	b, err := os.ReadFile(filepath.Join(hostWorkspace, "workflow-output.json"))
	if err != nil || string(b) != `{"ok":true}` {
		t.Fatalf("result = %q, %v", b, err)
	}
	if st, _ := os.Stat(filepath.Join(hostWorkspace, "workflow-output.json")); st.Mode().Perm() != 0o600 {
		t.Fatalf("result mode = %v", st.Mode())
	}
	if _, err := os.Stat(filepath.Join(hostWorkspace, "triggers.json")); !os.IsNotExist(err) {
		t.Fatalf("missing optional result was created: %v", err)
	}

	// A required missing result and a result above its limit are errors.
	for name, result := range map[string]GuestResultFile{
		"required missing": {SourcePath: job + "/absent.json", DestinationPath: filepath.Join(hostWorkspace, "absent.json"), MaxBytes: 10},
		"too large":        {SourcePath: job + "/too-big.bin", DestinationPath: filepath.Join(hostWorkspace, "too-big.bin"), MaxBytes: 1024, Optional: true},
	} {
		s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
			Args: []string{"/bin/true"}, Results: []GuestResultFile{result},
		})
		if err != nil {
			t.Fatal(err)
		}
		out := runSession(t, s)
		s.Close()
		if out.err == nil {
			t.Fatalf("%s: Wait did not fail", name)
		}
		if _, err := os.Stat(result.DestinationPath); !os.IsNotExist(err) {
			t.Fatalf("%s: result file was created", name)
		}
	}
}

func TestVsockTransportSignalTerminatesCommand(t *testing.T) {
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args: []string{"/bin/sh", "-c", "echo started; sleep 30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	buf := make([]byte, len("started\n"))
	if _, err := io.ReadFull(s.Stdout(), buf); err != nil {
		t.Fatal(err)
	}
	if err := s.Signal("TERM"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out := runSession(t, s)
	if out.code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit = %d, %v", out.code, out.err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("TERM did not stop the process group")
	}
	if err := s.Signal("BOGUS"); err == nil {
		t.Fatal("accepted an unknown signal")
	}
}

func TestVsockSessionCloseStopsGuestCommand(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args: []string{"/bin/sh", "-c", "echo $$ > " + pidFile + "; exec sleep 30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	for i := 0; i < 200 && pid == 0; i++ {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the command did not start")
	}
	_ = s.Close()
	for i := 0; i < 500; i++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still runs after Close", pid)
}

func TestVsockTransportProbeAndVersionMismatch(t *testing.T) {
	tr := NewVsockTransport()
	if err := tr.Probe(context.Background(), pipeGuest(t)); err != nil {
		t.Fatal(err)
	}
	old := GuestAddr{Dial: func(context.Context) (net.Conn, error) {
		host, guest := net.Pipe()
		go func() {
			defer guest.Close()
			_, _, _ = vmguest.ReadFrame(guest)
			_ = vmguest.WriteJSON(guest, vmguest.FrameHello, vmguest.Hello{Version: 99})
		}()
		return host, nil
	}}
	if err := tr.Probe(context.Background(), old); err == nil || !strings.Contains(err.Error(), "protocol version 99") {
		t.Fatalf("err = %v", err)
	}
	if _, err := tr.Start(context.Background(), old, GuestCreds{}, GuestCommand{Args: []string{"true"}}); err == nil {
		t.Fatal("Start accepted an old guest daemon")
	}
	// A guest daemon with an older protocol refuses a newer worker.
	host, guest := net.Pipe()
	go (&vmguest.Server{}).ServeConn(guest)
	_ = vmguest.WriteJSON(host, vmguest.FrameHello, vmguest.Hello{Version: vmguest.ProtocolVersion + 1})
	if typ, _, _ := vmguest.ReadFrame(host); typ != vmguest.FrameHello {
		t.Fatalf("frame = %q", typ)
	}
	if typ, payload, _ := vmguest.ReadFrame(host); typ != vmguest.FrameError || !strings.Contains(string(payload), "protocol version") {
		t.Fatalf("frame = %q %q", typ, payload)
	}
	host.Close()
}

func TestVsockTransportOverTCPAndUnknownUser(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	go func() { _ = (&vmguest.Server{}).Serve(ln) }()
	addr := GuestAddr{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
	s, err := NewVsockTransport().Start(context.Background(), addr, GuestCreds{}, GuestCommand{Args: []string{"/bin/echo", "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if out := runSession(t, s); out.stdout != "tcp\n" || out.code != 0 {
		t.Fatalf("out = %+v", out)
	}
	s.Close()
	if _, err := NewVsockTransport().Start(context.Background(), addr, GuestCreds{User: "no-such-user-rc"}, GuestCommand{Args: []string{"/bin/true"}}); err == nil {
		t.Fatal("Start accepted an unknown guest account")
	}
}

func TestVMRunnerWithGuestDaemon(t *testing.T) {
	lifecycle := &fakeLifecycle{bootHandle: "vm-1", bootAddr: pipeGuest(t)}
	runner := New(&fakeImageSource{path: "base"}, lifecycle, NewVsockTransport(), GuestCreds{},
		WithConnectTimeout(5*time.Second), WithConnectRetryInterval(10*time.Millisecond))
	cfg := baseTestConfig()
	cfg.Command = []string{"/bin/sh", "-c", `echo "$FOO"; exit 7`}
	id, err := runner.SpawnJob(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runner.StreamLogs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(stdout)
	code, err := runner.WaitForCompletion(context.Background(), id)
	if err != nil || code != 7 || string(b) != "bar\n" {
		t.Fatalf("code = %d, err = %v, stdout = %q", code, err, b)
	}
	if err := runner.Cleanup(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestBufferedPipe(t *testing.T) {
	p := newBufferedPipe(4)
	if _, err := p.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan struct{})
	go func() {
		_, _ = p.Write([]byte("gh")) // blocks until the reader takes bytes
		close(wrote)
	}()
	select {
	case <-wrote:
		t.Fatal("Write did not wait for a full buffer")
	case <-time.After(50 * time.Millisecond):
	}
	buf := make([]byte, 10)
	n, _ := p.Read(buf)
	<-wrote
	p.CloseWithError(nil)
	rest, _ := io.ReadAll(p)
	if got := string(buf[:n]) + string(rest); got != "abcdefgh" {
		t.Fatalf("read %q", got)
	}
	if _, err := p.Write([]byte("x")); err == nil {
		t.Fatal("Write after close succeeded")
	}
}

// The hello of the worker names the guest account; the start request repeats
// it. This pins the wire names that the guest daemon reads.
func TestStartRequestWireFormat(t *testing.T) {
	b, _ := json.Marshal(vmguest.StartRequest{Args: []string{"a"}, User: "u", Dir: "/d", Results: []vmguest.ResultSpec{{Path: "/r", MaxBytes: 5}}})
	if string(b) != `{"args":["a"],"dir":"/d","user":"u","results":[{"path":"/r","max_bytes":5}]}` {
		t.Fatalf("wire format = %s", b)
	}
}

// With a guest account, the daemon reports the account and if it is an
// administrator, and the job log starts with that line.
func TestVsockTransportReportsGuestAccount(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{User: me.Username}, GuestCommand{
		Args: []string{"/bin/sh", "-c", "echo job >&2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := runSession(t, s)
	line, rest, _ := strings.Cut(out.stderr, "\n")
	if !strings.HasPrefix(line, "reactorcide: running as "+me.Username+" which ") || !strings.HasSuffix(line, " an administrator") {
		t.Fatalf("first stderr line = %q", line)
	}
	if rest != "job\n" {
		t.Fatalf("job stderr = %q", rest)
	}
}

// A guest daemon that does not report the account (an older build) gives no
// account line.
func TestVsockTransportNoAccountLineWithoutGuestReport(t *testing.T) {
	s, err := NewVsockTransport().Start(context.Background(), pipeGuest(t), GuestCreds{}, GuestCommand{
		Args: []string{"/bin/sh", "-c", "echo job >&2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out := runSession(t, s); out.stderr != "job\n" {
		t.Fatalf("stderr = %q", out.stderr)
	}
}
