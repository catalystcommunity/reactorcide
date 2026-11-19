package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// GitHub limits of the Checks API.
const (
	maxCheckRunTitle       = 255
	maxCheckRunSummary     = 65535
	maxCheckRunText        = 65535
	maxCheckRunAnnotations = 50
	maxActionLabel         = 20
	maxActionDescription   = 40
	maxAnnotationMessage   = 64 * 1024
)

// AppClient is a GitHub client that authenticates with an installation token.
// It is a vcs.Client (through the embedded GitHubClient) and a
// vcs.CheckRunPublisher. "Reactorcide Checks" and "Reactorcide Reviews" are
// check runs; every other status context stays a commit status.
type AppClient struct {
	*vcs.GitHubClient
	appID          int64
	installationID int64
	token          string
	api            *api
	runs           *checkRunCache
	now            func() time.Time
}

var _ vcs.Client = (*AppClient)(nil)
var _ vcs.CheckRunPublisher = (*AppClient)(nil)

// The agreement report (WP10) reads check runs through the App client.
var _ vcs.CommitResultsReader = (*AppClient)(nil)

// AppID returns the App of the client.
func (c *AppClient) AppID() int64 { return c.appID }

// InstallationID returns the installation of the client.
func (c *AppClient) InstallationID() int64 { return c.installationID }

// UpdateCommitStatus posts a check run for the check run contexts and a
// commit status for every other context.
func (c *AppClient) UpdateCommitStatus(ctx context.Context, repo string, update vcs.StatusUpdate) error {
	if vcs.CheckRunContexts[update.Context] {
		return c.PublishCheckRun(ctx, repo, vcs.CheckRunUpdate{
			Name: update.Context, HeadSHA: update.SHA, State: update.State, DetailsURL: update.TargetURL,
			Title: update.Description, Summary: update.Description,
		})
	}
	return c.GitHubClient.UpdateCommitStatus(ctx, repo, update)
}

// checkRunState maps a Reactorcide state to the GitHub status and conclusion.
func checkRunState(state vcs.StatusState) (status, conclusion string) {
	switch state {
	case vcs.StatusSuccess:
		return "completed", "success"
	case vcs.StatusFailure, vcs.StatusError:
		return "completed", "failure"
	case vcs.StatusCancelled:
		return "completed", "cancelled"
	default:
		return "in_progress", ""
	}
}

type checkRunOutput struct {
	Title       string               `json:"title"`
	Summary     string               `json:"summary"`
	Text        string               `json:"text,omitempty"`
	Annotations []checkRunAnnotation `json:"annotations,omitempty"`
}

type checkRunAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title,omitempty"`
	Message         string `json:"message"`
}

type checkRunActionPayload struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Identifier  string `json:"identifier"`
}

// CheckRunPayload is the JSON body of a check run create or update.
type CheckRunPayload struct {
	Name        string                  `json:"name,omitempty"`
	HeadSHA     string                  `json:"head_sha,omitempty"`
	DetailsURL  string                  `json:"details_url,omitempty"`
	ExternalID  string                  `json:"external_id,omitempty"`
	Status      string                  `json:"status"`
	Conclusion  string                  `json:"conclusion,omitempty"`
	StartedAt   string                  `json:"started_at,omitempty"`
	CompletedAt string                  `json:"completed_at,omitempty"`
	Output      *checkRunOutput         `json:"output,omitempty"`
	Actions     []checkRunActionPayload `json:"actions,omitempty"`
}

// BuildCheckRunPayload converts an update. withAnnotations is true only for
// the first completed write: GitHub adds the annotations of each update to
// the run, so a repeated write would duplicate them.
func BuildCheckRunPayload(update vcs.CheckRunUpdate, create, withAnnotations bool, now time.Time) CheckRunPayload {
	status, conclusion := checkRunState(update.State)
	payload := CheckRunPayload{Status: status, Conclusion: conclusion, DetailsURL: update.DetailsURL, ExternalID: update.ExternalID}
	if create {
		payload.Name = update.Name
		payload.HeadSHA = update.HeadSHA
		payload.StartedAt = now.UTC().Format(time.RFC3339)
	}
	if status == "completed" {
		payload.CompletedAt = now.UTC().Format(time.RFC3339)
	}
	title := strings.TrimSpace(update.Title)
	if title == "" {
		title = update.Name
	}
	summary := strings.TrimSpace(update.Summary)
	if summary == "" {
		summary = title
	}
	output := &checkRunOutput{Title: truncate(title, maxCheckRunTitle), Summary: truncate(summary, maxCheckRunSummary),
		Text: truncate(update.Text, maxCheckRunText)}
	if withAnnotations {
		for _, annotation := range update.Annotations {
			if len(output.Annotations) == maxCheckRunAnnotations {
				break
			}
			if annotation.Path == "" || annotation.StartLine <= 0 {
				continue
			}
			end := annotation.EndLine
			if end < annotation.StartLine {
				end = annotation.StartLine
			}
			level := annotation.Level
			switch level {
			case "notice", "warning", "failure":
			default:
				level = "failure"
			}
			message := annotation.Message
			if message == "" {
				message = annotation.Title
			}
			output.Annotations = append(output.Annotations, checkRunAnnotation{Path: annotation.Path, StartLine: annotation.StartLine,
				EndLine: end, AnnotationLevel: level, Title: truncate(annotation.Title, 255), Message: truncate(message, maxAnnotationMessage)})
		}
	}
	payload.Output = output
	for _, action := range update.Actions {
		if len(payload.Actions) == 3 {
			break
		}
		payload.Actions = append(payload.Actions, checkRunActionPayload{Label: truncate(action.Label, maxActionLabel),
			Description: truncate(action.Description, maxActionDescription), Identifier: truncate(action.Identifier, 20)})
	}
	return payload
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	cut := limit - 3
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut] + "..."
}

