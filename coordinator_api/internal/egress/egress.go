// Package egress holds the parts of the egress proxy contract that the
// coordinator, the worker, and the runners share: the per-job proxy
// credential, secret placeholders, the per-job policy that the worker sends
// to the proxy, and the client of the proxy control API.
//
// The proxy itself is the Rust program in egress_proxy/ at the repository
// root. The design is in docs/egress-proxy.md.
package egress

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// credentialDomain separates the proxy credential from other uses of the
// key. The proxy uses the same value (egress_proxy/src/proxy.rs).
const credentialDomain = "reactorcide-egress-job:"

// WorkerCharacteristic is the custom characteristic of a worker with an
// egress proxy (egress_proxy=true). The coordinator leases a job with egress
// control only to such a worker.
const WorkerCharacteristic = "egress_proxy"

// MinKeyBytes is the smallest job key and control token that the proxy
// accepts.
const MinKeyBytes = 32

// MinSecretBytes is the shortest secret value of a proxy grant. The proxy
// scanner does not look for a shorter value (egress_proxy/src/scan.rs
// MIN_SECRET), so a shorter value could leave the job in a response or a
// request without detection.
const MinSecretBytes = 8

// PlaceholderPrefix starts each secret placeholder.
const PlaceholderPrefix = "reactorcide-secret-"

// placeholderPattern is the form that the proxy recognizes
// (egress_proxy/src/scan.rs PLACEHOLDER).
var placeholderPattern = regexp.MustCompile(`^reactorcide-secret-[A-Za-z0-9_.-]+$`)

// FindPlaceholders matches each placeholder in a text.
var FindPlaceholders = regexp.MustCompile(`reactorcide-secret-[A-Za-z0-9_.-]+`)

// Credential returns the proxy password of a job: the hex HMAC-SHA256 of
// "reactorcide-egress-job:" + jobID with the job key. The proxy user is the
// job ID.
func Credential(key []byte, jobID string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(credentialDomain))
	m.Write([]byte(jobID))
	return hex.EncodeToString(m.Sum(nil))
}

// ReadSecretFile reads a key or token file. Surrounding white space is not
// part of the value. The value must have at least MinKeyBytes bytes. The
// error never contains the file content.
func ReadSecretFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	v := []byte(strings.TrimSpace(string(b)))
	if len(v) < MinKeyBytes {
		return nil, fmt.Errorf("%s: the value must have at least %d bytes", path, MinKeyBytes)
	}
	return v, nil
}

// NewPlaceholder returns a new random placeholder:
// "reactorcide-secret-" and 32 lowercase hex digits.
func NewPlaceholder() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("placeholder: %w", err)
	}
	return PlaceholderPrefix + hex.EncodeToString(b[:]), nil
}

// IsPlaceholder reports whether s is one whole placeholder.
func IsPlaceholder(s string) bool { return placeholderPattern.MatchString(s) }

// DefaultHeader is the request header where the proxy puts a secret when
// the grant names no header.
const DefaultHeader = "authorization"

var headerName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// hopHeaders are headers of one connection. A grant cannot name them.
var hopHeaders = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authorization": true, "proxy-authenticate": true,
	"proxy-connection": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
	"content-length": true, "content-encoding": true, "host": true,
}

// ValidateHeader checks the header name of a proxy grant. Empty is valid
// (DefaultHeader).
func ValidateHeader(name string) error {
	if name == "" {
		return nil
	}
	if !headerName.MatchString(name) {
		return fmt.Errorf("proxy_header %q is not a valid header name", name)
	}
	if hopHeaders[strings.ToLower(name)] {
		return fmt.Errorf("proxy_header %q is a connection header", name)
	}
	return nil
}

// ValidateHeaderFormat checks the optional header value template of a proxy
// grant: "{}" must occur exactly one time, for example "Bearer {}".
func ValidateHeaderFormat(format string) error {
	if format == "" {
		return nil
	}
	if strings.Count(format, "{}") != 1 {
		return errors.New(`proxy_header_format must contain "{}" exactly one time`)
	}
	if strings.ContainsAny(format, "\r\n") {
		return errors.New("proxy_header_format cannot contain a line break")
	}
	return nil
}

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateHostPattern checks a destination host pattern of a proxy grant:
// "api.example.com" (that name only) or "*.example.com" (each name below
// example.com, not example.com). A lone "*" is not permitted: a secret must
// be bound to named hosts. "localhost" and a public suffix ("co.uk",
// "github.io", "herokuapp.com") or a wildcard of one ("*.co.uk",
// "*.github.io") are not permitted either: each name below a public suffix
// can belong to a different owner, so the secret could go to any of them.
func ValidateHostPattern(pattern string) error {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return errors.New("proxy host pattern is empty")
	}
	base := strings.TrimPrefix(p, "*.")
	if strings.Contains(base, "*") {
		return fmt.Errorf("proxy host %q: only a leading \"*.\" is permitted", pattern)
	}
	base = strings.TrimSuffix(base, ".")
	labels := strings.Split(base, ".")
	if len(labels) < 2 {
		return fmt.Errorf("proxy host %q must have at least two labels", pattern)
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return fmt.Errorf("proxy host %q is not a valid host name", pattern)
		}
	}
	if labels[len(labels)-1] == "localhost" {
		return fmt.Errorf("proxy host %q is a loopback name", pattern)
	}
	if net.ParseIP(base) == nil {
		if ps, _ := publicsuffix.PublicSuffix(base); ps == base {
			return fmt.Errorf("proxy host %q is a public suffix: name a host or a domain that one owner controls", pattern)
		}
	}
	return nil
}

// HostMatch reports whether host matches pattern, with the rules of the
// proxy (egress_proxy/src/policy.rs host_match).
func HostMatch(pattern, host string) bool {
	p := strings.ToLower(pattern)
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if p == "*" {
		return true
	}
	if base, ok := strings.CutPrefix(p, "*."); ok {
		return len(h) > len(base)+1 && strings.HasSuffix(h, "."+base)
	}
	return p == h
}
