package test

// Minimal, self-contained CSIL-RPC envelope-in-body helpers for driving the
// real, router-mounted uiapi.NewHandler(...) (mounted at uiapi.RpcPath by
// handlers.createAppMux) end-to-end from coordinator_api/test's UI-auth
// integration tests.
//
// coordinator_api/internal/uiapi/csilapi already exposes full bidirectional
// codecs (Encode<Type>Request/Decode<Type>Response etc.) for every request/
// response type, but the envelope-in-body wire shape itself
// ({v, service, op, payload: tag24(cbor), ?auth} -> {v, status, variant,
// ?error, ?payload}) lives in uiapi's own unexported cbor_envelope.go, which
// this file (package "test", not "uiapi") cannot import. This is the same
// situation coordinator_api/internal/uiapi/dispatcher_test.go documents for
// itself (see that file's header comment) — reimplemented independently here
// since these are two different Go packages in the same module.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/stretchr/testify/require"
)

func cborUintBytes(major byte, n uint64) []byte {
	mt := major << 5
	switch {
	case n < 24:
		return []byte{mt | byte(n)}
	case n < 0x100:
		return []byte{mt | 24, byte(n)}
	case n < 0x10000:
		b := make([]byte, 3)
		b[0] = mt | 25
		binary.BigEndian.PutUint16(b[1:], uint16(n))
		return b
	default:
		b := make([]byte, 5)
		b[0] = mt | 26
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b
	}
}

func cborTextItem(s string) []byte {
	return append(cborUintBytes(3, uint64(len(s))), []byte(s)...)
}

func cborBytesItem(b []byte) []byte {
	return append(cborUintBytes(2, uint64(len(b))), b...)
}

func cborTag24Item(b []byte) []byte {
	return append(cborUintBytes(6, 24), cborBytesItem(b)...)
}

// buildCSILRequestEnvelope encodes one CsilRpcRequest envelope
// (csil-rpc-transport.md §1.1), mirroring
// uiapi/dispatcher_test.go's testBuildRequestEnvelope.
func buildCSILRequestEnvelope(service, op string, payload []byte, auth string, hasAuth bool) []byte {
	n := uint64(4)
	if hasAuth {
		n = 5
	}
	var buf bytes.Buffer
	buf.Write(cborUintBytes(5, n)) // map header
	buf.Write(cborTextItem("v"))
	buf.Write(cborUintBytes(0, 1))
	buf.Write(cborTextItem("service"))
	buf.Write(cborTextItem(service))
	buf.Write(cborTextItem("op"))
	buf.Write(cborTextItem(op))
	buf.Write(cborTextItem("payload"))
	buf.Write(cborTag24Item(payload))
	if hasAuth {
		buf.Write(cborTextItem("auth"))
		buf.Write(cborTextItem(auth))
	}
	return buf.Bytes()
}

// --- minimal CBOR value-tree decoder, scoped to exactly what a
// CsilRpcResponse envelope can contain (uint, text, bytes, map, tag24). ---

type cborVal interface{ isCborVal() }
type cborValUint uint64
type cborValText string
type cborValBytes []byte
type cborValMap []cborValEntry
type cborValTag struct {
	num   uint64
	inner cborVal
}
type cborValEntry struct{ key, val cborVal }

func (cborValUint) isCborVal()  {}
func (cborValText) isCborVal()  {}
func (cborValBytes) isCborVal() {}
func (cborValMap) isCborVal()   {}
func (cborValTag) isCborVal()   {}

func cborDecodeArg(b []byte, pos *int, low byte) (uint64, error) {
	if low < 24 {
		*pos++
		return uint64(low), nil
	}
	switch low {
	case 24:
		v := uint64(b[*pos+1])
		*pos += 2
		return v, nil
	case 25:
		v := uint64(b[*pos+1])<<8 | uint64(b[*pos+2])
		*pos += 3
		return v, nil
	case 26:
		var v uint64
		for i := 1; i <= 4; i++ {
			v = v<<8 | uint64(b[*pos+i])
		}
		*pos += 5
		return v, nil
	case 27:
		var v uint64
		for i := 1; i <= 8; i++ {
			v = v<<8 | uint64(b[*pos+i])
		}
		*pos += 9
		return v, nil
	default:
		return 0, fmt.Errorf("unsupported cbor additional info %d", low)
	}
}

