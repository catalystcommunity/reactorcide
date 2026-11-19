package vmguest

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameStdout, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&buf, FrameExit, ExitStatus{Code: 3}); err != nil {
		t.Fatal(err)
	}
	typ, p, err := ReadFrame(&buf)
	if err != nil || typ != FrameStdout || string(p) != "hello" {
		t.Fatalf("frame = %q %q %v", typ, p, err)
	}
	typ, p, err = ReadFrame(&buf)
	if err != nil || typ != FrameExit || string(p) != `{"code":3}` {
		t.Fatalf("frame = %q %q %v", typ, p, err)
	}
	if _, _, err := ReadFrame(&buf); err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
}

func TestFrameLimits(t *testing.T) {
	if err := WriteFrame(io.Discard, FrameData, make([]byte, MaxFrame+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v", err)
	}
	hdr := []byte{FrameData, 0, 1, 0, 1} // 65537 bytes
	if _, _, err := ReadFrame(bytes.NewReader(hdr)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v", err)
	}
	short := []byte{FrameData, 0, 0, 0, 4, 'a'}
	if _, _, err := ReadFrame(bytes.NewReader(short)); err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v", err)
	}
}

func TestDataStreamChunksAndEnds(t *testing.T) {
	var buf bytes.Buffer
	data := bytes.Repeat([]byte("0123456789"), MaxFrame/4) // 2.5 frames
	w := &DataWriter{W: &buf, Type: FrameData}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = WriteFrame(&buf, FrameStart, []byte("next"))
	r := &DataReader{R: &buf}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
	// The frame after the stream is still there.
	typ, p, err := ReadFrame(&buf)
	if err != nil || typ != FrameStart || string(p) != "next" {
		t.Fatalf("next frame = %q %q %v", typ, p, err)
	}
}

func TestDataReaderRejectsOtherFrames(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, FrameStdout, []byte("x"))
	if _, err := io.ReadAll(&DataReader{R: &buf}); err == nil {
		t.Fatal("expected a protocol error")
	}
}

