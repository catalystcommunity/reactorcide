//go:build !darwin || !vz

package vmrunner

import (
	"context"
	"fmt"
	"runtime"
)

func buildMacImage(context.Context, MacImageBuildOptions) error {
	return fmt.Errorf("vmrunner: macOS image build requires Apple Silicon macOS and a CGO binary built with -tags vz (running on %s)", runtime.GOOS)
}
