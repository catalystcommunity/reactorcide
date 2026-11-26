package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/shedding"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/sirupsen/logrus"
)

// WebhookHandler handles VCS webhook events
type WebhookHandler struct {
	store          store.Store
	corndogsClient corndogs.ClientInterface
	vcsClients     map[vcs.Provider]vcs.Client
	tokenResolver  vcs.TokenResolverFunc         // optional: per-project secret resolution
	clientFactory  vcs.ClientFactoryFunc         // optional: create client with per-project token
	statusUpdater  vcs.JobStatusUpdaterInterface // optional: used to refresh comments for in-flight jobs on merge
	githubApps     githubAppWebhooks             // optional: GitHub App deliveries (github_app_webhook.go)
	logger         *logrus.Logger
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(store store.Store, corndogsClient corndogs.ClientInterface) *WebhookHandler {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &WebhookHandler{
		store:          store,
		corndogsClient: corndogsClient,
		vcsClients:     make(map[vcs.Provider]vcs.Client),
		logger:         logger,
	}
}

// AddVCSClient adds a VCS client for a specific provider
func (h *WebhookHandler) AddVCSClient(provider vcs.Provider, client vcs.Client) {
	h.vcsClients[provider] = client
	h.logger.WithField("provider", provider).Info("Added VCS client")
}

// SetTokenResolver sets the function used to resolve per-project VCS token secrets.
func (h *WebhookHandler) SetTokenResolver(fn vcs.TokenResolverFunc) {
	h.tokenResolver = fn
}

// SetClientFactory sets the function used to create per-project VCS clients.
func (h *WebhookHandler) SetClientFactory(fn vcs.ClientFactoryFunc) {
	h.clientFactory = fn
}

// SetStatusUpdater wires a VCS status updater so that on a PR-merge webhook,
// in-flight jobs for that PR can be nudged to the per-job-comment flow.
func (h *WebhookHandler) SetStatusUpdater(u vcs.JobStatusUpdaterInterface) {
	h.statusUpdater = u
}

// HandleGitHubWebhook handles GitHub webhook events
func (h *WebhookHandler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, vcs.GitHub)
}

// HandleGitLabWebhook handles GitLab webhook events
func (h *WebhookHandler) HandleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleWebhook(w, r, vcs.GitLab)
}

// extractRepoCloneURL extracts the repository clone URL from a raw webhook
// payload without full parsing. This is used to look up the project before
// signature validation, enabling per-project webhook secrets.
func extractRepoCloneURL(body []byte, contentType string) (string, error) {
	jsonBody := body

	// Handle form-encoded payloads (payload=<url-encoded-json>)
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", fmt.Errorf("parse form: %w", err)
		}
		p := values.Get("payload")
		if p == "" {
			return "", fmt.Errorf("form-encoded body has no 'payload' field")
		}
		jsonBody = []byte(p)
	}

	// GitHub and GitLab both include repository info at the top level.
	// GitHub: {"repository": {"clone_url": "...", "full_name": "..."}}
	// GitLab: {"project": {"git_http_url": "...", "path_with_namespace": "..."}}
	var payload struct {
		Repository struct {
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
		Project struct {
			GitHTTPURL string `json:"git_http_url"`
		} `json:"project"`
	}
	if err := json.Unmarshal(jsonBody, &payload); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if payload.Repository.CloneURL != "" {
		return payload.Repository.CloneURL, nil
	}
	if payload.Project.GitHTTPURL != "" {
		return payload.Project.GitHTTPURL, nil
	}
	return "", fmt.Errorf("no clone URL found in payload")
}

// webhookSecretRotationStore is the narrow store interface needed to list
// active rotatable webhook secrets and stamp last-used timestamps. Defined
// here on the consumer side (repo convention: narrow interface + type
// assertion on the store) so tests can supply a fake without implementing
// the full store.Store interface, and so this file stays decoupled from
// unrelated store surface. The concrete PostgresDbStore satisfies it via
// internal/store/postgres_store/rotation_operations.go.
type webhookSecretRotationStore interface {
	ListActiveProjectWebhookSecrets(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error)
	TouchProjectWebhookSecretLastUsed(ctx context.Context, id string) error
}

type ciEvaluationFactsStore interface {
	ResolveVCSIdentityLink(ctx context.Context, provider, subject string) (*models.VCSIdentityLink, error)
	ListGroupsForUser(ctx context.Context, userID string) ([]models.Group, error)
	store.CIApprovalStore
}

type vcsReportGenerationStore interface {
	StartVCSReportGeneration(context.Context, *models.VCSReportTarget, string) error
}

type ciPolicyStore interface {
	GetCIPolicyByProject(context.Context, string) (*models.CIPolicy, error)
}

func (h *WebhookHandler) startVCSReportGeneration(ctx context.Context, job *models.Job, project *models.Project, event *vcs.WebhookEvent) error {
	if event.PullRequest == nil {
		return nil
	}
	reports, ok := h.store.(vcsReportGenerationStore)
	if !ok {
		return nil
	}
	target := &models.VCSReportTarget{OrgID: project.OwnershipOrgID(), ProjectID: &project.ProjectID,
		Provider: string(event.Provider), Repository: event.Repository.FullName, TargetType: "pull_request",
		ExternalTargetID: fmt.Sprintf("%d", event.PullRequest.Number), RootMarker: "<!-- reactorcide:report:v1 -->",
		CurrentGeneration: 1}
	if err := reports.StartVCSReportGeneration(ctx, target, event.PullRequest.HeadSHA); err != nil {
		return err
	}
	job.JobEnvVars["REACTORCIDE_REPORT_TARGET_ID"] = target.ReportTargetID
	job.JobEnvVars["REACTORCIDE_REPORT_GENERATION"] = target.CurrentGeneration
	return nil
}

