package vmguest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// waitDelay bounds how long Wait waits for output pipes after the command
// exits. A background process that keeps stdout open must not hold the job.
const waitDelay = 2 * time.Second

// Server is the job channel side of the guest daemon. It serves one command
// for each connection.
type Server struct {
	// ExtraEnv is added to the daemon environment before the request
	// environment, for example the proxy variables on Windows.
	ExtraEnv []string
	// Log receives operational messages. It never receives environment
	// values, file data, or command output.
	Log *slog.Logger
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			s.log().Warn("job channel accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go s.ServeConn(c)
	}
}

// conn is one job channel connection. Writes from the output pumps and the
// main goroutine share mu so that frames do not mix.
type conn struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *conn) send(typ byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.c, typ, payload)
}

func (c *conn) sendJSON(typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.send(typ, b)
}

func (c *conn) fail(err error) {
	msg := err.Error()
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	_ = c.send(FrameError, []byte(msg))
}

// ServeConn serves one connection and closes it.
func (s *Server) ServeConn(nc net.Conn) {
	defer nc.Close()
	c := &conn{c: nc}

	typ, payload, err := ReadFrame(nc)
	if err != nil {
		// A reachability probe can close the connection at once.
		return
	}
	if typ != FrameHello {
		c.fail(fmt.Errorf("vmguest: expected hello, got frame %q", typ))
		return
	}
	var hello Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		c.fail(fmt.Errorf("vmguest: bad hello: %w", err))
		return
	}
	if hello.Version != ProtocolVersion {
		// Send the hello first, so the worker can report both versions.
		_ = c.sendJSON(FrameHello, Hello{Version: ProtocolVersion, OS: runtime.GOOS})
		c.fail(fmt.Errorf("vmguest: protocol version %d, the guest daemon has version %d", hello.Version, ProtocolVersion))
		return
	}
	// An error in place of the hello stops the worker before it sends files.
	createAdmin := hello.CreateAdministrator == nil || *hello.CreateAdministrator
	acct, err := lookupAccount(hello.User, createAdmin)
	if err != nil {
		c.fail(err)
		return
	}
	reply := Hello{Version: ProtocolVersion, OS: runtime.GOOS}
	if hello.User != "" {
		admin := acct.admin
		reply.User, reply.Administrator = acct.name, &admin
		s.log().Info(AccountLine(acct.name, admin))
	}
	if err := c.sendJSON(FrameHello, reply); err != nil {
		return
	}

	for {
		typ, payload, err := ReadFrame(nc)
		if err != nil {
			return
		}
		switch typ {
		case FrameFile:
			var h FileHeader
			if err := json.Unmarshal(payload, &h); err != nil {
				c.fail(fmt.Errorf("vmguest: bad file header: %w", err))
				return
			}
			if err := receiveFile(h, &DataReader{R: nc}, acct); err != nil {
				c.fail(err)
				return
			}
			if err := c.send(FrameAck, nil); err != nil {
				return
			}
		case FrameTree:
			var h TreeHeader
			if err := json.Unmarshal(payload, &h); err != nil {
				c.fail(fmt.Errorf("vmguest: bad tree header: %w", err))
				return
			}
			if err := extractTree(h.Dest, &DataReader{R: nc}, acct, s.log()); err != nil {
				c.fail(err)
				return
			}
			if err := c.send(FrameAck, nil); err != nil {
				return
			}
		case FrameStart:
			var req StartRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				c.fail(fmt.Errorf("vmguest: bad start request: %w", err))
				return
			}
			s.run(c, req, acct)
			return
		default:
			c.fail(fmt.Errorf("vmguest: unexpected frame %q", typ))
			return
		}
	}
}

