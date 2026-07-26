//go:build !(darwin && vz) && !windows

// This stub keeps `go build ./...` working on every platform/tag combination
// that is NOT a real smoke target (darwin && vz, or windows). The actual smoke
// tests live in main_vz.go (macOS) and main_windows.go (Hyper-V); without this
// file the package would have zero Go files on Linux / darwin-without-vz and
// `go build ./...` would error with "no Go files in .../cmd/vmsmoke".
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"vmsmoke is only available on Apple Silicon macOS built with the vz backend.\n"+
			"Build it with: CGO_ENABLED=1 go build -tags vz -o vmsmoke ./cmd/vmsmoke")
	os.Exit(1)
}
