package characteristics

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

// DefaultOS is the value injected for a job's "os" characteristic when the
// job spec omits it. It is the one and only default in the characteristic
// model.
const DefaultOS = "linux"

// ParseJobCharacteristics validates a job spec's raw `characteristics` map
// (job or queue characteristics — the two share validation rules) and
// returns a canonical-ready Characteristics set.
//
// Rules:
//   - Keys must be non-empty strings.
//   - Values must be scalar: string, a Go integer type, or bool. Lists are
//     rejected with a clear error — list values are reserved for future
//     filter-expression syntax (e.g. ge(3)), not supported today.
//   - If the "os" key is absent after validation, "os": "linux" is injected.
//     No other key is ever defaulted.
//
// A nil or empty raw map is valid and yields {"os": "linux"}.
func ParseJobCharacteristics(raw map[string]any) (Characteristics, error) {
	c := make(Characteristics, len(raw)+1)
	for k, v := range raw {
		if k == "" {
			return nil, fmt.Errorf("characteristics: empty key not allowed")
		}
		val, err := parseScalarValue(k, v)
		if err != nil {
			return nil, err
		}
		c[k] = val
	}
	if _, ok := c["os"]; !ok {
		c["os"] = StringValue(DefaultOS)
	}
	return c, nil
}

// ParseWorkerCharacteristics validates and assembles a worker's
// Characteristics from its required os/arch and its custom key/value pairs.
//
// os and arch are taken verbatim from the caller (auto-detected or
// operator-configured) with no normalization or capitalization fixups; both
// must be non-empty. custom entries may be scalar or a homogeneous list
// (mixed-type lists are rejected). "os" and "arch" may not be repeated in
// custom (they are already supplied via the dedicated parameters); duplicate
// custom keys are rejected. The custom key "queue" is not special — it is
// validated and stored like any other key.
func ParseWorkerCharacteristics(os, arch string, custom []KV) (Characteristics, error) {
	if os == "" {
		return nil, fmt.Errorf("characteristics: worker os is required")
	}
	if arch == "" {
		return nil, fmt.Errorf("characteristics: worker arch is required")
	}

	c := make(Characteristics, len(custom)+2)
	c["os"] = StringValue(os)
	c["arch"] = StringValue(arch)

	seen := map[string]bool{"os": true, "arch": true}
	for _, kv := range custom {
		if kv.Key == "" {
			return nil, fmt.Errorf("characteristics: empty key not allowed")
		}
		if kv.Key == "os" || kv.Key == "arch" {
			return nil, fmt.Errorf("characteristics: custom key %q is reserved; set it via the os/arch parameters", kv.Key)
		}
		if seen[kv.Key] {
			return nil, fmt.Errorf("characteristics: duplicate custom key %q", kv.Key)
		}
		seen[kv.Key] = true

		val, err := parseWorkerValue(kv.Key, kv.Value)
		if err != nil {
			return nil, err
		}
		c[kv.Key] = val
	}
	return c, nil
}

// parseScalarValue validates a job/queue characteristic value: scalar only.
func parseScalarValue(key string, v any) (Value, error) {
	if isListLike(v) {
		return nil, fmt.Errorf("characteristics: key %q is a list; job/queue characteristics must be scalar (list filter syntax is not supported yet)", key)
	}
	val, ok := classifyScalar(v)
	if !ok {
		return nil, fmt.Errorf("characteristics: key %q has unsupported value type %T (expected string, int, or bool)", key, v)
	}
	return val, nil
}

// parseWorkerValue validates a worker custom characteristic value: scalar or
// a homogeneous list.
func parseWorkerValue(key string, v any) (Value, error) {
	if isListLike(v) {
		return classifyList(key, v)
	}
	val, ok := classifyScalar(v)
	if !ok {
		return nil, fmt.Errorf("characteristics: worker custom key %q has unsupported value type %T (expected string, int, bool, or a homogeneous list of those)", key, v)
	}
	return val, nil
}

