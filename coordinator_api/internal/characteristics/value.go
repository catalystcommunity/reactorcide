// Package characteristics implements the pure logic for reactorcide's
// characteristic model: parsing/validation, canonicalization, hashing, and
// the worker-satisfies-queue matching predicate described in). This package
// does no I/O (no DB, no network) and holds no secrets.
package characteristics

import "fmt"

// Kind identifies the concrete Go type carried by a Value. It is also used
// as the wire type tag for JSON encoding and as part of the canonical byte
// encoding used for hashing, so a value of one Kind never compares equal to
// (or hashes the same as) a value of another Kind, even when their textual
// representations coincide (e.g. the string "1" versus the int 1).
type Kind string

const (
	KindString     Kind = "string"
	KindInt        Kind = "int"
	KindBool       Kind = "bool"
	KindStringList Kind = "string_list"
	KindIntList    Kind = "int_list"
	KindBoolList   Kind = "bool_list"
)

// Value is a single characteristic value: a scalar string/int64/bool, or (on
// workers only) a homogeneous list of one of those. It is a closed set — the
// only implementations are the Value* types declared in this file — so a
// type switch or type assertion against the concrete types below is
// exhaustive.
//
// Value round-trips through JSON via Characteristics' MarshalJSON/
// UnmarshalJSON (see wire.go), which tag each value with its Kind so decoding
// never has to guess between an int and a numeric string, or a list and a
// scalar. The same closed, explicitly-Kinded shape is intended to translate
// directly onto a CSIL/CBOR wire type in a later task without redesign.
type Value interface {
	// Kind reports the concrete type of the value.
	Kind() Kind
	// IsList reports whether the value is a list (always false for job/queue
	// characteristics, which are scalar-only by construction).
	IsList() bool
	// Matches reports whether this value (typically a worker's) satisfies a
	// scalar characteristic value (typically a queue's): true if the
	// receiver is an equal scalar of the same Kind, or a list of the same
	// element Kind that contains other. Matching is always strict on type —
	// a string never matches an int or bool of the "same" literal text.
	Matches(other Value) bool
	// String renders a debug/log-friendly "<kind>:<value>" form. It is not
	// used for hashing or canonicalization (see Canonicalize/Hash).
	String() string

	// isValue closes the interface to the types declared in this file.
	isValue()
}

// StringValue is a scalar string characteristic value.
type StringValue string

func (StringValue) Kind() Kind   { return KindString }
func (StringValue) IsList() bool { return false }
func (StringValue) isValue()     {}
func (v StringValue) String() string {
	return fmt.Sprintf("string:%s", string(v))
}
func (v StringValue) Matches(other Value) bool {
	o, ok := other.(StringValue)
	return ok && v == o
}

// IntValue is a scalar int64 characteristic value.
type IntValue int64

func (IntValue) Kind() Kind   { return KindInt }
func (IntValue) IsList() bool { return false }
func (IntValue) isValue()     {}
func (v IntValue) String() string {
	return fmt.Sprintf("int:%d", int64(v))
}
func (v IntValue) Matches(other Value) bool {
	o, ok := other.(IntValue)
	return ok && v == o
}

// BoolValue is a scalar bool characteristic value.
type BoolValue bool

func (BoolValue) Kind() Kind   { return KindBool }
func (BoolValue) IsList() bool { return false }
func (BoolValue) isValue()     {}
func (v BoolValue) String() string {
	return fmt.Sprintf("bool:%t", bool(v))
}
func (v BoolValue) Matches(other Value) bool {
	o, ok := other.(BoolValue)
	return ok && v == o
}

// StringListValue is a homogeneous list of strings. Worker-only.
type StringListValue []string

func (StringListValue) Kind() Kind   { return KindStringList }
func (StringListValue) IsList() bool { return true }
func (StringListValue) isValue()     {}
func (v StringListValue) String() string {
	return fmt.Sprintf("string_list:%v", []string(v))
}
func (v StringListValue) Matches(other Value) bool {
	o, ok := other.(StringValue)
	if !ok {
		return false
	}
	for _, s := range v {
		if s == string(o) {
			return true
		}
	}
	return false
}

// IntListValue is a homogeneous list of int64s. Worker-only.
type IntListValue []int64

func (IntListValue) Kind() Kind   { return KindIntList }
func (IntListValue) IsList() bool { return true }
func (IntListValue) isValue()     {}
func (v IntListValue) String() string {
	return fmt.Sprintf("int_list:%v", []int64(v))
}
func (v IntListValue) Matches(other Value) bool {
	o, ok := other.(IntValue)
	if !ok {
		return false
	}
	for _, i := range v {
		if IntValue(i) == o {
			return true
		}
	}
	return false
}

// BoolListValue is a homogeneous list of bools. Worker-only.
type BoolListValue []bool

func (BoolListValue) Kind() Kind   { return KindBoolList }
func (BoolListValue) IsList() bool { return true }
func (BoolListValue) isValue()     {}
func (v BoolListValue) String() string {
	return fmt.Sprintf("bool_list:%v", []bool(v))
}
func (v BoolListValue) Matches(other Value) bool {
	o, ok := other.(BoolValue)
	if !ok {
		return false
	}
	for _, b := range v {
		if BoolValue(b) == o {
			return true
		}
	}
	return false
}

// KV is one worker custom characteristic key/value pair as supplied by a
// caller (e.g. worker config or auto-detection) before parsing. Value must
// be a Go string/int-family/bool, or a slice of one of those (homogeneous);
// see ParseWorkerCharacteristics.
type KV struct {
	Key   string
	Value any
}
