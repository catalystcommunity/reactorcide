package worker

import (
	approvalstate "github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const overrideRuleID = approvalstate.OverrideRuleID

func runAuthoritySnapshot(parentJob *models.Job) (approvalstate.RunAuthority, error) {
	return approvalstate.DecodeRunAuthority(envString(parentJob, approvalstate.RunAuthorityEnv))
}
