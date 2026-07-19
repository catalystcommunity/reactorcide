package uiclient

import "context"

// authTokenKey is an unexported context key type so this package's context
// values never collide with keys set elsewhere.
type authTokenKey struct{}

// WithAuthToken returns a context carrying a session token for the next
// CSIL-RPC call made with it. The generated client methods
// (csilapi.ReactorcideAuthClient / csilapi.ReactorcideUiClient) only take a
// context, not an auth parameter, so this is how a webapp request handler
// supplies the browser's session token (from its cookie) per call: build a
// context once per incoming HTTP request with the resolved token and pass it
// through to every client call made while handling that request. A missing
// token (context without WithAuthToken applied) means an anonymous call —
// the envelope's "auth" field is omitted, matching REACTORCIDE_UI_AUTH_MODE
// "none" and anonymous browsing in "local-rp"/"rp" modes.
func WithAuthToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, authTokenKey{}, token)
}

// AuthTokenFromContext returns the token set by WithAuthToken, if any.
func AuthTokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(authTokenKey{}).(string)
	return token, ok
}
