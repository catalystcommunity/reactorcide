package githubapp

import (
	"context"
	"sync"
	"time"
)

// tokenRefreshMargin: a cached installation token is used until 5 minutes
// before it expires.
const tokenRefreshMargin = 5 * time.Minute

// SystemPermissions are the permissions that Reactorcide asks for in an
// installation token for coordinator calls. A token request asks only for
// the ones that the installation granted (GitHub refuses a request for more).
var SystemPermissions = map[string]string{
	"contents":      "read",
	"metadata":      "read",
	"checks":        "write",
	"statuses":      "write",
	"pull_requests": "write",
	"members":       "read",
}

// CheckoutPermissions are the permissions of a checkout token: it reads the
// contents of one repository and nothing more.
var CheckoutPermissions = map[string]string{
	"contents": "read",
	"metadata": "read",
}

// narrowPermissions returns the wanted permissions that the installation
// granted, at the wanted level or lower. With no granted map (an old row),
// it returns the wanted map; GitHub then decides.
func narrowPermissions(wanted, granted map[string]string) map[string]string {
	if len(granted) == 0 {
		out := make(map[string]string, len(wanted))
		for k, v := range wanted {
			out[k] = v
		}
		return out
	}
	out := map[string]string{}
	for name, level := range wanted {
		have, ok := granted[name]
		if !ok {
			continue
		}
		if level == "write" && have == "read" {
			level = "read"
		}
		out[name] = level
	}
	return out
}

type tokenKey struct {
	installationID int64
	// scope is "system" or "checkout:<owner/name>".
	scope string
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// tokenCache keeps installation tokens per installation and scope. One fetch
// runs at a time for each key; other callers wait for it.
type tokenCache struct {
	mu       sync.Mutex
	tokens   map[tokenKey]cachedToken
	inflight map[tokenKey]*sync.Mutex
}

func newTokenCache() *tokenCache {
	return &tokenCache{tokens: map[tokenKey]cachedToken{}, inflight: map[tokenKey]*sync.Mutex{}}
}

func (c *tokenCache) get(key tokenKey, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.tokens[key]
	if !ok || !now.Before(entry.expiresAt.Add(-tokenRefreshMargin)) {
		return "", false
	}
	return entry.token, true
}

// getOrFetch returns a cached token or calls fetch once for the key.
func (c *tokenCache) getOrFetch(ctx context.Context, key tokenKey, now func() time.Time,
	fetch func(ctx context.Context) (string, time.Time, error)) (string, error) {
	if token, ok := c.get(key, now()); ok {
		return token, nil
	}
	c.mu.Lock()
	lock, ok := c.inflight[key]
	if !ok {
		lock = &sync.Mutex{}
		c.inflight[key] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if token, ok := c.get(key, now()); ok {
		return token, nil
	}
	token, expiresAt, err := fetch(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.tokens[key] = cachedToken{token: token, expiresAt: expiresAt}
	c.mu.Unlock()
	return token, nil
}

// forgetInstallation drops every token of an installation (suspended,
// deleted, or its App disabled).
func (c *tokenCache) forgetInstallation(installationID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.tokens {
		if key.installationID == installationID {
			delete(c.tokens, key)
		}
	}
}

func (c *tokenCache) forgetAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens = map[tokenKey]cachedToken{}
}
