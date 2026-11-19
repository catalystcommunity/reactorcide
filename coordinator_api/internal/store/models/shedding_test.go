package models

import "testing"

func TestEffectiveShedState(t *testing.T) {
	cases := []struct {
		global, org, level, source string
	}{
		{"normal", "normal", "normal", ""},
		{"degraded", "normal", "degraded", "global"},
		{"normal", "critical", "critical", "org"},
		{"critical", "degraded", "critical", "global"},
		{"degraded", "degraded", "degraded", "org"},
		{"bogus", "", "normal", ""},
	}
	for _, tc := range cases {
		state := EffectiveShedState(tc.global, tc.org, nil)
		if state.Level != tc.level || state.Source != tc.source {
			t.Errorf("global=%s org=%s: level=%s source=%s, want %s %s", tc.global, tc.org, state.Level, state.Source, tc.level, tc.source)
		}
	}
	critical := EffectiveShedState("critical", "normal", nil).Actions
	if !critical.SkipDrafts || !critical.PauseSchedules || !critical.LowerPRPriority || !critical.SkipAdvisory || !critical.RefuseManualRuns {
		t.Fatalf("critical must have every built-in action: %+v", critical)
	}
	degraded := EffectiveShedState("degraded", "normal", nil).Actions
	if !degraded.SkipDrafts || !degraded.PauseSchedules || !degraded.LowerPRPriority || degraded.SkipAdvisory || degraded.RefuseManualRuns {
		t.Fatalf("degraded built-in actions are wrong: %+v", degraded)
	}
	if EffectiveShedState("normal", "normal", nil).Actions.Any() {
		t.Fatal("normal must have no actions")
	}
	custom := &ShedActionsConfig{Degraded: ShedActions{PauseSchedules: true}}
	if got := EffectiveShedState("degraded", "normal", custom).Actions; got.SkipDrafts || !got.PauseSchedules {
		t.Fatalf("organization actions must replace the built-in actions: %+v", got)
	}
}

func TestEffectiveDraftPRs(t *testing.T) {
	cases := []struct{ project, org, want string }{
		{"inherit", "run", "run"},
		{"inherit", "skip", "skip"},
		{"skip_when_shedding", "run", "skip_when_shedding"},
		{"run", "skip", "run"},
		{"", "", "run"},
		{"inherit", "bogus", "run"},
	}
	for _, tc := range cases {
		if got := EffectiveDraftPRs(tc.project, tc.org); got != tc.want {
			t.Errorf("project=%q org=%q: %s, want %s", tc.project, tc.org, got, tc.want)
		}
	}
}

func TestSettingsForDefaultsProfile(t *testing.T) {
	secure := SettingsForDefaultsProfile(DefaultsProfileSecure)
	if len(secure.RunGate) != 1 || secure.RunGate[0] != RunGateOutsideContributors || secure.DynamicJobs != DynamicJobsDeny || !secure.BuiltInPolicy {
		t.Fatalf("secure profile settings: %+v", secure)
	}
	legacy := SettingsForDefaultsProfile(DefaultsProfileLegacy)
	if len(legacy.RunGate) != 0 || legacy.DynamicJobs != DynamicJobsAllow || legacy.BuiltInPolicy {
		t.Fatalf("legacy profile settings: %+v", legacy)
	}
	if UsesBuiltInPolicy("") || UsesBuiltInPolicy("unknown") {
		t.Fatal("an unknown profile must keep the legacy behavior")
	}
}
