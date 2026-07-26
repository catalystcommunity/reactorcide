package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
)

// kvSet is a small helper so test expectations don't depend on gather
// order: it turns a []characteristics.KV into a key->value map.
func kvSet(t *testing.T, kvs []characteristics.KV) map[string]any {
	t.Helper()
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if _, dup := out[kv.Key]; dup {
			t.Fatalf("kvSet: duplicate key %q in result %#v", kv.Key, kvs)
		}
		out[kv.Key] = kv.Value
	}
	return out
}

func TestGatherCustomCharacteristics_EnvOnly(t *testing.T) {
	environ := []string{
		"REACTORCIDE_WORKER_CUSTOM_GPU=true",
		"REACTORCIDE_WORKER_CUSTOM_SLOTS=4",
		"REACTORCIDE_WORKER_CUSTOM_ZONES=us,eu",
		"REACTORCIDE_UNRELATED=ignored",
		"PATH=/usr/bin",
	}

	got, err := gatherCustomCharacteristics(nil, environ)
	if err != nil {
		t.Fatalf("gatherCustomCharacteristics: unexpected error: %v", err)
	}

	want := map[string]any{
		"gpu":   true,
		"slots": int64(4),
		"zones": []string{"us", "eu"},
	}
	if got := kvSet(t, got); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestGatherCustomCharacteristics_FlagOnly(t *testing.T) {
	got, err := gatherCustomCharacteristics([]string{"gpu=true", "slots=4", "zones=us,eu"}, nil)
	if err != nil {
		t.Fatalf("gatherCustomCharacteristics: unexpected error: %v", err)
	}

	want := map[string]any{
		"gpu":   true,
		"slots": int64(4),
		"zones": []string{"us", "eu"},
	}
	if got := kvSet(t, got); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestGatherCustomCharacteristics_EnvAndFlagMerged(t *testing.T) {
	environ := []string{"REACTORCIDE_WORKER_CUSTOM_GPU=true"}
	flags := []string{"slots=4"}

	got, err := gatherCustomCharacteristics(flags, environ)
	if err != nil {
		t.Fatalf("gatherCustomCharacteristics: unexpected error: %v", err)
	}

	want := map[string]any{
		"gpu":   true,
		"slots": int64(4),
	}
	if got := kvSet(t, got); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestGatherCustomCharacteristics_DuplicateKeyAcrossSources(t *testing.T) {
	environ := []string{"REACTORCIDE_WORKER_CUSTOM_GPU=true"}
	flags := []string{"gpu=false"}

	_, err := gatherCustomCharacteristics(flags, environ)
	if err == nil {
		t.Fatal("expected error for key set by both env and flag, got nil")
	}
	if !strings.Contains(err.Error(), "gpu") {
		t.Errorf("error %q should name the offending key %q", err.Error(), "gpu")
	}
}

func TestGatherCustomCharacteristics_DuplicateKeyWithinFlags(t *testing.T) {
	_, err := gatherCustomCharacteristics([]string{"gpu=true", "gpu=false"}, nil)
	if err == nil {
		t.Fatal("expected error for duplicate --custom key, got nil")
	}
	if !strings.Contains(err.Error(), "gpu") {
		t.Errorf("error %q should name the offending key %q", err.Error(), "gpu")
	}
}

func TestGatherCustomCharacteristics_DuplicateKeyWithinEnv(t *testing.T) {
	// Env var names are case-sensitive, so REACTORCIDE_WORKER_CUSTOM_Gpu and
	// REACTORCIDE_WORKER_CUSTOM_GPU are two distinct entries in os.Environ()
	// that both lowercase to the same characteristic key "gpu" -- a
	// legitimate way to collide within a single source.
	environ := []string{
		"REACTORCIDE_WORKER_CUSTOM_GPU=true",
		"REACTORCIDE_WORKER_CUSTOM_Gpu=false",
	}
	_, err := gatherCustomCharacteristics(nil, environ)
	if err == nil {
		t.Fatal("expected error for duplicate custom key within env source, got nil")
	}
	if !strings.Contains(err.Error(), "gpu") {
		t.Errorf("error %q should name the offending key %q", err.Error(), "gpu")
	}
}

func TestGatherCustomCharacteristics_ReservedOSCustom(t *testing.T) {
	_, err := gatherCustomCharacteristics([]string{"os=windows"}, nil)
	if err == nil {
		t.Fatal("expected error for reserved 'os' custom key, got nil")
	}
	if !strings.Contains(err.Error(), "os") || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error %q should mention the reserved key 'os'", err.Error())
	}
}

func TestGatherCustomCharacteristics_ReservedArchCustomViaEnv(t *testing.T) {
	_, err := gatherCustomCharacteristics(nil, []string{"REACTORCIDE_WORKER_CUSTOM_ARCH=arm64"})
	if err == nil {
		t.Fatal("expected error for reserved 'arch' custom key via env, got nil")
	}
	if !strings.Contains(err.Error(), "arch") || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error %q should mention the reserved key 'arch'", err.Error())
	}
}

func TestGatherCustomCharacteristics_MalformedValue(t *testing.T) {
	_, err := gatherCustomCharacteristics([]string{"tiers=1,foo"}, nil)
	if err == nil {
		t.Fatal("expected error for malformed mixed-type value, got nil")
	}
	if !strings.Contains(err.Error(), "tiers") {
		t.Errorf("error %q should name the offending key %q", err.Error(), "tiers")
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error %q should name the offending element %q", err.Error(), "foo")
	}
}

func TestGatherCustomCharacteristics_MalformedValueViaEnv(t *testing.T) {
	_, err := gatherCustomCharacteristics(nil, []string{"REACTORCIDE_WORKER_CUSTOM_TIERS=1,foo"})
	if err == nil {
		t.Fatal("expected error for malformed mixed-type value via env, got nil")
	}
	if !strings.Contains(err.Error(), "TIERS") {
		t.Errorf("error %q should name the offending env var", err.Error())
	}
}

func TestGatherCustomCharacteristics_InvalidFlagSyntax(t *testing.T) {
	_, err := gatherCustomCharacteristics([]string{"novalue"}, nil)
	if err == nil {
		t.Fatal("expected error for --custom value with no '=', got nil")
	}
}

func TestGatherCustomCharacteristics_Empty(t *testing.T) {
	got, err := gatherCustomCharacteristics(nil, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no custom characteristics, got %#v", got)
	}
}

func TestGatherCustomCharacteristics_IntList(t *testing.T) {
	got, err := gatherCustomCharacteristics([]string{"tiers=1,2,3"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]any{"tiers": []int64{1, 2, 3}}
	if got := kvSet(t, got); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}
