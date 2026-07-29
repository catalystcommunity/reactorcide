package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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

// SSHTransport implements GuestTransport over SSH (golang.org/x/crypto/ssh)
// -- the VM-1 prototype guest channel (VM_RUNNERS_PLAN.md notes vsock as a
// possible future replacement behind the same GuestTransport interface).
//
// Host key verification: guests are freshly copy-on-write cloned per job
// from a known base image and have no persistent identity to pin against
// (each boot is, by design, a brand new host key unless the base image
// bakes in a fixed one), so SSHTransport intentionally does not verify the
// guest's host key. This is judged acceptable because the channel is a
// direct host<->guest link over a virtual/loopback network the VM
// lifecycle controls end to end, not a channel exposed to anything else.
// A future VMLifecycle that bakes a fixed host key into the golden image
// could tighten this with a pinned callback.
//
// Env delivery: SSHTransport tries the wire-level SSH "env" request
// (Session.Setenv) for each variable, but most sshd configurations reject
// it outright unless every name is explicitly AcceptEnv-listed, so it also
// *always* prefixes the remote command with shell-quoted `export`
// statements as the mechanism jobs can actually depend on. Env values
// (which may carry secrets/VCS-auth material) are therefore only ever
// embedded in the command text sent over the already-authenticated SSH
// channel -- never written to a log by this file.
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
func (t *SSHTransport) Start(ctx context.Context, addr GuestAddr, creds GuestCreds, cmd []string, env map[string]string) (GuestSession, error) {
	if len(cmd) == 0 {
		return nil, errors.New("vmrunner/ssh: command must not be empty")
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

	// Best-effort wire-level env; see the type doc for why the shell
	// `export` prefix below is the mechanism that actually matters.
	for k, v := range env {
		_ = sess.Setenv(k, v)
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

	// Capture the remote shell's PID (before it execs into the job command,
	// which replaces the process image but keeps the PID) into a per-job
	// scratch file, so Signal's kill-fallback can target the right process
	// without relying on pattern matching over `ps`.
	pidFile := fmt.Sprintf("/tmp/.reactorcide-vm-%s.pid", uuid.New().String())
	remoteCmd := fmt.Sprintf("echo $$>%s 2>/dev/null; %sexec %s", pidFile, shellExports(env), shellJoin(cmd))

	if err := sess.Start(remoteCmd); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("vmrunner/ssh: start command: %w", err)
	}

	return &sshSession{
		client:  client,
		sess:    sess,
		stdout:  stdoutPipe,
		stderr:  stderrPipe,
		pidFile: pidFile,
		doneCh:  make(chan struct{}),
	}, nil
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
	pidFile string

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
		close(s.doneCh)
	})
	<-s.doneCh
	return s.waitCode, s.waitErr
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

	return &ssh.ClientConfig{
		User: creds.User,
		Auth: auths,
		// See SSHTransport's doc comment for why host key verification is
		// intentionally skipped for this prototype transport.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         timeout,
	}, nil
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
