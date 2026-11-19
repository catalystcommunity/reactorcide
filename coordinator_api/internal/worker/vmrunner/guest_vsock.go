package vmrunner

import (
	"archive/tar"
	"context"
	"encoding/json"
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

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

const defaultHandshakeTimeout = 10 * time.Second

// VsockTransport implements GuestTransport with the guest daemon
// (cmd/vmguest) over a hypervisor socket: vsock on macOS
// (Virtualization.framework) and hvsock on Hyper-V. The lifecycle supplies
// the dial function in GuestAddr.Dial. Each Start uses a new connection.
//
// The transport sends environment values and file data only inside protocol
// frames. It never logs them.
type VsockTransport struct {
	// HandshakeTimeout bounds the hello exchange and a probe.
	HandshakeTimeout time.Duration
}

// NewVsockTransport builds a VsockTransport with default timeouts.
func NewVsockTransport() *VsockTransport {
	return &VsockTransport{HandshakeTimeout: defaultHandshakeTimeout}
}

var _ GuestTransport = (*VsockTransport)(nil)
var _ GuestProber = (*VsockTransport)(nil)

func (t *VsockTransport) handshakeTimeout() time.Duration {
	if t.HandshakeTimeout <= 0 {
		return defaultHandshakeTimeout
	}
	return t.HandshakeTimeout
}

// dialGuest uses addr.Dial. A GuestAddr without Dial is a TCP address, which
// tests use to reach a guest daemon on loopback.
func dialGuest(ctx context.Context, addr GuestAddr) (net.Conn, error) {
	if addr.Dial != nil {
		return addr.Dial(ctx)
	}
	d := net.Dialer{Timeout: defaultHandshakeTimeout}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(addr.Host, strconv.Itoa(addr.Port)))
}

// handshake exchanges hello frames and returns the hello of the guest.
func (t *VsockTransport) handshake(c net.Conn, hello vmguest.Hello) (vmguest.Hello, error) {
	_ = c.SetDeadline(time.Now().Add(t.handshakeTimeout()))
	defer c.SetDeadline(time.Time{})
	hello.Version = vmguest.ProtocolVersion
	if err := vmguest.WriteJSON(c, vmguest.FrameHello, hello); err != nil {
		return vmguest.Hello{}, fmt.Errorf("vmrunner/vsock: send hello: %w", err)
	}
	typ, payload, err := vmguest.ReadFrame(c)
	if err != nil {
		return vmguest.Hello{}, fmt.Errorf("vmrunner/vsock: read hello: %w", err)
	}
	if typ == vmguest.FrameError {
		return vmguest.Hello{}, fmt.Errorf("vmrunner/vsock: guest: %s", payload)
	}
	if typ != vmguest.FrameHello {
		return vmguest.Hello{}, fmt.Errorf("vmrunner/vsock: expected hello, got frame %q", typ)
	}
	var h vmguest.Hello
	if err := json.Unmarshal(payload, &h); err != nil {
		return vmguest.Hello{}, fmt.Errorf("vmrunner/vsock: bad hello: %w", err)
	}
	if h.Version != vmguest.ProtocolVersion {
		return vmguest.Hello{}, fmt.Errorf("vmrunner/vsock: the guest daemon has protocol version %d, the worker has %d; update the guest daemon in the image", h.Version, vmguest.ProtocolVersion)
	}
	return h, nil
}

// Probe dials the guest daemon and exchanges hello frames. VMRunner polls it
// until the guest is ready.
func (t *VsockTransport) Probe(ctx context.Context, addr GuestAddr) error {
	ctx, cancel := context.WithTimeout(ctx, t.handshakeTimeout())
	defer cancel()
	c, err := dialGuest(ctx, addr)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = t.handshake(c, vmguest.Hello{})
	return err
}

