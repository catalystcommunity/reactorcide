// Package vmguest is the guest daemon of the "vm" job backend and the wire
// protocol that the worker uses to talk to it.
//
// A VM guest has no network card. The worker reaches the guest through a
// hypervisor socket (vsock on macOS Virtualization.framework, hvsock on
// Hyper-V). The guest daemon listens on JobPort. Each connection runs one
// command: the worker sends files, directory trees, and a start request, and
// the daemon streams stdout, stderr, result files, and the exit status back.
//
// The guest also has no route to the network. Each guest TCP flow becomes an
// HTTP CONNECT request on a new hypervisor socket connection to EgressPort on
// the host. The host egress forwarder (vmrunner) dials the destination, or
// sends the flow to an egress proxy.
//
// The frame format and the egress parts are adapted from the Magi envd
// (primitives/execution/envd).
package vmguest

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is the version of the job channel protocol. The worker and
// the guest daemon must use the same version. Change it when a frame or a
// message changes in a way that an older peer cannot read.
const ProtocolVersion = 1

// Ports of the hypervisor sockets. Hyper-V uses the vsock service GUID
// template for these ports (go-winio VsockServiceID).
const (
	// JobPort is the guest port of the job channel.
	JobPort uint32 = 7100
	// EgressPort is the host port of the egress tunnel.
	EgressPort uint32 = 7101
)

// HostCID is VMADDR_CID_HOST: the vsock address of the host.
const HostCID uint32 = 2

// A frame is one type byte, a 4-byte big-endian payload length, and the
// payload:
//
//	+------+-----------------+-------------+
//	| type | length (u32 BE) | payload ... |
//	+------+-----------------+-------------+
//
// Worker to guest:
//
//	'H' hello       JSON Hello
//	'F' file        JSON FileHeader, then data frames, then an empty data frame
//	'T' tree        JSON TreeHeader, then data frames (a tar stream), then an empty data frame
//	'S' start       JSON StartRequest; the command starts
//	'K' signal      JSON SignalRequest
//
// Guest to worker:
//
//	'H' hello       JSON Hello
//	'A' ack         no payload: the last file or tree is in place
//	'E' error       UTF-8 text; the last frame of the connection
//	'O' stdout      output bytes
//	'R' stderr      output bytes
//	'F' result      JSON ResultHeader, then data frames, then an empty data frame
//	'X' exit        JSON ExitStatus; the last frame of the connection
//
// 'D' (data) frames carry file, tree, and result bytes in both directions.
const (
	FrameHello  byte = 'H'
	FrameFile   byte = 'F'
	FrameTree   byte = 'T'
	FrameStart  byte = 'S'
	FrameSignal byte = 'K'
	FrameData   byte = 'D'
	FrameAck    byte = 'A'
	FrameError  byte = 'E'
	FrameStdout byte = 'O'
	FrameStderr byte = 'R'
	FrameResult byte = 'F'
	FrameExit   byte = 'X'
)

// MaxFrame is the largest frame payload.
const MaxFrame = 64 << 10

// ErrFrameTooLarge is returned for a frame with a payload above MaxFrame.
var ErrFrameTooLarge = errors.New("vmguest: frame larger than 64 KiB")

// WriteFrame writes one frame with a single Write call, so frames of two
// writers that use a lock do not mix.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrFrameTooLarge
	}
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], uint32(len(payload)))
	copy(b[5:], payload)
	_, err := w.Write(b)
	return err
}

// WriteJSON writes one frame with a JSON payload.
func WriteJSON(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFrame(w, typ, b)
}

// ReadFrame reads one frame. The payload is a new slice.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:5])
	if n > MaxFrame {
		return 0, nil, ErrFrameTooLarge
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return h[0], p, nil
}

// DataWriter splits a byte stream into frames of type typ. Close writes the
// empty end frame. Lock is held for each frame when it is not nil.
type DataWriter struct {
	W    io.Writer
	Type byte
	Lock interface {
		Lock()
		Unlock()
	}
}

func (d *DataWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > MaxFrame {
			chunk = chunk[:MaxFrame]
		}
		if err := d.frame(chunk); err != nil {
			return n, err
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}

// Close writes the empty frame that ends a data stream.
func (d *DataWriter) Close() error { return d.frame(nil) }

func (d *DataWriter) frame(p []byte) error {
	if d.Lock != nil {
		d.Lock.Lock()
		defer d.Lock.Unlock()
	}
	return WriteFrame(d.W, d.Type, p)
}

// DataReader reads the data frames of one stream until the empty end frame.
// Any other frame type is a protocol error.
type DataReader struct {
	R    io.Reader
	buf  []byte
	done bool
}

func (d *DataReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			return 0, io.EOF
		}
		typ, payload, err := ReadFrame(d.R)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		if typ != FrameData {
			return 0, fmt.Errorf("vmguest: frame %q in a data stream", typ)
		}
		if len(payload) == 0 {
			d.done = true
			continue
		}
		d.buf = payload
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

// Drain reads the rest of the stream.
func (d *DataReader) Drain() error {
	_, err := io.Copy(io.Discard, d)
	return err
}
