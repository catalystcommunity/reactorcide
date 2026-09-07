package main

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// Version is the release version of this binary, without a leading "v".
//
// The release job overrides it at link time with
//
//	-ldflags=-X main.Version=X.Y.Z -w -s
//
// (see .reactorcide/plugins/plugin_release_jobs.py). A binary built any other
// way reports "dev", which is the honest answer: it did not come from a
// release, so there is no version to claim.
var Version = "dev"

// versionLine is the single line printed by --version.
//
// urfave's default is "<name> version <ver>". This drops the middle word so
// the output is two whitespace-separated fields, which is what the release job
// compares against the tag it is building. The printer and that check must
// agree, so anything that changes this format has to change
// _verify_binary_version in the release plugin with it.
func versionLine(appName, version string) string {
	return fmt.Sprintf("%s %s", appName, version)
}

// useVersionLine installs versionLine as the CLI's version printer.
//
// cli.VersionPrinter is a package-level variable in urfave/cli, so this is a
// global side effect. It is called from newApp rather than an init function so
// the wiring is visible where the app is built, and it is idempotent because
// newApp runs twice on the Windows service path.
func useVersionLine() {
	cli.VersionPrinter = func(cCtx *cli.Context) {
		fmt.Fprintln(cCtx.App.Writer, versionLine(cCtx.App.Name, cCtx.App.Version))
	}
}
