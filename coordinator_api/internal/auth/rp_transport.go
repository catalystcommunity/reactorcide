package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"time"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	localrp "github.com/catalystcommunity/linkkeys/sdks/local-rp/go"
)

// maxRPFrameSize bounds a single CSIL-RPC frame, matching the local-RP
// SDK's own cap (sdks/local-rp/go/rpc.go's maxFrameSize) so a malicious or
// compromised RP server can't drive this client to an unbounded allocation
// via a forged length prefix.
const maxRPFrameSize = 1024 * 1024

// tcpRPTransport is the production RPTransport: a TLS connection pinned to
// a configured set of SPKI SHA-256 fingerprints (no CA chain — the pin is
// the entire trust anchor, exactly as example.md's dialPinnedTLS and
// sdks/local-rp/go/rpc.go's unexported dialTLS do), carrying CSIL-RPC calls
// to the "Rp" service with the API key in the envelope's auth field.
type tcpRPTransport struct {
	addr         string
	fingerprints []string
	apiKey       string
	dial         func(hostPort string) (net.Conn, error)
	dialTimeout  time.Duration
}

// newTCPRPTransport constructs the default production RPTransport.
func newTCPRPTransport(addr string, fingerprints []string, apiKey string) *tcpRPTransport {
	std := localrp.NewStdTransport()
	return &tcpRPTransport{
		addr:         addr,
		fingerprints: fingerprints,
		apiKey:       apiKey,
		dial:         std.Dial,
		dialTimeout:  30 * time.Second,
	}
}

func (t *tcpRPTransport) dialPinnedTLS() (*tls.Conn, error) {
	raw, err := t.dial(t.addr)
	if err != nil {
		return nil, err
	}

	hostname := t.addr
	if h, _, err := net.SplitHostPort(t.addr); err == nil {
		hostname = h
	}

	tlsCfg := &tls.Config{
		ServerName:         hostname,
		InsecureSkipVerify: true, // pinned verification below replaces the WebPKI chain
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("no peer certificate presented")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("bad certificate encoding: %w", err)
			}
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return fmt.Errorf("certificate is not within its validity period")
			}
			pub, ok := cert.PublicKey.(ed25519.PublicKey)
			if !ok {
				return fmt.Errorf("peer certificate is not an Ed25519 key")
			}
			fp := localrp.Fingerprint([]byte(pub))
			for _, want := range t.fingerprints {
				if fp == want {
					return nil
				}
			}
			return fmt.Errorf("certificate fingerprint %s does not match any pinned RP fingerprint", fp)
		},
	}

	conn := tls.Client(raw, tlsCfg)
	ctx, cancel := context.WithTimeout(context.Background(), t.dialTimeout)
	defer cancel()
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("TLS handshake with RP server: %w", err)
	}
	return conn, nil
}

// Call implements RPTransport: dial, TLS-pin, send one API-key-authenticated
// CSIL-RPC request to the "Rp" service, and return the response payload.
func (t *tcpRPTransport) Call(_ context.Context, op string, payload []byte) ([]byte, error) {
	conn, err := t.dialPinnedTLS()
	if err != nil {
		return nil, fmt.Errorf("dial RP: %w", err)
	}
	defer conn.Close()

	carrier := rpctransport.NewStreamCarrierWithMaxFrame(conn, maxRPFrameSize)

	req := rpctransport.NewRpcRequest("Rp", op, payload).WithAuth(t.apiKey)
	frame, err := req.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	if err := carrier.SendFrame(frame); err != nil {
		return nil, fmt.Errorf("send frame: %w", err)
	}

	respBytes, err := carrier.RecvFrame()
	if err != nil {
		return nil, fmt.Errorf("recv frame: %w", err)
	}
	if respBytes == nil {
		return nil, fmt.Errorf("connection closed before response")
	}

	resp, err := rpctransport.DecodeRpcResponse(respBytes)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if err := resp.AsTransportError(); err != nil {
		return nil, fmt.Errorf("Rp/%s: %w", op, err)
	}
	return resp.Payload, nil
}