// run starts the command, streams its output, and sends the results and the
// exit status.
func (s *Server) run(c *conn, req StartRequest, acct account) {
	if len(req.Args) == 0 || req.Args[0] == "" {
		c.fail(errors.New("vmguest: args are required"))
		return
	}
	for _, a := range req.Args {
		if strings.IndexByte(a, 0) >= 0 {
			c.fail(errors.New("vmguest: args must not contain NUL"))
			return
		}
	}
	env, err := s.commandEnv(req.Env, acct)
	if err != nil {
		c.fail(err)
		return
	}
	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	if req.Dir != "" {
		dir := filepath.Clean(filepath.FromSlash(req.Dir))
		if err := mkdirAllOwned(dir, 0o755, acct); err != nil {
			c.fail(fmt.Errorf("vmguest: create working directory: %w", err))
			return
		}
		cmd.Dir = dir
	}
	cmd.Env = env
	// exec.Command looks up a bare program name in the daemon PATH. Look it
	// up again in the command PATH, so the request PATH applies.
	if !strings.ContainsAny(req.Args[0], `/\`) {
		if p, err := lookPathIn(req.Args[0], env); err == nil {
			cmd.Path, cmd.Err = p, nil
		}
	}
	configureCmd(cmd, acct)
	cmd.Stdout = &DataWriter{W: c.c, Type: FrameStdout, Lock: &c.mu}
	cmd.Stderr = &DataWriter{W: c.c, Type: FrameStderr, Lock: &c.mu}
	cmd.WaitDelay = waitDelay

	code := 0
	if err := cmd.Start(); err != nil {
		// The command did not start, for example an unknown program.
		_ = c.send(FrameStderr, []byte("vmguest: start command: "+err.Error()+"\n"))
		code = 127
	} else {
		done := make(chan struct{})
		go s.control(c, cmd, done)
		err := cmd.Wait()
		close(done)
		code = exitCode(cmd, err)
		if errors.Is(err, exec.ErrWaitDelay) {
			_ = c.send(FrameStderr, []byte("vmguest: the command exited, but a process that it started kept the output open; the daemon stopped that process\n"))
		}
		// Processes that the command left in its group stop with it.
		cleanupGroup(cmd)
	}
	sendResults(c, req.Results)
	_ = c.sendJSON(FrameExit, ExitStatus{Code: code})
}

// control reads signal frames while the command runs. When the worker closes
// the connection before the command exits, the daemon stops the command.
func (s *Server) control(c *conn, cmd *exec.Cmd, done <-chan struct{}) {
	for {
		typ, payload, err := ReadFrame(c.c)
		if err != nil {
			select {
			case <-done:
			default:
				killGroup(cmd)
			}
			return
		}
		if typ != FrameSignal {
			continue
		}
		var req SignalRequest
		if json.Unmarshal(payload, &req) != nil {
			continue
		}
		sig, err := NormalizeSignal(req.Signal)
		if err != nil {
			s.log().Warn("signal", "err", err)
			continue
		}
		if err := signalGroup(cmd, sig); err != nil {
			s.log().Warn("signal", "signal", sig, "err", err)
		}
	}
}

func exitCode(cmd *exec.Cmd, err error) int {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return stateExitCode(exitErr.ProcessState)
	case cmd.ProcessState != nil:
		// exec.ErrWaitDelay: the command exited, but its output stayed open.
		return stateExitCode(cmd.ProcessState)
	default:
		return -1
	}
}

func sendResults(c *conn, results []ResultSpec) {
	for i, spec := range results {
		h := ResultHeader{Index: i}
		max := spec.MaxBytes
		if max <= 0 {
			max = 1 << 20
		}
		f, err := openRegular(spec.Path)
		if err != nil {
			h.Missing = true
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errNotRegular) {
				h.Error = err.Error()
			}
			if c.sendJSON(FrameResult, h) != nil {
				return
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, max+1))
		f.Close()
		switch {
		case err != nil:
			h.Missing, h.Error = true, err.Error()
		case int64(len(data)) > max:
			h.TooLarge = true
		}
		if c.sendJSON(FrameResult, h) != nil {
			return
		}
		if h.Missing || h.TooLarge {
			continue
		}
		w := &DataWriter{W: c.c, Type: FrameData, Lock: &c.mu}
		if _, err := w.Write(data); err != nil {
			return
		}
		if w.Close() != nil {
			return
		}
	}
}

var errNotRegular = errors.New("not a regular file")

func openRegular(path string) (*os.File, error) {
	f, err := os.Open(filepath.FromSlash(path))
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

// commandEnv is the daemon environment (without REACTORCIDE_VMGUEST_
// variables), then ExtraEnv, then the account variables, then the request
// environment. A later value replaces an earlier one.
func (s *Server) commandEnv(reqEnv map[string]string, acct account) ([]string, error) {
	var order []string
	values := map[string]string{}
	set := func(k, v string) {
		key := envKey(k)
		if _, ok := values[key]; !ok {
			order = append(order, key)
		}
		values[key] = k + "=" + v
	}
	add := func(kvs []string) {
		for _, kv := range kvs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				continue
			}
			set(k, v)
		}
	}
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "REACTORCIDE_VMGUEST_") {
			base = append(base, kv)
		}
	}
	add(base)
	add(s.ExtraEnv)
	add(acct.env())
	for k, v := range reqEnv {
		if !ValidEnvName(k) || strings.IndexByte(v, 0) >= 0 {
			return nil, fmt.Errorf("vmguest: bad environment variable name %q", k)
		}
		set(k, v)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, values[k])
	}
	return out, nil
}

func envKey(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}

// lookPathIn finds a program in the PATH of env.
func lookPathIn(name string, env []string) (string, error) {
	path := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		for _, candidate := range executableNames(filepath.Join(dir, name)) {
			if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() && isExecutable(st) {
				return candidate, nil
			}
		}
	}
	return "", exec.ErrNotFound
}
