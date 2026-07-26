// CSIL-RPC transport carrier for the corndogs Go library, over a persistent TCP
// StreamCarrier connection (the canonical corndogs client transport):
//
//	c := corndogs.New("corndogs.example.com:5080")
//	resp, err := c.SubmitTask(ctx, corndogs.SubmitTaskRequest{Queue: "q"})
//
// The wire is CSIL-RPC (csilgen docs/csil-rpc-transport.md) over a single TCP
// connection framed with the canonical 4-byte big-endian length prefix. One
// connection MULTIPLEXES many concurrent calls: each request carries a correlation
// `id`, a background reader matches each response back to its waiting caller, so
// concurrent SubmitTask/GetNextTask calls do not head-of-line-block each other.
// The connection is dialed lazily and re-dialed on failure.
//
// This package is the single corndogs Go library: generated types, codec, the
// CorndogsService interface (implemented by the server) and the typed
// CorndogsClient (used by Go callers), plus this hand-written carrier. The carrier
// owns only the CSIL-RPC envelope + framing + connection; the generated codec owns
// (de)serialization.
package corndogs

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	tagEncodedCBOR   = 24              // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
	maxFrame         = 256 << 20       // frame size guard
	defaultDialWait  = 5 * time.Second // connect timeout
	controlServiceHB = "CorndogsService"
	opPing           = "$ping" // control-plane heartbeat op (never collides with app ops)
)

// StreamTransport implements Transport over a persistent, multiplexed TCP
// connection. Safe for concurrent use.
type StreamTransport struct {
	Addr        string        // host:port
	DialTimeout time.Duration // 0 => defaultDialWait

	mu      sync.Mutex // guards conn, pending, nextID, closed
	conn    net.Conn
	nextID  uint64
	pending map[uint64]chan rpcResult
	closed  bool

	writeMu sync.Mutex // serializes frame writes on the shared connection

	hbStop chan struct{}
}

type rpcResult struct {
	payload []byte
	err     error
}

// New returns a CorndogsClient wired to a StreamTransport at addr (host:port).
func New(addr string) *CorndogsClient {
	return NewCorndogsClient(&StreamTransport{Addr: addr})
}

func (t *StreamTransport) dialTimeout() time.Duration {
	if t.DialTimeout > 0 {
		return t.DialTimeout
	}
	return defaultDialWait
}

// ensureConn returns a live connection, dialing (and starting the reader) if
// needed.
func (t *StreamTransport) ensureConn() (net.Conn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, fmt.Errorf("corndogs: transport closed")
	}
	if t.conn != nil {
		return t.conn, nil
	}
	conn, err := net.DialTimeout("tcp", t.Addr, t.dialTimeout())
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}
	t.conn = conn
	t.pending = map[uint64]chan rpcResult{}
	go t.readLoop(conn)
	return conn, nil
}

// teardown closes the given connection (if it is still current) and fails all
// pending calls, so the next call re-dials.
func (t *StreamTransport) teardown(conn net.Conn, cause error) {
	t.mu.Lock()
	if t.conn != conn {
		t.mu.Unlock()
		return
	}
	pending := t.pending
	t.conn = nil
	t.pending = nil
	t.mu.Unlock()
	_ = conn.Close()
	for _, ch := range pending {
		select {
		case ch <- rpcResult{err: cause}:
		default:
		}
	}
}

// readLoop reads response frames off one connection and delivers each to its
// waiting caller by correlation id.
func (t *StreamTransport) readLoop(conn net.Conn) {
	for {
		frame, err := readFrame(conn, maxFrame)
		if err != nil || frame == nil {
			t.teardown(conn, fmt.Errorf("corndogs: connection closed: %w", errOrEOF(err)))
			return
		}
		id, payload, rerr := parseResponse(frame)
		t.mu.Lock()
		ch := t.pending[id]
		if ch != nil {
			delete(t.pending, id)
		}
		t.mu.Unlock()
		if ch != nil {
			ch <- rpcResult{payload: payload, err: rerr}
		}
	}
}

// Call sends one request and waits for its correlated response (or ctx timeout).
func (t *StreamTransport) Call(ctx context.Context, service, op string, req []byte) ([]byte, error) {
	conn, err := t.ensureConn()
	if err != nil {
		return nil, &ClientError{Err: err}
	}

	t.mu.Lock()
	id := t.nextID + 1
	t.nextID = id
	ch := make(chan rpcResult, 1)
	if t.pending == nil { // torn down between ensureConn and here
		t.mu.Unlock()
		return nil, &ClientError{Err: fmt.Errorf("corndogs: connection lost")}
	}
	t.pending[id] = ch
	t.mu.Unlock()

	env := cborEncode(cborMap{
		{key: cborText("v"), val: cborUint(1)},
		{key: cborText("service"), val: cborText(service)},
		{key: cborText("op"), val: cborText(op)},
		{key: cborText("id"), val: cborUint(id)},
		{key: cborText("payload"), val: cborTag{num: tagEncodedCBOR, inner: cborBytes(req)}},
	})

	t.writeMu.Lock()
	werr := writeFrame(conn, env, maxFrame)
	t.writeMu.Unlock()
	if werr != nil {
		t.teardown(conn, werr)
		return nil, &ClientError{Err: werr}
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, &ClientError{Err: ctx.Err()}
	case res := <-ch:
		return res.payload, res.err
	}
}

