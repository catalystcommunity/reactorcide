//go:build windows

package vmrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The generated scripts must parse with the PowerShell parser of the host.
func TestWindowsLifecycleScriptsParse(t *testing.T) {
	scripts := map[string]string{
		"create": hypervCreateScript(hypervCreateParams{
			BaseVHDX: `C:\images\base\disk.vhdx`, DiffPath: `D:\scratch\job.vhdx`, VMName: "reactorcide-vm-test",
			JobID: "job-1234", MemoryBytes: 4 << 30, CPUs: 2, SecureBoot: true,
		}),
		"start":    hypervStartScript("reactorcide-vm-test"),
		"register": hvsockRegistrationScript(hvsockServices()),
	}
	for name, script := range scripts {
		path := filepath.Join(t.TempDir(), name+".ps1")
		if err := os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		parser := "$tokens = $null; $errors = $null; " +
			"[void][Management.Automation.Language.Parser]::ParseFile(" + psQuote(path) + ", [ref]$tokens, [ref]$errors); " +
			"if ($errors.Count -gt 0) { $errors | ForEach-Object { Write-Error $_.Message }; exit 1 }"
		if _, err := runPowerShell(context.Background(), parser); err != nil {
			t.Fatalf("generated %s PowerShell did not parse: %v", name, err)
		}
	}
}
