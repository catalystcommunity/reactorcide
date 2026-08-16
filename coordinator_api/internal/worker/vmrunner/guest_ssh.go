package vmrunner

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const defaultSSHDialTimeout = 15 * time.Second

// signalMap allowlists the POSIX signal names VMRunner ever sends
// ("TERM" today) plus the common set RFC 4254 defines, and doubles as the
// only place a caller-supplied sig string is turned into shell text --
// keeping the kill-fallback command injection-proof regardless of what a
// future caller passes.
var signalMap = map[string]ssh.Signal{
	"ABRT": ssh.SIGABRT,
	"ALRM": ssh.SIGALRM,
	"FPE":  ssh.SIGFPE,
	"HUP":  ssh.SIGHUP,
	"ILL":  ssh.SIGILL,
	"INT":  ssh.SIGINT,
	"KILL": ssh.SIGKILL,
	"PIPE": ssh.SIGPIPE,
	"QUIT": ssh.SIGQUIT,
	"SEGV": ssh.SIGSEGV,
	"TERM": ssh.SIGTERM,
	"USR1": ssh.SIGUSR1,
	"USR2": ssh.SIGUSR2,
}

func lookupSignal(sig string) (ssh.Signal, error) {
	name := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(sig), "SIG"))
	s, ok := signalMap[name]
	if !ok {
		return "", fmt.Errorf("vmrunner/ssh: unsupported signal %q", sig)
	}
	return s, nil
}

// SSHTransport implements GuestTransport over SSH.
//
// The transport verifies the guest host key when GuestCreds contains a pinned
// public key. Without a pinned key, it accepts the key from the private guest
// network. Operators should pin a stable key that is part of the base image.
//
// The transport sends a bootstrap script through SSH standard input. The SSH
// command line contains no environment values or file data.
type SSHTransport struct {
	// DialTimeout bounds the SSH handshake (TCP connect + auth) for a
	// single Start call. Guest reachability polling happens in
	// VMRunner.SpawnJob (waitTCPReachable) before Start is ever invoked, so
	// this is just the final handshake, not the guest boot wait.
	DialTimeout time.Duration
}

// NewSSHTransport builds an SSHTransport with sensible defaults.
func NewSSHTransport() *SSHTransport {
	return &SSHTransport{DialTimeout: defaultSSHDialTimeout}
}

func (t *SSHTransport) dialTimeout() time.Duration {
	if t.DialTimeout <= 0 {
		return defaultSSHDialTimeout
	}
	return t.DialTimeout
}

// Start dials the guest over SSH, authenticates with creds, and runs cmd
// (with env exported into its shell environment) as a background remote
// process. It returns as soon as the remote command has started -- it does
// not wait for completion.
func (t *SSHTransport) Start(ctx context.Context, addr GuestAddr, creds GuestCreds, command GuestCommand) (GuestSession, error) {
	if len(command.Args) == 0 {
		return nil, errors.New("vmrunner/ssh: command must not be empty")
	}
	if command.Platform == "" {
		command.Platform = GuestPlatformPOSIX
	}
	if command.Platform != GuestPlatformPOSIX && command.Platform != GuestPlatformWindows {
		return nil, fmt.Errorf("vmrunner/ssh: unsupported guest platform %q", command.Platform)
	}

	clientConfig, err := sshClientConfig(creds, t.dialTimeout())
	if err != nil {
		return nil, err
	}

	target := net.JoinHostPort(addr.Host, strconv.Itoa(addr.Port))
	client, err := sshDial(ctx, target, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("vmrunner/ssh: dial %s: %w", target, err)
	}

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: new session: %w", err)
	}

	// This is a compatibility aid for guests that accept SSH environment
	// requests. The bootstrap script remains the reliable delivery path.
	for k, v := range command.Env {
		_ = sess.Setenv(k, v)
	}
	if err := uploadGuestTrees(client, command.Platform, command.Trees); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, err
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: stdin pipe: %w", err)
	}

	stdoutPipe, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: stdout pipe: %w", err)
	}
	stderrPipe, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: stderr pipe: %w", err)
	}

	pidFile := fmt.Sprintf("/tmp/.reactorcide-vm-%s.pid", uuid.New().String())
	remoteCmd := "/bin/sh -s"
	script, err := posixBootstrap(command, pidFile)
	if command.Platform == GuestPlatformWindows {
		pidFile = fmt.Sprintf(`$env:TEMP/.reactorcide-vm-%s.pid`, uuid.New().String())
		remoteCmd = "powershell.exe -NoProfile -NonInteractive -Command -"
		script, err = windowsBootstrap(command, pidFile)
	}
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, err
	}

	if err := sess.Start(remoteCmd); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: start command: %w", err)
	}
	if _, err := io.WriteString(stdin, script); err != nil {
		_ = stdin.Close()
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: send bootstrap: %w", err)
	}
	if err := stdin.Close(); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: close bootstrap input: %w", err)
	}

	return &sshSession{
		client:   client,
		sess:     sess,
		stdout:   stdoutPipe,
		stderr:   stderrPipe,
		pidFile:  pidFile,
		platform: command.Platform,
		results:  command.Results,
		doneCh:   make(chan struct{}),
	}, nil
}