// --- heartbeat -------------------------------------------------------------

// Ping sends a single control-plane heartbeat and returns an error if the server
// is unreachable. Cheap; keeps an idle connection alive and detects a dead server.
func (t *StreamTransport) Ping(ctx context.Context) error {
	_, err := t.Call(ctx, controlServiceHB, opPing, nil)
	return err
}

// RunHeartbeat is the SYNCHRONOUS heartbeat start: it blocks, pinging every
// interval, until ctx is cancelled (returns ctx.Err()) or a ping fails (returns
// that error). Run it in a goroutine you own, or use StartHeartbeat for the async
// form.
func (t *StreamTransport) RunHeartbeat(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := t.Ping(ctx); err != nil {
				return err
			}
		}
	}
}

// StartHeartbeat is the ASYNCHRONOUS heartbeat start: it launches the heartbeat in
// a background goroutine and returns a stop function. Calling stop (or Close) ends
// it. Only one background heartbeat runs at a time.
func (t *StreamTransport) StartHeartbeat(interval time.Duration) (stop func()) {
	t.mu.Lock()
	if t.hbStop != nil {
		existing := t.hbStop
		t.mu.Unlock()
		return func() { closeOnce(existing) }
	}
	stopCh := make(chan struct{})
	t.hbStop = stopCh
	t.mu.Unlock()

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-stopCh; cancel() }()
		defer cancel()
		_ = t.RunHeartbeat(ctx, interval)
	}()
	return func() {
		t.mu.Lock()
		if t.hbStop == stopCh {
			t.hbStop = nil
		}
		t.mu.Unlock()
		closeOnce(stopCh)
	}
}

// Close shuts the transport down: stops any heartbeat and closes the connection.
func (t *StreamTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	conn := t.conn
	hb := t.hbStop
	t.hbStop = nil
	t.mu.Unlock()
	if hb != nil {
		closeOnce(hb)
	}
	if conn != nil {
		t.teardown(conn, fmt.Errorf("corndogs: transport closed"))
	}
	return nil
}

// --- envelope + framing helpers --------------------------------------------

// parseResponse decodes a CsilRpcResponse frame into (id, inner-payload, err). A
// non-zero transport status or a "ServiceError" arm becomes a *ClientError.
func parseResponse(frame []byte) (uint64, []byte, error) {
	val, err := cborDecode(frame)
	if err != nil {
		return 0, nil, &ClientError{Err: fmt.Errorf("decode response envelope: %w", err)}
	}
	var id uint64
	if idv, ok := cborMapGet(val, "id"); ok {
		if i, e := cborAsI64(idv); e == nil && i >= 0 {
			id = uint64(i)
		}
	}
	if sv, ok := cborMapGet(val, "status"); ok {
		if status, e := cborAsI64(sv); e == nil && status != 0 {
			msg := ""
			if ev, ok := cborMapGet(val, "error"); ok {
				msg, _ = cborAsText(ev)
			}
			return id, nil, &ClientError{Err: fmt.Errorf("transport status %d: %s", status, msg)}
		}
	}
	pv, ok := cborMapGet(val, "payload")
	if !ok {
		return id, nil, nil // control replies (e.g. $pong) may carry no payload
	}
	if tag, ok := pv.(cborTag); ok {
		pv = tag.inner
	}
	inner, err := cborAsBytes(pv)
	if err != nil {
		return id, nil, &ClientError{Err: fmt.Errorf("response payload is not a tag-24 byte string: %w", err)}
	}
	if vv, ok := cborMapGet(val, "variant"); ok {
		if variant, _ := cborAsText(vv); variant == "ServiceError" {
			if se, derr := DecodeServiceError(inner); derr == nil {
				return id, nil, &ClientError{Code: int64(se.Code), Message: se.Message}
			}
			return id, nil, &ClientError{Err: fmt.Errorf("undecodable ServiceError payload")}
		}
	}
	return id, inner, nil
}

func writeFrame(w io.Writer, b []byte, max int) error {
	if len(b) > max {
		return fmt.Errorf("corndogs: frame too large (%d > %d)", len(b), max)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(b)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r io.Reader, max int) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if uint64(n) > uint64(max) {
		return nil, fmt.Errorf("corndogs: frame too large (%d)", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func errOrEOF(err error) error {
	if err == nil {
		return io.EOF
	}
	return err
}

func closeOnce(ch chan struct{}) {
	defer func() { _ = recover() }() // tolerate a double close
	close(ch)
}
