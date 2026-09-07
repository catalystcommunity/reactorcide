package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/cmd"
)

// runApp drives the real app the way main does, including NormalizeArgs, and
// returns everything it wrote. Going through newApp rather than asserting on
// versionLine directly is the point: the flag has to be REGISTERED, reach the
// printer, and exit without an error, and only running the app proves that.
func runApp(t *testing.T, args ...string) (string, error) {
	t.Helper()
	app := newApp()
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	argv := append([]string{"reactorcide"}, args...)
	err := app.Run(cmd.NormalizeArgs(app, argv))
	return out.String(), err
}

func TestVersionFlagPrintsOneExactLine(t *testing.T) {
	original := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = original })

	// The release job asserts on this exact string, so it is spelled out here
	// rather than built from the same helper the code under test uses.
	const want = "reactorcide 1.2.3\n"

	for _, flag := range []string{"--version", "-v"} {
		got, err := runApp(t, flag)
		if err != nil {
			// A non-nil error is a non-zero exit from main.
			t.Fatalf("%s returned an error, so the process would exit non-zero: %v", flag, err)
		}
		if got != want {
			t.Errorf("%s printed %q, want %q", flag, got, want)
		}
	}
}

// An unset ldflag must not look like a release. "dev" is a version nobody can
// mistake for one; an empty string would make urfave hide the flag entirely
// and turn `reactorcide --version` into "flag provided but not defined".
func TestVersionDefaultsToDev(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("the compiled-in default is %q, want %q", Version, "dev")
	}
	got, err := runApp(t, "--version")
	if err != nil {
		t.Fatalf("--version returned an error: %v", err)
	}
	if got != "reactorcide dev\n" {
		t.Errorf("--version printed %q, want %q", got, "reactorcide dev\n")
	}
}

// The version line is two fields on one line. The release check splits on
// whitespace and compares, so a version that ever carried a space, or a
// printer that ever wrapped, would break it silently.
func TestVersionLineIsTwoFieldsOnOneLine(t *testing.T) {
	line := versionLine("reactorcide", "1.2.3")
	if strings.Contains(line, "\n") {
		t.Fatalf("version line contains a newline: %q", line)
	}
	if fields := strings.Fields(line); len(fields) != 2 {
		t.Fatalf("version line has %d fields, want 2: %q", len(fields), line)
	}
}

// The release plugin greps `go version -m` output for `-X main.Version=`, so
// the variable has to stay in package main under that name. This test fails to
// COMPILE if it is renamed or moved, which is the intent.
func TestVersionIsAddressableInPackageMain(t *testing.T) {
	if _, err := os.Stat("main.go"); err != nil {
		t.Fatalf("expected to run in the package main directory: %v", err)
	}
	var _ *string = &Version
}