// Start uploads the command files and trees, then starts the command. It
// returns when the command has started.
func (t *VsockTransport) Start(ctx context.Context, addr GuestAddr, creds GuestCreds, command GuestCommand) (GuestSession, error) {
	if len(command.Args) == 0 {
		return nil, errors.New("vmrunner/vsock: command must not be empty")
	}
	if command.Platform == "" {
		command.Platform = GuestPlatformPOSIX
	}
	if command.Platform != GuestPlatformPOSIX && command.Platform != GuestPlatformWindows {
		return nil, fmt.Errorf("vmrunner/vsock: unsupported guest platform %q", command.Platform)
	}
	c, err := dialGuest(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("vmrunner/vsock: dial guest: %w", err)
	}
	// Close the connection when ctx ends during setup.
	setupDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-setupDone:
		}
	}()
	fail := func(err error) (GuestSession, error) {
		close(setupDone)
		_ = c.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w (%v)", err, ctx.Err())
		}
		return nil, err
	}
	guest, err := t.handshake(c, vmguest.Hello{User: creds.User, CreateAdministrator: command.CreateAdministrator})
	if err != nil {
		return fail(err)
	}
	for _, file := range command.Files {
		if err := validateGuestFile(file); err != nil {
			return fail(err)
		}
		if err := sendGuestFile(c, file); err != nil {
			return fail(err)
		}
	}
	for _, tree := range command.Trees {
		if err := sendGuestTree(c, tree); err != nil {
			return fail(err)
		}
	}
	req := vmguest.StartRequest{Args: command.Args, Env: command.Env, Dir: command.WorkingDir, User: creds.User}
	for _, result := range command.Results {
		if strings.TrimSpace(result.SourcePath) == "" || strings.TrimSpace(result.DestinationPath) == "" {
			return fail(errors.New("vmrunner/vsock: guest result paths must not be empty"))
		}
		req.Results = append(req.Results, vmguest.ResultSpec{Path: result.SourcePath, MaxBytes: resultLimit(result)})
	}
	if err := vmguest.WriteJSON(c, vmguest.FrameStart, req); err != nil {
		return fail(fmt.Errorf("vmrunner/vsock: start command: %w", err))
	}
	close(setupDone)
	s := &vsockSession{
		conn:    c,
		stdout:  newBufferedPipe(sessionPipeLimit),
		stderr:  newBufferedPipe(sessionPipeLimit),
		results: command.Results,
		done:    make(chan struct{}),
	}
	// One line in the job log and the worker log tells which account runs
	// the command. An older guest daemon does not report it.
	if guest.User != "" && guest.Administrator != nil {
		line := vmguest.AccountLine(guest.User, *guest.Administrator)
		_, _ = s.stderr.Write([]byte("reactorcide: " + line + "\n"))
		logging.Log.WithField("job_id", command.JobID).Info("vmrunner: guest command " + line)
	}
	go s.readLoop()
	return s, nil
}

func resultLimit(r GuestResultFile) int64 {
	if r.MaxBytes <= 0 {
		return 1 << 20
	}
	return r.MaxBytes
}

func readAck(c net.Conn) error {
	typ, payload, err := vmguest.ReadFrame(c)
	if err != nil {
		return fmt.Errorf("vmrunner/vsock: read acknowledgement: %w", err)
	}
	switch typ {
	case vmguest.FrameAck:
		return nil
	case vmguest.FrameError:
		return fmt.Errorf("vmrunner/vsock: guest: %s", payload)
	}
	return fmt.Errorf("vmrunner/vsock: expected acknowledgement, got frame %q", typ)
}

func sendGuestFile(c net.Conn, file GuestFile) error {
	mode := file.Mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	if err := vmguest.WriteJSON(c, vmguest.FrameFile, vmguest.FileHeader{Path: file.Path, Mode: uint32(mode)}); err != nil {
		return fmt.Errorf("vmrunner/vsock: send guest file: %w", err)
	}
	w := &vmguest.DataWriter{W: c, Type: vmguest.FrameData}
	if _, err := w.Write(file.Data); err != nil {
		return fmt.Errorf("vmrunner/vsock: send guest file: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("vmrunner/vsock: send guest file: %w", err)
	}
	return readAck(c)
}

func sendGuestTree(c net.Conn, tree GuestTree) error {
	info, err := os.Stat(tree.SourcePath)
	if err != nil {
		return fmt.Errorf("vmrunner/vsock: inspect guest tree source: %w", err)
	}
	if !info.IsDir() {
		return errors.New("vmrunner/vsock: guest tree source must be a directory")
	}
	if strings.TrimSpace(tree.Destination) == "" {
		return errors.New("vmrunner/vsock: guest tree destination must not be empty")
	}
	if err := vmguest.WriteJSON(c, vmguest.FrameTree, vmguest.TreeHeader{Dest: tree.Destination}); err != nil {
		return fmt.Errorf("vmrunner/vsock: send guest tree: %w", err)
	}
	w := &vmguest.DataWriter{W: c, Type: vmguest.FrameData}
	if err := writeTarTree(w, tree.SourcePath); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("vmrunner/vsock: send guest tree: %w", err)
	}
	return readAck(c)
}

// writeTarTree streams root as a tar archive. It does not follow links and
// does not load the tree into memory.
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
		return fmt.Errorf("vmrunner/vsock: archive guest tree: %w", walkErr)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("vmrunner/vsock: finish guest tree archive: %w", err)
	}
	return nil
}

// sessionPipeLimit bounds the output that the session holds when a reader
// is slow. A full pipe stops the connection reader, so the guest waits.
const sessionPipeLimit = 4 << 20

// vsockSession is one running guest command.
type vsockSession struct {
	conn    net.Conn
	wmu     sync.Mutex
	stdout  *bufferedPipe
	stderr  *bufferedPipe
	results []GuestResultFile

	done chan struct{}
	code int
	err  error

	closeOnce sync.Once
}

var _ GuestSession = (*vsockSession)(nil)

