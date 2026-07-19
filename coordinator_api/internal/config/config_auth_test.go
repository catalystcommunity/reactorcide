package config

import "testing"

// withUIAuthConfig saves and restores every UI-auth config var a test
// mutates, so tests can run in any order without leaking state (these are
// plain package vars, read once at init from the environment — the same
// pattern the rest of this package uses).
func withUIAuthConfig(t *testing.T) func() {
	t.Helper()
	origMode := UIAuthMode
	origAddr := LinkKeysRPAddr
	origFingerprints := LinkKeysRPFingerprints
	origLocalName := LocalRPName
	return func() {
		UIAuthMode = origMode
		LinkKeysRPAddr = origAddr
		LinkKeysRPFingerprints = origFingerprints
		LocalRPName = origLocalName
	}
}

func TestValidateUIAuthMode(t *testing.T) {
	defer withUIAuthConfig(t)()

	tests := []struct {
		name           string
		mode           string
		localRPName    string
		rpAddr         string
		rpFingerprints string
		wantErr        bool
	}{
		{name: "none is always valid", mode: UIAuthModeNone, wantErr: false},
		{name: "default empty is invalid", mode: "", wantErr: true},
		{name: "unrecognized mode", mode: "sso", wantErr: true},
		{name: "local-rp missing name", mode: UIAuthModeLocalRP, localRPName: "", wantErr: true},
		{name: "local-rp with name", mode: UIAuthModeLocalRP, localRPName: "My Reactorcide", wantErr: false},
		{name: "rp missing everything", mode: UIAuthModeRP, wantErr: true},
		{name: "rp missing fingerprints", mode: UIAuthModeRP, rpAddr: "rp.internal:4987", rpFingerprints: "", wantErr: true},
		{name: "rp missing addr", mode: UIAuthModeRP, rpAddr: "", rpFingerprints: "abc123", wantErr: true},
		{name: "rp fully configured", mode: UIAuthModeRP, rpAddr: "rp.internal:4987", rpFingerprints: "abc123,def456", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			UIAuthMode = tt.mode
			LocalRPName = tt.localRPName
			LinkKeysRPAddr = tt.rpAddr
			LinkKeysRPFingerprints = tt.rpFingerprints

			err := ValidateUIAuthMode()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUIAuthMode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSplitCommaList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "single", in: "abc123", want: []string{"abc123"}},
		{name: "multiple with whitespace", in: " abc123 , def456,ghi789 ", want: []string{"abc123", "def456", "ghi789"}},
		{name: "skips empty entries", in: "abc123,,def456,", want: []string{"abc123", "def456"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitCommaList(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitCommaList(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("SplitCommaList(%q) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestSplitTrustedFingerprints(t *testing.T) {
	defer withUIAuthConfig(t)()
	LinkKeysRPFingerprints = "aa:bb, cc:dd"
	got := SplitTrustedFingerprints()
	if len(got) != 2 || got[0] != "aa:bb" || got[1] != "cc:dd" {
		t.Fatalf("SplitTrustedFingerprints() = %v", got)
	}
}