func uploadGuestTrees(client *ssh.Client, platform GuestPlatform, trees []GuestTree) error {
	for _, tree := range trees {
		info, err := os.Stat(tree.SourcePath)
		if err != nil {
			return fmt.Errorf("vmrunner/ssh: inspect guest tree source: %w", err)
		}
		if !info.IsDir() {
			return errors.New("vmrunner/ssh: guest tree source must be a directory")
		}
		if strings.TrimSpace(tree.Destination) == "" {
			return errors.New("vmrunner/ssh: guest tree destination must not be empty")
		}

		sess, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("vmrunner/ssh: create upload session: %w", err)
		}
		stdin, err := sess.StdinPipe()
		if err != nil {
			_ = sess.Close()
			return fmt.Errorf("vmrunner/ssh: open upload input: %w", err)
		}
		remoteCmd := "mkdir -p -- " + shellQuote(tree.Destination) + " && tar -xf - -C " + shellQuote(tree.Destination)
		if platform == GuestPlatformWindows {
			destination := powerShellQuote(tree.Destination)
			remoteCmd = "powershell.exe -NoProfile -NonInteractive -Command \"$d=" + destination + "; New-Item -ItemType Directory -Force -Path $d | Out-Null; tar.exe -xf - -C $d\""
		}
		if err := sess.Start(remoteCmd); err != nil {
			_ = stdin.Close()
			_ = sess.Close()
			return fmt.Errorf("vmrunner/ssh: start tree upload: %w", err)
		}
		writeErr := writeTarTree(stdin, tree.SourcePath)
		closeErr := stdin.Close()
		waitErr := sess.Wait()
		_ = sess.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return fmt.Errorf("vmrunner/ssh: close tree upload: %w", closeErr)
		}
		if waitErr != nil {
			return fmt.Errorf("vmrunner/ssh: extract guest tree: %w", waitErr)
		}
	}
	return nil
}

func writeTarTree(dst io.Writer, root string) error {
	tw := tar.NewWriter(dst)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		_ = tw.Close()
		return fmt.Errorf("vmrunner/ssh: archive guest tree: %w", walkErr)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("vmrunner/ssh: finish guest tree archive: %w", err)
	}
	return nil
}

var _ GuestTransport = (*SSHTransport)(nil)