func (s *vsockSession) Stdout() io.Reader { return s.stdout }
func (s *vsockSession) Stderr() io.Reader { return s.stderr }

func (s *vsockSession) readLoop() {
	var resultErr error
	defer func() {
		s.stdout.CloseWithError(nil)
		s.stderr.CloseWithError(nil)
		close(s.done)
	}()
	for {
		typ, payload, err := vmguest.ReadFrame(s.conn)
		if err != nil {
			s.code, s.err = -1, fmt.Errorf("vmrunner/vsock: the guest connection ended without an exit status: %w", err)
			return
		}
		switch typ {
		case vmguest.FrameStdout:
			_, _ = s.stdout.Write(payload)
		case vmguest.FrameStderr:
			_, _ = s.stderr.Write(payload)
		case vmguest.FrameResult:
			var h vmguest.ResultHeader
			if err := json.Unmarshal(payload, &h); err != nil {
				s.code, s.err = -1, fmt.Errorf("vmrunner/vsock: bad result header: %w", err)
				return
			}
			err := s.receiveResult(h)
			var protoErr *protocolError
			if errors.As(err, &protoErr) {
				s.code, s.err = -1, err
				return
			}
			if err != nil && resultErr == nil {
				resultErr = err
			}
		case vmguest.FrameExit:
			var st vmguest.ExitStatus
			if err := json.Unmarshal(payload, &st); err != nil {
				s.code, s.err = -1, fmt.Errorf("vmrunner/vsock: bad exit status: %w", err)
				return
			}
			s.code, s.err = st.Code, resultErr
			if st.Error != "" && s.err == nil {
				s.err = fmt.Errorf("vmrunner/vsock: guest: %s", st.Error)
			}
			return
		case vmguest.FrameError:
			s.code, s.err = -1, fmt.Errorf("vmrunner/vsock: guest: %s", payload)
			return
		}
	}
}

type protocolError struct{ err error }

func (e *protocolError) Error() string { return e.err.Error() }
func (e *protocolError) Unwrap() error { return e.err }

// receiveResult reads one result file. A protocolError means that the
// connection cannot continue.
func (s *vsockSession) receiveResult(h vmguest.ResultHeader) error {
	if h.Index < 0 || h.Index >= len(s.results) {
		return &protocolError{fmt.Errorf("vmrunner/vsock: result index %d is not valid", h.Index)}
	}
	result := s.results[h.Index]
	switch {
	case h.TooLarge:
		return fmt.Errorf("vmrunner/vsock: guest result exceeds %d bytes", resultLimit(result))
	case h.Missing && h.Error != "":
		return fmt.Errorf("vmrunner/vsock: read guest result: %s", h.Error)
	case h.Missing && result.Optional:
		return nil
	case h.Missing:
		return errors.New("vmrunner/vsock: required guest result is missing")
	}
	r := &vmguest.DataReader{R: s.conn}
	limit := resultLimit(result)
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return &protocolError{fmt.Errorf("vmrunner/vsock: read guest result: %w", err)}
	}
	if err := r.Drain(); err != nil {
		return &protocolError{fmt.Errorf("vmrunner/vsock: read guest result: %w", err)}
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("vmrunner/vsock: guest result exceeds %d bytes", limit)
	}
	return writeGuestResult(result.DestinationPath, data)
}

// Wait blocks until the command exits and the result files are on the host.
func (s *vsockSession) Wait() (int, error) {
	<-s.done
	return s.code, s.err
}

// Signal asks the guest daemon to signal the process group of the command.
func (s *vsockSession) Signal(sig string) error {
	name, err := vmguest.NormalizeSignal(sig)
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := vmguest.WriteJSON(s.conn, vmguest.FrameSignal, vmguest.SignalRequest{Signal: name}); err != nil {
		return fmt.Errorf("vmrunner/vsock: send signal: %w", err)
	}
	return nil
}

// Close closes the connection. The guest daemon stops a command that is
// still running when its connection closes.
func (s *vsockSession) Close() error {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
		s.stdout.CloseWithError(nil)
		s.stderr.CloseWithError(nil)
	})
	return nil
}

// bufferedPipe is an in-memory pipe with a buffer of up to max bytes, so
// that a reader of one stream does not stop the other stream.
type bufferedPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	max    int
	closed bool
	err    error
}

func newBufferedPipe(max int) *bufferedPipe {
	p := &bufferedPipe{max: max}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bufferedPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) >= p.max && !p.closed {
		p.cond.Wait()
	}
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

func (p *bufferedPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 {
		if p.err != nil {
			return 0, p.err
		}
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	p.cond.Broadcast()
	return n, nil
}

// CloseWithError ends the stream. A reader gets the buffered bytes, then err
// (io.EOF when err is nil).
func (p *bufferedPipe) CloseWithError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed, p.err = true, err
	}
	p.cond.Broadcast()
}
