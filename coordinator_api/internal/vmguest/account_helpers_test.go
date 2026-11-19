package vmguest

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf16"
)

func TestValidWindowsAccountName(t *testing.T) {
	for _, ok := range []string{"reactorcide", "ci-runner", "build.user", "a", strings.Repeat("x", 20)} {
		if err := validWindowsAccountName(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 21), "...", " . ", `a\b`, "a/b", "a@b", "a:b", "a*b", "a%b", "a\tb", "a,b"} {
		if err := validWindowsAccountName(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestNewWindowsPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := newWindowsPassword()
		if err != nil {
			t.Fatal(err)
		}
		if seen[p] {
			t.Fatal("repeated password")
		}
		seen[p] = true
		var upper, lower, digit, symbol bool
		for _, r := range p {
			switch {
			case unicode.IsUpper(r):
				upper = true
			case unicode.IsLower(r):
				lower = true
			case unicode.IsDigit(r):
				digit = true
			default:
				symbol = true
			}
		}
		if !upper || !lower || !digit || !symbol || len(p) < 40 {
			t.Fatalf("password does not meet the complexity rule (len %d)", len(p))
		}
		// The account name never appears in the password (a Windows
		// complexity rule), because the alphabet has no run of the name.
		if strings.Contains(strings.ToLower(p), "reactorcide") {
			t.Fatal("password contains the account name")
		}
	}
}

func TestParseEnvBlock(t *testing.T) {
	var block []uint16
	for _, kv := range []string{"=C:=C:\\", "USERPROFILE=C:\\Users\\reactorcide", "TEMP=C:\\Users\\reactorcide\\AppData\\Local\\Temp", "Path=C:\\Windows;C:\\Tools", "BROKEN"} {
		block = append(block, utf16.Encode([]rune(kv))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	got := parseEnvBlock(block)
	want := []string{"USERPROFILE=C:\\Users\\reactorcide", "TEMP=C:\\Users\\reactorcide\\AppData\\Local\\Temp", "Path=C:\\Windows;C:\\Tools"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parseEnvBlock = %q, want %q", got, want)
	}
	if got := parseEnvBlock([]uint16{0, 0}); len(got) != 0 {
		t.Fatalf("empty block gave %q", got)
	}
}

func TestWithHome(t *testing.T) {
	got := withHome([]string{"USERPROFILE=C:\\Users\\reactorcide"}, `C:\Users\reactorcide`)
	if strings.Join(got, "|") != `USERPROFILE=C:\Users\reactorcide|HOME=C:\Users\reactorcide` {
		t.Fatalf("withHome added %q", got)
	}
	keep := []string{`Home=D:\h`}
	if got := withHome(keep, `C:\Users\reactorcide`); len(got) != 1 || got[0] != `Home=D:\h` {
		t.Fatalf("withHome replaced an existing HOME: %q", got)
	}
	if got := withHome(nil, ""); len(got) != 0 {
		t.Fatalf("withHome with no home gave %q", got)
	}
}

func TestAccountLine(t *testing.T) {
	if got := AccountLine("reactorcide", true); got != "running as reactorcide which is an administrator" {
		t.Fatal(got)
	}
	if got := AccountLine("runner", false); got != "running as runner which is not an administrator" {
		t.Fatal(got)
	}
	for name, want := range map[string]bool{"admin": true, "wheel": true, "sudo": true, "staff": false, "": false} {
		if isAdminGroup(name) != want {
			t.Fatalf("isAdminGroup(%q) != %v", name, want)
		}
	}
}
