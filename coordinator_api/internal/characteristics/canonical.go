package characteristics

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// canonicalFormatVersion is the leading byte of Canonicalize's output. Bump
// it (and accept that Hash values change) if the encoding below ever changes
// in a way that could alter output for existing inputs.
const canonicalFormatVersion = 1

// Canonicalize produces a deterministic byte encoding of c: keys are sorted,
// every key and value is length-prefixed (so no delimiter choice can create
// ambiguity between adjacent fields), and every value is tagged with its
// Kind (so a StringValue("1") and an IntValue(1) never produce the same
// bytes). Two Characteristics maps that are equal as Go maps (ignoring key
// order) always produce identical output; maps that differ in any key,
// value, or value type never do. This is the input to Hash.
func Canonicalize(c Characteristics) []byte {
	keys := sortedKeys(c)

	var buf []byte
	buf = append(buf, canonicalFormatVersion)
	buf = appendUvarint(buf, uint64(len(keys)))
	for _, k := range keys {
		buf = appendLenPrefixed(buf, []byte(k))
		buf = appendValue(buf, c[k])
	}
	return buf
}

// CanonicalString renders c as a deterministic, human-readable,
// order-independent string ("arch=string:amd64,os=string:linux") for
// logging/debugging/tests. It is not used by Hash — Canonicalize's binary
// form is — so it does not need to be unambiguous against adversarial key or
// string-value content.
func CanonicalString(c Characteristics) string {
	keys := sortedKeys(c)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, c[k].String()))
	}
	return strings.Join(parts, ",")
}

// Hash returns a stable, type-sensitive hex-encoded SHA-256 hash of c's
// canonical encoding. It is suitable as a DB unique key for find-or-create
// queue lookups: two logically-equal Characteristics sets always hash
// identically regardless of construction order; sets that differ in any key,
// value, or value type (including scalar-vs-list) never collide by
// construction of Canonicalize.
func Hash(c Characteristics) string {
	sum := sha256.Sum256(Canonicalize(c))
	return hex.EncodeToString(sum[:])
}

func sortedKeys(c Characteristics) []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func appendUvarint(buf []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(buf, tmp[:n]...)
}

func appendLenPrefixed(buf []byte, b []byte) []byte {
	buf = appendUvarint(buf, uint64(len(b)))
	return append(buf, b...)
}

func appendInt64(buf []byte, v int64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(v))
	return append(buf, tmp[:]...)
}

func appendBool(buf []byte, v bool) []byte {
	if v {
		return append(buf, 1)
	}
	return append(buf, 0)
}

// appendValue writes a self-delimiting, Kind-tagged encoding of v: the Kind
// string (length-prefixed, so it can never collide with the value bytes that
// follow), then the value payload.
func appendValue(buf []byte, v Value) []byte {
	buf = appendLenPrefixed(buf, []byte(v.Kind()))
	switch t := v.(type) {
	case StringValue:
		buf = appendLenPrefixed(buf, []byte(t))
	case IntValue:
		buf = appendInt64(buf, int64(t))
	case BoolValue:
		buf = appendBool(buf, bool(t))
	case StringListValue:
		buf = appendUvarint(buf, uint64(len(t)))
		for _, s := range t {
			buf = appendLenPrefixed(buf, []byte(s))
		}
	case IntListValue:
		buf = appendUvarint(buf, uint64(len(t)))
		for _, i := range t {
			buf = appendInt64(buf, i)
		}
	case BoolListValue:
		buf = appendUvarint(buf, uint64(len(t)))
		for _, b := range t {
			buf = appendBool(buf, b)
		}
	default:
		// Unreachable for the closed Value set defined in this package.
		panic(fmt.Sprintf("characteristics: unsupported Value implementation %T", v))
	}
	return buf
}