func TestNormalizeSignal(t *testing.T) {
	for in, want := range map[string]string{"TERM": "TERM", "sigterm": "TERM", " KILL ": "KILL", "SIGINT": "INT"} {
		if got, err := NormalizeSignal(in); err != nil || got != want {
			t.Errorf("NormalizeSignal(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "STOP", "9", "TERM; rm -rf /"} {
		if _, err := NormalizeSignal(bad); err == nil {
			t.Errorf("NormalizeSignal(%q) did not fail", bad)
		}
	}
}

func TestCommandEnvOrderAndOverrides(t *testing.T) {
	t.Setenv("REACTORCIDE_VMGUEST_PRIVATE", "hidden")
	t.Setenv("VMGUEST_TEST_BASE", "base")
	s := &Server{ExtraEnv: []string{"VMGUEST_TEST_EXTRA=extra", "VMGUEST_TEST_BASE=from-extra"}}
	env, err := s.commandEnv(map[string]string{"VMGUEST_TEST_JOB": "job", "VMGUEST_TEST_EXTRA": "from-job"}, account{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate variable %s", k)
		}
		got[k] = v
	}
	if _, ok := got["REACTORCIDE_VMGUEST_PRIVATE"]; ok {
		t.Fatal("daemon variable leaked into the command environment")
	}
	if got["VMGUEST_TEST_BASE"] != "from-extra" || got["VMGUEST_TEST_EXTRA"] != "from-job" || got["VMGUEST_TEST_JOB"] != "job" {
		t.Fatalf("env = %v", got)
	}
	if _, err := s.commandEnv(map[string]string{"BAD=NAME": "x"}, account{}); err == nil {
		t.Fatal("accepted an invalid variable name")
	}
	if _, err := s.commandEnv(map[string]string{"OK": "a\x00b"}, account{}); err == nil {
		t.Fatal("accepted a NUL in a value")
	}
}

func tarStream(t *testing.T, entries []tar.Header, bodies map[string]string) *bytes.Buffer {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, h := range entries {
		h := h
		if body, ok := bodies[h.Name]; ok {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body, ok := bodies[h.Name]; ok {
			_, _ = tw.Write([]byte(body))
		}
	}
	_ = tw.Close()
	var framed bytes.Buffer
	w := &DataWriter{W: &framed, Type: FrameData}
	_, _ = w.Write(raw.Bytes())
	_ = w.Close()
	return &framed
}

func TestExtractTree(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "job")
	stream := tarStream(t, []tar.Header{
		{Name: "src", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "src/main.go", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "src/run.sh", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "src/link", Typeflag: tar.TypeSymlink, Linkname: "main.go"},
	}, map[string]string{"src/main.go": "package main\n", "src/run.sh": "#!/bin/sh\n"})
	if err := extractTree(dest, &DataReader{R: stream}, account{}, testLog()); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "src", "main.go")); err != nil || string(b) != "package main\n" {
		t.Fatalf("main.go = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dest, "src", "run.sh"))
		if st.Mode().Perm()&0o100 == 0 {
			t.Fatalf("run.sh mode = %v", st.Mode())
		}
		if target, err := os.Readlink(filepath.Join(dest, "src", "link")); err != nil || target != "main.go" {
			t.Fatalf("link = %q, %v", target, err)
		}
	}
}

// The destination itself can be a link (macOS: /job is a synthetic link to
// the Data volume). The tree goes into the directory that it points to.
func TestExtractTreeIntoLinkedDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX links")
	}
	base := t.TempDir()
	realDir := filepath.Join(base, "data", "job")
	if err := os.MkdirAll(realDir, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "job")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	stream := tarStream(t, []tar.Header{{Name: "src/a.txt", Typeflag: tar.TypeReg, Mode: 0o644}}, map[string]string{"src/a.txt": "a"})
	if err := extractTree(link, &DataReader{R: stream}, account{}, testLog()); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(realDir, "src", "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("a.txt = %q, %v", b, err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the destination link was replaced: %v, %v", st, err)
	}
}

func TestExtractTreeRefusesEscapes(t *testing.T) {
	for name, entries := range map[string][]tar.Header{
		"dotdot":   {{Name: "../evil", Typeflag: tar.TypeReg}},
		"absolute": {{Name: "/etc/evil", Typeflag: tar.TypeReg}},
		"through a link": {
			{Name: "out", Typeflag: tar.TypeSymlink, Linkname: "/"},
			{Name: "out/evil", Typeflag: tar.TypeReg},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "job")
			stream := tarStream(t, entries, nil)
			// Add a frame after the stream: a failed extraction must still
			// read its whole stream.
			_ = WriteFrame(stream, FrameStart, nil)
			if err := extractTree(dest, &DataReader{R: stream}, account{}, testLog()); err == nil {
				t.Fatal("expected an error")
			}
			if typ, _, err := ReadFrame(stream); err != nil || typ != FrameStart {
				t.Fatalf("the stream was not drained: %q %v", typ, err)
			}
		})
	}
}

func TestReceiveFileIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "vcs", "credentials")
	var framed bytes.Buffer
	w := &DataWriter{W: &framed, Type: FrameData}
	_, _ = w.Write([]byte("secret-token"))
	_ = w.Close()
	if err := receiveFile(FileHeader{Path: target}, &DataReader{R: &framed}, account{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "secret-token" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(target)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", st.Mode())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
	if err := receiveFile(FileHeader{Path: "relative/path"}, &DataReader{R: bytes.NewReader(nil)}, account{}); err == nil {
		t.Fatal("accepted a relative path")
	}
}

func dnsQuery(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	_ = b.StartQuestions()
	_ = b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
	q, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func dnsAnswer(t *testing.T, f *FakeDNS, name string, typ dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	out, err := f.Answer(dnsQuery(t, name, typ))
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(out); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFakeDNS(t *testing.T) {
	f := NewFakeDNS()
	f.Search = []string{"corp.example"}
	m := dnsAnswer(t, f, "github.com.", dnsmessage.TypeA)
	if len(m.Answers) != 1 {
		t.Fatalf("answers = %v", m.Answers)
	}
	a := netip.AddrFrom4(m.Answers[0].Body.(*dnsmessage.AResource).A)
	if !FakeNet.Contains(a) {
		t.Fatalf("address %s is not in %s", a, FakeNet)
	}
	if n, ok := f.Name(a); !ok || n != "github.com" {
		t.Fatalf("name = %q %v", n, ok)
	}
	again := dnsAnswer(t, f, "GitHub.com.", dnsmessage.TypeA)
	if netip.AddrFrom4(again.Answers[0].Body.(*dnsmessage.AResource).A) != a {
		t.Fatal("the same name got a different address")
	}
	if m := dnsAnswer(t, f, "github.com.", dnsmessage.TypeAAAA); len(m.Answers) != 0 {
		t.Fatalf("AAAA answers = %v", m.Answers)
	}
	if m := dnsAnswer(t, f, "localhost.", dnsmessage.TypeA); m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{127, 0, 0, 1} {
		t.Fatalf("localhost = %v", m.Answers)
	}
	if m := dnsAnswer(t, f, "github.com.corp.example.", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("search domain rcode = %v", m.RCode)
	}
	if m := dnsAnswer(t, f, "github.com.", dnsmessage.TypeTXT); m.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("TXT rcode = %v", m.RCode)
	}
	dest, ok := TransparentDestination(netip.AddrPortFrom(a, 443), f)
	if !ok || dest != "github.com:443" {
		t.Fatalf("destination = %q %v", dest, ok)
	}
	if _, ok := TransparentDestination(netip.MustParseAddrPort("198.18.200.1:443"), f); ok {
		t.Fatal("an unknown fake address got a destination")
	}
	if dest, ok := TransparentDestination(netip.MustParseAddrPort("10.0.0.5:22"), f); !ok || dest != "10.0.0.5:22" {
		t.Fatalf("real address destination = %q %v", dest, ok)
	}
}

// fakeTunnel answers CONNECT requests like the host forwarder and records the
// destinations. Each tunnel echoes its bytes back.
func fakeTunnel(t *testing.T, refuse string) (Tunnel, *[]string) {
	t.Helper()
	var dests []string
	return Tunnel{Dial: func() (net.Conn, error) {
		guest, host := net.Pipe()
		go func() {
			defer host.Close()
			br := bufio.NewReader(host)
			req, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			dests = append(dests, req.Method+" "+req.Host)
			if req.Host == refuse {
				_, _ = io.WriteString(host, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
				return
			}
			_, _ = io.WriteString(host, "HTTP/1.1 200 Connection established\r\n\r\n")
			_, _ = io.Copy(host, br)
		}()
		return guest, nil
	}}, &dests
}

func TestServeProxyConnectAndAbsoluteForm(t *testing.T) {
	tunnel, dests := fakeTunnel(t, "blocked.example:443")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	go func() { _ = ServeProxy(ln, tunnel, testLog()) }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: Basic eDp5\r\n\r\n")
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("CONNECT = %v, %v", res, err)
	}
	_, _ = io.WriteString(c, "ping")
	buf := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("tunnel echo = %q, %v", buf, err)
	}
	c.Close()

	c, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET http://plain.example/path?q=1 HTTP/1.1\r\nHost: plain.example\r\nProxy-Connection: keep-alive\r\n\r\n")
	// The fake tunnel echoes: the proxy must send the origin form.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	echoed, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	if echoed.RequestURI != "/path?q=1" || echoed.Header.Get("Proxy-Connection") != "" {
		t.Fatalf("origin request = %q %v", echoed.RequestURI, echoed.Header)
	}
	c.Close()

	c, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "CONNECT blocked.example:443 HTTP/1.1\r\nHost: blocked.example:443\r\n\r\n")
	res, err = http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("refused CONNECT = %v, %v", res, err)
	}
	c.Close()

	want := []string{"CONNECT example.com:443", "CONNECT plain.example:80", "CONNECT blocked.example:443"}
	if strings.Join(*dests, ",") != strings.Join(want, ",") {
		t.Fatalf("tunnel destinations = %v, want %v", *dests, want)
	}
}

