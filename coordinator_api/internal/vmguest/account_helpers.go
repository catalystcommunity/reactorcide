package vmguest

// Helpers of the Windows guest account. They have no build tag, so the tests
// run on every OS.

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf16"
)

// validWindowsAccountName checks a local account name: 1-20 characters, not
// only dots and spaces, and none of the characters that Windows refuses.
func validWindowsAccountName(name string) error {
	if name == "" || len([]rune(name)) > 20 {
		return fmt.Errorf("vmguest: guest account name %q must have 1-20 characters", name)
	}
	if strings.Trim(name, ". ") == "" {
		return fmt.Errorf("vmguest: guest account name %q has only dots and spaces", name)
	}
	if strings.ContainsAny(name, "\"/\\[]:;|=,+*?<>@%") {
		return fmt.Errorf("vmguest: guest account name %q has a character that Windows refuses", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("vmguest: guest account name %q has a control character", name)
		}
	}
	return nil
}

const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_.!#$^&()"

// newWindowsPassword returns a random password that meets the default Windows
// complexity rule (upper case, lower case, digit, and symbol). Nobody sees it:
// the daemon sets it, logs the account on, and keeps only the token.
func newWindowsPassword() (string, error) {
	const n = 40
	var b strings.Builder
	b.WriteString("Rc7!")
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("vmguest: random password: %w", err)
		}
		b.WriteByte(passwordAlphabet[v.Int64()])
	}
	return b.String(), nil
}

// parseEnvBlock splits a Windows environment block (NUL-separated UTF-16
// strings, ended by an empty string) into NAME=value items. Items that start
// with "=" (the per-drive current directories) are skipped.
func parseEnvBlock(block []uint16) []string {
	var out []string
	start := 0
	for i, c := range block {
		if c != 0 {
			continue
		}
		if i == start {
			break
		}
		kv := string(utf16.Decode(block[start:i]))
		if !strings.HasPrefix(kv, "=") && strings.Contains(kv, "=") {
			out = append(out, kv)
		}
		start = i + 1
	}
	return out
}

// withHome adds HOME=home when env has no HOME (names compare without case).
// Unix guests set HOME to the account home; Git for Windows, OpenSSH, and
// many cross-platform tools also read HOME on Windows.
func withHome(env []string, home string) []string {
	if home == "" {
		return env
	}
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "HOME") {
			return env
		}
	}
	return append(env, "HOME="+home)
}

// isAdminGroup reports if a Unix group name gives administrator rights.
func isAdminGroup(name string) bool {
	switch name {
	case "admin", "wheel", "sudo":
		return true
	}
	return false
}
