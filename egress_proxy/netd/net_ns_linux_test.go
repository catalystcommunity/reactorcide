package netd

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// envNSChild marks the test process that runs as root of a new user
// namespace with a new network namespace (unshare -rn).
const envNSChild = "REACTORCIDE_NETD_NS_CHILD"

// TestNamespaceInboundBlocked installs the real rules in a network
// namespace that has a veth link to a second namespace (the "outside").
// It proves that the job can still talk to the proxy, and that a host
// outside cannot open a connection to the job and receive data on it (the
// inbound bypass of the guard). It runs unprivileged with unshare -rn and
// is skipped when unprivileged user namespaces are not available.
func TestNamespaceInboundBlocked(t *testing.T) {
	if os.Getenv(envNSChild) != "1" {
		if _, err := exec.LookPath("unshare"); err != nil {
			t.Skip("no unshare")
		}
		if err := exec.Command("unshare", "-rn", "true").Run(); err != nil {
			t.Skipf("no unprivileged user and network namespaces: %v", err)
		}
		cmd := exec.Command("unshare", "-rn", "--", os.Args[0], "-test.run", "^TestNamespaceInboundBlocked$", "-test.v")
		cmd.Env = append(os.Environ(), envNSChild+"=1")
		out, err := cmd.CombinedOutput()
		t.Logf("namespace child:\n%s", out)
		if err != nil {
			t.Fatalf("namespace child failed: %v", err)
		}
		return
	}
	nsChild(t)
}

// inner is a goroutine locked to a thread in the job network namespace.
// Each function sent to it runs in that namespace; sockets made there stay
// in that namespace.
type inner struct{ run chan func() }

func (n inner) do(f func()) {
	done := make(chan struct{})
	n.run <- func() { f(); close(done) }
	<-done
}

func nsChild(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	lo, err := netlink.LinkByName("lo")
	if err != nil || netlink.LinkSetUp(lo) != nil {
		t.Fatalf("outside loopback: %v", err)
	}

	job := inner{run: make(chan func())}
	nsFd := make(chan int)
	go func() {
		// The thread is never unlocked, so it ends with the goroutine and
		// no other goroutine runs in the job namespace.
		runtime.LockOSThread()
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			nsFd <- -1
			return
		}
		fd, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			fd = -1
		}
		nsFd <- fd
		for f := range job.run {
			f()
		}
	}()
	fd := <-nsFd
	if fd < 0 {
		t.Fatal("job network namespace")
	}
	defer close(job.run)

	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "rcout0"}, PeerName: "rcjob0"}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("veth: %v", err)
	}
	peer, err := netlink.LinkByName("rcjob0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(peer, fd); err != nil {
		t.Fatalf("move veth: %v", err)
	}
	outAddr, _ := netlink.ParseAddr("10.9.0.1/24")
	if err := netlink.AddrAdd(veth, outAddr); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(veth); err != nil {
		t.Fatal(err)
	}
	var setupErr error
	job.do(func() {
		for _, step := range []func() error{
			func() error {
				l, err := netlink.LinkByName("lo")
				if err != nil {
					return err
				}
				return netlink.LinkSetUp(l)
			},
			func() error {
				l, err := netlink.LinkByName("rcjob0")
				if err != nil {
					return err
				}
				a, _ := netlink.ParseAddr("10.9.0.2/24")
				if err := netlink.AddrAdd(l, a); err != nil {
					return err
				}
				if err := netlink.LinkSetUp(l); err != nil {
					return err
				}
				return netlink.RouteAdd(&netlink.Route{LinkIndex: l.Attrs().Index, Gw: net.ParseIP("10.9.0.1")})
			},
		} {
			if setupErr = step(); setupErr != nil {
				return
			}
		}
	})
	if setupErr != nil {
		t.Fatalf("job namespace network: %v", setupErr)
	}

	// exfil listens in the job namespace and writes a message to each
	// connection, as a job that serves data to an inbound connection.
	exfil := func(port string) net.Listener {
		var ln net.Listener
		var err error
		job.do(func() { ln, err = net.Listen("tcp", "10.9.0.2:"+port) })
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte("exfil-via-inbound-connection"))
				_ = c.Close()
			}
		}()
		return ln
	}
	fetch := func(port string) []byte {
		c, err := net.DialTimeout("tcp", "10.9.0.2:"+port, 2*time.Second)
		if err != nil {
			return nil
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		b, _ := io.ReadAll(c)
		return b
	}

	// Control: without the rules, the inbound connection carries data out.
	ln := exfil("9100")
	if got := fetch("9100"); !bytes.Contains(got, []byte("exfil")) {
		t.Fatalf("control: the harness must carry data without the rules, got %q", got)
	}
	ln.Close()

	job.do(func() { setupErr = installRules(netip.MustParseAddrPort("10.9.0.1:3128")) })
	if setupErr != nil {
		t.Fatalf("install rules: %v", setupErr)
	}

	ln = exfil("9101")
	defer ln.Close()
	if got := fetch("9101"); len(got) != 0 {
		t.Fatalf("an inbound connection carried data out past the guard: %q", got)
	}

	// The job still reaches the proxy, and the proxy replies come in.
	px, err := net.Listen("tcp", "10.9.0.1:3128")
	if err != nil {
		t.Fatal(err)
	}
	defer px.Close()
	go func() {
		c, err := px.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err == nil {
			_, _ = c.Write([]byte("pong:" + string(buf)))
		}
	}()
	var c net.Conn
	job.do(func() { c, err = net.DialTimeout("tcp", "10.9.0.1:3128", 2*time.Second) })
	if err != nil {
		t.Fatalf("the job must reach the proxy: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil || string(reply) != "pong:hello" {
		t.Fatalf("proxy reply %q %v", reply, err)
	}

	// Other egress from the job (TCP to another port is redirected to the
	// local transparent port, which is not served here) does not arrive.
	other, err := net.Listen("tcp", "10.9.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	got := make(chan bool, 1)
	go func() {
		c, err := other.Accept()
		if err == nil {
			c.Close()
		}
		got <- err == nil
	}()
	job.do(func() {
		if c, err := net.DialTimeout("tcp", "10.9.0.1:9000", time.Second); err == nil {
			c.Close()
		}
	})
	select {
	case ok := <-got:
		if ok {
			t.Fatal("a direct job connection arrived outside")
		}
	case <-time.After(500 * time.Millisecond):
	}
}
