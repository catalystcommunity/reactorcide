//go:build !windows

package vmrunner

import (
	"context"
	"fmt"
	"runtime"
)

func buildWindowsImage(context.Context, WindowsImageBuildOptions) error {
	return fmt.Errorf("vmrunner: Windows image build requires a Windows host with Hyper-V (running on %s)", runtime.GOOS)
}
