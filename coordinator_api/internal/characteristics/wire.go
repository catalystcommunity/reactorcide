package characteristics

import (
	"encoding/json"
	"fmt"
)

// wireEntry is the JSON-tagged-union wire shape for one Value: Type
// discriminates which of the payload fields is meaningful, so decoding never
// has to guess a type from a bare JSON literal (which would conflate, say,
// the string "1" and the int 1). This is what makes Characteristics'
// MarshalJSON/UnmarshalJSON a lossless, type-preserving round trip.
type wireEntry struct {
	Type Kind `json:"type"`

	String     string   `json:"string,omitempty"`
	Int        int64    `json:"int,omitempty"`
	Bool       bool     `json:"bool,omitempty"`
	StringList []string `json:"string_list,omitempty"`
	IntList    []int64  `json:"int_list,omitempty"`
	BoolList   []bool   `json:"bool_list,omitempty"`
}

func toWireEntry(v Value) (wireEntry, error) {
	switch t := v.(type) {
	case StringValue:
		return wireEntry{Type: KindString, String: string(t)}, nil
	case IntValue:
		return wireEntry{Type: KindInt, Int: int64(t)}, nil
	case BoolValue:
		return wireEntry{Type: KindBool, Bool: bool(t)}, nil
	case StringListValue:
		return wireEntry{Type: KindStringList, StringList: []string(t)}, nil
	case IntListValue:
		return wireEntry{Type: KindIntList, IntList: []int64(t)}, nil
	case BoolListValue:
		return wireEntry{Type: KindBoolList, BoolList: []bool(t)}, nil
	default:
		return wireEntry{}, fmt.Errorf("characteristics: unsupported Value implementation %T", v)
	}
}

func fromWireEntry(key string, we wireEntry) (Value, error) {
	switch we.Type {
	case KindString:
		return StringValue(we.String), nil
	case KindInt:
		return IntValue(we.Int), nil
	case KindBool:
		return BoolValue(we.Bool), nil
	case KindStringList:
		return StringListValue(we.StringList), nil
	case KindIntList:
		return IntListValue(we.IntList), nil
	case KindBoolList:
		return BoolListValue(we.BoolList), nil
	default:
		return nil, fmt.Errorf("characteristics: key %q has unknown wire type %q", key, we.Type)
	}
}

// MarshalJSON implements a type-preserving JSON encoding of Characteristics:
// each entry is tagged with its Kind so decoding round-trips the exact Value
// type (see wireEntry).
func (c Characteristics) MarshalJSON() ([]byte, error) {
	out := make(map[string]wireEntry, len(c))
	for k, v := range c {
		we, err := toWireEntry(v)
		if err != nil {
			return nil, err
		}
		out[k] = we
	}
	return json.Marshal(out)
}

// UnmarshalJSON implements the inverse of MarshalJSON. It does not apply
// ParseJobCharacteristics/ParseWorkerCharacteristics validation (no "os"
// default injection, no scalar-vs-list policy) — it only reconstructs
// whatever Characteristics were previously marshaled by MarshalJSON.
func (c *Characteristics) UnmarshalJSON(data []byte) error {
	var raw map[string]wireEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	result := make(Characteristics, len(raw))
	for k, we := range raw {
		v, err := fromWireEntry(k, we)
		if err != nil {
			return err
		}
		result[k] = v
	}
	*c = result
	return nil
}
