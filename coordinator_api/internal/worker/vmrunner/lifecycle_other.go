//go:build !darwin && !windows

package vmrunner

import (
	"fmt"
	"runtime"
)

// newVMLifecycle is the build-tag-selected VMLifecycle constructor. On
// every OS other than darwin/windows there is no VM backend at all (see
// lifecycle_darwin.go / lifecycle_windows.go for the platforms that will
// have one), so this always errors -- VMRunner.NewDefault and, through it,
// worker.NewJobRunner("vm") surface this error unchanged, giving operators
// a clear reason rather than a generic failure.
func newVMLifecycle() (VMLifecycle, error) {
	return nil, fmt.Errorf(
		"vm backend not supported on this OS (%s); need darwin (Virtualization.framework, VM-3) or windows (Hyper-V, VM-4)",
		runtime.GOOS,
	)
}