// attachCIEvaluationFacts attaches the actor, approval and policy facts and
// reports what it found for the project's CI policy.
func (h *WebhookHandler) attachCIEvaluationFacts(ctx context.Context, job *models.Job, project *models.Project, event *vcs.WebhookEvent, client vcs.Client) ciPolicyState {
	if job.JobEnvVars == nil {
		job.JobEnvVars = models.JSONB{}
	}
	subjects := []string{}
	if subject := vcsUserSubject(event.Provider, event.SenderLogin); subject != "" {
		subjects = append(subjects, subject)
	}
	if resolver, ok := client.(vcs.ActorSubjectResolver); ok {
		resolved, err := resolver.ResolveActorSubjects(ctx, event.Repository.FullName, event.SenderLogin)
		if err != nil {
			h.logger.WithError(err).Warn("Could not resolve VCS actor facts; head CI will fail closed")
		} else {
			subjects = append(subjects, resolved...)
		}
	}
	// The principal of the sender: its identity subjects, the subjects of
	// every linked identity, and its groups. The actor of the job is the
	// principal that started the run (the sender, or the starter of a
	// coordinator event).
	if sender := h.resolveVCSPrincipal(ctx, event.Provider, event.SenderLogin); sender != nil {
		if extra, err := h.principalSubjects(ctx, project, sender); err != nil {
			h.logger.WithError(err).Warn("Could not resolve the principal facts of the actor")
		} else {
			subjects = append(subjects, extra...)
		}
	}
	principals.ApplyToJob(job, h.webhookActor(ctx, event))
	factsStore, ok := h.store.(ciEvaluationFactsStore)
	if ok {
		link, err := factsStore.ResolveVCSIdentityLink(ctx, string(event.Provider), event.SenderLogin)
		if err == nil && link != nil && link.VerifiedBy != "" {
			groups, groupErr := factsStore.ListGroupsForUser(ctx, link.UserID)
			if groupErr == nil {
				for _, group := range groups {
					if group.OrgID == project.OwnershipOrgID() {
						subjects = append(subjects, "reactorcide_group:"+group.Name)
					}
				}
			}
		}
		if event.PullRequest != nil {
			pr := event.PullRequest
			// The approval actions of this head, for jobs that want to show or
			// check them. Admission does not read this snapshot: it loads the
			// current actions when it decides.
			actions, err := factsStore.ListCIApprovalsForHead(ctx, project.ProjectID, pr.Number, pr.HeadSHA)
			if err != nil {
				h.logger.WithError(err).Warn("Could not load CI approval snapshot")
			} else {
				if actions == nil {
					actions = []models.CIApproval{}
				}
				if encoded, marshalErr := json.Marshal(actions); marshalErr == nil {
					job.JobEnvVars["REACTORCIDE_CI_APPROVALS"] = string(encoded)
				}
			}
		}
	}
	if encoded, err := json.Marshal(uniqueStrings(subjects)); err == nil {
		job.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = string(encoded)
	}
	state := h.attachCIPolicy(ctx, job, project)
	if state == ciPolicyNone && event.PullRequest != nil {
		// Secure defaults: a pull request of a project with no stored policy
		// gets the built-in policy (defaults_policy.go).
		state = h.attachBuiltInCIPolicy(ctx, job, project)
	}
	return state
}

// ciPolicyState is what attachCIPolicy found.
type ciPolicyState int

const (
	// ciPolicyNone: the project stores no CI policy. Nothing is gated; the
	// eval runs the configured or the event's own CI code.
	ciPolicyNone ciPolicyState = iota
	// ciPolicyAttached: the policy snapshot is on the job.
	ciPolicyAttached
	// ciPolicyUnavailable: a policy may exist but could not be loaded or
	// failed validation. The eval keeps the trusted base, so head CI fails
	// closed.
	ciPolicyUnavailable
)

func (h *WebhookHandler) attachCIPolicy(ctx context.Context, job *models.Job, project *models.Project) ciPolicyState {
	policies, ok := h.store.(ciPolicyStore)
	if !ok {
		return ciPolicyNone
	}
	stored, err := policies.GetCIPolicyByProject(ctx, project.ProjectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ciPolicyNone
		}
		h.logger.WithError(err).WithField("project_id", project.ProjectID).Error("Could not load coordinator CI policy; head CI will fail closed")
		return ciPolicyUnavailable
	}
	document, err := json.Marshal(stored.Document)
	if err != nil {
		h.logger.WithError(err).WithField("project_id", project.ProjectID).Error("Could not encode coordinator CI policy; head CI will fail closed")
		return ciPolicyUnavailable
	}
	parsed, err := cipolicy.ParseDocument(document)
	if err != nil || parsed.Revision != stored.Revision {
		h.logger.WithError(err).WithField("project_id", project.ProjectID).Error("Coordinator CI policy failed integrity validation; head CI will fail closed")
		return ciPolicyUnavailable
	}
	job.JobEnvVars["REACTORCIDE_CI_POLICY"] = string(document)
	job.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"] = stored.Revision
	return ciPolicyAttached
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func vcsUserSubject(provider vcs.Provider, login string) string {
	providerName := strings.ToLower(strings.TrimSpace(string(provider)))
	login = strings.ToLower(strings.TrimSpace(login))
	if providerName == "" || login == "" {
		return ""
	}
	return "vcs_user:" + providerName + "/" + login
}

// webhookSecretCandidate is one secret value to try against an incoming
// webhook signature. RotationID is non-empty when the candidate came from a
// project_webhook_secrets row, so a successful match can stamp
// last_used_at on that row.
type webhookSecretCandidate struct {
	Secret     string
	Source     string
	RotationID string
}

// resolveWebhookSecretCandidates gathers the secret values that should be
// tried against an incoming webhook signature.
//
// Candidates are grouped into three tiers, and only the FIRST non-empty tier
// is returned — tiers are mutually exclusive so a project with its own
// dedicated secret(s) never also accepts a signature computed with the
// shared org or global secret (that would be a forgery risk: anyone who
// knows the broadly-shared org/global secret could forge webhooks for a
// project that deliberately configured its own). Within a tier, multiple
// candidates remain (that's what makes rotation possible):
//
//  1. Project-scoped: all active project_webhook_secrets rotation rows for
//     (project, provider), newest first, PLUS the legacy project jsonb/text
//     ref. Both are project-scoped and coexist during migration to rotation
//     rows, so they share a tier — either one configured is enough to make
//     this the winning (and only) tier.
//  2. Org-scoped: the project owner's org-level ref (jsonb/legacy).
//  3. Global/env fallback.
//
// Verification tries each candidate within the winning tier in order and
// stops at the first match. Secret values are never logged.
func (h *WebhookHandler) resolveWebhookSecretCandidates(ctx context.Context, project *models.Project, provider vcs.Provider) []webhookSecretCandidate {
	var projectTier []webhookSecretCandidate

	if project != nil {
		if rotationStore, ok := h.store.(webhookSecretRotationStore); ok {
			rows, err := rotationStore.ListActiveProjectWebhookSecrets(ctx, project.ProjectID, string(provider))
			if err != nil {
				h.logger.WithError(err).WithFields(logrus.Fields{
					"project":  project.Name,
					"provider": provider,
				}).Warn("Failed to list active rotatable webhook secrets")
			}
			// Rows come back oldest-first (created_at ASC); try newest first.
			for _, row := range vcs.ActiveWebhookSecretsNewestFirst(rows) {
				secret := h.resolveSecretRef(ctx, row.SecretRef, "project-rotation", provider, project)
				if secret == "" {
					continue
				}
				projectTier = append(projectTier, webhookSecretCandidate{
					Secret:     secret,
					Source:     "project-rotation",
					RotationID: row.ID,
				})
			}
		}
	}
	if ref := vcs.ProjectWebhookSecretRef(project, provider); ref != "" {
		if secret := h.resolveSecretRef(ctx, ref, "project", provider, project); secret != "" {
			projectTier = append(projectTier, webhookSecretCandidate{Secret: secret, Source: "project"})
		}
	}
	if len(projectTier) > 0 {
		return projectTier
	}

	var orgTier []webhookSecretCandidate
	if owner := h.projectOwner(ctx, project); owner != nil {
		if ref := vcs.UserWebhookSecretRef(owner, provider); ref != "" {
			if secret := h.resolveSecretRef(ctx, ref, "org", provider, project); secret != "" {
				orgTier = append(orgTier, webhookSecretCandidate{Secret: secret, Source: "org"})
			}
		}
	}
	if len(orgTier) > 0 {
		return orgTier
	}

	if secret := globalWebhookSecret(provider); secret != "" {
		h.logger.WithField("provider", provider).Info("Global webhook secret available as fallback candidate")
		return []webhookSecretCandidate{{Secret: secret, Source: "env"}}
	}

	return nil
}

