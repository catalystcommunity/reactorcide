package provenance

import (
	"errors"
	"fmt"
	"strings"
)

// Requirements are the facts that an admission policy can require of a
// verified statement.
type Requirements struct {
	// RefClasses must all be in the ref classes of the statement.
	RefClasses []string
	// CIOrigin, when set, must be the CI origin ("base" or "head").
	CIOrigin string
	// BuilderID, when set, must be the builder ID.
	BuilderID string
	// Repository, when set, must be the repository of the statement
	// (compared without scheme, ".git", and case).
	Repository string
	// Project, when set, must be the project ID of the statement, or the
	// project name when Organization is also set (a name is unique only in
	// one organization).
	Project string
	// Organization, when set, must be the organization name.
	Organization string
	// WorkflowSecurityID, when set, must be the security ID of the workflow.
	WorkflowSecurityID string
	// Subject, when set, must be the sha256 digest of one subject.
	SubjectSHA256 string
	// CoordinatorHashed requires that the subject of SubjectSHA256 (or, with
	// no SubjectSHA256, every subject) has the source coordinator_hashed: the
	// coordinator computed the digest from the uploaded bytes. A declared
	// digest (provenance_subjects) is what the job said.
	CoordinatorHashed bool
	// AllowAnyRepository lets a SubjectSHA256 check pass with no Repository
	// and no Project requirement. Without it such a check fails: every
	// project can sign a statement for any digest it declares, so a digest
	// alone does not say who built it.
	AllowAnyRepository bool
}

// ErrSubjectNeedsScope is returned when a subject check has no repository
// or project requirement.
var ErrSubjectNeedsScope = errors.New("a subject check needs a repository or a project requirement " +
	"(--require-repository or --require-project): any project can attest any digest it declares. " +
	"Give --allow-any-repository to accept a statement of any project")

// normalizeRepository makes a repository comparable: no scheme, no "git+",
// no user info, no ".git", no slashes at the ends, lowercase.
func normalizeRepository(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "git+")
	if _, rest, ok := strings.Cut(value, "://"); ok {
		value = rest
	}
	if at := strings.LastIndex(value, "@"); at >= 0 && at < strings.Index(value+"/", "/") {
		value = value[at+1:]
	}
	value = strings.Trim(value, "/")
	return strings.TrimSuffix(value, ".git")
}

// Check returns an error for the first requirement that the statement does
// not meet.
func Check(statement *Statement, req Requirements) error {
	internal := statement.Predicate.BuildDefinition.InternalParameters
	external := statement.Predicate.BuildDefinition.ExternalParameters
	digest := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.SubjectSHA256), "sha256:"))
	repository, project := strings.TrimSpace(req.Repository), strings.TrimSpace(req.Project)
	org := strings.TrimSpace(req.Organization)
	if digest != "" && repository == "" && project == "" && !req.AllowAnyRepository {
		return ErrSubjectNeedsScope
	}
	have := map[string]bool{}
	for _, class := range internal.RefClasses {
		have[class] = true
	}
	for _, class := range req.RefClasses {
		class = strings.TrimSpace(class)
		if class != "" && !have[class] {
			return fmt.Errorf("the ref classes %v do not include %q", internal.RefClasses, class)
		}
	}
	if req.CIOrigin != "" && internal.CIOrigin != req.CIOrigin {
		return fmt.Errorf("the CI origin is %q, not %q", internal.CIOrigin, req.CIOrigin)
	}
	if req.BuilderID != "" && statement.Predicate.RunDetails.Builder.ID != req.BuilderID {
		return fmt.Errorf("the builder is %q, not %q", statement.Predicate.RunDetails.Builder.ID, req.BuilderID)
	}
	if org != "" && internal.Organization != org {
		return fmt.Errorf("the organization is %q, not %q", internal.Organization, org)
	}
	if repository != "" && (internal.Repository == "" || normalizeRepository(internal.Repository) != normalizeRepository(repository)) {
		return fmt.Errorf("the repository is %q, not %q", internal.Repository, repository)
	}
	if project != "" {
		switch {
		case internal.ProjectID != "" && internal.ProjectID == project:
		case org != "" && internal.Project != "" && internal.Project == project:
		case org == "" && internal.Project == project && internal.ProjectID != project:
			return fmt.Errorf("the project name %q needs an organization requirement (--require-org), or give the project ID", project)
		default:
			return fmt.Errorf("the project is %q (%s), not %q", internal.Project, internal.ProjectID, project)
		}
	}
	if req.WorkflowSecurityID != "" && external.Workflow.SecurityID != strings.TrimSpace(req.WorkflowSecurityID) {
		return fmt.Errorf("the workflow security ID is %q, not %q", external.Workflow.SecurityID, req.WorkflowSecurityID)
	}
	if digest != "" {
		found, hashed := false, false
		for _, subject := range statement.Subject {
			if subject.Digest["sha256"] == digest {
				found = true
				if subject.Source() == SubjectCoordinatorHashed {
					hashed = true
				}
			}
		}
		if !found {
			return fmt.Errorf("no subject has the digest sha256:%s", digest)
		}
		if req.CoordinatorHashed && !hashed {
			return fmt.Errorf("the subject sha256:%s is not coordinator_hashed: the job declared the digest", digest)
		}
	} else if req.CoordinatorHashed {
		for _, subject := range statement.Subject {
			if subject.Source() != SubjectCoordinatorHashed {
				return fmt.Errorf("the subject %q is not coordinator_hashed: the job declared the digest", subject.Name)
			}
		}
	}
	return nil
}

// Facts returns the main facts of a statement as label and value pairs,
// in a stable order.
func Facts(statement *Statement) [][2]string {
	def := statement.Predicate.BuildDefinition
	ext, internal := def.ExternalParameters, def.InternalParameters
	rows := [][2]string{
		{"builder", statement.Predicate.RunDetails.Builder.ID},
		{"build type", def.BuildType},
		{"job", statement.Predicate.RunDetails.Metadata.InvocationID + " (" + ext.Job + ")"},
		{"organization", internal.Organization},
		{"project", internal.Project},
		{"repository", internal.Repository},
		{"workflow", ext.Workflow.Name + " (security ID " + ext.Workflow.SecurityID + ")"},
		{"node", ext.Node},
		{"ref", ext.Ref},
		{"event", ext.Event.Type},
		{"ref classes", strings.Join(internal.RefClasses, ",")},
		{"ci origin", internal.CIOrigin},
		{"execution profile", internal.ExecutionProfile},
		{"policy revision", internal.PolicyRevision},
		{"policy rule", internal.PolicyRule},
		{"approval", internal.ApprovalID},
		{"worker class", internal.WorkerClass},
		{"actor", internal.ActorPrincipalID + " (" + internal.ActorKind + ")"},
		{"started", statement.Predicate.RunDetails.Metadata.StartedOn},
		{"finished", statement.Predicate.RunDetails.Metadata.FinishedOn},
	}
	if ext.SubmittedRef != "" {
		rows = append(rows, [2]string{"submitted ref", ext.SubmittedRef})
	}
	for _, dep := range def.ResolvedDependencies {
		rows = append(rows, [2]string{"dependency " + dep.Name, dep.URI + "@" + dep.Digest["gitCommit"]})
	}
	for _, subject := range statement.Subject {
		source := subject.Source()
		if source == "" {
			source = "unknown source"
		}
		rows = append(rows, [2]string{"subject", subject.Name + " sha256:" + subject.Digest["sha256"] + " (" + source + ")"})
	}
	return rows
}
