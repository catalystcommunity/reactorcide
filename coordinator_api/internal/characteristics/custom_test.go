package characteristics

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCustomValue_TypeInference(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Value
	}{
		{"int scalar", "5", IntValue(5)},
		{"int scalar negative", "-5", IntValue(-5)},
		{"int scalar leading zero", "007", IntValue(7)},
		{"int list", "1,2,3", IntListValue{1, 2, 3}},
		{"int list with negatives", "-1,0,1", IntListValue{-1, 0, 1}},
		{"bool scalar true", "true", BoolValue(true)},
		{"bool scalar True", "True", BoolValue(true)},
		{"bool scalar TRUE", "TRUE", BoolValue(true)},
		{"bool scalar false", "false", BoolValue(false)},
		{"bool scalar False", "False", BoolValue(false)},
		{"bool scalar FALSE", "FALSE", BoolValue(false)},
		{"bool list", "true,false,True", BoolListValue{true, false, true}},
		{"string scalar", "gpu", StringValue("gpu")},
		{"string list", "us,eu,apac", StringListValue{"us", "eu", "apac"}},
		{"yes is not bool, it's a string", "yes", StringValue("yes")},
		{"on is not bool, it's a string", "on", StringValue("on")},
		{"off is not bool, it's a string", "off", StringValue("off")},
		{"no is not bool, it's a string", "no", StringValue("no")},
		{"1abc is not an int, it's a string", "1abc", StringValue("1abc")},
		{
			"first element keys the type: foo,1 is a string list",
			"foo,1", StringListValue{"foo", "1"},
		},
		{
			"escaped comma inside a string element",
			`foo\,bar,baz`, StringListValue{"foo,bar", "baz"},
		},
		{
			"escaped backslash inside a string element",
			`foo\\bar`, StringValue(`foo\bar`),
		},
		{
			"whitespace trimmed around elements",
			" foo , bar ", StringListValue{"foo", "bar"},
		},
		{
			"whitespace trimmed around int elements",
			" 1 , 2 ", IntListValue{1, 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCustomValue(tc.raw)
			if err != nil {
				t.Fatalf("ParseCustomValue(%q): unexpected error: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseCustomValue(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseCustomValue_Errors(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		errContains []string
	}{
		{
			name:        "empty string",
			raw:         "",
			errContains: []string{"empty"},
		},
		{
			name:        "whitespace only",
			raw:         "   ",
			errContains: []string{"empty"},
		},
		{
			name:        "empty element between commas",
			raw:         "foo,,bar",
			errContains: []string{"empty element", "position 2"},
		},
		{
			name:        "trailing comma leaves trailing empty element",
			raw:         "foo,",
			errContains: []string{"empty element", "position 2"},
		},
		{
			name:        "first is int, rest is not: 1,foo",
			raw:         "1,foo",
			errContains: []string{"foo", "position 2", "not an integer", "mixed types are not allowed"},
		},
		{
			name:        "first is bool, rest is not",
			raw:         "true,notabool",
			errContains: []string{"notabool", "position 2", "not a recognized YAML boolean", "mixed types are not allowed"},
		},
		{
			name:        "first is bool, rest is yes/no style",
			raw:         "true,yes",
			errContains: []string{"yes", "position 2", "not a recognized YAML boolean"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCustomValue(tc.raw)
			if err == nil {
				t.Fatalf("ParseCustomValue(%q): expected error, got nil", tc.raw)
			}
			for _, want := range tc.errContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("ParseCustomValue(%q) error = %q, want it to contain %q", tc.raw, err.Error(), want)
				}
			}
		})
	}
}

// TestParseCustomValue_FirstElementAsymmetry documents and locks in the
// deliberate asymmetry: the type is decided by the FIRST element only, so
// "foo,1" (string first) succeeds as a string list, but "1,foo" (int first)
// is a loud error -- the two are not the same input reordered into an
// equivalent result.
func TestParseCustomValue_FirstElementAsymmetry(t *testing.T) {
	got, err := ParseCustomValue("foo,1")
	if err != nil {
		t.Fatalf("ParseCustomValue(\"foo,1\"): unexpected error: %v", err)
	}
	want := StringListValue{"foo", "1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseCustomValue(\"foo,1\") = %#v, want %#v", got, want)
	}

	if _, err := ParseCustomValue("1,foo"); err == nil {
		t.Fatalf("ParseCustomValue(\"1,foo\"): expected error, got nil")
	}
}
