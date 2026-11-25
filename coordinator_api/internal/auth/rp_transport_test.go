package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	localrp "github.com/catalystcommunity/linkkeys/sdks/local-rp/go"
	api "github.com/catalystcommunity/linkkeys/sdks/local-rp/go/generated"
)

// startFakeRPServer serves one CSIL-RPC reply on a TLS listener with an
// Ed25519 certificate, and returns its address and pinned fingerprint.
func startFakeRPServer(t *testing.T, reply rpctransport.RpcResponse) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rp.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	frame, err := reply.Encode()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		carrier, err := rpctransport.NewStreamCarrierWithMaxFrame(conn, maxRPFrameSize)
		if err != nil {
			return
		}
		if _, err := carrier.RecvFrame(); err != nil {
			return
		}
		_ = carrier.SendFrame(frame)
	}()
	return ln.Addr().String(), localrp.Fingerprint([]byte(pub))
}

func newTestTCPRPTransport(addr, fingerprint string) *tcpRPTransport {
	return &tcpRPTransport{
		addr:         addr,
		fingerprints: []string{fingerprint},
		apiKey:       "test-key",
		dial:         func(hostPort string) (net.Conn, error) { return net.Dial("tcp", hostPort) },
		dialTimeout:  5 * time.Second,
	}
}

func TestTCPRPTransportReturnsSuccessVariantPayload(t *testing.T) {
	payload := mustEncode(api.EncodeRpSignResponse(api.RpSignResponse{SignedRequest: "signed"}))
	addr, fp := startFakeRPServer(t, rpctransport.NewRpcResponseOk("RpSignResponse", payload))

	got, err := newTestTCPRPTransport(addr, fp).Call(context.Background(), "sign-request", nil)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	resp, err := api.DecodeRpSignResponse(got)
	if err != nil || resp.SignedRequest != "signed" {
		t.Fatalf("decoded reply = %+v, %v", resp, err)
	}
}

// Since LinkKeys 0.16.11 an Rp refusal is the RpError reply variant with
// status 0. The transport must return it as an error, not as a payload.
func TestTCPRPTransportReturnsRpErrorVariantAsError(t *testing.T) {
	payload := mustEncode(api.EncodeRpError(api.RpError{Code: api.RpErrorCodeApplicationMismatch, Message: "token is for another application"}))
	addr, fp := startFakeRPServer(t, rpctransport.NewRpcResponseOk("RpError", payload))

	_, err := newTestTCPRPTransport(addr, fp).Call(context.Background(), "decrypt-token", nil)
	var rpErr *RPError
	if !errors.As(err, &rpErr) {
		t.Fatalf("Call() error = %v, want *RPError", err)
	}
	if rpErr.Code != api.RpErrorCodeApplicationMismatch || rpErr.Op != "decrypt-token" {
		t.Errorf("RPError = %+v", rpErr)
	}
	if !strings.Contains(err.Error(), "application_mismatch") {
		t.Errorf("error %q does not name the code", err)
	}
}
