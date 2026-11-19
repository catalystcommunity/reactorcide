package handlers

import (
	"encoding/json"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestParseProjectInfraRetries(t *testing.T) {
	cases := []struct {
		raw     string
		want    *int
		set     bool
		wantErr bool
	}{
		{"", nil, false, false},
		{`"inherit"`, nil, true, false},
		{`null`, nil, true, false},
		{`0`, intPointer(0), true, false},
		{`3`, intPointer(3), true, false},
		{`6`, nil, false, true},
		{`-1`, nil, false, true},
		{`"two"`, nil, false, true},
	}
	for _, tc := range cases {
		value, set, err := parseProjectInfraRetries(json.RawMessage(tc.raw))
		if (err != nil) != tc.wantErr || set != tc.set || (value == nil) != (tc.want == nil) || (value != nil && *value != *tc.want) {
			t.Errorf("parseProjectInfraRetries(%s) = %v %v %v", tc.raw, value, set, err)
		}
	}
}

func intPointer(v int) *int { return &v }

func TestJobResponseCarriesFailureClass(t *testing.T) {
	class := models.FailureClassWorkerLost
	job := &models.Job{JobID: "j", Status: "failed", FailureClass: &class, FailureDetail: "no lease", RetryCount: 2}
	h := &JobHandler{}
	response := h.jobToResponse(job)
	if response.FailureClass != class || response.FailureOwner != models.FailureOwnerInfrastructure || response.FailureDetail != "no lease" || response.RetryCount != 2 {
		t.Fatalf("response = %+v", response)
	}
}
