package corndogs

import (
	"net"
	"testing"
	"time"
)

// TestDialAddrStripsScheme guards corndogs 0.7.0's raw-TCP address handling: a
// scheme-bearing REACTORCIDE_CORNDOGS_BASE_URL such as "http://host:5080" must
// be reduced to a bare "host:port" before net.Dial, which otherwise fails with
// "too many colons in address". This is the regression that took down all k8s
// job dispatch (every corndogs SubmitTask/GetNextTaskGroup failed → jobs were
// marked failed at submit).
func TestDialAddrStripsScheme(t *testing.T) {
	cases := map[string]string{
		"http://corndogs.reactorcide.svc.cluster.local:5080": "corndogs.reactorcide.svc.cluster.local:5080",
		"tcp://corndogs:5080":   "corndogs:5080",
		"corndogs:5080":         "corndogs:5080",
		"http://host:5080/path": "host:5080",
	}
	for in, want := range cases {
		if got := dialAddr(in); got != want {
			t.Errorf("dialAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStreamTransportDialsSchemeBearingAddr guards the single-address
// StreamTransport path (csil.New, used for a single corndogs endpoint): its
// dial must strip the scheme via dialAddr, not use the raw Addr. Without the
// fix, ensureConn dials "http://127.0.0.1:PORT" and fails with "too many
// colons in address" — so a scheme-bearing corndogs URL never connects.
func TestStreamTransportDialsSchemeBearingAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()

	tr := &StreamTransport{Addr: "http://" + ln.Addr().String()}
	defer tr.Close()

	conn, err := tr.ensureConn()
	if err != nil {
		t.Fatalf("ensureConn(%q): %v (scheme not stripped?)", tr.Addr, err)
	}
	if conn == nil {
		t.Fatal("ensureConn returned a nil connection")
	}

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted a connection (scheme-bearing addr not dialed)")
	}
}
