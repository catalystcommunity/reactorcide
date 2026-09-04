package test

// UI auth flows against the live coordinator started by setup_test.go's
// TestMain. These cover the endpoints that own the session cookie -- the only
// part of the UI that is not a CSIL call through the bridge.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// newBrowserClient keeps a cookie jar, like a browser, but does NOT follow
// redirects.
//
// Following them silently breaks any assertion about the response: a successful
// bootstrap answers 302 with the Set-Cookie header, and net/http then follows to
// /app/ and hands back THAT response, whose headers contain no cookie at all.
// The jar still holds it, so both are checked below.
func newBrowserClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// sessionCookieFrom returns the session cookie a response set, if any.
func sessionCookieFrom(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "rc_session" {
			return c
		}
	}
	return nil
}

// TestBootstrapAdminMintsASession covers the one-time bootstrap: it must set the
// session cookie, and that cookie must not be readable by page JavaScript.
func TestBootstrapAdminMintsASession(t *testing.T) {
	client := newBrowserClient(t)

	resp, err := client.PostForm(webBaseURL+"/app/auth/bootstrap", url.Values{"token": {testBootstrapAdminToken}})
	if err != nil {
		t.Fatalf("POST /app/auth/bootstrap: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("bootstrap status = %d, want a successful session mint; body = %s", resp.StatusCode, body)
	}

	sessionCookie := sessionCookieFrom(resp)
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("bootstrap did not set a session cookie")
	}
	if !sessionCookie.HttpOnly {
		t.Error("the session cookie must be HttpOnly; page JavaScript must never be able to read a session token")
	}

	// The session must now be visible to the SPA's own session endpoint. This
	// travels via the jar, which is what proves the cookie is actually usable
	// rather than merely present in one response.
	sessionResp, err := client.Get(webBaseURL + "/app/auth/session")
	if err != nil {
		t.Fatalf("GET /app/auth/session: %v", err)
	}
	defer sessionResp.Body.Close()

	var session struct {
		LoggedIn      bool `json:"logged_in"`
		IsGlobalAdmin bool `json:"is_global_admin"`
	}
	if err := json.NewDecoder(sessionResp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !session.LoggedIn {
		t.Error("the bootstrapped session should report logged_in")
	}
	if !session.IsGlobalAdmin {
		t.Error("the bootstrap admin should report is_global_admin")
	}
}

// TestBootstrapRejectsWrongTokenWithoutSettingACookie is the negative half: a
// failed bootstrap must leave the browser with no session at all.
func TestBootstrapRejectsWrongTokenWithoutSettingACookie(t *testing.T) {
	client := newBrowserClient(t)

	resp, err := client.PostForm(webBaseURL+"/app/auth/bootstrap", url.Values{"token": {"definitely-not-the-token"}})
	if err != nil {
		t.Fatalf("POST /app/auth/bootstrap: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusOK {
		t.Fatalf("a wrong bootstrap token was accepted (status %d)", resp.StatusCode)
	}
	if cookie := sessionCookieFrom(resp); cookie != nil && cookie.Value != "" {
		t.Error("a failed bootstrap must not set a session cookie")
	}
}

// TestAuthCallbackWithoutParamsRedirectsIntoTheSPA covers the IDP return leg.
// It is a top-level navigation, so a failure has to be a redirect the browser
// can follow rather than a JSON body nothing is listening for.
func TestAuthCallbackWithoutParamsRedirectsIntoTheSPA(t *testing.T) {
	client := newBrowserClient(t)
	resp, err := client.Get(webBaseURL + "/app/auth/callback")
	if err != nil {
		t.Fatalf("GET /app/auth/callback: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); !strings.Contains(location, "login_error=") {
		t.Errorf("Location = %q, want the failure reason carried back to the SPA", location)
	}
}
