package cmd

import (
	"testing"

	"github.com/urfave/cli/v2"
)

func TestInsecureTransportFlagsHaveNoEnvironmentOverride(t *testing.T) {
	assertCLIOnly := func(flags []cli.Flag, name string) {
		t.Helper()
		for _, flag := range flags {
			if flag.Names()[0] != name {
				continue
			}
			boolFlag, ok := flag.(*cli.BoolFlag)
			if !ok {
				t.Fatalf("%s has type %T, want *cli.BoolFlag", name, flag)
			}
			if len(boolFlag.EnvVars) != 0 {
				t.Fatalf("%s has environment overrides: %v", name, boolFlag.EnvVars)
			}
			return
		}
		t.Fatalf("flag %s not found", name)
	}

	assertCLIOnly(apiFlags(), "allow-insecure-transport")
	assertCLIOnly(workerFlags, "allow-insecure-transport")
}

func TestPlainHTTPRegistryFlagHasNoEnvironmentOverride(t *testing.T) {
	for _, flag := range workerFlags {
		if flag.Names()[0] != "vm-image-registry-plain-http" {
			continue
		}
		stringFlag, ok := flag.(*cli.StringSliceFlag)
		if !ok {
			t.Fatalf("flag has type %T, want *cli.StringSliceFlag", flag)
		}
		if len(stringFlag.EnvVars) != 0 {
			t.Fatalf("flag has environment overrides: %v", stringFlag.EnvVars)
		}
		return
	}
	t.Fatal("vm-image-registry-plain-http flag not found")
}
