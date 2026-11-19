package outboundevents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
)

// ValidationError is an invalid subscription input. REST answers 400 and
// CSIL "invalid_argument".
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// IsValidationError reports whether err is a ValidationError.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// Result values that a results filter can name.
var ResultValues = []string{
	"completed", "failed", "cancelled", "timeout", "skipped", // nodes
	"success",             // workflows (also failed, cancelled, skipped)
	"evaluated",           // events (also failed)
	"run", "not_admitted", // workflow decisions of an event
}

// ActorKinds are the principal kinds that an actor filter can name.
var ActorKinds = []string{"human", "service", "system"}

var (
	subscriptionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	filterValuePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
)

// SubscriptionInput is a new subscription.
type SubscriptionInput struct {
	Name               string
	Description        string
	Scope              string
	ProjectID          *string
	WorkflowSecurityID string
	OwnerGroup         string
	EventTypes         []string
	RefClassesAny      []string
	Results            []string
	ActorKinds         []string
	FailureClasses     []string
	SinkType           string
	WebhookURL         string
	MaxAttempts        int
	Enabled            *bool
	// SigningSecret is optional. When it is empty, a random secret is made
	// and returned one time.
	SigningSecret string
	// OwnerPrincipalID is the owner. Empty means the caller.
	OwnerPrincipalID string
}

// SubscriptionPatch changes a subscription. Nil fields do not change.
type SubscriptionPatch struct {
	Description   *string
	EventTypes    *[]string
	RefClassesAny *[]string
	Results       *[]string
	ActorKinds    *[]string
	// FailureClasses is the failure class filter (docs/diagnosis.md).
	FailureClasses *[]string
	WebhookURL     *string
	MaxAttempts    *int
	Enabled        *bool
	// RotateSecret makes a new random signing secret, or uses SigningSecret
	// when it is set.
	RotateSecret  bool
	SigningSecret string
}

// PrincipalLookup reads a principal for the owner check.
type PrincipalLookup interface {
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
}

// Manager validates and stores subscriptions. REST handlers and CSIL
// operations both use it after they authorize the caller.
type Manager struct {
	Store      store.OutboundEventStore
	Principals PrincipalLookup
	Crypto     interface {
		Encrypter
		Decrypter
	}
	Sinks map[string]Sink
}