// touchWebhookSecretLastUsed stamps last_used_at for the rotation row that
// successfully validated a webhook signature. Best-effort: a stamp failure
// must never fail the webhook request itself.
func (h *WebhookHandler) touchWebhookSecretLastUsed(ctx context.Context, rotationID string) {
	rotationStore, ok := h.store.(webhookSecretRotationStore)
	if !ok {
		return
	}
	if err := rotationStore.TouchProjectWebhookSecretLastUsed(ctx, rotationID); err != nil {
		h.logger.WithError(err).WithField("rotation_id", rotationID).Warn("Failed to stamp webhook secret last_used_at")
	}
}

// handleWebhook processes webhook events from a specific provider
func (h *WebhookHandler) handleWebhook(w http.ResponseWriter, r *http.Request, provider vcs.Provider) {
	// Get the VCS client for this provider
	client, ok := h.vcsClients[provider]
	if !ok {
		h.logger.WithField("provider", provider).Error("VCS client not configured")
		http.Error(w, "VCS provider not configured", http.StatusInternalServerError)
		return
	}

	// Read the request body first for validation
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.WithError(err).Error("Failed to read webhook body")
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	// Replace the body for parsing
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Handle ping events early. VCS providers send ping events when a webhook
	// is first configured to verify the endpoint is reachable. These may not
	// include a repository field, so we respond before project lookup.
	if isPingEvent(r, provider) {
		h.logger.WithField("provider", provider).Info("Received webhook ping — endpoint verified")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "pong"})
		return
	}

	// A GitHub App delivery is verified with the secret of its App
	// (github_app_webhook.go). Installation and check run events end there;
	// the other events continue below as for a repository webhook.
	appVerified := false
	var appID int64
	if provider == vcs.GitHub {
		if id, isApp := githubAppDeliveryTarget(r); isApp {
			if done := h.handleGitHubAppDelivery(w, r, client, body, id); done {
				return
			}
			appVerified, appID = true, id
		}
	}

	// Extract the repo clone URL from the raw payload and look up the project.
	// This enables per-project webhook secrets: we identify which project the
	// webhook is for, resolve its secret, then validate the HMAC signature.
	var project *models.Project
	repoCloneURL, extractErr := extractRepoCloneURL(body, r.Header.Get("Content-Type"))
	if extractErr != nil {
		h.logger.WithError(extractErr).Warn("Could not extract repo clone URL from webhook payload")
	} else {
		normalizedURL := vcs.NormalizeRepoURL(repoCloneURL)
		if p, err := h.store.GetProjectByRepoURL(context.Background(), normalizedURL); err == nil {
			project = p
		} else {
			h.logger.WithError(err).WithField("normalized_url", normalizedURL).Warn("Failed to look up project by repo URL")
		}
	}

	// An App delivery skips the project secrets below. The App secret only
	// proves that the delivery came from that App. The repository must also
	// be bound to an installation of that App, and the project must be the
	// project of that binding (github_app_webhook.go).
	if appVerified {
		bound, reason, err := h.githubAppDeliveryProject(r.Context(), appID, r.Header.Get("X-GitHub-Event"), body, project)
		if err != nil {
			h.logger.WithError(err).WithField("app_id", appID).Error("Could not read the GitHub App binding of a delivery")
			http.Error(w, "GitHub App binding unavailable", http.StatusInternalServerError)
			return
		}
		if reason != "" {
			h.logger.WithFields(logrus.Fields{"app_id": appID, "event_type": r.Header.Get("X-GitHub-Event"),
				"delivery_id": r.Header.Get("X-GitHub-Delivery")}).Warn("GitHub App delivery ignored: " + reason)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ignored", "reason": reason})
			return
		}
		project = bound
	}

	// Resolve webhook secret candidates: active rotation rows first (newest
	// first), then legacy project/org/env fallbacks. Try each until one
	// validates the signature; constant-time comparison happens inside
	// client.ValidateWebhook, same as before rotation support existed.
	var candidates []webhookSecretCandidate
	if !appVerified {
		candidates = h.resolveWebhookSecretCandidates(context.Background(), project, provider)
		if len(candidates) == 0 {
			h.logger.WithField("project_found", project != nil).Error("Webhook secret not configured — rejecting request")
			http.Error(w, "Webhook secret not configured", http.StatusInternalServerError)
			return
		}
	}

	matched := appVerified
	for _, candidate := range candidates {
		// Each attempt needs its own fresh body reader: some ValidateWebhook
		// implementations (e.g. GitHub's) consume the request body.
		validateReq, _ := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(body))
		validateReq.Header = r.Header

		if err := client.ValidateWebhook(validateReq, candidate.Secret); err != nil {
			continue
		}
		matched = true
		if candidate.RotationID != "" {
			h.touchWebhookSecretLastUsed(context.Background(), candidate.RotationID)
		}
		break
	}
	if !matched {
		h.logger.Warn("Invalid webhook signature")
		http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
		return
	}

	// Parse the webhook event
	event, err := client.ParseWebhook(r)
	if err != nil {
		h.logger.WithError(err).Error("Failed to parse webhook")
		http.Error(w, "Failed to parse webhook", http.StatusBadRequest)
		return
	}

	event.DeliveryID = webhookDeliveryID(r, provider)

	// Log the received event
	h.logger.WithFields(logrus.Fields{
		"provider":    provider,
		"event_type":  event.EventType,
		"repository":  event.Repository.FullName,
		"delivery_id": event.DeliveryID,
	}).Info("Received webhook event")
	if event.Team != nil {
		// A team or membership change: sync the linked groups (group_vcs_handler.go).
		h.handleTeamEvent(r.Context(), event)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	if event.PullRequestReview != nil {
		if err := h.processReview(r.Context(), event, project); err != nil {
			h.logger.WithError(err).Warn("Could not process pull request review")
			http.Error(w, "Could not process pull request review", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}
	if event.IssueComment != nil {
		if err := h.processApprovalComment(r.Context(), event, project); err != nil {
			h.logger.WithError(err).Warn("Could not process CI approval comment")
			http.Error(w, "Could not process CI approval comment", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// Skip events that don't map to a known generic event type
	if event.GenericEvent == vcs.EventUnknown {
		h.logger.WithFields(logrus.Fields{
			"event_type": event.EventType,
			"provider":   provider,
		}).Debug("Ignoring unsupported event type")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// Overload protection (plan item D4): a project over its webhook rate
	// limit gets 429, and the event is recorded as rate_limited.
	if h.refuseRateLimitedWebhook(w, project, event) {
		return
	}

	// Process the event based on type, passing the already-fetched project
	// to avoid a duplicate database lookup.
	switch {
	case event.PullRequest != nil:
		if err := h.processPullRequestEvent(event, client, project); err != nil {
			h.logger.WithError(err).Error("Failed to process pull request event")
			http.Error(w, "Failed to process event", http.StatusInternalServerError)
			return
		}
	case event.Push != nil:
		if err := h.processPushEvent(event, client, project); err != nil {
			h.logger.WithError(err).Error("Failed to process push event")
			http.Error(w, "Failed to process event", http.StatusInternalServerError)
			return
		}
	default:
		h.logger.WithField("event_type", event.EventType).Debug("Ignoring event with no PR or push info")
	}

	// Send success response
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// processApprovalComment runs a /reactorcide command from a pull request
// comment through the one approval path. A comment that is not a command does
// nothing. A command the commenter may not give is recorded in the audit log
// and does nothing; it is not a delivery error.
func (h *WebhookHandler) processApprovalComment(ctx context.Context, event *vcs.WebhookEvent, project *models.Project) error {
	comment := event.IssueComment
	if comment == nil || comment.Action != "created" || !comment.IsPullRequest {
		return nil
	}
	command, ok := approvals.ParseComment(comment.Body)
	if !ok {
		if parseErr := approvals.CommentError(comment.Body); parseErr != nil && project != nil {
			// A retry or rerun with arguments that are not shifts is
			// refused, never run with the arguments ignored.
			h.logger.WithError(parseErr).WithFields(logrus.Fields{"pr_number": comment.IssueNumber, "login": event.SenderLogin}).
				Info("Pull request command refused")
			audit.Record(ctx, h.store, project.OwnershipOrgID(), "ci_approval.refused", "project", project.ProjectID,
				models.JSONB{"pr_number": comment.IssueNumber, "person": event.SenderLogin, "channel": models.CIApprovalChannelComment,
					"reason": parseErr.Error()})
			// The parse error can contain the arguments; the reply does not.
			h.replyToCommand(ctx, project, comment.IssueNumber, invalidCommandReply("the retry or rerun arguments are not valid shifts"))
		} else if approvals.IsCommandAttempt(comment.Body) && project != nil {
			h.logger.WithFields(logrus.Fields{"pr_number": comment.IssueNumber, "login": event.SenderLogin}).
				Info("Pull request comment is not a valid command")
			h.replyToCommand(ctx, project, comment.IssueNumber, invalidCommandReply(""))
		}
		return nil
	}
	if project == nil {
		return fmt.Errorf("project is not configured")
	}
	request := approvals.Request{Project: project, PRNumber: comment.IssueNumber, Command: command,
		Channel: models.CIApprovalChannelComment, Provider: string(event.Provider), Login: event.SenderLogin}
	if comment.CommentID != 0 {
		request.SourceObject = fmt.Sprintf("%s:comment:%d", event.Provider, comment.CommentID)
	}
	return h.executeProviderCommand(ctx, request)
}

// processReview turns a pull request review into an accept or a revoke. A
// review never grants execution: approved is an accept, changes requested is
// a revoke of the reviewer's accepts, and a dismissed review cancels only the
// reviewer's own accept. A review body can name a label with an
// "/reactorcide accept LABEL" line.
func (h *WebhookHandler) processReview(ctx context.Context, event *vcs.WebhookEvent, project *models.Project) error {
	review := event.PullRequestReview
	if review == nil || project == nil {
		return nil
	}
	request := approvals.Request{Project: project, PRNumber: review.PRNumber, HeadSHA: review.CommitSHA,
		Channel: models.CIApprovalChannelReview, Provider: string(event.Provider), Login: review.ReviewerLogin}
	switch {
	case review.Action == "submitted" && review.State == "approved":
		request.Command = approvals.Command{Name: approvals.CommandAccept, Label: approvals.ParseReviewLabel(review.Body)}
	case review.Action == "submitted" && review.State == "changes_requested":
		request.Command = approvals.Command{Name: approvals.CommandRevoke}
		request.RevokeOnly = models.CIApprovalKindAccept
	case review.Action == "dismissed":
		request.Command = approvals.Command{Name: approvals.CommandRevoke}
		request.RevokeOnly = models.CIApprovalKindAccept
		request.PersonSubjectsOnly = true
	default:
		return nil
	}
	if request.Login == "" {
		request.Login = event.SenderLogin
	}
	if review.ReviewID != 0 {
		request.SourceObject = fmt.Sprintf("%s:review:%d:%s", event.Provider, review.ReviewID, review.Action)
	}
	return h.executeProviderCommand(ctx, request)
}

func (h *WebhookHandler) executeProviderCommand(ctx context.Context, request approvals.Request) error {
	result, err := h.Execute(ctx, request)
	fields := logrus.Fields{"pr_number": request.PRNumber, "command": request.Command.Name, "login": request.Login}
	if request.Channel == models.CIApprovalChannelComment {
		// A comment gets a short reply when the command was refused, and
		// for a required set change (which is otherwise not visible).
		h.replyToCommand(ctx, request.Project, request.PRNumber, commandReplyText(err, result, request.Command.Name))
	}
	var refused *approvals.RefusedError
	switch {
	case errors.Is(err, approvals.ErrNotAllowed), errors.Is(err, approvals.ErrHeadChanged), errors.Is(err, approvals.ErrPullRequestClosed),
		errors.As(err, &refused):
		h.logger.WithFields(fields).WithError(err).Info("Pull request command refused")
		return nil
	case err != nil:
		return err
	}
	h.logger.WithFields(fields).WithField("effect", result.Effect).Info("Pull request command done")
	return nil
}

// processPullRequestEvent processes a pull request event.
// The project parameter may be non-nil if it was already looked up during
// webhook secret resolution. If nil, the project is fetched by repo URL.
func (h *WebhookHandler) processPullRequestEvent(event *vcs.WebhookEvent, client vcs.Client, project *models.Project) error {
	// On merge, record the merge state and refresh any in-flight jobs so
	// their next status change uses the per-job comment flow. This runs
	// alongside (not instead of) normal event processing — projects that
	// opt into pull_request_merged as a trigger still get their job created.
	if event.GenericEvent == vcs.EventPullRequestMerged {
		h.handlePRMerged(event)
	}

	pr := event.PullRequest

	// Use the pre-fetched project or look it up now
	if project == nil {
		normalizedRepoURL := vcs.NormalizeRepoURL(event.Repository.CloneURL)
		var err error
		project, err = h.store.GetProjectByRepoURL(context.Background(), normalizedRepoURL)
		if err != nil {
			h.logger.WithFields(logrus.Fields{
				"repo_url":   event.Repository.CloneURL,
				"normalized": normalizedRepoURL,
				"error":      err.Error(),
			}).Debug("No project found for repository - skipping event")
			return nil // Not an error - just no project configured
		}
	}

	// Apply event filtering using the generic event type
	if !project.ShouldProcessEvent(string(event.GenericEvent), pr.BaseRef) {
		h.logger.WithFields(logrus.Fields{
			"project":       project.Name,
			"generic_event": string(event.GenericEvent),
			"base_branch":   pr.BaseRef,
		}).Debug("Event filtered out by project configuration")
		return nil
	}

	ciEvent, proceed, err := h.recordCIEvent(context.Background(), project, event)
	if err != nil {
		return fmt.Errorf("recording CI event: %w", err)
	}
	if !proceed {
		return nil
	}
	// Until the eval job is linked, a failure gives the event claim back.
	evalJobLinked := false
	defer func() {
		if !evalJobLinked {
			h.releaseCIEventClaim(context.Background(), ciEvent)
		}
	}()

	statusClient := h.getStatusClient(context.Background(), project, event.Provider, client)
	// A new head cancels the approvals of the old heads before the run gate
	// reads them.
	if factsStore, ok := h.store.(ciEvaluationFactsStore); ok {
		invalidated, invalidateErr := factsStore.InvalidateCIApprovalsForNewHead(context.Background(), project.ProjectID, pr.Number, pr.HeadSHA)
		if invalidateErr != nil {
			h.logger.WithError(invalidateErr).Warn("Could not invalidate old CI approvals")
		} else if invalidated > 0 {
			audit.Record(context.Background(), h.store, project.OwnershipOrgID(), "ci_approval.invalidate", "project", project.ProjectID,
				models.JSONB{"pr_number": pr.Number, "new_head_sha": pr.HeadSHA, "count": invalidated})
		}
	}
	reviewTarget := &prTarget{Provider: event.Provider, Repo: event.Repository.FullName, CloneURL: event.Repository.CloneURL,
		Client: statusClient, PR: pr}
	if event.Coordinator == nil && event.GenericEvent != vcs.EventPullRequestMerged && event.GenericEvent != vcs.EventPullRequestClosed {
		// A new head needs its reviews again.
		h.publishReviewsStatus(context.Background(), project, reviewTarget)
	}
	// Drafts and shed levels (plan item D4): a skipped draft records the
	// reason and posts it in "Reactorcide Checks". The run gate does not
	// apply to a skipped draft; it applies when the draft becomes ready.
	shedState := shedding.State(context.Background(), h.store, project.OwnershipOrgID())
	if reason := h.draftSkipReason(context.Background(), project, event, shedState); reason != nil {
		evalJobLinked = true
		h.skipCIEvent(context.Background(), project, ciEvent, models.CIEventStatusSkipped, *reason)
		return nil
	}
	runAuthority, held, gateErr := h.runAuthorityFor(context.Background(), project, event, statusClient)
	if gateErr != nil {
		h.logger.WithError(gateErr).WithField("project", project.Name).Warn("Could not decide the run gate; holding the pull request")
		held = true
	}
	if held {
		// The event keeps its claim as "awaiting_approval": a redelivery does
		// not evaluate it, and approve-ci starts a new evaluation.
		evalJobLinked = true
		h.holdForRunGate(context.Background(), ciEvent)
		return nil
	}

	// Build eval job using the shared builder
	job := BuildEvalJob(project, event)
	applyCIEventToEvalJob(job, ciEvent)
	// The shed level lowers pull request priority through the lanes
	// (priority.Assign below reads it: priority.ShedAdjustment).
	if encoded, encodeErr := runAuthority.Encode(); encodeErr != nil {
		return fmt.Errorf("encoding run authority: %w", encodeErr)
	} else if encoded != "" {
		job.JobEnvVars[approvals.RunAuthorityEnv] = encoded
	}
	if h.attachCIEvaluationFacts(context.Background(), job, project, event, statusClient) == ciPolicyNone && !runAuthority.Gated {
		// A gated run uses trusted base CI, also with no stored policy.
		useEventCIWithoutPolicy(job, project, event)
	}
	if err := h.startVCSReportGeneration(context.Background(), job, project, event); err != nil {
		return fmt.Errorf("starting VCS report generation: %w", err)
	}

	// Store VCS metadata for status updates.
	metadata := vcs.JobMetadata{
		VCSProvider:   string(event.Provider),
		Repo:          event.Repository.FullName,
		PRNumber:      pr.Number,
		CommitSHA:     pr.HeadSHA,
		StatusContext: "reactorcide/eval",
		IsEval:        true,
	}
	if err := metadata.ApplyToJob(job); err != nil {
		return fmt.Errorf("applying VCS metadata: %w", err)
	}

	if err := h.resolveJobQueue(context.Background(), job); err != nil {
		if h.refuseEvalImage(project, ciEvent, err) {
			evalJobLinked = true
			return nil
		}
		return err
	}
	// The lane and group shifts give the queue priority (docs/priority.md).
	priority.Assign(context.Background(), h.store, job)

	// Create the job in the database
	if err := h.store.CreateJob(context.Background(), job); err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	h.attachEvalJobToCIEvent(context.Background(), ciEvent, job)
	evalJobLinked = true

	// Submit job to Corndogs task queue
	h.submitJobToCorndogs(job)

	// Register a pending check on the commit so branch protection sees it
	// immediately — don't wait for the worker to pick it up.
	h.postEvalPendingStatus(job, statusClient, event.Repository.FullName, pr.HeadSHA)

	h.logger.WithFields(logrus.Fields{
		"job_id":    job.JobID,
		"project":   project.Name,
		"pr_number": pr.Number,
		"sha":       pr.HeadSHA,
	}).Info("Created eval job for pull request")

	return nil
}

// processPushEvent processes a push event.
// The project parameter may be non-nil if it was already looked up during
// webhook secret resolution. If nil, the project is fetched by repo URL.
func (h *WebhookHandler) processPushEvent(event *vcs.WebhookEvent, client vcs.Client, project *models.Project) error {
	push := event.Push

	// Skip deleted branches
	if push.Deleted {
		h.logger.WithField("ref", push.Ref).Debug("Ignoring branch deletion")
		return nil
	}

	// Extract the branch or tag name from the full Git ref.
	branch := extractBranchOrTag(push.Ref)

	// Use the pre-fetched project or look it up now
	if project == nil {
		normalizedRepoURL := vcs.NormalizeRepoURL(event.Repository.CloneURL)
		var err error
		project, err = h.store.GetProjectByRepoURL(context.Background(), normalizedRepoURL)
		if err != nil {
			h.logger.WithFields(logrus.Fields{
				"repo_url":   event.Repository.CloneURL,
				"normalized": normalizedRepoURL,
				"error":      err.Error(),
			}).Debug("No project found for repository - skipping event")
			return nil // Not an error - just no project configured
		}
	}

	// Apply event filtering using the generic event type
	if !project.ShouldProcessEvent(string(event.GenericEvent), branch) {
		h.logger.WithFields(logrus.Fields{
			"project":       project.Name,
			"generic_event": string(event.GenericEvent),
			"branch":        branch,
		}).Debug("Event filtered out by project configuration")
		return nil
	}

	ciEvent, proceed, err := h.recordCIEvent(context.Background(), project, event)
	if err != nil {
		return fmt.Errorf("recording CI event: %w", err)
	}
	if !proceed {
		return nil
	}
	// Until the eval job is linked, a failure gives the event claim back.
	evalJobLinked := false
	defer func() {
		if !evalJobLinked {
			h.releaseCIEventClaim(context.Background(), ciEvent)
		}
	}()

	// Build eval job using the shared builder
	job := BuildEvalJob(project, event)
	applyCIEventToEvalJob(job, ciEvent)
	statusClient := h.getStatusClient(context.Background(), project, event.Provider, client)
	// Push events go through CI policy like pull requests (owner decision
	// D3), so the eval carries the same actor, approval and policy facts.
	// Without a stored policy nothing is gated.
	if h.attachCIEvaluationFacts(context.Background(), job, project, event, statusClient) == ciPolicyNone {
		useEventCIWithoutPolicy(job, project, event)
	}

	// Store VCS metadata for status updates.
	metadata := vcs.JobMetadata{
		VCSProvider:   string(event.Provider),
		Repo:          event.Repository.FullName,
		Branch:        branch,
		CommitSHA:     push.After,
		StatusContext: "reactorcide/eval",
		IsEval:        true,
	}
	if err := metadata.ApplyToJob(job); err != nil {
		return fmt.Errorf("applying VCS metadata: %w", err)
	}

	if err := h.resolveJobQueue(context.Background(), job); err != nil {
		if h.refuseEvalImage(project, ciEvent, err) {
			evalJobLinked = true
			return nil
		}
		return err
	}
	// The lane and group shifts give the queue priority (docs/priority.md).
	priority.Assign(context.Background(), h.store, job)

	// Create the job in the database
	if err := h.store.CreateJob(context.Background(), job); err != nil {
		return fmt.Errorf("creating job: %w", err)
	}

	h.attachEvalJobToCIEvent(context.Background(), ciEvent, job)
	evalJobLinked = true

	// Submit job to Corndogs task queue
	h.submitJobToCorndogs(job)

	// Update commit status to pending (use per-project client if available)
	h.postEvalPendingStatus(job, statusClient, event.Repository.FullName, push.After)

	h.logger.WithFields(logrus.Fields{
		"job_id":  job.JobID,
		"project": project.Name,
		"branch":  branch,
		"sha":     push.After,
	}).Info("Created eval job for push")

	return nil
}

// eventChecksPublisher publishes a CI event's "Reactorcide Checks" status.
type eventChecksPublisher interface {
	SupportsEventChecks() bool
	PublishEventChecks(ctx context.Context, eventID string) error
}

// postEvalPendingStatus posts the pending status of a new eval job. An eval
// of a CI event feeds the event's "Reactorcide Checks" status; a job without
// an event (or a store without events) keeps the legacy "reactorcide/eval"
// context. A failure here never fails the webhook.
func (h *WebhookHandler) postEvalPendingStatus(job *models.Job, statusClient vcs.Client, repo, sha string) {
	ctx := context.Background()
	if job.EventID != nil && *job.EventID != "" {
		if publisher, ok := h.statusUpdater.(eventChecksPublisher); ok && publisher.SupportsEventChecks() {
			if err := publisher.PublishEventChecks(ctx, *job.EventID); err != nil {
				h.logger.WithError(err).WithField("event_id", *job.EventID).Warn("Failed to update event checks status")
			}
			return
		}
	}
	if statusClient == nil || !h.vcsStatusesAllowedForJob(ctx, job) {
		return
	}
	statusUpdate := vcs.StatusUpdate{
		SHA:         sha,
		State:       vcs.StatusPending,
		TargetURL:   h.getJobURL(job.JobID),
		Description: "CI build queued",
		Context:     "reactorcide/eval",
	}
	if err := statusClient.UpdateCommitStatus(ctx, repo, statusUpdate); err != nil {
		h.logger.WithError(err).Warn("Failed to update commit status")
	}
}

// handlePRMerged records the merge in pr_merged and nudges any still-in-flight
// jobs for that PR into the per-job comment flow, so jobs that straddle the
// merge event don't get orphaned mid-transition. Runs as a side-effect on
// merge webhooks; the caller continues with normal event processing.
func (h *WebhookHandler) handlePRMerged(event *vcs.WebhookEvent) {
	ctx := context.Background()
	repo := event.Repository.FullName
	prNumber := event.PullRequest.Number

	if err := h.store.MarkPRMerged(ctx, repo, prNumber); err != nil {
		h.logger.WithError(err).WithFields(logrus.Fields{
			"repo":      repo,
			"pr_number": prNumber,
		}).Warn("Failed to mark PR merged")
	}

	if h.statusUpdater == nil {
		return
	}

	jobs, err := h.store.ListJobsForPR(ctx, repo, prNumber)
	if err != nil {
		h.logger.WithError(err).Warn("Failed to list jobs for merged PR")
		return
	}

	for i := range jobs {
		job := &jobs[i]
		if job.IsCompleted() {
			continue
		}
		// A workflow node has no status of its own: its workflow's status
		// covers it. Its Notes still carry the job name as a status context,
		// so refreshing it here posted one stray status per in-flight node.
		// A job of a CI event is covered by the event's aggregate status.
		if job.WorkflowNodeID != nil || (job.EventID != nil && *job.EventID != "") {
			continue
		}
		if err := h.statusUpdater.UpdateJobStatus(ctx, job); err != nil {
			h.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to refresh job status after PR merge")
		}
	}
}

// vcsCredentialRotationStore is the narrow store interface needed to list
// active rotatable VCS credentials and stamp last-used timestamps. Defined
// on the consumer side per the repo's narrow-interface + type-assertion
// pattern (see webhookSecretRotationStore above).
type vcsCredentialRotationStore interface {
	ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error)
	TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error
}

// getStatusClient returns a per-project VCS client, preferring the
// highest-precedence active project_vcs_credentials rotation row over the
// legacy project ref, then falling back to org and the provided global
// client. Deactivated rotation rows are never used (the store only returns
// active rows).
func (h *WebhookHandler) getStatusClient(ctx context.Context, project *models.Project, provider vcs.Provider, fallback vcs.Client) vcs.Client {
	if client := h.scopedVCSClient(ctx, project, provider); client != nil {
		return client
	}
	return fallback
}

// scopedVCSClient returns the VCS client of a credential that belongs to the
// project or its organization (a GitHub App installation, a project
// credential, the organization credential), or nil when there is none. It
// never returns the global client of the coordinator.
func (h *WebhookHandler) scopedVCSClient(ctx context.Context, project *models.Project, provider vcs.Provider) vcs.Client {
	// A repository in a GitHub App installation uses an installation token
	// (docs/github-app.md). Other projects keep the token path below.
	if client := vcs.AppClientForProject(ctx, vcs.AppSource(), project, provider); client != nil {
		return client
	}
	if project != nil {
		if rotationStore, ok := h.store.(vcsCredentialRotationStore); ok {
			rows, err := rotationStore.ListActiveProjectVCSCredentials(ctx, project.ProjectID, string(provider))
			if err != nil {
				h.logger.WithError(err).WithFields(logrus.Fields{
					"project":  project.Name,
					"provider": provider,
				}).Warn("Failed to list active rotatable VCS credentials")
			}
			if row, ok := vcs.HighestPrecedenceActiveVCSCredential(rows); ok {
				if client := h.clientForSecretRef(ctx, row.SecretRef, provider, "project-rotation", project); client != nil {
					h.touchVCSCredentialLastUsed(ctx, row.ID)
					return client
				}
			}
		}
	}
	if ref := vcs.ProjectVCSCredentialSecretRef(project, provider); ref != "" {
		if client := h.clientForSecretRef(ctx, ref, provider, "project", project); client != nil {
			return client
		}
	}
	if owner := h.projectOwner(ctx, project); owner != nil {
		if ref := vcs.UserVCSCredentialSecretRef(owner, provider); ref != "" {
			if client := h.clientForSecretRef(ctx, ref, provider, "org", project); client != nil {
				return client
			}
		}
	}
	return nil
}

// touchVCSCredentialLastUsed stamps last_used_at for the rotation row that
// was successfully resolved into a usable VCS client. Best-effort: a stamp
// failure must never fail the caller's operation.
func (h *WebhookHandler) touchVCSCredentialLastUsed(ctx context.Context, rotationID string) {
	rotationStore, ok := h.store.(vcsCredentialRotationStore)
	if !ok {
		return
	}
	if err := rotationStore.TouchProjectVCSCredentialLastUsed(ctx, rotationID); err != nil {
		h.logger.WithError(err).WithField("rotation_id", rotationID).Warn("Failed to stamp VCS credential last_used_at")
	}
}

func (h *WebhookHandler) resolveSecretRef(ctx context.Context, secretRef, scope string, provider vcs.Provider, project *models.Project) string {
	if h.tokenResolver == nil {
		h.logger.WithFields(logrus.Fields{
			"provider": provider,
			"scope":    scope,
		}).Warn("Secret reference configured but no token resolver is available")
		return ""
	}
	if project != nil {
		ctx = vcs.WithOrganization(ctx, project.OwnershipOrgID())
	}
	secret, err := h.tokenResolver(ctx, secretRef)
	if err != nil {
		fields := logrus.Fields{"provider": provider, "scope": scope}
		if project != nil {
			fields["project"] = project.Name
		}
		h.logger.WithError(err).WithFields(fields).Error("Failed to resolve secret reference")
		return ""
	}
	h.logger.WithFields(logrus.Fields{
		"provider": provider,
		"scope":    scope,
	}).Info("Resolved VCS secret reference")
	return secret
}

func (h *WebhookHandler) clientForSecretRef(ctx context.Context, secretRef string, provider vcs.Provider, scope string, project *models.Project) vcs.Client {
	if h.tokenResolver == nil || h.clientFactory == nil {
		h.logger.WithFields(logrus.Fields{
			"provider": provider,
			"scope":    scope,
		}).Warn("VCS token reference configured but token resolver or client factory is unavailable")
		return nil
	}
	if project != nil {
		ctx = vcs.WithOrganization(ctx, project.OwnershipOrgID())
	}
	token, err := h.tokenResolver(ctx, secretRef)
	if err != nil {
		fields := logrus.Fields{"provider": provider, "scope": scope}
		if project != nil {
			fields["project"] = project.Name
		}
		h.logger.WithError(err).WithFields(fields).Warn("Failed to resolve VCS token, falling back")
		return nil
	}
	if token == "" {
		return nil
	}
	client, err := h.clientFactory(provider, token)
	if err != nil {
		h.logger.WithError(err).WithFields(logrus.Fields{
			"provider": provider,
			"scope":    scope,
		}).Warn("Failed to create scoped VCS client, falling back")
		return nil
	}
	h.logger.WithFields(logrus.Fields{
		"provider": provider,
		"scope":    scope,
	}).Info("Using scoped VCS client")
	return client
}

func (h *WebhookHandler) projectOwner(ctx context.Context, project *models.Project) *models.User {
	ownerID := ""
	if project != nil && project.UserID != nil {
		ownerID = *project.UserID
	}
	if ownerID == "" {
		return nil
	}
	user, err := h.store.GetUserByID(ctx, ownerID)
	if err != nil {
		h.logger.WithError(err).WithField("owner_id", ownerID).Debug("Could not load project owner for VCS secret lookup")
		return nil
	}
	return user
}

func globalWebhookSecret(provider vcs.Provider) string {
	switch provider {
	case vcs.GitHub:
		if config.VCSGitHubSecret != "" {
			return config.VCSGitHubSecret
		}
	case vcs.GitLab:
		if config.VCSGitLabSecret != "" {
			return config.VCSGitLabSecret
		}
	}
	return config.VCSWebhookSecret
}

// refuseEvalImage records an image refusal of the eval job
// (imagealias.RefusedError: an image alias that does not fit the job, or
// the image policy) on the CI event, and posts it in "Reactorcide Checks".
// The refusal is permanent, so the event is failed and not released for a
// redelivery. It returns false for any other error.
func (h *WebhookHandler) refuseEvalImage(project *models.Project, ciEvent *models.CIEvent, err error) bool {
	refused, ok := imagealias.IsRefused(err)
	if !ok {
		return false
	}
	h.logger.WithField("project", project.Name).WithField("reason", refused.Message).
		Warn("The eval job was refused at creation")
	h.skipCIEvent(context.Background(), project, ciEvent, models.CIEventStatusFailed, models.DecisionReason{
		Kind: refused.Code, Value: "eval_not_created", Detail: "eval job not created: " + refused.Message,
	})
	return true
}

// resolveJobQueue resolves job.Characteristics to a queue (find-or-create)
// and sets job.QueueName to the resolved Queue.QueueUUID, mutating job in
// place before it is persisted. A no-op (job.QueueName left as BuildEvalJob
// set it) when the store doesn't implement queueResolvingStore, matching
// job_handler.go's CreateJob so tests with a narrower store mock still work.
func (h *WebhookHandler) resolveJobQueue(ctx context.Context, job *models.Job) error {
	// The image alias of the job, before the queue (internal/imagealias).
	if _, err := imagealias.ResolveJob(ctx, h.store, job); err != nil {
		return fmt.Errorf("resolving job image: %w", err)
	}
	// A job with egress control waits for a worker with an egress proxy.
	egress.RouteJob(ctx, h.store, job)
	if qs, ok := h.store.(orgQueueResolvingStore); ok {
		queue, err := qs.FindOrCreateQueueForOrg(ctx, job.OrgID, job.WorkerClass, job.Characteristics)
		if err != nil {
			return fmt.Errorf("resolving queue: %w", err)
		}
		job.QueueName = queue.QueueUUID
		return nil
	}
	qs, ok := h.store.(queueResolvingStore)
	if !ok {
		return nil
	}
	queue, err := qs.FindOrCreateQueueByCharacteristics(ctx, job.Characteristics)
	if err != nil {
		return fmt.Errorf("resolving queue: %w", err)
	}
	job.QueueName = queue.QueueUUID
	return nil
}

// submitJobToCorndogs submits a job to the Corndogs task queue
func (h *WebhookHandler) submitJobToCorndogs(job *models.Job) {
	if h.corndogsClient == nil {
		return
	}

	// Dereference pointer fields for payload
	sourceTypeStr := ""
	if job.SourceType != nil {
		sourceTypeStr = string(*job.SourceType)
	}
	sourceURL := ""
	if job.SourceURL != nil {
		sourceURL = *job.SourceURL
	}
	sourceRef := ""
	if job.SourceRef != nil {
		sourceRef = *job.SourceRef
	}
	sourcePath := ""
	if job.SourcePath != nil {
		sourcePath = *job.SourcePath
	}

	taskPayload := &corndogs.TaskPayload{
		JobID:   job.JobID,
		JobType: "run",
		Config: map[string]interface{}{
			"image":       job.RunnerImage,
			"command":     job.JobCommand,
			"working_dir": job.JobDir,
			"timeout":     job.TimeoutSeconds,
			"code_dir":    job.CodeDir,
			"job_dir":     job.JobDir,
		},
		Source: map[string]interface{}{
			"type":        sourceTypeStr,
			"url":         sourceURL,
			"ref":         sourceRef,
			"source_path": sourcePath,
		},
		Metadata: map[string]interface{}{
			"submitted_at": job.CreatedAt,
			"name":         job.Name,
			"description":  job.Description,
		},
	}

	// Add environment variables if present
	if job.JobEnvVars != nil {
		taskPayload.Config["environment"] = job.JobEnvVars
	}
	if job.JobEnvFile != "" {
		taskPayload.Config["env_file"] = job.JobEnvFile
	}

	task, err := h.corndogsClient.SubmitTaskToQueue(context.Background(), job.QueueName, taskPayload, int64(job.Priority))
	if err != nil {
		h.logger.WithFields(logrus.Fields{
			"job_id":   job.JobID,
			"job_name": job.Name,
			"queue":    job.QueueName,
			"error":    err.Error(),
		}).Error("Failed to submit task to Corndogs")
		job.Status = "failed"
		job.SetFailure(models.FailureClassPlatform, "queue submission")
		metrics.RecordJobFinished(job.Status, models.FailureClassPlatform, job.QueueName)
		metrics.RecordCornDogsTaskSubmission(job.QueueName, false)
		if err := h.store.UpdateJob(context.Background(), job); err != nil {
			h.logger.WithFields(logrus.Fields{
				"job_id": job.JobID,
				"error":  err.Error(),
			}).Error("Failed to record queue submission failure")
		}
		return
	}
	metrics.RecordCornDogsTaskSubmission(job.QueueName, true)
	// A worker can claim the task before this write; do not overwrite it.
	if err := worker.RecordQueueSubmission(context.Background(), h.store, job, task.Uuid, task.CurrentState); err != nil {
		h.logger.WithFields(logrus.Fields{
			"job_id": job.JobID,
			"error":  err.Error(),
		}).Error("Failed to update job after Corndogs submission")
	}
}

// isPingEvent returns true if the incoming request is a connectivity-check ping
// from the VCS provider. Each provider uses a different mechanism to signal this.
func isPingEvent(r *http.Request, provider vcs.Provider) bool {
	switch provider {
	case vcs.GitHub:
		return r.Header.Get("X-GitHub-Event") == "ping"
	default:
		return false
	}
}

// getJobURL returns the URL for a job
func (h *WebhookHandler) getJobURL(jobID string) string {
	if config.VCSBaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/app/jobs/%s", config.VCSBaseURL, jobID)
}
