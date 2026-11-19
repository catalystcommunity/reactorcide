package models

import (
	"time"

	"github.com/lib/pq"
)

// Reporting and ownership settings (plan items A3, A2, A7; migration 000056;
// docs/reporting-and-ownership.md).

// Report modes. full is the behavior before the feature.
const (
	ReportModeFull         = "full"
	ReportModeStatusesOnly = "statuses_only"
	ReportModeSilent       = "silent"
)

// Code owners import values.
const (
	CodeownersImportOff = "off"
	CodeownersImportOn  = "on"
)

// Path acceptance values.
const (
	PathAcceptanceOff     = "off"
	PathAcceptanceWarn    = "warn"
	PathAcceptanceEnforce = "enforce"
)

// DefaultCodeownersCacheTTLSeconds is the cache TTL of a parsed code owners
// file when no setting gives one.
const DefaultCodeownersCacheTTLSeconds = 600

// PathOwnerRule is one rule of the coordinator path ownership registry. The
// pattern uses the code owners syntax. The last matching rule wins. An empty
// owner list means the path has no owner.
type PathOwnerRule struct {
	Pattern string   `json:"pattern"`
	Owners  []string `json:"owners"`
}

// OrgReportingSettings are the organization values.
type OrgReportingSettings struct {
	OrgID                     string          `gorm:"column:org_id;primaryKey;type:uuid" json:"org_id"`
	ReportMode                string          `gorm:"not null;default:full" json:"report_mode"`
	AgreementContexts         pq.StringArray  `gorm:"type:text[];not null;default:'{}'" json:"agreement_contexts"`
	CodeownersImport          string          `gorm:"not null;default:off" json:"codeowners_import"`
	CodeownersCacheTTLSeconds int             `gorm:"column:codeowners_cache_ttl_seconds;not null;default:600" json:"codeowners_cache_ttl_seconds"`
	PathAcceptance            string          `gorm:"not null;default:off" json:"path_acceptance"`
	PathOwners                []PathOwnerRule `gorm:"type:jsonb;serializer:json;not null" json:"path_owners"`
	UpdatedAt                 time.Time       `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (OrgReportingSettings) TableName() string { return "org_reporting_settings" }

// DefaultOrgReportingSettings returns the built-in values: the behavior from
// before the feature.
func DefaultOrgReportingSettings(orgID string) *OrgReportingSettings {
	return &OrgReportingSettings{OrgID: orgID, ReportMode: ReportModeFull, AgreementContexts: pq.StringArray{},
		CodeownersImport: CodeownersImportOff, CodeownersCacheTTLSeconds: DefaultCodeownersCacheTTLSeconds,
		PathAcceptance: PathAcceptanceOff, PathOwners: []PathOwnerRule{}}
}

// ProjectReportingSettings are the values of one project. A nil field
// inherits the organization value.
type ProjectReportingSettings struct {
	ProjectID                 string           `gorm:"column:project_id;primaryKey;type:uuid" json:"project_id"`
	OrgID                     string           `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ReportMode                *string          `json:"report_mode,omitempty"`
	AgreementContexts         *pq.StringArray  `gorm:"type:text[]" json:"agreement_contexts,omitempty"`
	CodeownersImport          *string          `json:"codeowners_import,omitempty"`
	CodeownersCacheTTLSeconds *int             `gorm:"column:codeowners_cache_ttl_seconds" json:"codeowners_cache_ttl_seconds,omitempty"`
	PathAcceptance            *string          `json:"path_acceptance,omitempty"`
	PathOwners                *[]PathOwnerRule `gorm:"type:jsonb;serializer:json" json:"path_owners,omitempty"`
	UpdatedAt                 time.Time        `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (ProjectReportingSettings) TableName() string { return "project_reporting_settings" }

// EffectiveReportingSettings are the values that apply to one project, with
// the source of each value ("project", "org", or "default").
type EffectiveReportingSettings struct {
	ReportMode                string          `json:"report_mode"`
	ReportModeSource          string          `json:"report_mode_source"`
	AgreementContexts         []string        `json:"agreement_contexts"`
	AgreementContextsSource   string          `json:"agreement_contexts_source"`
	CodeownersImport          string          `json:"codeowners_import"`
	CodeownersImportSource    string          `json:"codeowners_import_source"`
	CodeownersCacheTTLSeconds int             `json:"codeowners_cache_ttl_seconds"`
	PathAcceptance            string          `json:"path_acceptance"`
	PathAcceptanceSource      string          `json:"path_acceptance_source"`
	PathOwners                []PathOwnerRule `json:"path_owners"`
	PathOwnersSource          string          `json:"path_owners_source"`
}

// ResolveReportingSettings merges the project values over the organization
// values. nil arguments give the built-in defaults.
func ResolveReportingSettings(org *OrgReportingSettings, project *ProjectReportingSettings) EffectiveReportingSettings {
	source := "default"
	if org == nil {
		org = DefaultOrgReportingSettings("")
	} else {
		source = "org"
	}
	out := EffectiveReportingSettings{
		ReportMode: org.ReportMode, ReportModeSource: source,
		AgreementContexts: append([]string{}, org.AgreementContexts...), AgreementContextsSource: source,
		CodeownersImport: org.CodeownersImport, CodeownersImportSource: source,
		CodeownersCacheTTLSeconds: org.CodeownersCacheTTLSeconds,
		PathAcceptance:            org.PathAcceptance, PathAcceptanceSource: source,
		PathOwners: append([]PathOwnerRule{}, org.PathOwners...), PathOwnersSource: source,
	}
	if project != nil {
		if project.ReportMode != nil {
			out.ReportMode, out.ReportModeSource = *project.ReportMode, "project"
		}
		if project.AgreementContexts != nil {
			out.AgreementContexts, out.AgreementContextsSource = append([]string{}, (*project.AgreementContexts)...), "project"
		}
		if project.CodeownersImport != nil {
			out.CodeownersImport, out.CodeownersImportSource = *project.CodeownersImport, "project"
		}
		if project.CodeownersCacheTTLSeconds != nil {
			out.CodeownersCacheTTLSeconds = *project.CodeownersCacheTTLSeconds
		}
		if project.PathAcceptance != nil {
			out.PathAcceptance, out.PathAcceptanceSource = *project.PathAcceptance, "project"
		}
		if project.PathOwners != nil {
			out.PathOwners, out.PathOwnersSource = append([]PathOwnerRule{}, (*project.PathOwners)...), "project"
		}
	}
	if out.ReportMode == "" {
		out.ReportMode = ReportModeFull
	}
	if out.CodeownersImport == "" {
		out.CodeownersImport = CodeownersImportOff
	}
	if out.PathAcceptance == "" {
		out.PathAcceptance = PathAcceptanceOff
	}
	if out.CodeownersCacheTTLSeconds <= 0 {
		out.CodeownersCacheTTLSeconds = DefaultCodeownersCacheTTLSeconds
	}
	return out
}

// Agreement statuses and results.
const (
	AgreementStatusPending  = "pending"
	AgreementStatusComplete = "complete"
	AgreementStatusNoOther  = "no_other"

	AgreementResultSuccess = "success"
	AgreementResultFailure = "failure"
)

// AgreementOtherResult is the result of one context of the other CI system
// on a commit.
type AgreementOtherResult struct {
	// Name is the status context or the check run name.
	Name string `json:"name"`
	// Kind is "status" (a commit status) or "check" (a check run).
	Kind string `json:"kind"`
	// State is the normalized state: success, failure, or pending.
	State string `json:"state"`
	// Raw is the provider state or conclusion.
	Raw       string `json:"raw,omitempty"`
	TargetURL string `json:"target_url,omitempty"`
}

// CIAgreement is the comparison of the Reactorcide result of one commit with
// the result of another CI system on the same commit.
type CIAgreement struct {
	ProjectID              string                 `gorm:"column:project_id;primaryKey;type:uuid" json:"project_id"`
	OrgID                  string                 `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	CommitSHA              string                 `gorm:"column:commit_sha;primaryKey" json:"commit_sha"`
	EventID                *string                `gorm:"type:uuid" json:"event_id,omitempty"`
	Provider               string                 `gorm:"not null;default:''" json:"provider"`
	Repository             string                 `gorm:"not null;default:''" json:"repository"`
	PRNumber               *int                   `gorm:"column:pr_number" json:"pr_number,omitempty"`
	Branch                 string                 `gorm:"not null;default:''" json:"branch,omitempty"`
	ReactorcideResult      string                 `gorm:"not null" json:"reactorcide_result"`
	ReactorcideDescription string                 `gorm:"not null;default:''" json:"reactorcide_description,omitempty"`
	OtherResults           []AgreementOtherResult `gorm:"type:jsonb;serializer:json;not null" json:"other_results"`
	OtherResult            string                 `gorm:"not null;default:''" json:"other_result,omitempty"`
	Agree                  *bool                  `json:"agree,omitempty"`
	Status                 string                 `gorm:"not null;default:pending" json:"status"`
	Attempts               int                    `gorm:"not null;default:0" json:"attempts"`
	NextCheckAt            *time.Time             `json:"next_check_at,omitempty"`
	LastError              string                 `gorm:"not null;default:''" json:"last_error,omitempty"`
	ReactorcideCompletedAt time.Time              `gorm:"autoCreateTime:false" json:"reactorcide_completed_at"`
	ObservedAt             *time.Time             `json:"observed_at,omitempty"`
	CreatedAt              time.Time              `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt              time.Time              `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (CIAgreement) TableName() string { return "ci_agreement" }

// Path acceptance states.
const (
	PathAcceptanceRequested = "requested"
	PathAcceptanceAccepted  = "accepted"
	PathAcceptanceRejected  = "rejected"
	PathAcceptanceRevoked   = "revoked"
)

// PathAcceptance is the acceptance (or the request for it) by the owner of a
// path area that a workflow depends on paths in that area.
type PathAcceptance struct {
	AcceptanceID       string     `gorm:"column:acceptance_id;primaryKey;type:uuid;default:generate_ulid()" json:"acceptance_id"`
	ProjectID          string     `gorm:"type:uuid;not null" json:"project_id"`
	OrgID              string     `gorm:"type:uuid;not null" json:"org_id"`
	WorkflowSecurityID string     `gorm:"not null" json:"workflow_security_id"`
	PathPattern        string     `gorm:"not null" json:"path_pattern"`
	OwnerSubject       string     `gorm:"not null" json:"owner_subject"`
	State              string     `gorm:"not null;default:requested" json:"state"`
	Reason             string     `gorm:"not null;default:''" json:"reason,omitempty"`
	RequestedBy        *string    `gorm:"type:uuid" json:"requested_by,omitempty"`
	RequestedAt        *time.Time `json:"requested_at,omitempty"`
	AcceptedBy         *string    `gorm:"type:uuid" json:"accepted_by,omitempty"`
	AcceptedAt         *time.Time `json:"accepted_at,omitempty"`
	RevokedBy          *string    `gorm:"type:uuid" json:"revoked_by,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
	DecisionReason     string     `gorm:"not null;default:''" json:"decision_reason,omitempty"`
	CreatedAt          time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (PathAcceptance) TableName() string { return "path_acceptances" }

// Active reports whether the acceptance counts at admission.
func (a PathAcceptance) Active() bool {
	return a.State == PathAcceptanceAccepted && a.RevokedAt == nil
}
