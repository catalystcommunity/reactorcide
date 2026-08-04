package vmrunner

import (
	"context"
	"io"
)

// MacImageBuildOptions configures creation of a derived macOS image from an
// existing bootstrap bundle.
type MacImageBuildOptions struct {
	SourceBundle     string
	OutputBundle     string
	CPUs             int
	MemoryBytes      int64
	Creds            GuestCreds
	Scripts          []string
	LogWriter        io.Writer
	AllowUncleanStop bool
}

// BuildMacImage clones a bootstrap image, provisions it in a VM, shuts it
// down, and seals the result as a new bundle.
func BuildMacImage(ctx context.Context, opts MacImageBuildOptions) error {
	return buildMacImage(ctx, opts)
}
