package approvals

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestParseComment_RetryAndRerunShifts(t *testing.T) {
	command, ok := ParseComment("/reactorcide retry memory=8Gi timeout=2h")
	if !ok || command.Name != CommandRetry {
		t.Fatalf("got %+v %v", command, ok)
	}
	if want := (models.RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 7200}); command.Shifts != want {
		t.Fatalf("shifts = %+v, want %+v", command.Shifts, want)
	}
	command, ok = ParseComment("  /reactorcide rerun cpu=4 storage=20Gi \n")
	if !ok || command.Name != CommandRerun || command.Shifts.CPULimit != "4" || command.Shifts.StorageLimit != "20Gi" {
		t.Fatalf("rerun: %+v %v", command, ok)
	}
	if command, ok := ParseComment("/reactorcide retry"); !ok || !command.Shifts.IsZero() {
		t.Fatalf("plain retry: %+v %v", command, ok)
	}
}

func TestParseComment_RefusesOtherArguments(t *testing.T) {
	for _, body := range []string{
		"/reactorcide retry please",
		"/reactorcide retry image=evil:latest",
		"/reactorcide retry command=sh",
		"/reactorcide retry memory=lots",
		"/reactorcide rerun memory=8Gi env=A=1",
		"/reactorcide retry memory=8Gi memory=9Gi",
		"/reactorcide approve-ci memory=8Gi",
		"/reactorcide revoke timeout=1h",
	} {
		if command, ok := ParseComment(body); ok {
			t.Errorf("%q parsed as %+v", body, command)
		}
	}
}

func TestCommentError(t *testing.T) {
	if err := CommentError("/reactorcide retry image=evil"); err == nil {
		t.Fatal("expected an error for a change that is not a shift")
	}
	if err := CommentError("/reactorcide retry memory=8Gi"); err != nil {
		t.Fatalf("valid shifts: %v", err)
	}
	for _, body := range []string{"looks good", "/reactorcide accept", "/reactorcide retry", "/reactorcide approve-ci x"} {
		if err := CommentError(body); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}
