package testresults

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Caller is the caller of a quarantine operation. It is the same shape as
// the required set caller, so that REST and the UI build it the same way.
type Caller = requiredset.Caller

// Error is a refused or failed operation. Code is permission_denied,
// invalid_argument, not_found, unavailable, or internal.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func newError(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode returns the code of an Error, or "internal".
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}

// Recorder writes one audit event.
type Recorder func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB)

// QuarantineService runs the test quarantine operations for every channel
// (REST, CLI, UI), so that the authorization and the audit events are the
// same everywhere.
//
// Who can quarantine a test of a project:
//   - an org admin of the project organization,
//   - a principal with the project role owner or admin,
//   - an owner of the workflow that the test ran in (WP7 workflow owners,
//     when the request names the workflow security ID),
//   - an instance or service token with no roles (the admin token of a
//     deployment with no logins) that has projects:manage for the
//     organization.
type QuarantineService struct {
	// Store must implement store.TestQuarantineStore and authz.RoleStore.
	// It can implement store.TestResultStore (names of a test ID),
	// store.RequiredSetStore (the maximum quarantine days of the
	// organization), and a principal lookup.
	Store  any
	Record Recorder
	Now    func() time.Time
}

type principalLookup interface {
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
}

type orgSettingsLookup interface {
	GetOrgRequiredSetSettings(ctx context.Context, orgID string) (*models.OrgRequiredSetSettings, error)
}

func (s *QuarantineService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *QuarantineService) deps() (store.TestQuarantineStore, *authz.Resolver, error) {
	st, ok := s.Store.(store.TestQuarantineStore)
	if !ok {
		return nil, nil, newError("unavailable", "test quarantine is not available")
	}
	roles, ok := s.Store.(authz.RoleStore)
	if !ok {
		return nil, nil, newError("unavailable", "test quarantine is not available")
	}
	return st, authz.NewResolver(roles), nil
}

func roleless(caller Caller) bool {
	return caller.Identity.Token != nil && caller.Identity.UserID == "" && caller.Identity.PrincipalID == ""
}

func tokenAllows(caller Caller, orgID, capability string) bool {
	token := caller.Identity.Token
	if token == nil || caller.Identity.PrincipalID != "" {
		return true
	}
	return token.HasOrganization(orgID) && token.HasCapability(capability)
}

// MayView reports whether the caller can list the quarantines of project.
func (s *QuarantineService) MayView(ctx context.Context, caller Caller, project *models.Project) error {
	_, resolver, err := s.deps()
	if err != nil {
		return err
	}
	orgID := project.OwnershipOrgID()
	if roleless(caller) {
		if tokenAllows(caller, orgID, tokencaps.ProjectsRead) {
			return nil
		}
		return newError("permission_denied", "the token cannot read this project")
	}
	visible, err := resolver.CanViewProject(ctx, caller.Identity, project)
	if err != nil {
		return newError("internal", "could not resolve the access of the caller")
	}
	if !visible || !tokenAllows(caller, orgID, tokencaps.ProjectsRead) {
		return newError("not_found", "project not found")
	}
	return nil
}

// MayManage reports whether the caller can quarantine a test of project.
// workflowSecurityID is optional: with it, an owner of that workflow can
// act too.
func (s *QuarantineService) MayManage(ctx context.Context, caller Caller, project *models.Project, workflowSecurityID string) error {
	_, resolver, err := s.deps()
	if err != nil {
		return err
	}
	orgID := project.OwnershipOrgID()
	if roleless(caller) {
		if tokenAllows(caller, orgID, tokencaps.ProjectsManage) {
			return nil
		}
		return newError("permission_denied", "the token needs projects:manage for this organization")
	}
	if !tokenAllows(caller, orgID, tokencaps.ProjectsManage) && caller.Identity.PrincipalID == "" {
		return newError("permission_denied", "the token needs projects:manage for this organization")
	}
	if admin, err := resolver.IsOrgAdmin(ctx, caller.Identity, orgID); err != nil {
		return newError("internal", "could not resolve the role of the caller")
	} else if admin {
		return nil
	}
	role, err := resolver.EffectiveRoleForProject(ctx, caller.Identity, project.ProjectID)
	if err != nil {
		return newError("internal", "could not resolve the role of the caller")
	}
	if role == models.RoleOwner || role == models.RoleAdmin {
		return nil
	}
	workflowSecurityID = strings.TrimSpace(workflowSecurityID)
	if workflowSecurityID != "" && requiredset.OwnerApprover != nil &&
		requiredset.OwnerApprover(ctx, s.Store, project, workflowSecurityID, caller) {
		return nil
	}
	return newError("permission_denied", "only a project owner, an org admin, or an owner of the workflow can quarantine a test")
}

