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
