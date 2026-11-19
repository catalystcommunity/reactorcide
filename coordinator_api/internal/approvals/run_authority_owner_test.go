package approvals

import "testing"

func TestRunAuthorityOwnerOverrideRoundTrip(t *testing.T) {
	encoded, err := RunAuthority{OwnerOverride: &RunOwnerOverride{Profile: "pr-untrusted"}}.Encode()
	if err != nil || encoded == "" {
		t.Fatalf("an owner override alone must encode: %q %v", encoded, err)
	}
	decoded, err := DecodeRunAuthority(encoded)
	if err != nil || decoded.OwnerOverride == nil || decoded.OwnerOverride.Profile != "pr-untrusted" || decoded.Override != nil {
		t.Fatalf("decoded = %+v %v", decoded, err)
	}
	if _, err := DecodeRunAuthority(`{"owner_override":{"profile":""}}`); err == nil {
		t.Fatal("an owner override needs a profile")
	}
}