func TestProxyEnv(t *testing.T) {
	env := strings.Join(ProxyEnv("127.0.0.1:3128"), "\n")
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128", "HTTP_PROXY=http://127.0.0.1:3128", "NO_PROXY=localhost,127.0.0.1,::1"} {
		if !strings.Contains(env, want) {
			t.Fatalf("proxy env does not contain %q", want)
		}
	}
}

func TestParsePfState(t *testing.T) {
	out := []byte("No ALTQ support in kernel\nALL tcp 127.0.0.1:15001 <- 198.18.0.5:443 <- 10.0.0.2:50000       ESTABLISHED:ESTABLISHED\n" +
		"ALL tcp 127.0.0.1:15001 <- 198.18.0.9:80 <- 10.0.0.2:50001       ESTABLISHED:ESTABLISHED\n")
	got, err := parsePfState(out, netip.MustParseAddrPort("10.0.0.2:50001"))
	if err != nil || got != netip.MustParseAddrPort("198.18.0.9:80") {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parsePfState(out, netip.MustParseAddrPort("10.0.0.2:1")); err == nil {
		t.Fatal("expected no state")
	}
}

func TestMacNetworkSteps(t *testing.T) {
	steps := macNetworkSteps("15001", []string{"127.0.0.1"})
	var cmds []string
	for _, s := range steps {
		cmds = append(cmds, s.name+" "+strings.Join(s.args, " ")+" <<"+s.stdin)
	}
	all := strings.Join(cmds, "\n")
	for _, want := range []string{
		"/sbin/route -n add -net 0.0.0.0/1 -interface lo0",
		"/sbin/route -n add -net 128.0.0.0/1 -interface lo0",
		"/sbin/pfctl -q -a com.apple/250.reactorcide -f - <<rdr pass on lo0 inet proto tcp from any to ! 127.0.0.0/8 -> 127.0.0.1 port 15001",
		"/sbin/pfctl -q -E",
		"d.add ServerAddresses * 127.0.0.1",
		"d.add SupplementalMatchDomains * \"\"",
		"set State:/Network/Service/reactorcide/DNS",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("steps do not contain %q:\n%s", want, all)
		}
	}
	if len(macNetworkSteps("15001", nil)) != 4 {
		t.Fatal("a resolver step without DNS servers")
	}
}

func TestWindowsProxyScript(t *testing.T) {
	script := windowsProxyScript("127.0.0.1:3128")
	for _, want := range []string{
		"& netsh.exe winhttp set proxy 'proxy-server=127.0.0.1:3128' 'bypass-list=<local>'",
		"ProxySettingsPerUser -PropertyType DWord -Value 0",
		"-Name ProxyEnable -PropertyType DWord -Value 1",
		"-Name ProxyServer -PropertyType String -Value '127.0.0.1:3128'",
		"if (-not (Test-Path -LiteralPath $policy))",
		"foreach ($name in 'DefaultConnectionSettings', 'SavedLegacySettings')",
		"$settings = [byte[]](0x46,0x00,0x00,0x00,0x01,0x00,0x00,0x00,0x03,0x00,0x00,0x00,0x0e,0x00,0x00,0x00,0x31,",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script does not contain %q:\n%s", want, script)
		}
	}
	if !strings.Contains(windowsProxyScript("it's"), "'it''s'") {
		t.Fatal("the address is not quoted")
	}
}

