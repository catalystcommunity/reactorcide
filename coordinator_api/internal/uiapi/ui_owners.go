package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// ownershipToWire converts an owner snapshot (WP7). An unknown snapshot is
// nil. A known snapshot with no owner has no group and no subjects.
func ownershipToWire(o models.WorkflowOwnership) *csilapi.WorkflowOwnership {
	if !o.Known() {
		return nil
	}
	out := &csilapi.WorkflowOwnership{}
	if group := o.GroupName(); group != "" {
		out.Group = &group
	}
	if len(o.Subjects) > 0 {
		out.Subjects = append([]string{}, o.Subjects...)
	}
	if o.Contact != nil {
		out.Channel = optionalText(o.Contact.Channel)
		out.HelpUrl = optionalText(o.Contact.HelpURL)
		out.Email = optionalText(o.Contact.Email)
	}
	out.Source = optionalText(o.Source)
	out.CiSha = optionalText(o.CISHA)
	return out
}

// workflowOwnership loads the owner snapshot of one workflow instance.
func (s *UiService) workflowOwnership(ctx context.Context, workflowID string) *csilapi.WorkflowOwnership {
	if workflowID == "" {
		return nil
	}
	wig, ok := s.deps.Store.(workflowInstanceGetter)
	if !ok {
		return nil
	}
	wf, err := wig.GetWorkflowInstance(ctx, workflowID)
	if err != nil || wf == nil {
		return nil
	}
	out := ownershipToWire(wf.Ownership())
	if out != nil {
		// Only the workflow row records whether the snapshot came from a
		// trusted ref; the job copy does not, so a job owner has no flag.
		out.Trusted = optionalBool(wf.OwnerTrusted)
	}
	return out
}

// optionalBool returns a pointer to value.
func optionalBool(value bool) *bool { return &value }
