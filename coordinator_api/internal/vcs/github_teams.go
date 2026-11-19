package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Team sync (plan item B3). A group of Reactorcide can bind to a VCS team.
// The sync loop (internal/vcsteams) reads the members of the team with the
// interfaces in this file. The VCS stays the authority: a webhook about a
// team is only a hint to sync again.

// Team is one VCS team.
type Team struct {
	ID   int64
	Slug string
	Name string
}

// TeamMemberLister reads teams and their members.
type TeamMemberLister interface {
	// GetTeam returns the team, or ErrTeamNotFound.
	GetTeam(ctx context.Context, owner, slug string) (*Team, error)
	// ListTeamMembers returns the logins of all members of the team (all
	// pages), or ErrTeamNotFound.
	ListTeamMembers(ctx context.Context, owner, slug string) ([]string, error)
}

// ErrTeamNotFound is returned when the VCS does not show the team. GitHub
// also answers 404 when the token cannot see the team.
var ErrTeamNotFound = errors.New("VCS team not found or not visible to the credential")

// RateLimitError is returned when the VCS refuses a request because of its
// rate limit. Reset is when the limit resets (zero when unknown).
type RateLimitError struct {
	Status int
	Reset  time.Time
}

func (e *RateLimitError) Error() string {
	if e.Reset.IsZero() {
		return fmt.Sprintf("VCS rate limit reached (status %d)", e.Status)
	}
	return fmt.Sprintf("VCS rate limit reached (status %d), resets at %s", e.Status, e.Reset.UTC().Format(time.RFC3339))
}

// maxTeamPages limits one listing to 100 pages of 100 members.
const maxTeamPages = 100

// GetTeam implements TeamMemberLister.
func (c *GitHubClient) GetTeam(ctx context.Context, owner, slug string) (*Team, error) {
	endpoint := fmt.Sprintf("%s/orgs/%s/teams/%s", c.config.BaseURL, url.PathEscape(owner), url.PathEscape(slug))
	var body struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if _, err := c.teamGet(ctx, endpoint, &body); err != nil {
		return nil, err
	}
	return &Team{ID: body.ID, Slug: body.Slug, Name: body.Name}, nil
}

// ListTeamMembers implements TeamMemberLister. GitHub includes the members
// of child teams. It follows the Link header; without one it stops at a
// short page.
func (c *GitHubClient) ListTeamMembers(ctx context.Context, owner, slug string) ([]string, error) {
	next := fmt.Sprintf("%s/orgs/%s/teams/%s/members?per_page=100&page=1", c.config.BaseURL, url.PathEscape(owner), url.PathEscape(slug))
	logins := []string{}
	for page := 1; next != ""; page++ {
		if page > maxTeamPages {
			return nil, fmt.Errorf("team %s/%s has more than %d pages of members", owner, slug, maxTeamPages)
		}
		var members []struct {
			Login string `json:"login"`
		}
		header, err := c.teamGet(ctx, next, &members)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			if login := strings.TrimSpace(member.Login); login != "" {
				logins = append(logins, login)
			}
		}
		link := nextLink(header.Get("Link"))
		switch {
		case link != "":
			// Only follow a link to the same API host.
			if !strings.HasPrefix(link, c.config.BaseURL+"/") {
				return nil, fmt.Errorf("GitHub returned a next page on another host")
			}
			next = link
		case header.Get("Link") == "" && len(members) == 100:
			next = fmt.Sprintf("%s/orgs/%s/teams/%s/members?per_page=100&page=%d", c.config.BaseURL, url.PathEscape(owner), url.PathEscape(slug), page+1)
		default:
			next = ""
		}
	}
	return logins, nil
}

var linkNextPattern = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		if match := linkNextPattern.FindStringSubmatch(strings.TrimSpace(part)); match != nil {
			return match[1]
		}
	}
	return ""
}

func (c *GitHubClient) teamGet(ctx context.Context, endpoint string, dst any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.config.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if rate := rateLimitOf(resp); rate != nil {
		return nil, rate
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrTeamNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("GitHub refused the credential (status %d)", resp.StatusCode)
	default:
		return nil, fmt.Errorf("GitHub returned status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return resp.Header, nil
}

// rateLimitOf returns a RateLimitError for a primary (X-RateLimit-Remaining
// 0) or secondary (Retry-After) rate limit response.
func rateLimitOf(resp *http.Response) *RateLimitError {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	if after := strings.TrimSpace(resp.Header.Get("Retry-After")); after != "" {
		if seconds, err := strconv.Atoi(after); err == nil {
			return &RateLimitError{Status: resp.StatusCode, Reset: time.Now().Add(time.Duration(seconds) * time.Second)}
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		limit := &RateLimitError{Status: resp.StatusCode}
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			limit.Reset = time.Unix(reset, 0)
		}
		return limit
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{Status: resp.StatusCode}
	}
	return nil
}

// TeamEventInfo is a GitHub `team` or `membership` event.
type TeamEventInfo struct {
	// Kind is "team" or "membership".
	Kind string
	// Action: for membership "added" or "removed"; for team "created",
	// "deleted", "edited", "added_to_repository", "removed_from_repository".
	Action string
	// Owner is the organization login, lowercase.
	Owner string
	// Slug is the team slug, lowercase (the new slug after a rename).
	Slug   string
	TeamID int64
	// MemberLogin is the member of a membership event.
	MemberLogin string
	// Renamed is true for an edited team whose name changed.
	Renamed bool
}

func parseGitHubTeamEvent(kind string, body []byte, event *WebhookEvent) error {
	var payload struct {
		Action       string `json:"action"`
		Scope        string `json:"scope"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
		Team struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		} `json:"team"`
		Member struct {
			Login string `json:"login"`
		} `json:"member"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		Changes struct {
			Name *struct {
				From string `json:"from"`
			} `json:"name"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	event.SenderLogin = payload.Sender.Login
	if kind == "membership" && payload.Scope != "" && payload.Scope != "team" {
		return nil
	}
	owner := strings.ToLower(strings.TrimSpace(payload.Organization.Login))
	slug := strings.ToLower(strings.TrimSpace(payload.Team.Slug))
	if owner == "" || (slug == "" && payload.Team.ID == 0) {
		return nil
	}
	event.Team = &TeamEventInfo{Kind: kind, Action: payload.Action, Owner: owner, Slug: slug,
		TeamID: payload.Team.ID, MemberLogin: payload.Member.Login,
		Renamed: kind == "team" && payload.Action == "edited" && payload.Changes.Name != nil}
	return nil
}