// QuarantineRequest quarantines one test until Until.
type QuarantineRequest struct {
	// TestID, or Suite, Classname, and Name to compute it.
	TestID    string
	Suite     string
	Classname string
	Name      string
	Reason    string
	Until     time.Time
	// OwnerPrincipalID is the owner of the quarantine. Empty uses the
	// principal of the caller. The admin token has no principal, so it
	// must name an owner.
	OwnerPrincipalID string
	// WorkflowSecurityID lets an owner of that workflow quarantine.
	WorkflowSecurityID string
}

// MaxReasonLength is the longest quarantine reason, in characters.
const MaxReasonLength = 1000

// Quarantine adds a quarantine. A quarantine of the same test replaces the
// current one.
func (s *QuarantineService) Quarantine(ctx context.Context, caller Caller, project *models.Project, req QuarantineRequest) (*models.TestQuarantine, error) {
	st, _, err := s.deps()
	if err != nil {
		return nil, err
	}
	req.TestID = strings.ToLower(strings.TrimSpace(req.TestID))
	req.Suite, req.Classname, req.Name = strings.TrimSpace(req.Suite), strings.TrimSpace(req.Classname), strings.TrimSpace(req.Name)
	if req.TestID == "" {
		if req.Name == "" {
			return nil, newError("invalid_argument", "give a test ID, or the suite, class, and name of the test")
		}
		req.TestID = TestID(project.ProjectID, req.Suite, req.Classname, req.Name)
	}
	if !ValidTestID(req.TestID) {
		return nil, newError("invalid_argument", "the test ID must have %d lowercase hex characters", TestIDLength)
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return nil, newError("invalid_argument", "a quarantine needs a reason")
	}
	if utf8.RuneCountInString(req.Reason) > MaxReasonLength {
		return nil, newError("invalid_argument", "the reason has more than %d characters", MaxReasonLength)
	}
	if err := s.MayManage(ctx, caller, project, req.WorkflowSecurityID); err != nil {
		return nil, err
	}
	now := s.now()
	until := req.Until.UTC()
	if until.IsZero() {
		return nil, newError("invalid_argument", "a quarantine needs an expiry")
	}
	if !until.After(now) {
		return nil, newError("invalid_argument", "the expiry must be in the future")
	}
	maxDays := models.DefaultMaxQuarantineDays
	if settings, ok := s.Store.(orgSettingsLookup); ok {
		if row, err := settings.GetOrgRequiredSetSettings(ctx, project.OwnershipOrgID()); err == nil && row != nil && row.MaxQuarantineDays > 0 {
			maxDays = row.MaxQuarantineDays
		}
	}
	if until.After(now.Add(time.Duration(maxDays) * 24 * time.Hour)) {
		return nil, newError("invalid_argument", "the expiry must be at most %d days from now", maxDays)
	}
	owner := strings.TrimSpace(req.OwnerPrincipalID)
	if owner == "" {
		owner = caller.Actor.PrincipalID
	}
	if owner == "" {
		return nil, newError("invalid_argument", "a quarantine needs an owner principal (give owner_principal_id)")
	}
	if lookup, ok := s.Store.(principalLookup); ok {
		principal, err := lookup.GetPrincipal(ctx, owner)
		if err != nil || principal == nil {
			return nil, newError("invalid_argument", "the owner principal is not found")
		}
		if !principal.IsActive() {
			return nil, newError("invalid_argument", "the owner principal is not active")
		}
		owner = principal.PrincipalID
	}
	if req.Name == "" {
		if results, ok := s.Store.(store.TestResultStore); ok {
			if rows, err := results.GetTestFlakiness(ctx, project.ProjectID, []string{req.TestID}); err == nil {
				if row, found := rows[req.TestID]; found {
					req.Suite, req.Classname, req.Name = row.Suite, row.Classname, row.Name
				}
			}
		}
	}
	quarantine := &models.TestQuarantine{
		OrgID: project.OwnershipOrgID(), ProjectID: project.ProjectID, TestID: req.TestID,
		Suite: req.Suite, Classname: req.Classname, Name: req.Name,
		OwnerPrincipalID: owner, Reason: req.Reason, ExpiresAt: until,
		CreatedByPrincipalID: caller.Actor.Ptr(), CreatedBy: caller.Actor.Identity,
	}
	if err := st.CreateTestQuarantine(ctx, quarantine); err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			return nil, newError("invalid_argument", "the quarantine is not valid")
		}
		return nil, newError("internal", "could not store the quarantine")
	}
	s.record(ctx, project, "test.quarantine", quarantine.QuarantineID, models.JSONB{
		"test_id": quarantine.TestID, "suite": quarantine.Suite, "classname": quarantine.Classname, "name": quarantine.Name,
		"owner_principal_id": owner, "reason": quarantine.Reason, "until": until.Format(time.RFC3339)})
	return quarantine, nil
}

