// Package outboundevents delivers phase events to outbound sinks (plan items
// E1 and E2, decisions D7, D22, D27). See docs/outbound-events.md.
//
// Flow:
//
//  1. Database triggers (migration 000058) write an event_outbox row in the
//     transaction of each status change.
//  2. The Dispatcher (one replica, lease "outbound-events") assigns the
//     sequence of each event in its order key, builds the payload, and makes
//     one delivery for each matching subscription. It submits one Corndogs
//     task (keyed submit) for the first open delivery of each
//     (subscription, order key), on an internal queue that no worker reads.
//  3. The DeliveryLoop (every replica) claims a task, calls the sink, and
//     records the attempt. A failure retries with backoff and jitter; after
//     the attempt limit the delivery goes to dead_letter. A replay puts it
//     back.
//
// Nothing here can delay or stop a run: the outbox write is the only part in
// the run's transaction, and the triggers catch their own errors.
package outboundevents

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// SchemaVersion is the version of the payload schema. A change that removes
// or renames a field increases it.
const SchemaVersion = 1

// Ref is an ID with a display name.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// WorkflowRef identifies a workflow.
type WorkflowRef struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	SecurityID string `json:"security_id,omitempty"`
}

// JobRef identifies a job.
type JobRef struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
}

// Actor is the principal that started the run.
type Actor struct {
	PrincipalID string `json:"principal_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Links are URLs for a human.
type Links struct {
	UI string `json:"ui,omitempty"`
}

// Payload is the JSON body of an outbound event (schema version 1).
type Payload struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	SchemaVersion int            `json:"schema_version"`
	OccurredAt    string         `json:"occurred_at"`
	Sequence      int64          `json:"sequence"`
	OrderKey      string         `json:"order_key"`
	Org           Ref            `json:"org"`
	Project       *Ref           `json:"project,omitempty"`
	CIEvent       *Ref           `json:"ci_event,omitempty"`
	Workflow      *WorkflowRef   `json:"workflow,omitempty"`
	Node          *Ref           `json:"node,omitempty"`
	Job           *JobRef        `json:"job,omitempty"`
	Actor         Actor          `json:"actor"`
	Repo          string         `json:"repo,omitempty"`
	Ref           string         `json:"ref"`
	SHA           string         `json:"sha"`
	RefClasses    []string       `json:"ref_classes"`
	OwnerGroup    string         `json:"owner_group"`
	Status        string         `json:"status"`
	Result        string         `json:"result"`
	FailureClass  string         `json:"failure_class"`
	Details       map[string]any `json:"details,omitempty"`
	Links         Links          `json:"links"`
}

func strValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// BuildPayload makes the payload of one outbox row. uiBaseURL is the base
// URL of the web UI (empty for no link).
func BuildPayload(f models.OutboxEventFacts, sequence int64, uiBaseURL string) Payload {
	p := Payload{
		ID:            f.EventID,
		Type:          f.EventType,
		SchemaVersion: SchemaVersion,
		OccurredAt:    f.OccurredAt.UTC().Format(time.RFC3339Nano),
		Sequence:      sequence,
		OrderKey:      f.OrderKey,
		Org:           Ref{ID: f.OrgID, Name: f.OrgName},
		Actor:         Actor{Kind: f.ActorKind, DisplayName: f.ActorDisplayName, PrincipalID: strValue(f.ActorPrincipalID)},
		Repo:          f.VCSRepo,
		Ref:           firstNonEmpty(f.Ref, f.EventRef),
		SHA:           firstNonEmpty(f.CommitSHA, f.EventSHA),
		RefClasses:    append([]string{}, f.RefClasses...),
		OwnerGroup:    f.OwnerGroup,
		Status:        f.Status,
		Result:        f.Result,
		FailureClass:  f.FailureClass,
	}
	if f.ProjectID != nil {
		p.Project = &Ref{ID: *f.ProjectID, Name: f.ProjectName}
	}
	if f.CIEventID != nil {
		p.CIEvent = &Ref{ID: *f.CIEventID}
	}
	securityID := firstNonEmpty(f.WorkflowSecurityID, f.WorkflowSecID)
	if f.WorkflowID != nil || securityID != "" {
		p.Workflow = &WorkflowRef{ID: strValue(f.WorkflowID), Name: f.WorkflowName, SecurityID: securityID}
	}
	if f.NodeID != nil || f.NodeName != "" {
		p.Node = &Ref{ID: strValue(f.NodeID), Name: f.NodeName}
	}
	details := map[string]any{}
	for k, v := range f.Details {
		switch k {
		case "job_name", "node_name", "workflow_name":
			continue
		}
		if v != nil {
			details[k] = v
		}
	}
	if f.JobID != nil {
		job := &JobRef{ID: *f.JobID, Name: f.JobName}
		if attempt, ok := details["attempt"].(float64); ok {
			job.Attempt = int(attempt)
			delete(details, "attempt")
		}
		p.Job = job
	}
	if len(details) > 0 {
		p.Details = details
	}
	p.Links.UI = uiLink(uiBaseURL, f)
	return p
}

func uiLink(base string, f models.OutboxEventFacts) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	switch {
	case f.WorkflowID != nil:
		return base + "/app/workflows/" + *f.WorkflowID
	case f.EventType == models.OutboundEventEventEvaluated && f.CIEventID != nil:
		return base + "/app/events/" + *f.CIEventID
	case f.JobID != nil:
		return base + "/app/jobs/" + *f.JobID
	case f.CIEventID != nil:
		return base + "/app/events/" + *f.CIEventID
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ToJSONB converts the payload to the stored map form.
func (p Payload) ToJSONB() models.JSONB {
	raw, err := json.Marshal(p)
	if err != nil {
		return models.JSONB{"id": p.ID, "type": p.Type}
	}
	out := models.JSONB{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return models.JSONB{"id": p.ID, "type": p.Type}
	}
	return out
}