// isListLike reports whether v is a slice or array (and therefore a
// candidate for list-value handling / list rejection), as opposed to a
// scalar.
func isListLike(v any) bool {
	if v == nil {
		return false
	}
	k := reflect.TypeOf(v).Kind()
	return k == reflect.Slice || k == reflect.Array
}

// classifyScalar converts a Go value of a supported scalar type into a
// Value. Floating point types are intentionally unsupported (ambiguous
// intent between int and a rounded value); ok is false for any unsupported
// or non-scalar type.
func classifyScalar(v any) (Value, bool) {
	switch t := v.(type) {
	case string:
		return StringValue(t), true
	case bool:
		return BoolValue(t), true
	case int:
		return IntValue(int64(t)), true
	case int8:
		return IntValue(int64(t)), true
	case int16:
		return IntValue(int64(t)), true
	case int32:
		return IntValue(int64(t)), true
	case int64:
		return IntValue(t), true
	case uint:
		if uint64(t) > math.MaxInt64 {
			return nil, false
		}
		return IntValue(int64(t)), true
	case uint8:
		return IntValue(int64(t)), true
	case uint16:
		return IntValue(int64(t)), true
	case uint32:
		return IntValue(int64(t)), true
	case uint64:
		if t > math.MaxInt64 {
			return nil, false
		}
		return IntValue(int64(t)), true
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return nil, false
		}
		return IntValue(i), true
	default:
		return nil, false
	}
}

// classifyList converts a Go slice/array value into a homogeneous list
// Value, rejecting empty untyped lists (type cannot be inferred) and mixed
// element types.
func classifyList(key string, v any) (Value, error) {
	switch t := v.(type) {
	case []string:
		out := make([]string, len(t))
		copy(out, t)
		return StringListValue(out), nil
	case []int64:
		out := make([]int64, len(t))
		copy(out, t)
		return IntListValue(out), nil
	case []bool:
		out := make([]bool, len(t))
		copy(out, t)
		return BoolListValue(out), nil
	case []any:
		return classifyDynamicList(key, t)
	default:
		rv := reflect.ValueOf(v)
		elems := make([]any, rv.Len())
		for i := range elems {
			elems[i] = rv.Index(i).Interface()
		}
		return classifyDynamicList(key, elems)
	}
}

// classifyDynamicList classifies a list of dynamically-typed elements
// (e.g. from a generic []any decode), requiring every element to classify
// to the same scalar Kind.
func classifyDynamicList(key string, elems []any) (Value, error) {
	if len(elems) == 0 {
		return nil, fmt.Errorf("characteristics: list value for key %q is empty; its element type cannot be inferred", key)
	}
	first, ok := classifyScalar(elems[0])
	if !ok {
		return nil, fmt.Errorf("characteristics: list value for key %q has unsupported element type %T", key, elems[0])
	}

	switch first.Kind() {
	case KindString:
		out := make([]string, len(elems))
		for i, e := range elems {
			sv, ok := classifyScalar(e)
			if !ok || sv.Kind() != KindString {
				return nil, fmt.Errorf("characteristics: list value for key %q is not homogeneous (mixed types)", key)
			}
			out[i] = string(sv.(StringValue))
		}
		return StringListValue(out), nil
	case KindInt:
		out := make([]int64, len(elems))
		for i, e := range elems {
			sv, ok := classifyScalar(e)
			if !ok || sv.Kind() != KindInt {
				return nil, fmt.Errorf("characteristics: list value for key %q is not homogeneous (mixed types)", key)
			}
			out[i] = int64(sv.(IntValue))
		}
		return IntListValue(out), nil
	case KindBool:
		out := make([]bool, len(elems))
		for i, e := range elems {
			sv, ok := classifyScalar(e)
			if !ok || sv.Kind() != KindBool {
				return nil, fmt.Errorf("characteristics: list value for key %q is not homogeneous (mixed types)", key)
			}
			out[i] = bool(sv.(BoolValue))
		}
		return BoolListValue(out), nil
	default:
		// Unreachable: classifyScalar never returns a list Kind.
		return nil, fmt.Errorf("characteristics: list value for key %q has unsupported element kind %s", key, first.Kind())
	}
}