type checkRunRecord struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// PublishCheckRun creates or updates the named check run of the commit. A
// completed run that goes back to pending (a re-run) gets a new run, so
// GitHub shows the history; branch protection reads the newest run of the
// name.
func (c *AppClient) PublishCheckRun(ctx context.Context, repo string, update vcs.CheckRunUpdate) error {
	if update.Name == "" || update.HeadSHA == "" {
		return fmt.Errorf("a check run needs a name and a head SHA")
	}
	key := checkRunKey{appID: c.appID, repo: strings.ToLower(repo), sha: update.HeadSHA, name: update.Name}
	lock := c.runs.lock(key)
	lock.Lock()
	defer lock.Unlock()

	existing, found := c.runs.get(key)
	if !found {
		record, ok, err := c.findCheckRun(ctx, repo, update.HeadSHA, update.Name)
		if err != nil {
			return err
		}
		if ok {
			existing, found = record, true
		}
	}
	now := c.now()
	status, _ := checkRunState(update.State)
	auth := tokenAuth(c.token)
	if found && !(existing.Status == "completed" && status != "completed") {
		payload := BuildCheckRunPayload(update, false, existing.Status != "completed" && status == "completed", now)
		var out checkRunRecord
		if _, err := c.api.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/check-runs/%d", repo, existing.ID), auth, payload, &out); err != nil {
			return fmt.Errorf("updating check run: %w", err)
		}
		if out.ID == 0 {
			out.ID = existing.ID
		}
		if out.Status == "" {
			out.Status = payload.Status
		}
		c.runs.put(key, out)
		return nil
	}
	payload := BuildCheckRunPayload(update, true, status == "completed", now)
	var out checkRunRecord
	if _, err := c.api.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/check-runs", repo), auth, payload, &out); err != nil {
		return fmt.Errorf("creating check run: %w", err)
	}
	if out.Status == "" {
		out.Status = payload.Status
	}
	c.runs.put(key, out)
	return nil
}

func (c *AppClient) findCheckRun(ctx context.Context, repo, sha, name string) (checkRunRecord, bool, error) {
	var out struct {
		CheckRuns []checkRunRecord `json:"check_runs"`
	}
	path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?check_name=%s&app_id=%d&filter=latest&per_page=1",
		repo, url.PathEscape(sha), url.QueryEscape(name), c.appID)
	if _, err := c.api.do(ctx, http.MethodGet, path, tokenAuth(c.token), nil, &out); err != nil {
		return checkRunRecord{}, false, fmt.Errorf("listing check runs: %w", err)
	}
	if len(out.CheckRuns) == 0 {
		return checkRunRecord{}, false, nil
	}
	return out.CheckRuns[0], true, nil
}

type checkRunKey struct {
	appID     int64
	repo, sha string
	name      string
}

// checkRunCache remembers the check run ID of (App, repo, SHA, name), so an
// update does not list runs first. It is bounded; an evicted key is found
// again through the list API.
type checkRunCache struct {
	mu    sync.Mutex
	runs  map[checkRunKey]checkRunRecord
	locks map[checkRunKey]*sync.Mutex
}

const checkRunCacheLimit = 10000

func newCheckRunCache() *checkRunCache {
	return &checkRunCache{runs: map[checkRunKey]checkRunRecord{}, locks: map[checkRunKey]*sync.Mutex{}}
}

func (c *checkRunCache) lock(key checkRunKey) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock, ok := c.locks[key]
	if !ok {
		if len(c.locks) >= checkRunCacheLimit {
			c.locks = map[checkRunKey]*sync.Mutex{}
		}
		lock = &sync.Mutex{}
		c.locks[key] = lock
	}
	return lock
}

func (c *checkRunCache) get(key checkRunKey) (checkRunRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.runs[key]
	return record, ok
}

func (c *checkRunCache) put(key checkRunKey, record checkRunRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.runs) >= checkRunCacheLimit {
		c.runs = map[checkRunKey]checkRunRecord{}
	}
	c.runs[key] = record
}
