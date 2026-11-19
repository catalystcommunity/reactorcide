package models

import "testing"

func TestRerunShiftLimits_ProjectOverridesKeyByKey(t *testing.T) {
	org := &CITrustSettings{RerunShiftLimits: &RerunShiftLimits{MaxMemory: "8Gi", MaxTimeoutSeconds: 3600}}
	project := &CITrustSettings{RerunShiftLimits: &RerunShiftLimits{MaxMemory: "16Gi", MaxCPU: "8"}}
	got := EffectiveTrust(org, project).RerunShiftLimits
	want := RerunShiftLimits{MaxCPU: "8", MaxMemory: "16Gi", MaxTimeoutSeconds: 3600}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if (EffectiveTrust(nil, nil).RerunShiftLimits != RerunShiftLimits{}) {
		t.Fatal("no rows must give no limits")
	}
}

func TestRerunShiftLimits_ValidateAndLooser(t *testing.T) {
	if err := (CITrustSettings{RerunShiftLimits: &RerunShiftLimits{MaxMemory: "lots"}}).Validate(); err == nil {
		t.Fatal("expected an invalid memory limit to be refused")
	}
	if err := (CITrustSettings{RerunShiftLimits: &RerunShiftLimits{MaxTimeoutSeconds: -1}}).Validate(); err == nil {
		t.Fatal("expected a negative timeout limit to be refused")
	}
	current := EffectiveTrustSettings{RerunShiftLimits: RerunShiftLimits{MaxMemory: "8Gi", MaxTimeoutSeconds: 3600}}
	next := current
	next.RerunShiftLimits = RerunShiftLimits{MaxMemory: "16Gi", MaxTimeoutSeconds: 3600}
	if reasons := LooserThan(next, current, nil); len(reasons) != 1 {
		t.Fatalf("raising a limit: %v", reasons)
	}
	next.RerunShiftLimits = RerunShiftLimits{MaxMemory: "4Gi", MaxTimeoutSeconds: 600}
	if reasons := LooserThan(next, current, nil); len(reasons) != 0 {
		t.Fatalf("lowering limits is stricter: %v", reasons)
	}
}

func TestRunShifts_Merge(t *testing.T) {
	got := RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 60}.Merge(RunShifts{TimeoutSeconds: 120, CPULimit: "4"})
	if got != (RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 120, CPULimit: "4"}) {
		t.Fatalf("got %+v", got)
	}
	if (RunShifts{}).Ptr() != nil {
		t.Fatal("zero shifts must be nil")
	}
}