func cleanList(field string, values []string, allowed []string, pattern *regexp.Regexp) ([]string, error) {
	seen := map[string]struct{}{}
	out := []string{}
	if len(values) > 32 {
		return nil, invalid("%s has more than 32 values", field)
	}
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if allowed != nil && !contains(allowed, v) {
			return nil, invalid("%s: unknown value %q (allowed: %s)", field, v, strings.Join(allowed, ", "))
		}
		if pattern != nil && !pattern.MatchString(v) {
			return nil, invalid("%s: invalid value %q", field, v)
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

func (m *Manager) sink(sinkType string) (Sink, error) {
	sink := m.Sinks[sinkType]
	if sink == nil {
		return nil, invalid("unknown sink type %q (allowed: %s)", sinkType, models.SinkTypeWebhook)
	}
	return sink, nil
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "rcwh_" + hex.EncodeToString(b), nil
}

func (m *Manager) encryptSecret(secret string) (string, []byte, error) {
	if m.Crypto == nil {
		return "", nil, errors.New("the coordinator has no master keys; signing secrets cannot be stored")
	}
	if len(secret) < 16 || len(secret) > 512 {
		return "", nil, invalid("the signing secret must have 16 to 512 characters")
	}
	return m.Crypto.EncryptWithPrimary([]byte(secret))
}

// Create validates in and stores a new subscription of orgID. It returns the
// subscription and the plaintext signing secret (show it one time).
func (m *Manager) Create(ctx context.Context, orgID string, in SubscriptionInput, callerPrincipalID string) (*models.EventSubscription, string, error) {
	name := strings.TrimSpace(in.Name)
	if !subscriptionNamePattern.MatchString(name) {
		return nil, "", invalid("the name must have 1 to 63 lowercase letters, digits, or hyphens, and start with a letter or a digit")
	}
	if len(in.Description) > 1000 {
		return nil, "", invalid("the description has more than 1000 characters")
	}
	sub := &models.EventSubscription{OrgID: orgID, Name: name, Description: strings.TrimSpace(in.Description),
		Scope: strings.TrimSpace(in.Scope), Enabled: true}
	if in.Enabled != nil {
		sub.Enabled = *in.Enabled
	}
	switch sub.Scope {
	case models.SubscriptionScopeOrg:
		if in.ProjectID != nil || in.WorkflowSecurityID != "" || in.OwnerGroup != "" {
			return nil, "", invalid("an org subscription has no project, workflow, or owner group")
		}
	case models.SubscriptionScopeProject:
		if in.ProjectID == nil || in.WorkflowSecurityID != "" || in.OwnerGroup != "" {
			return nil, "", invalid("a project subscription needs a project, and no workflow or owner group")
		}
	case models.SubscriptionScopeWorkflow:
		if in.ProjectID == nil || strings.TrimSpace(in.WorkflowSecurityID) == "" || in.OwnerGroup != "" {
			return nil, "", invalid("a workflow subscription needs a project and a workflow security ID")
		}
		if len(in.WorkflowSecurityID) > 256 {
			return nil, "", invalid("the workflow security ID has more than 256 characters")
		}
		sub.WorkflowSecurityID = strings.TrimSpace(in.WorkflowSecurityID)
	case models.SubscriptionScopeOwnerGroup:
		group := strings.TrimSpace(in.OwnerGroup)
		if group == "" || !filterValuePattern.MatchString(group) || in.WorkflowSecurityID != "" {
			return nil, "", invalid("an owner_group subscription needs a valid owner group, and no workflow")
		}
		sub.OwnerGroup = group
	default:
		return nil, "", invalid("the scope must be org, project, workflow, or owner_group")
	}
	sub.ProjectID = in.ProjectID
	if err := m.applyFilters(sub, &in.EventTypes, &in.RefClassesAny, &in.Results, &in.ActorKinds); err != nil {
		return nil, "", err
	}
	if err := applyFailureClasses(sub, &in.FailureClasses); err != nil {
		return nil, "", err
	}
	if in.SinkType == "" {
		in.SinkType = models.SinkTypeWebhook
	}
	sink, err := m.sink(in.SinkType)
	if err != nil {
		return nil, "", err
	}
	sub.SinkType = in.SinkType
	sub.SinkConfig = models.JSONB{"url": strings.TrimSpace(in.WebhookURL)}
	if err := sink.Validate(sub.SinkConfig); err != nil {
		return nil, "", &ValidationError{Message: err.Error()}
	}
	if in.MaxAttempts < 0 || in.MaxAttempts > 100 {
		return nil, "", invalid("max attempts must be 0 (the coordinator default) to 100")
	}
	sub.MaxAttempts = in.MaxAttempts

	owner := strings.TrimSpace(in.OwnerPrincipalID)
	if owner == "" {
		owner = callerPrincipalID
	}
	if owner == "" {
		return nil, "", invalid("the owner principal is required: this credential has no principal")
	}
	resolvedOwner, err := m.checkOwner(ctx, orgID, owner)
	if err != nil {
		return nil, "", err
	}
	sub.OwnerPrincipalID = resolvedOwner
	if callerPrincipalID != "" {
		createdBy := callerPrincipalID
		sub.CreatedBy = &createdBy
	}

	secret := in.SigningSecret
	if secret == "" {
		if secret, err = newSecret(); err != nil {
			return nil, "", err
		}
	}
	keyName, ciphertext, err := m.encryptSecret(secret)
	if err != nil {
		return nil, "", err
	}
	sub.SigningSecretKeyName, sub.SigningSecretCiphertext = keyName, ciphertext
	if err := m.Store.CreateEventSubscription(ctx, sub); err != nil {
		return nil, "", err
	}
	stored, err := m.Store.GetEventSubscription(ctx, sub.SubscriptionID)
	if err != nil {
		return nil, "", err
	}
	return stored, secret, nil
}

// checkOwner returns the owner principal after merges. The owner must be
// active, not the system principal, and a service principal must belong to
// the organization (an instance service principal has none).
func (m *Manager) checkOwner(ctx context.Context, orgID, principalID string) (string, error) {
	if m.Principals == nil {
		return principalID, nil
	}
	principal, err := m.Principals.GetPrincipal(ctx, principalID)
	if err != nil || principal == nil {
		return "", invalid("the owner principal %s does not exist", principalID)
	}
	if !principal.IsActive() {
		return "", invalid("the owner principal is disabled")
	}
	if principal.Kind == "system" {
		return "", invalid("the system principal cannot own a subscription")
	}
	if principal.Kind == "service" && principal.OrgID != nil && *principal.OrgID != orgID {
		return "", invalid("a service principal of another organization cannot own this subscription")
	}
	return principal.PrincipalID, nil
}

func (m *Manager) applyFilters(sub *models.EventSubscription, eventTypes, refClasses, results, actorKinds *[]string) error {
	if eventTypes != nil {
		v, err := cleanList("event types", *eventTypes, models.OutboundEventTypes, nil)
		if err != nil {
			return err
		}
		sub.EventTypes = pq.StringArray(v)
	}
	if refClasses != nil {
		v, err := cleanList("ref classes", *refClasses, nil, filterValuePattern)
		if err != nil {
			return err
		}
		sub.RefClassesAny = pq.StringArray(v)
	}
	if results != nil {
		v, err := cleanList("results", *results, ResultValues, nil)
		if err != nil {
			return err
		}
		sub.Results = pq.StringArray(v)
	}
	if actorKinds != nil {
		v, err := cleanList("actor kinds", *actorKinds, ActorKinds, nil)
		if err != nil {
			return err
		}
		sub.ActorKinds = pq.StringArray(v)
	}
	return nil
}

// applyFailureClasses sets the failure class filter. A value must be a known
// failure class (models.FailureClasses).
func applyFailureClasses(sub *models.EventSubscription, classes *[]string) error {
	if classes == nil {
		return nil
	}
	v, err := cleanList("failure classes", *classes, models.FailureClasses, nil)
	if err != nil {
		return err
	}
	sub.FailureClasses = pq.StringArray(v)
	return nil
}

// Update applies patch to sub and stores it. It returns the new plaintext
// secret when the patch rotates it.
func (m *Manager) Update(ctx context.Context, sub *models.EventSubscription, patch SubscriptionPatch) (*models.EventSubscription, string, error) {
	if patch.Description != nil {
		if len(*patch.Description) > 1000 {
			return nil, "", invalid("the description has more than 1000 characters")
		}
		sub.Description = strings.TrimSpace(*patch.Description)
	}
	if err := m.applyFilters(sub, patch.EventTypes, patch.RefClassesAny, patch.Results, patch.ActorKinds); err != nil {
		return nil, "", err
	}
	if err := applyFailureClasses(sub, patch.FailureClasses); err != nil {
		return nil, "", err
	}
	if patch.WebhookURL != nil {
		sink, err := m.sink(sub.SinkType)
		if err != nil {
			return nil, "", err
		}
		config := models.JSONB{"url": strings.TrimSpace(*patch.WebhookURL)}
		if err := sink.Validate(config); err != nil {
			return nil, "", &ValidationError{Message: err.Error()}
		}
		sub.SinkConfig = config
	}
	if patch.MaxAttempts != nil {
		if *patch.MaxAttempts < 0 || *patch.MaxAttempts > 100 {
			return nil, "", invalid("max attempts must be 0 (the coordinator default) to 100")
		}
		sub.MaxAttempts = *patch.MaxAttempts
	}
	if patch.Enabled != nil {
		sub.Enabled = *patch.Enabled
	}
	secret := ""
	if patch.RotateSecret || patch.SigningSecret != "" {
		secret = patch.SigningSecret
		if secret == "" {
			var err error
			if secret, err = newSecret(); err != nil {
				return nil, "", err
			}
		}
		keyName, ciphertext, err := m.encryptSecret(secret)
		if err != nil {
			return nil, "", err
		}
		sub.SigningSecretKeyName, sub.SigningSecretCiphertext = keyName, ciphertext
	}
	if err := m.Store.UpdateEventSubscription(ctx, sub); err != nil {
		return nil, "", err
	}
	stored, err := m.Store.GetEventSubscription(ctx, sub.SubscriptionID)
	if err != nil {
		return nil, "", err
	}
	return stored, secret, nil
}

// Test sends one test.ping event to the sink of sub now and returns the
// outcome. It does not use the outbox or Corndogs and records nothing.
func (m *Manager) Test(ctx context.Context, sub *models.EventSubscription, orgName string) Outcome {
	sink, err := m.sink(sub.SinkType)
	if err != nil {
		return Outcome{Err: err, Refused: true}
	}
	if m.Crypto == nil {
		return Outcome{Err: errors.New("the coordinator has no master keys to read the signing secret")}
	}
	secret, err := m.Crypto.DecryptWithKey(sub.SigningSecretKeyName, sub.SigningSecretCiphertext)
	if err != nil {
		return Outcome{Err: errors.New("failed to decrypt the signing secret")}
	}
	id, err := newSecret()
	if err != nil {
		return Outcome{Err: err}
	}
	eventID := "test-" + id[len("rcwh_"):len("rcwh_")+32]
	body, _ := json.Marshal(map[string]any{
		"id": eventID, "type": models.OutboundEventTest, "schema_version": SchemaVersion,
		"occurred_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"org":          map[string]string{"id": sub.OrgID, "name": orgName},
		"subscription": map[string]string{"id": sub.SubscriptionID, "name": sub.Name},
	})
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return sink.Send(sendCtx, Message{EventID: eventID, EventType: models.OutboundEventTest, SchemaVersion: SchemaVersion,
		Body: body, Secret: secret, Config: sub.SinkConfig})
}
