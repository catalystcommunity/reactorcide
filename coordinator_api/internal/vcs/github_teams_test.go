package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func teamTestClient(t *testing.T, handler http.HandlerFunc) *GitHubClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewGitHubClient(Config{Provider: GitHub, Token: "t", BaseURL: server.URL, AllowInsecureTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestListTeamMembersFollowsLinkHeader(t *testing.T) {
	var base string
	client := teamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/acme/teams/platform/members" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/orgs/acme/teams/platform/members?per_page=100&page=2>; rel="next", <%s/x?page=2>; rel="last"`, base, base))
			_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "alice"}, {"login": "bob"}})
		case "2":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "carol"}})
		default:
			t.Errorf("unexpected page %q", r.URL.RawQuery)
		}
	})
	base = client.config.BaseURL
	logins, err := client.ListTeamMembers(context.Background(), "acme", "platform")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(logins, ",") != "alice,bob,carol" {
		t.Fatalf("logins = %v", logins)
	}
}

func TestListTeamMembersRefusesForeignNextLink(t *testing.T) {
	client := teamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://evil.example/next>; rel="next"`)
		_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "alice"}})
	})
	if _, err := client.ListTeamMembers(context.Background(), "acme", "platform"); err == nil {
		t.Fatal("a next link to another host must fail")
	}
}

func TestTeamErrors(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	client := teamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/gone"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/limited"):
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset))
			w.WriteHeader(http.StatusForbidden)
		case strings.Contains(r.URL.Path, "/secondary"):
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusForbidden)
		case strings.Contains(r.URL.Path, "/denied"):
			w.WriteHeader(http.StatusForbidden)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9, "slug": "ok", "name": "OK"})
		}
	})
	ctx := context.Background()
	team, err := client.GetTeam(ctx, "acme", "ok")
	if err != nil || team.ID != 9 {
		t.Fatalf("team = %+v, %v", team, err)
	}
	if _, err := client.GetTeam(ctx, "acme", "gone"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("404 = %v", err)
	}
	var limit *RateLimitError
	if _, err := client.ListTeamMembers(ctx, "acme", "limited"); !errors.As(err, &limit) || limit.Reset.Unix() != reset {
		t.Fatalf("primary rate limit = %v", err)
	}
	if _, err := client.GetTeam(ctx, "acme", "secondary"); !errors.As(err, &limit) || time.Until(limit.Reset) < 20*time.Second {
		t.Fatalf("secondary rate limit = %v", err)
	}
	if _, err := client.GetTeam(ctx, "acme", "denied"); err == nil || errors.As(err, &limit) || errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("403 without rate limit = %v", err)
	}
}

func TestParseTeamWebhooks(t *testing.T) {
	cases := []struct {
		kind, body string
		want       *TeamEventInfo
	}{
		{"membership", `{"action":"removed","scope":"team","member":{"login":"Bob"},"team":{"id":5,"slug":"Platform"},"organization":{"login":"Acme"},"sender":{"login":"admin"}}`,
			&TeamEventInfo{Kind: "membership", Action: "removed", Owner: "acme", Slug: "platform", TeamID: 5, MemberLogin: "Bob"}},
		{"team", `{"action":"edited","changes":{"name":{"from":"Old"}},"team":{"id":5,"slug":"new-name"},"organization":{"login":"acme"}}`,
			&TeamEventInfo{Kind: "team", Action: "edited", Owner: "acme", Slug: "new-name", TeamID: 5, Renamed: true}},
		{"team", `{"action":"deleted","team":{"id":5,"slug":"platform"},"organization":{"login":"acme"}}`,
			&TeamEventInfo{Kind: "team", Action: "deleted", Owner: "acme", Slug: "platform", TeamID: 5}},
		{"membership", `{"action":"added","scope":"organization","organization":{"login":"acme"}}`, nil},
	}
	for _, c := range cases {
		event := &WebhookEvent{}
		if err := parseGitHubTeamEvent(c.kind, []byte(c.body), event); err != nil {
			t.Fatal(err)
		}
		if (event.Team == nil) != (c.want == nil) || (c.want != nil && *event.Team != *c.want) {
			t.Errorf("%s %s: got %+v want %+v", c.kind, c.body, event.Team, c.want)
		}
	}
	// Through ParseWebhook.
	client := teamTestClient(t, func(http.ResponseWriter, *http.Request) {})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(cases[0].body))
	req.Header.Set("X-GitHub-Event", "membership")
	event, err := client.ParseWebhook(req)
	if err != nil || event.Team == nil || event.GenericEvent != EventUnknown {
		t.Fatalf("event = %+v, %v", event, err)
	}
}
