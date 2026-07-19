package uiapi

import "context"

// authTokenKey is an unexported context key type so this package's context
// values never collide with keys set elsewhere.
type authTokenKey struct{}

// WithAuthToken returns a context carrying the CSIL-RPC envelope's "auth"
// field (the caller's session token, or "" for anonymous callers). The
// dispatcher calls this once per request before invoking the routed
// implementation method; implementations call AuthTokenFromContext to
// recover it for session resolution / authorization.
func WithAuthToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, authTokenKey{}, token)
}

// AuthTokenFromContext returns the session token set by WithAuthToken and
// whether the envelope carried an "auth" field at all. A request with no
// "auth" field (anonymous caller) returns ("", false); a request with an
// empty "auth" field returns ("", true).
func AuthTokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(authTokenKey{}).(string)
	return token, ok
}