func cborDecodeVal(b []byte, pos *int) (cborVal, error) {
	if *pos >= len(b) {
		return nil, fmt.Errorf("unexpected end of cbor input")
	}
	ib := b[*pos]
	major := ib >> 5
	low := ib & 0x1f
	arg, err := cborDecodeArg(b, pos, low)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		return cborValUint(arg), nil
	case 2:
		n := int(arg)
		v := make([]byte, n)
		copy(v, b[*pos:*pos+n])
		*pos += n
		return cborValBytes(v), nil
	case 3:
		n := int(arg)
		s := string(b[*pos : *pos+n])
		*pos += n
		return cborValText(s), nil
	case 5:
		n := int(arg)
		entries := make(cborValMap, 0, n)
		for i := 0; i < n; i++ {
			k, err := cborDecodeVal(b, pos)
			if err != nil {
				return nil, err
			}
			v, err := cborDecodeVal(b, pos)
			if err != nil {
				return nil, err
			}
			entries = append(entries, cborValEntry{k, v})
		}
		return entries, nil
	case 6:
		inner, err := cborDecodeVal(b, pos)
		if err != nil {
			return nil, err
		}
		return cborValTag{num: arg, inner: inner}, nil
	default:
		return nil, fmt.Errorf("unsupported cbor major type %d", major)
	}
}

func cborMapLookup(v cborVal, key string) (cborVal, bool) {
	m, ok := v.(cborValMap)
	if !ok {
		return nil, false
	}
	for _, e := range m {
		if k, ok := e.key.(cborValText); ok && string(k) == key {
			return e.val, true
		}
	}
	return nil, false
}

type csilResponseEnvelope struct {
	status     int64
	variant    string
	errMsg     string
	payload    []byte
	hasPayload bool
}

func parseCSILResponseEnvelope(t *testing.T, body []byte) csilResponseEnvelope {
	t.Helper()
	pos := 0
	root, err := cborDecodeVal(body, &pos)
	require.NoError(t, err, "decode CSIL-RPC response envelope")

	var out csilResponseEnvelope
	sv, ok := cborMapLookup(root, "status")
	require.True(t, ok, "response envelope missing status")
	su, ok := sv.(cborValUint)
	require.True(t, ok, "response envelope status is not a uint")
	out.status = int64(su)

	if vv, ok := cborMapLookup(root, "variant"); ok {
		vt, ok := vv.(cborValText)
		require.True(t, ok)
		out.variant = string(vt)
	}
	if ev, ok := cborMapLookup(root, "error"); ok {
		et, ok := ev.(cborValText)
		require.True(t, ok)
		out.errMsg = string(et)
	}
	if pv, ok := cborMapLookup(root, "payload"); ok {
		if tag, ok := pv.(cborValTag); ok {
			pv = tag.inner
		}
		pb, ok := pv.(cborValBytes)
		require.True(t, ok, "response envelope payload is not a byte string")
		out.payload = []byte(pb)
		out.hasPayload = true
	}
	return out
}

// csilCall drives one CSIL-RPC op through the real, router-mounted
// uiapi.Handler over net/http/httptest, using mux directly (no network
// listener needed: mux.ServeHTTP is the same handler chain
// handlers.GetAppMux()/GetTestMux() serve in production). token is the
// caller's session token ("" for an anonymous/session-less call). Returns
// the decoded success response, or (zero value, non-nil ServiceError) for
// the CSIL-RPC "ServiceError" response variant. Any transport-level failure
// (malformed envelope, unknown route, handler panic) fails the test
// immediately via require, since none of these integration tests expect one.
func csilCall[Req any, Resp any](
	t *testing.T,
	mux http.Handler,
	service, op string,
	encode func(Req) []byte,
	decode func([]byte) (Resp, error),
	req Req,
	token string,
) (Resp, *csilapi.ServiceError) {
	t.Helper()

	hasAuth := token != ""
	body := buildCSILRequestEnvelope(service, op, encode(req), token, hasAuth)

	httpReq := httptest.NewRequest(http.MethodPost, uiapi.RpcPath, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/cbor")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httpReq)
	require.Equal(t, http.StatusOK, rr.Code, "CSIL-RPC envelope-in-body responses are always HTTP 200")

	env := parseCSILResponseEnvelope(t, rr.Body.Bytes())
	require.Equal(t, int64(0), env.status, "CSIL-RPC transport error calling %s/%s: %s", service, op, env.errMsg)

	if env.variant == "ServiceError" {
		require.True(t, env.hasPayload, "ServiceError response has no payload")
		se, err := csilapi.DecodeServiceError(env.payload)
		require.NoError(t, err, "decode ServiceError payload")
		var zero Resp
		return zero, &se
	}

	require.True(t, env.hasPayload, "success response %s/%s has no payload", service, op)
	resp, err := decode(env.payload)
	require.NoError(t, err, "decode %s response", op)
	return resp, nil
}
