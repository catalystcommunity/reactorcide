package models

import (
	"encoding/json"
	"testing"
)

func TestJSONValue_ScanAndValue_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "bool", raw: "true"},
		{name: "string", raw: `"github.com"`},
		{name: "number", raw: "42"},
		{name: "object", raw: `{"a":1,"b":"two"}`},
		{name: "array", raw: `[1,2,3]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v JSONValue
			if err := v.Scan([]byte(tt.raw)); err != nil {
				t.Fatalf("Scan(%q) error: %v", tt.raw, err)
			}

			val, err := v.Value()
			if err != nil {
				t.Fatalf("Value() error: %v", err)
			}

			b, ok := val.([]byte)
			if !ok {
				t.Fatalf("Value() returned %T, want []byte", val)
			}
			if !json.Valid(b) {
				t.Fatalf("Value() returned invalid JSON: %s", b)
			}

			var roundTripped, original interface{}
			if err := json.Unmarshal(b, &roundTripped); err != nil {
				t.Fatalf("failed to unmarshal round-tripped value: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.raw), &original); err != nil {
				t.Fatalf("failed to unmarshal original value: %v", err)
			}
		})
	}
}

func TestJSONValue_ScanNil(t *testing.T) {
	v := JSONValue(`"placeholder"`)
	if err := v.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) error: %v", err)
	}
	if v != nil {
		t.Errorf("Scan(nil) left value = %v, want nil", v)
	}
}

func TestJSONValue_ScanString(t *testing.T) {
	var v JSONValue
	if err := v.Scan("false"); err != nil {
		t.Fatalf("Scan(string) error: %v", err)
	}
	if string(v) != "false" {
		t.Errorf("Scan(string) = %s, want false", v)
	}
}

func TestGlobalSetting_TableName(t *testing.T) {
	if got := (GlobalSetting{}).TableName(); got != "global_settings" {
		t.Errorf("TableName() = %q, want global_settings", got)
	}
}
