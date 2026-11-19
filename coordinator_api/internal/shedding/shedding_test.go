package shedding

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type fakeShedStore struct {
	state models.ShedState
	err   error
}

func (f fakeShedStore) EffectiveDraftPRs(context.Context, string) (string, error) { return "run", nil }
func (f fakeShedStore) SetOrganizationDraftPRs(context.Context, string, string) error {
	return nil
}
func (f fakeShedStore) GetShedState(context.Context, string) (models.ShedState, error) {
	return f.state, f.err
}
func (f fakeShedStore) SetGlobalShedLevel(context.Context, string) error { return nil }
func (f fakeShedStore) SetOrganizationShed(context.Context, string, string, *models.ShedActionsConfig, bool) error {
	return nil
}

func TestStateFailsOpen(t *testing.T) {
	if State(context.Background(), struct{}{}, "org").Level != models.ShedLevelNormal {
		t.Fatal("a store without shed settings must be normal")
	}
	if State(context.Background(), fakeShedStore{err: errors.New("db down")}, "org").Actions.Any() {
		t.Fatal("a failed read must be normal")
	}
	critical := fakeShedStore{state: models.EffectiveShedState("critical", "normal", nil)}
	if !SkipAdvisory(context.Background(), critical, "org") {
		t.Fatal("critical skips advisory workflows")
	}
	degraded := fakeShedStore{state: models.EffectiveShedState("degraded", "normal", nil)}
	if SkipAdvisory(context.Background(), degraded, "org") {
		t.Fatal("degraded does not skip advisory workflows")
	}
	if !strings.Contains(AdvisorySkipReason(critical.state), "critical (global)") {
		t.Fatal(AdvisorySkipReason(critical.state))
	}
}

func TestDraftDecision(t *testing.T) {
	normal := models.EffectiveShedState("normal", "normal", nil)
	degraded := models.EffectiveShedState("normal", "degraded", nil)
	if skip, _, _ := DraftDecision(models.DraftPRsRun, degraded); skip {
		t.Fatal("run never skips")
	}
	if skip, kind, _ := DraftDecision(models.DraftPRsSkip, normal); !skip || kind != models.SkipReasonDraft {
		t.Fatal("skip always skips with kind draft")
	}
	if skip, _, _ := DraftDecision(models.DraftPRsSkipWhenShedding, normal); skip {
		t.Fatal("skip_when_shedding runs at normal")
	}
	skip, kind, reason := DraftDecision(models.DraftPRsSkipWhenShedding, degraded)
	if !skip || kind != models.SkipReasonShed || !strings.Contains(reason, "shed level degraded (org)") {
		t.Fatalf("skip_when_shedding while degraded: %t %s %s", skip, kind, reason)
	}
}

func TestRefusals(t *testing.T) {
	degraded := models.EffectiveShedState("degraded", "normal", nil)
	critical := models.EffectiveShedState("critical", "normal", nil)
	if ScheduleRefusal(models.EffectiveShedState("normal", "normal", nil)) != "" || ScheduleRefusal(degraded) == "" {
		t.Fatal("degraded pauses schedules; normal does not")
	}
	if ManualRunRefusal(degraded, false) != "" {
		t.Fatal("degraded does not refuse manual runs")
	}
	if ManualRunRefusal(critical, false) == "" || ManualRunRefusal(critical, true) != "" {
		t.Fatal("critical refuses manual runs except from administrators")
	}
}
