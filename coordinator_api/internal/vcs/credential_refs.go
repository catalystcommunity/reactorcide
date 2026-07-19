package vcs

import (
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func ProviderSecretRef(refs models.JSONB, provider Provider) string {
	if refs == nil {
		return ""
	}
	keys := []string{
		strings.ToLower(string(provider)),
		string(provider),
		"default",
		"*",
	}
	for _, key := range keys {
		if ref := stringValue(refs[key]); ref != "" {
			return ref
		}
	}
	return ""
}

func ProjectWebhookSecretRef(project *models.Project, provider Provider) string {
	if project == nil {
		return ""
	}
	if ref := ProviderSecretRef(project.WebhookSecrets, provider); ref != "" {
		return ref
	}
	return project.WebhookSecret
}

func UserWebhookSecretRef(user *models.User, provider Provider) string {
	if user == nil {
		return ""
	}
	return ProviderSecretRef(user.WebhookSecrets, provider)
}

func ProjectVCSCredentialSecretRef(project *models.Project, provider Provider) string {
	if project == nil {
		return ""
	}
	if ref := ProviderSecretRef(project.VCSCredentialSecrets, provider); ref != "" {
		return ref
	}
	return project.VCSTokenSecret
}

func UserVCSCredentialSecretRef(user *models.User, provider Provider) string {
	if user == nil {
		return ""
	}
	return ProviderSecretRef(user.VCSCredentialSecrets, provider)
}

// ActiveWebhookSecretsNewestFirst reorders rows (as returned by a store's
// ListActiveProjectWebhookSecrets, which sorts oldest-first by created_at)
// into the newest-first precedence order that webhook signature verification
// should try them in: a freshly rotated-in secret should be tried before
// older still-active ones.
func ActiveWebhookSecretsNewestFirst(rows []models.ProjectWebhookSecret) []models.ProjectWebhookSecret {
	out := make([]models.ProjectWebhookSecret, len(rows))
	for i, row := range rows {
		out[len(rows)-1-i] = row
	}
	return out
}

// HighestPrecedenceActiveVCSCredential returns the highest-precedence active
// VCS credential row from rows (as returned by a store's
// ListActiveProjectVCSCredentials, which sorts oldest-first by created_at).
// The most recently created active row wins. ok is false when rows is empty.
func HighestPrecedenceActiveVCSCredential(rows []models.ProjectVCSCredential) (row models.ProjectVCSCredential, ok bool) {
	if len(rows) == 0 {
		return models.ProjectVCSCredential{}, false
	}
	return rows[len(rows)-1], true
}

func stringValue(v interface{}) string {
	switch typed := v.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}
