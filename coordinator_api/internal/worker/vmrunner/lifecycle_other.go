//go:build !darwin && !windows

package vmrunner

import (
	"fmt"
	"runtime"
)

// newVMLifecycle is the build-tag-selected VMLifecycle constructor. On
// every OS other than darwin/windows there is no VM backend at all (see
// lifecycle_darwin_vz.go and lifecycle_windows.go), so this always errors.
// worker.NewJobRunner("vm") surfaces this error unchanged and gives operators
// a clear reason rather than a generic failure.
func newVMLifecycle() (VMLifecycle, error) {
	return nil, fmt.Errorf(
		"vm backend not supported on this OS (%s); use darwin with Virtualization.framework or windows with Hyper-V",
		runtime.GOOS,
	)
}
