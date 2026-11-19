package imageref

import (
	"strings"
	"testing"
)

var (
	hexA = strings.Repeat("a", 64)
	hexB = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Reference
	}{
		{"oci pinned", "oci://registry.example.com/team/vm/windows-11:2026.11@sha256:" + hexA,
			Reference{Scheme: SchemeOCI, Location: "registry.example.com/team/vm/windows-11", Tag: "2026.11", Digest: "sha256:" + hexA}},
		{"plain pinned", "registry.example.com/team/app:1.2@sha256:" + hexB,
			Reference{Location: "registry.example.com/team/app", Tag: "1.2", Digest: "sha256:" + hexB}},
		{"s3 pinned", "s3://bucket/prefix/windows-11:2026.11@sha256:" + hexA,
			Reference{Scheme: SchemeS3, Location: "bucket/prefix/windows-11", Tag: "2026.11", Digest: "sha256:" + hexA}},
		{"registry port", "localhost:5000/team/app:v1@sha256:" + hexA,
			Reference{Location: "localhost:5000/team/app", Tag: "v1", Digest: "sha256:" + hexA}},
		{"registry port no tag", "localhost:5000/team/app@sha256:" + hexA,
			Reference{Location: "localhost:5000/team/app", Digest: "sha256:" + hexA}},
		{"tag looks like a hash", "ghcr.io/acme/app:" + hexB + "@sha256:" + hexA,
			Reference{Location: "ghcr.io/acme/app", Tag: hexB, Digest: "sha256:" + hexA}},
		{"tag looks like a digest", "ghcr.io/acme/app:sha256-" + hexB + "@sha256:" + hexA,
			Reference{Location: "ghcr.io/acme/app", Tag: "sha256-" + hexB, Digest: "sha256:" + hexA}},
		{"tag with colon", "s3://bucket/name:sha256:abc",
			Reference{Scheme: SchemeS3, Location: "bucket/name", Tag: "sha256:abc"}},
		{"uppercase tag is free form", "ghcr.io/acme/app:Release_2026",
			Reference{Location: "ghcr.io/acme/app", Tag: "Release_2026"}},
		{"bare name", "alpine", Reference{Location: "alpine"}},
		{"bare name with tag", "alpine:3.20", Reference{Location: "alpine", Tag: "3.20"}},
		{"unpinned oci", "oci://registry.example.com/vm/mac:latest",
			Reference{Scheme: SchemeOCI, Location: "registry.example.com/vm/mac", Tag: "latest"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
			if got.String() != tc.raw {
				t.Fatalf("String() = %q, want %q", got.String(), tc.raw)
			}
			again, err := Parse(got.String())
			if err != nil || again != got {
				t.Fatalf("round trip: %+v, %v", again, err)
			}
		})
	}
}

func TestParseRefuses(t *testing.T) {
	cases := map[string]string{
		"empty":                 "",
		"space":                 " ghcr.io/a/b",
		"inner space":           "ghcr.io/a b/c",
		"unknown scheme":        "https://ghcr.io/a/b",
		"docker scheme":         "docker://ghcr.io/a/b",
		"uppercase hex":         "ghcr.io/a/b@sha256:" + strings.ToUpper(hexA),
		"short digest":          "ghcr.io/a/b@sha256:abc",
		"long digest":           "ghcr.io/a/b@sha256:" + hexA + "a",
		"sha512 digest":         "ghcr.io/a/b@sha512:" + hexA,
		"empty digest":          "ghcr.io/a/b@",
		"two at signs":          "ghcr.io/a/b@sha256:" + hexA + "@sha256:" + hexA,
		"empty tag":             "ghcr.io/a/b:@sha256:" + hexA,
		"empty location":        "oci://:tag@sha256:" + hexA,
		"oci without repo":      "oci://registry.example.com:tag",
		"s3 without name":       "s3://bucket",
		"s3 bucket colon":       "s3://bucket:1/name",
		"empty segment":         "ghcr.io//a",
		"trailing slash":        "ghcr.io/a/",
		"dot dot":               "s3://bucket/../name",
		"colon inside path":     "ghcr.io/a:b/c",
		"tag too long":          "ghcr.io/a/b:" + strings.Repeat("t", MaxTagLength+1),
		"control char":          "ghcr.io/a/b\t",
		"scheme only":           "oci://",
		"digest without sha256": "ghcr.io/a/b@" + hexA,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if ref, err := Parse(raw); err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", raw, ref)
			}
		})
	}
}

func TestParsePinned(t *testing.T) {
	if _, err := ParsePinned("oci://registry.example.com/vm/mac:latest"); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("want a not pinned error, got %v", err)
	}
	ref, err := ParsePinned("oci://registry.example.com/vm/mac:latest@sha256:" + hexA)
	if err != nil || !ref.Pinned() {
		t.Fatalf("ParsePinned: %+v %v", ref, err)
	}
	if got := ref.WithoutScheme(); got != "registry.example.com/vm/mac:latest@sha256:"+hexA {
		t.Fatalf("WithoutScheme() = %q", got)
	}
}

func TestAliasName(t *testing.T) {
	valid := []string{"a", "windows-vs2022", "mac.xcode_16", "0abc", strings.Repeat("a", 63)}
	invalid := []string{"", "-a", ".a", "_a", "A", "Windows", "a/b", "a:b", "a@b", "a b", strings.Repeat("a", 64), "ghcr.io/a/b"}
	for _, name := range valid {
		if !IsAliasName(name) || ValidateAliasName(name) != nil {
			t.Errorf("%q must be a valid alias name", name)
		}
	}
	for _, name := range invalid {
		if IsAliasName(name) || ValidateAliasName(name) == nil {
			t.Errorf("%q must not be a valid alias name", name)
		}
	}
}

func TestValidDigest(t *testing.T) {
	if !ValidDigest("sha256:" + hexB) {
		t.Fatal("lowercase digest must be valid")
	}
	for _, digest := range []string{"", hexA, "sha256:" + strings.ToUpper(hexA), "sha256:" + hexA[:63], "SHA256:" + hexA} {
		if ValidDigest(digest) {
			t.Errorf("%q must not be valid", digest)
		}
	}
}
