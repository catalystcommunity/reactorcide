// Package imageref parses and formats image references and image alias
// names (VM_IMAGES_PLAN.md "References").
//
// A reference has an optional scheme, a location, an optional tag, and an
// optional digest:
//
//	oci://registry.example.com/team/vm/windows-11:2026.11@sha256:<hex>
//	registry.example.com/team/app:1.2@sha256:<hex>
//	s3://bucket/prefix/windows-11:2026.11@sha256:<hex>
//
// The tag is a free-form string (owner decision I3). A tag can look like a
// hash. The pin is the digest, never the tag. A reference with a digest is
// "pinned".
package imageref

import (
	"fmt"
	"regexp"
	"strings"
)

// Schemes of a reference. SchemeNone is a plain OCI or Docker reference
// with no scheme prefix.
const (
	SchemeNone = ""
	SchemeOCI  = "oci"
	SchemeS3   = "s3"
)

// DigestPrefix is the only digest algorithm that references can use.
const DigestPrefix = "sha256:"

// MaxTagLength is the maximum length of a tag.
const MaxTagLength = 128

// MaxReferenceLength is the maximum length of a full reference.
const MaxReferenceLength = 2048

var (
	aliasNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	digestPattern    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Reference is a parsed image reference.
type Reference struct {
	// Scheme is SchemeNone, SchemeOCI, or SchemeS3.
	Scheme string
	// Location is "host/repository" (OCI, plain) or "bucket/prefix/name"
	// (S3). For a plain reference with no host, it is the name as given
	// (for example "alpine" or "library/alpine").
	Location string
	// Tag is the free-form tag, or "" when the reference has no tag.
	Tag string
	// Digest is "sha256:<64 lowercase hex>", or "" when the reference is
	// not pinned.
	Digest string
}

// IsAliasName reports whether name has the form of an image alias name:
// `^[a-z0-9][a-z0-9._-]{0,62}$` (no "/", ":", or "@").
func IsAliasName(name string) bool { return aliasNamePattern.MatchString(name) }

// ValidateAliasName returns an error that tells why name is not a valid
// alias name, or nil.
func ValidateAliasName(name string) error {
	if !IsAliasName(name) {
		return fmt.Errorf("alias name %q is not valid: use 1 to 63 characters a-z, 0-9, '.', '_', '-', and start with a letter or a digit", name)
	}
	return nil
}

// ValidDigest reports whether digest is "sha256:" and 64 lowercase hex
// characters.
func ValidDigest(digest string) bool { return digestPattern.MatchString(digest) }

// Pinned reports whether the reference has a digest.
func (r Reference) Pinned() bool { return r.Digest != "" }

// String formats the reference. Parse(r.String()) gives r again.
func (r Reference) String() string {
	var b strings.Builder
	if r.Scheme != SchemeNone {
		b.WriteString(r.Scheme)
		b.WriteString("://")
	}
	b.WriteString(r.Location)
	if r.Tag != "" {
		b.WriteByte(':')
		b.WriteString(r.Tag)
	}
	if r.Digest != "" {
		b.WriteByte('@')
		b.WriteString(r.Digest)
	}
	return b.String()
}

// WithoutScheme returns the reference with no scheme prefix. For an "oci"
// reference, this is the plain OCI form that registries and the image
// policy use.
func (r Reference) WithoutScheme() string {
	r.Scheme = SchemeNone
	return r.String()
}

// Parse parses a reference. It does not require a digest; use ParsePinned
// when a digest is necessary.
func Parse(raw string) (Reference, error) {
	if raw != strings.TrimSpace(raw) || raw == "" {
		return Reference{}, fmt.Errorf("image reference %q is empty or has leading or trailing space", raw)
	}
	if len(raw) > MaxReferenceLength {
		return Reference{}, fmt.Errorf("image reference is longer than %d characters", MaxReferenceLength)
	}
	for _, c := range raw {
		if c <= ' ' || c == 0x7f {
			return Reference{}, fmt.Errorf("image reference %q has a space or a control character", raw)
		}
	}

	var ref Reference
	rest := raw
	if scheme, after, ok := strings.Cut(raw, "://"); ok {
		switch scheme {
		case SchemeOCI, SchemeS3:
			ref.Scheme = scheme
		default:
			return Reference{}, fmt.Errorf("image reference %q has an unknown scheme %q: use oci://, s3://, or no scheme", raw, scheme)
		}
		rest = after
	}

	// The digest is after the first "@". A location and a tag cannot have
	// "@".
	if before, digest, ok := strings.Cut(rest, "@"); ok {
		if !ValidDigest(digest) {
			return Reference{}, fmt.Errorf("image reference %q has a digest that is not valid: use @sha256: and 64 lowercase hexadecimal characters", raw)
		}
		ref.Digest = digest
		rest = before
	}

	// The tag is after the first ":" of the last path segment. A ":" before
	// the last "/" is a registry port.
	lastSlash := strings.LastIndexByte(rest, '/')
	if colon := strings.IndexByte(rest[lastSlash+1:], ':'); colon >= 0 {
		at := lastSlash + 1 + colon
		ref.Tag = rest[at+1:]
		rest = rest[:at]
		if ref.Tag == "" {
			return Reference{}, fmt.Errorf("image reference %q has an empty tag", raw)
		}
		if len(ref.Tag) > MaxTagLength {
			return Reference{}, fmt.Errorf("image reference %q has a tag longer than %d characters", raw, MaxTagLength)
		}
	}
	ref.Location = rest

	if err := validateLocation(ref); err != nil {
		return Reference{}, fmt.Errorf("image reference %q: %w", raw, err)
	}
	return ref, nil
}

// ParsePinned parses a reference and requires a digest.
func ParsePinned(raw string) (Reference, error) {
	ref, err := Parse(raw)
	if err != nil {
		return Reference{}, err
	}
	if !ref.Pinned() {
		return Reference{}, fmt.Errorf("image reference %q is not pinned: add @sha256:<digest>", raw)
	}
	return ref, nil
}

func validateLocation(ref Reference) error {
	if ref.Location == "" {
		return fmt.Errorf("the location is empty")
	}
	if strings.HasPrefix(ref.Location, "/") || strings.HasSuffix(ref.Location, "/") || strings.Contains(ref.Location, "//") {
		return fmt.Errorf("the location %q has an empty path segment", ref.Location)
	}
	segments := strings.Split(ref.Location, "/")
	switch ref.Scheme {
	case SchemeS3:
		if len(segments) < 2 {
			return fmt.Errorf("an s3 reference needs a bucket and a name (s3://bucket/name)")
		}
	case SchemeOCI:
		if len(segments) < 2 {
			return fmt.Errorf("an oci reference needs a registry host and a repository (oci://host/repository)")
		}
	}
	for i, segment := range segments {
		if segment == "." || segment == ".." {
			return fmt.Errorf("the location %q has a %q path segment", ref.Location, segment)
		}
		// Only the first segment of an OCI or plain reference can have a
		// ":" (a registry port). A tag is already removed.
		if i > 0 && strings.Contains(segment, ":") {
			return fmt.Errorf("the location %q has a ':' in a path segment", ref.Location)
		}
	}
	if ref.Scheme == SchemeS3 && strings.Contains(segments[0], ":") {
		return fmt.Errorf("the s3 bucket %q has a ':'", segments[0])
	}
	return nil
}
