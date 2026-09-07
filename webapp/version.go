package main

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// Version is the release version of this binary, without a leading "v".
//
// Set at link time by the release job with -X main.Version=X.Y.Z, exactly as
// the coordinator's is. webapp is a separate Go module, so this cannot be
// shared with coordinator_api/version.go; the two are deliberately identical
// apart from the app name they print.
var Version = "dev"

// useVersionLine makes --version print "<name> <version>" on one line rather
// than urfave's "<name> version <version>".
func useVersionLine() {
	cli.VersionPrinter = func(cCtx *cli.Context) {
		fmt.Fprintf(cCtx.App.Writer, "%s %s\n", cCtx.App.Name, cCtx.App.Version)
	}
}
