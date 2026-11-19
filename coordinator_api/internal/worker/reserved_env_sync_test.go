package worker

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestReservedEventEnvMatchesRunnerlib keeps reservedEventEnvNames the same
// as _RESERVED_EVENT_ENV in runnerlib/src/eval.py.
func TestReservedEventEnvMatchesRunnerlib(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "runnerlib", "src", "eval.py"))
	if err != nil {
		t.Skipf("runnerlib is not in this tree: %v", err)
	}
	block := regexp.MustCompile(`(?s)_RESERVED_EVENT_ENV = frozenset\(\{(.*?)\}\)`).FindSubmatch(data)
	if block == nil {
		t.Fatal("_RESERVED_EVENT_ENV not found in eval.py")
	}
	python := []string{}
	for _, m := range regexp.MustCompile(`"([A-Z0-9_]+)"`).FindAllSubmatch(block[1], -1) {
		python = append(python, string(m[1]))
	}
	golang := []string{}
	for name := range reservedEventEnvNames {
		golang = append(golang, name)
	}
	sort.Strings(python)
	sort.Strings(golang)
	if strings.Join(python, ",") != strings.Join(golang, ",") {
		t.Fatalf("reserved lists differ:\n go:     %v\n python: %v", golang, python)
	}
	for _, name := range []string{"REACTORCIDE_CACHE_NAMESPACE", "REACTORCIDE_CACHE_WRITABLE", "REACTORCIDE_ARTIFACT_NAMESPACE"} {
		if !isReservedEventEnv(name) || triggerMaySupplyEventEnv(name, false) {
			t.Fatalf("%s must be reserved and never supplied by a trigger", name)
		}
	}
}
