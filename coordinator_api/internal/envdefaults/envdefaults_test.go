package envdefaults

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	if err := Validate("GOPROXY", "https://proxy.example/go"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"REACTORCIDE_CACHE_NAMESPACE", "reactorcide_api_token", "RC_WF_VARS_JSON", "RC_WFU_X", "1BAD", "BAD-KEY", ""} {
		if Validate(key, "v") == nil {
			t.Fatalf("key %q must be refused", key)
		}
	}
	if err := Validate("TOKEN", "${secret:org/gh:token}"); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a secret reference must be refused: %v", err)
	}
	if Validate("BIG", strings.Repeat("x", MaxValueLength+1)) == nil {
		t.Fatal("a long value must be refused")
	}
}

func TestPrecedence(t *testing.T) {
	layers := Layers{
		Org:         map[string]string{"A": "org", "B": "org", "C": "org", "REACTORCIDE_X": "bad"},
		WorkerClass: map[string]string{"B": "class", "C": "class"},
		Project:     map[string]string{"C": "project"},
	}
	effective := layers.Effective()
	if effective["A"] != "org" || effective["B"] != "class" || effective["C"] != "project" {
		t.Fatalf("effective %v", effective)
	}
	if _, ok := effective["REACTORCIDE_X"]; ok {
		t.Fatal("a reserved key must be dropped")
	}
	env := map[string]string{"C": "job", "REACTORCIDE_JOB_ID": "j"}
	added := Apply(env, effective)
	if env["C"] != "job" || env["A"] != "org" || env["B"] != "class" || len(added) != 2 {
		t.Fatalf("job must win: %v added %v", env, added)
	}
	Apply(env, map[string]string{"REACTORCIDE_JOB_ID": "x"})
	if env["REACTORCIDE_JOB_ID"] != "j" {
		t.Fatal("a reserved key must never be applied")
	}
}
