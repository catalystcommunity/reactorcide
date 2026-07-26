//go:build darwin && !vz

package vmrunner

import "errors"

// errDarwinVZTagRequired is returned by newVMLifecycle on a darwin build that
// was NOT compiled with the "vz" build tag. The real macOS VMLifecycle
// (lifecycle_darwin_vz.go) imports github.com/Code-Hex/vz/v3, which needs Cgo
// and a code-signed binary with the com.apple.security.virtualization
// entitlement. The release CLI is cross-compiled with CGO_ENABLED=0 GOOS=darwin
// (no Cgo, no vz), so it lands here and gets a clear rebuild instruction rather
// than a link error; the Mac worker is built with `CGO_ENABLED=1 go build
// -tags vz` (see scripts/build-macos-worker.sh) to select the real backend.
var errDarwinVZTagRequired = errors.New(
	"vm runner: rebuild the worker on macOS with `CGO_ENABLED=1 go build -tags vz` " +
		"to enable the macOS VM backend (the default darwin build omits the vz/Cgo backend)",
)

// newVMLifecycle on a darwin && !vz build always errors: there is no VM
// backend compiled in. Callers (VMRunner.NewDefault -> worker.NewJobRunner
// ("vm")) surface this unchanged.
func newVMLifecycle() (VMLifecycle, error) {
	return nil, errDarwinVZTagRequired
}