// The layout that a Windows 11 guest accepted on hardware (2026-11-17):
// Invoke-WebRequest and .NET WebClient use the proxy only with this value.
func TestWininetConnectionSettings(t *testing.T) {
	got := wininetConnectionSettings("127.0.0.1:3128", "<local>")
	var want []byte
	want = append(want, 0x46, 0, 0, 0, 1, 0, 0, 0, 3, 0, 0, 0, 14, 0, 0, 0)
	want = append(want, "127.0.0.1:3128"...)
	want = append(want, 7, 0, 0, 0)
	want = append(want, "<local>"...)
	want = append(want, make([]byte, 36)...)
	if string(got) != string(want) {
		t.Fatalf("settings = % x\nwant       % x", got, want)
	}
}

func TestLaunchdPlist(t *testing.T) {
	p := LaunchdPlist("/usr/local/libexec/a&b")
	for _, want := range []string{
		"<string>" + MacLaunchdLabel + "</string>",
		"<string>/usr/local/libexec/a&amp;b</string><string>serve</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("plist does not contain %q:\n%s", want, p)
		}
	}
}

func TestTaskkillArgs(t *testing.T) {
	if got := strings.Join(taskkillArgs(42), " "); got != "/T /F /PID 42" {
		t.Fatalf("args = %q", got)
	}
}

func TestSpecs(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := Listen("unix:" + sock)
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	dial, err := Dialer("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	c, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	for _, bad := range []string{"", "vsock", "vsock:x:1", "hvsock:x", "ftp:host"} {
		if _, err := Dialer(bad); err == nil {
			t.Errorf("Dialer(%q) did not fail", bad)
		}
	}
	if _, err := Listen("bogus:1"); err == nil {
		t.Error("Listen accepted an unknown kind")
	}
}

// The host install script writes the same launchd file as LaunchdPlist.
func TestInstallScriptMatchesLaunchdPlist(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "deployment", "macos", "install-vmguest.sh"))
	if err != nil {
		t.Skip(err)
	}
	if !strings.Contains(string(script), LaunchdPlist(MacDaemonPath)) {
		t.Fatal("deployment/macos/install-vmguest.sh does not contain LaunchdPlist(MacDaemonPath)")
	}
}

// The /job setup script must run in POSIX sh, be idempotent, and write a
// synthetic.conf line with a TAB (macOS ignores a line with spaces).
func TestMacJobDirSetupScript(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "synthetic.conf")
	target := filepath.Join(dir, "job")
	script := strings.NewReplacer(
		"/etc/synthetic.conf", conf,
		MacJobDirTarget, target,
		"chown root:wheel", "true",
		"/System/Library/Filesystems/apfs.fs/Contents/Resources/apfs.util", "true",
	).Replace(MacJobDirSetupScript)
	for i := 0; i < 2; i++ {
		if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("run %d: %v: %s", i, err, out)
		}
	}
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "job\tprivate/var/reactorcide/job\n" {
		t.Fatalf("synthetic.conf = %q", data)
	}
	st, err := os.Stat(target)
	if err != nil || st.Mode()&os.ModeSticky == 0 || st.Mode().Perm() != 0o777 {
		t.Fatalf("job dir = %v, %v", st, err)
	}
}

func TestLoopbackAddressScript(t *testing.T) {
	script := loopbackAddressScript()
	for _, want := range []string{
		"Where-Object ComponentID -eq '*MSLOOP'",
		"-IPAddress '10.255.255.1' -PrefixLength 30",
		"throw 'the loopback adapter did not appear'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("loopbackAddressScript() is missing %q:\n%s", want, script)
		}
	}
	// No gateway and no DNS servers: the adapter must route nothing.
	for _, bad := range []string{"DefaultGateway", "NextHop", "Set-DnsClientServerAddress"} {
		if strings.Contains(script, bad) {
			t.Errorf("loopbackAddressScript() contains %q", bad)
		}
	}
}