// sshSession implements GuestSession over one SSH client+session pair
// dedicated to a single Start call.
type sshSession struct {
	client *ssh.Client
	sess   *ssh.Session
	stdout io.Reader
	stderr io.Reader

	// pidFile is where the remote shell wrote its PID before exec'ing into
	// the job command; Signal's kill-fallback reads it back.
	pidFile  string
	platform GuestPlatform
	results  []GuestResultFile

	waitOnce sync.Once
	waitCode int
	waitErr  error
	doneCh   chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func (s *sshSession) Stdout() io.Reader { return s.stdout }
func (s *sshSession) Stderr() io.Reader { return s.stderr }

// Wait blocks until the remote command exits. Safe to call concurrently or
// more than once -- the first caller does the real wait and every caller
// (including ones that arrive after it's already finished) observes the
// same (exitCode, err).
func (s *sshSession) Wait() (int, error) {
	s.waitOnce.Do(func() {
		s.waitCode, s.waitErr = exitCodeFromWaitErr(s.sess.Wait())
		if resultErr := downloadGuestResults(s.client, s.platform, s.results); resultErr != nil && s.waitErr == nil {
			s.waitErr = resultErr
		}
		close(s.doneCh)
	})
	<-s.doneCh
	return s.waitCode, s.waitErr
}

func downloadGuestResults(client *ssh.Client, platform GuestPlatform, results []GuestResultFile) error {
	for _, result := range results {
		if strings.TrimSpace(result.SourcePath) == "" || strings.TrimSpace(result.DestinationPath) == "" {
			return errors.New("vmrunner/ssh: guest result paths must not be empty")
		}
		data, missing, err := downloadGuestResult(client, platform, result.SourcePath, result.MaxBytes)
		if err != nil {
			return err
		}
		if missing && result.Optional {
			continue
		}
		if missing {
			return fmt.Errorf("vmrunner/ssh: required guest result is missing")
		}
		if err := writeGuestResult(result.DestinationPath, data); err != nil {
			return err
		}
	}
	return nil
}

func downloadGuestResult(client *ssh.Client, platform GuestPlatform, source string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, false, fmt.Errorf("vmrunner/ssh: create result session: %w", err)
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("vmrunner/ssh: open result output: %w", err)
	}
	command := "[ -f " + shellQuote(source) + " ] || exit 44; cat -- " + shellQuote(source)
	if platform == GuestPlatformWindows {
		quoted := powerShellQuote(source)
		command = "powershell.exe -NoProfile -NonInteractive -Command \"$p=" + quoted + "; if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { exit 44 }; $b=[IO.File]::ReadAllBytes($p); $o=[Console]::OpenStandardOutput(); $o.Write($b,0,$b.Length)\""
	}
	if err := sess.Start(command); err != nil {
		return nil, false, fmt.Errorf("vmrunner/ssh: start result download: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if int64(len(data)) > maxBytes {
		_ = sess.Close()
		return nil, false, fmt.Errorf("vmrunner/ssh: guest result exceeds %d bytes", maxBytes)
	}
	waitErr := sess.Wait()
	if exitErr, ok := waitErr.(*ssh.ExitError); ok && exitErr.ExitStatus() == 44 {
		return nil, true, nil
	}
	if waitErr != nil {
		return nil, false, fmt.Errorf("vmrunner/ssh: download guest result: %w", waitErr)
	}
	if readErr != nil {
		return nil, false, fmt.Errorf("vmrunner/ssh: read guest result: %w", readErr)
	}
	return data, false, nil
}

func writeGuestResult(destination string, data []byte) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("vmrunner/ssh: create result directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".reactorcide-result-*")
	if err != nil {
		return fmt.Errorf("vmrunner/ssh: create result file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("vmrunner/ssh: secure result file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("vmrunner/ssh: write result file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("vmrunner/ssh: close result file: %w", err)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		if removeErr := os.Remove(destination); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("vmrunner/ssh: replace result file: %w", err)
		}
		if retryErr := os.Rename(tempPath, destination); retryErr != nil {
			return fmt.Errorf("vmrunner/ssh: install result file: %w", retryErr)
		}
	}
	return nil
}

// Signal asks the remote command to terminate. It sends the RFC 4254
// SSH-protocol signal request, but does not trust it alone: OpenSSH's
// sshd has a long history of silently accepting or ignoring "signal"
// channel requests depending on version/build, with no reliable way for
// the client to tell which happened. So Signal *always* also opens a
// second exec channel that runs `kill -<sig> <pid>` against the PID
// captured at Start time -- that fallback is what callers should actually
// depend on; the SSH-protocol request is opportunistic best-effort on top.
func (s *sshSession) Signal(sig string) error {
	sshSig, sigErr := lookupSignal(sig)
	if sigErr == nil {
		_ = s.sess.Signal(sshSig)
	}
	return s.killFallback(sig)
}

func (s *sshSession) killFallback(sig string) error {
	sshSig, err := lookupSignal(sig)
	if err != nil {
		return err
	}
	if s.pidFile == "" {
		return nil
	}

	killSess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("vmrunner/ssh: open kill-fallback session: %w", err)
	}
	defer killSess.Close()

	if s.platform == GuestPlatformWindows {
		return s.killFallbackWindows(killSess, sig)
	}

	// Start returns when the guest accepts the exec request. The remote shell
	// can still be starting, so wait briefly for it to write the PID file.
	// sshSig and pidFile are internally generated values, not job input.
	killCmd := fmt.Sprintf(
		`i=0; while [ ! -s %s ] && [ "$i" -lt 20 ]; do sleep 0.05; i=$((i + 1)); done; `+
			`[ -s %s ] || exit 1; pid=$(cat %s 2>/dev/null) || exit 1; `+
			`kill -%s "$pid" 2>/dev/null || ! kill -0 "$pid" 2>/dev/null`,
		s.pidFile, s.pidFile, s.pidFile, string(sshSig),
	)
	return killSess.Run(killCmd)
}

func (s *sshSession) killFallbackWindows(killSess *ssh.Session, sig string) error {
	force := ""
	if strings.EqualFold(sig, "KILL") {
		force = " -Force"
	}
	cmd := fmt.Sprintf(
		`powershell.exe -NoProfile -NonInteractive -Command "$p=%s; if (Test-Path $p) { $id=[int](Get-Content $p); Stop-Process -Id $id%s -ErrorAction SilentlyContinue }"`,
		s.pidFile, force,
	)
	return killSess.Run(cmd)
}

// Close releases the SSH session and its underlying client connection. It
// does not touch the guest VM -- VMLifecycle.Destroy owns that. Safe to
// call more than once.
func (s *sshSession) Close() error {
	s.closeOnce.Do(func() {
		sessErr := s.sess.Close()
		clientErr := s.client.Close()
		switch {
		case sessErr != nil && sessErr != io.EOF:
			s.closeErr = sessErr
		case clientErr != nil:
			s.closeErr = clientErr
		}
	})
	return s.closeErr
}

var _ GuestSession = (*sshSession)(nil)

func sshClientConfig(creds GuestCreds, timeout time.Duration) (*ssh.ClientConfig, error) {
	var auths []ssh.AuthMethod
	if len(creds.PrivateKeyPEM) > 0 {
		signer, err := ssh.ParsePrivateKey(creds.PrivateKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("vmrunner/ssh: parse private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if creds.Password != "" {
		auths = append(auths, ssh.Password(creds.Password))
	}
	if len(auths) == 0 {
		return nil, errors.New("vmrunner/ssh: no auth method configured (need PrivateKeyPEM or Password)")
	}

	config := &ssh.ClientConfig{
		User:            creds.User,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         timeout,
	}
	if len(creds.HostPublicKey) > 0 {
		hostKey, _, _, _, err := ssh.ParseAuthorizedKey(creds.HostPublicKey)
		if err != nil {
			return nil, fmt.Errorf("vmrunner/ssh: parse guest host public key: %w", err)
		}
		config.HostKeyCallback = ssh.FixedHostKey(hostKey)
	}
	return config, nil
}

func sshDial(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: config.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// exitCodeFromWaitErr maps golang.org/x/crypto/ssh's Session.Wait error
// into (exitCode, error) the way GuestSession.Wait promises.
func exitCodeFromWaitErr(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return -1, fmt.Errorf("vmrunner/ssh: command exited without a status (likely killed by signal): %w", err)
	}
	return -1, fmt.Errorf("vmrunner/ssh: wait: %w", err)
}

// shellQuote POSIX single-quotes s so it is passed through the remote
// shell verbatim, including secret values that may contain arbitrary
// bytes -- this is the mechanism that lets env values reach the guest
// without ever needing to be logged or otherwise handled specially.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func posixBootstrap(command GuestCommand, pidFile string) (string, error) {
	var b strings.Builder
	b.WriteString("set -eu\numask 077\n")
	for _, file := range command.Files {
		if err := validateGuestFile(file); err != nil {
			return "", err
		}
		dir := guestParent(file.Path)
		fmt.Fprintf(&b, "mkdir -p %s\n", shellQuote(dir))
		encoded := base64.StdEncoding.EncodeToString(file.Data)
		fmt.Fprintf(&b, "(printf '%%s' %s | base64 --decode 2>/dev/null || printf '%%s' %s | base64 -D) > %s\n",
			shellQuote(encoded), shellQuote(encoded), shellQuote(file.Path))
		mode := file.Mode.Perm()
		if mode == 0 {
			mode = 0o600
		}
		fmt.Fprintf(&b, "chmod %04o %s\n", mode, shellQuote(file.Path))
	}
	for key, value := range command.Env {
		if isValidEnvName(key) {
			fmt.Fprintf(&b, "export %s=%s\n", key, shellQuote(value))
		}
	}
	if command.WorkingDir != "" {
		fmt.Fprintf(&b, "cd %s\n", shellQuote(command.WorkingDir))
	}
	fmt.Fprintf(&b, "echo $$ > %s\nexec %s\n", shellQuote(pidFile), shellJoin(command.Args))
	return b.String(), nil
}

func windowsBootstrap(command GuestCommand, pidFile string) (string, error) {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	for _, file := range command.Files {
		if err := validateGuestFile(file); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "$p=%s; New-Item -ItemType Directory -Force -Path (Split-Path -Parent $p) | Out-Null; [IO.File]::WriteAllBytes($p,[Convert]::FromBase64String(%s))\n",
			powerShellQuote(file.Path), powerShellQuote(base64.StdEncoding.EncodeToString(file.Data)))
	}
	for key, value := range command.Env {
		if isValidEnvName(key) {
			fmt.Fprintf(&b, "$env:%s=%s\n", key, powerShellQuote(value))
		}
	}
	if command.WorkingDir != "" {
		fmt.Fprintf(&b, "Set-Location -LiteralPath %s\n", powerShellQuote(command.WorkingDir))
	}
	fmt.Fprintf(&b, "$PID | Set-Content -NoNewline -LiteralPath %s\n", pidFile)
	b.WriteString("& ")
	for i, arg := range command.Args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(powerShellQuote(arg))
	}
	b.WriteString("\nexit $LASTEXITCODE\n")
	return b.String(), nil
}

func validateGuestFile(file GuestFile) error {
	if strings.TrimSpace(file.Path) == "" || strings.ContainsAny(file.Path, "\x00\r\n") {
		return errors.New("vmrunner/ssh: guest file has an invalid path")
	}
	return nil
}

func guestParent(filePath string) string {
	normalized := strings.ReplaceAll(filePath, `\`, "/")
	if index := strings.LastIndex(normalized, "/"); index > 0 {
		return normalized[:index]
	}
	return "."
}

func powerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// shellExports renders env as `export NAME='value'; ` statements, skipping
// any name that isn't a valid shell identifier rather than risking
// injection through a malformed key.
func shellExports(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range env {
		if !isValidEnvName(k) {
			continue
		}
		b.WriteString("export ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(v))
		b.WriteString("; ")
	}
	return b.String()
}

func isValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
