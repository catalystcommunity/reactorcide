package config

var (
	// Port is the port the web server listens on
	Port int = 5080

	// APIUrl is the base URL of the coordinator API
	APIUrl string = "http://localhost:6080"

	// There is deliberately no API token here any more.
	//
	// The webapp used to hold a coordinator SERVICE token and use it to read
	// jobs, workflows and projects on the browser's behalf. The coordinator's
	// visibility filter then ran against that service identity and passed every
	// row, and the webapp did not filter again, so a logged-out browser could
	// see private jobs.
	//
	// Every read now travels through the CSIL-RPC bridge under the BROWSER'S
	// OWN session (internal/handlers/rpc_bridge.go), and the event stream is
	// proxied under that same session. This process therefore holds no
	// coordinator credential at all — which is the point: a credential it does
	// not have cannot be used to over-fetch.
	//
	// Do not reintroduce one. If the UI needs data it cannot reach, add a CSIL
	// operation that authorizes the caller, as list-jobs and list-workflows do.

	// AllowInsecureTransport is set only by the explicit command-line flag.
	AllowInsecureTransport bool

	// WebCookieInsecure disables the Secure flag on the session cookie. Only
	// set this for local http (non-TLS) development; leaving it false (the
	// default) keeps the session cookie Secure, as required for real
	// deployments.
	WebCookieInsecure bool
)
