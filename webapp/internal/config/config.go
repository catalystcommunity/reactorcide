package config

var (
	// Port is the port the web server listens on
	Port int = 4080

	// APIUrl is the base URL of the coordinator API
	APIUrl string = "http://localhost:6080"

	// APIToken is the bearer token for authenticating with the coordinator API
	APIToken string

	// WebCookieInsecure disables the Secure flag on the session cookie. Only
	// set this for local http (non-TLS) development; leaving it false (the
	// default) keeps the session cookie Secure, as required for real
	// deployments.
	WebCookieInsecure bool
)
