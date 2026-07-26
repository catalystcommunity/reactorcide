// Cluster-aware transport for the corndogs Go client: leader-following for a
// Tier-1 clustered deployment (docs/clustering-tier1.md). Single-node clients use
// New(addr); clustered clients use NewCluster(seeds...) where seeds are nodes'
// CSIL-RPC TCP addresses (host:port).
//
// It needs no separate discovery channel: a write that lands on a follower returns
// a "not-leader leader=<addr>" redirect, so the client learns the leader and caches
// it; on a connection failure it rotates to the next seed. A not-leader response
// means the write was rejected before it executed, so retrying is safe. It keeps a
// persistent, multiplexed StreamTransport per node it talks to.
package corndogs

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ClusterTransport implements Transport with leader-following over a set of seed
// node TCP addresses.
type ClusterTransport struct {
	Seeds []string

	mu         sync.Mutex
	transports map[string]*StreamTransport // addr -> persistent connection
	leader     string
	seedIdx    int
}

// NewCluster returns a CorndogsClient that follows leadership across the given seed
// node TCP addresses (e.g. "host1:5080", "host2:5080").
func NewCluster(seeds ...string) *CorndogsClient {
	return NewCorndogsClient(&ClusterTransport{Seeds: seeds, transports: map[string]*StreamTransport{}})
}

// transportFor returns (creating if needed) the persistent transport for addr.
func (t *ClusterTransport) transportFor(addr string) *StreamTransport {
	addr = dialAddr(addr)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.transports == nil {
		t.transports = map[string]*StreamTransport{}
	}
	tr := t.transports[addr]
	if tr == nil {
		tr = &StreamTransport{Addr: addr}
		t.transports[addr] = tr
	}
	return tr
}

// dialAddr normalizes an advertised address to a dialable host:port. The server
// advertises a bare host:port (see CORNDOGS_CLUSTER_RPC_ADVERTISE), but if an
// operator configures a URL form ("tcp://host:5080" or "http://host:5080") we strip
// the scheme (and any path) rather than fail every dial on it.
func dialAddr(addr string) string {
	if i := indexOf(addr, "://"); i >= 0 {
		addr = addr[i+len("://"):]
	}
	if sl := indexOf(addr, "/"); sl >= 0 {
		addr = addr[:sl]
	}
	return addr
}

// target returns the address to try next: the cached leader if known, else the
// next seed in rotation.
func (t *ClusterTransport) target() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.leader != "" {
		return t.leader
	}
	if len(t.Seeds) == 0 {
		return ""
	}
	a := t.Seeds[t.seedIdx%len(t.Seeds)]
	t.seedIdx++
	return a
}

func (t *ClusterTransport) setLeader(a string) { t.mu.Lock(); t.leader = a; t.mu.Unlock() }
func (t *ClusterTransport) clearLeader()       { t.mu.Lock(); t.leader = ""; t.mu.Unlock() }

// Call sends the request to the current leader, following not-leader redirects and
// rotating seeds on connection failure.
func (t *ClusterTransport) Call(ctx context.Context, service, op string, req []byte) ([]byte, error) {
	attempts := len(t.Seeds) + 5
	var lastErr error
	for i := 0; i < attempts; i++ {
		addr := t.target()
		if addr == "" {
			return nil, &ClientError{Err: fmt.Errorf("cluster: no seeds configured")}
		}
		resp, err := t.transportFor(addr).Call(ctx, service, op, req)
		if err == nil {
			t.setLeader(addr)
			return resp, nil
		}
		lastErr = err
		if redir, ok := redirectLeader(err); ok {
			if redir != "" {
				t.setLeader(redir)
			} else {
				t.clearLeader()
			}
			continue
		}
		if isServiceError(err) {
			return nil, err // genuine application error — do not retry
		}
		t.clearLeader() // connection/transport failure — rotate to a seed
		sleepBackoff(ctx, i)
	}
	if lastErr == nil {
		lastErr = &ClientError{Err: fmt.Errorf("cluster: exhausted retries")}
	}
	return nil, lastErr
}

// Close closes every persistent connection the cluster transport opened.
func (t *ClusterTransport) Close() error {
	t.mu.Lock()
	trs := t.transports
	t.transports = map[string]*StreamTransport{}
	t.mu.Unlock()
	for _, tr := range trs {
		_ = tr.Close()
	}
	return nil
}

// redirectLeader detects a "not-leader leader=<addr>" redirect and extracts the
// leader address (which may be empty if the follower didn't know it yet).
func redirectLeader(err error) (string, bool) {
	msg := err.Error()
	if !contains(msg, "not-leader") {
		return "", false
	}
	if i := indexOf(msg, "leader="); i >= 0 {
		rest := msg[i+len("leader="):]
		if sp := indexAny(rest, " \"'"); sp >= 0 {
			rest = rest[:sp]
		}
		return rest, true
	}
	return "", true
}

func isServiceError(err error) bool {
	if ce, ok := err.(*ClientError); ok {
		return ce.Code != 0 || ce.Message != ""
	}
	return false
}

func sleepBackoff(ctx context.Context, attempt int) {
	d := time.Duration(50*(attempt+1)) * time.Millisecond
	if d > 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}