// Unquarantine ends a quarantine before its expiry.
func (s *QuarantineService) Unquarantine(ctx context.Context, caller Caller, project *models.Project, quarantineID, workflowSecurityID string) error {
	st, _, err := s.deps()
	if err != nil {
		return err
	}
	row, err := st.GetTestQuarantine(ctx, strings.TrimSpace(quarantineID))
	if err != nil || row.ProjectID != project.ProjectID {
		return newError("not_found", "the quarantine is not found")
	}
	if err := s.MayManage(ctx, caller, project, workflowSecurityID); err != nil {
		return err
	}
	removedBy := caller.Actor.Identity
	if removedBy == "" {
		removedBy = caller.Actor.PrincipalID
	}
	if removedBy == "" {
		removedBy = "api-token"
	}
	if err := st.EndTestQuarantine(ctx, row.QuarantineID, removedBy, s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return newError("not_found", "the quarantine already ended")
		}
		return newError("internal", "could not end the quarantine")
	}
	s.record(ctx, project, "test.unquarantine", row.QuarantineID, models.JSONB{"test_id": row.TestID, "name": row.Name})
	return nil
}

// List lists the quarantines of a project.
func (s *QuarantineService) List(ctx context.Context, caller Caller, project *models.Project, includeEnded bool) ([]models.TestQuarantine, error) {
	st, _, err := s.deps()
	if err != nil {
		return nil, err
	}
	if err := s.MayView(ctx, caller, project); err != nil {
		return nil, err
	}
	rows, err := st.ListTestQuarantines(ctx, project.ProjectID, includeEnded, s.now())
	if err != nil {
		return nil, newError("internal", "could not list the quarantines")
	}
	return rows, nil
}

func (s *QuarantineService) record(ctx context.Context, project *models.Project, action, subjectID string, details models.JSONB) {
	if s.Record == nil {
		return
	}
	details["project_id"] = project.ProjectID
	s.Record(ctx, project.OwnershipOrgID(), action, "test_quarantine", subjectID, details)
}

// QuarantineFile is the content of the file that a job reads from
// REACTORCIDE_QUARANTINED_TESTS_FILE.
type QuarantineFile struct {
	Version int                 `json:"version"`
	Tests   []QuarantineFileRow `json:"tests"`
}

// QuarantineFileRow is one quarantined test.
type QuarantineFileRow struct {
	TestID    string `json:"test_id"`
	Suite     string `json:"suite"`
	Classname string `json:"classname"`
	Name      string `json:"name"`
	ExpiresAt string `json:"expires_at"`
}
