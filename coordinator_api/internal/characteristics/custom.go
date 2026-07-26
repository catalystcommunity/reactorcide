package characteristics

import (
	"fmt"
	"strconv"
	"strings"
)

// yamlBools is the exact set of tokens ParseCustomValue recognizes as a YAML
// 1.2 core-schema boolean: true/True/TRUE and false/False/FALSE only.
// Deliberately NOT included: yes/no/on/off (YAML 1.1-only booleans) -- those
// are treated as plain strings here, since accepting them would make an
// operator's "zone=on" or "zone=no" silently become a bool instead of the
// string they almost certainly meant.
var yamlBools = map[string]bool{
	"true":  true,
	"True":  true,
	"TRUE":  true,
	"false": false,
	"False": false,
	"FALSE": false,
}

// ParseCustomValue parses one raw operator-supplied custom-characteristic
// value (from a `--custom key=value` flag or a
// REACTORCIDE_WORKER_CUSTOM_<KEY> env var) into a typed characteristics.Value,
// inferring int/bool/string and scalar-vs-list purely from the text. It is
// pure (no I/O) and does not know the characteristic's key -- callers should
// prepend the key to any returned error for context.
//
// Algorithm:
//  1. Split raw on commas, honoring backslash-escaping: "\," is a literal
//     comma, "\\" is a literal backslash (both only take effect when
//     immediately preceded by an unescaped backslash; splitting itself
//     happens on unescaped commas only). Each resulting element is then
//     unescaped and trimmed of surrounding ASCII whitespace.
//  2. An empty raw value (empty or all whitespace), or any element that is
//     empty after unescaping/trimming, is a loud error.
//  3. The type is inferred from the FIRST element only:
//     - If it parses as a base-10 int64, every element must; a single
//     element yields IntValue, more than one yields IntListValue.
//     - Else if it is a recognized YAML bool token (true/True/TRUE/
//     false/False/FALSE -- see yamlBools; NOT yes/no/on/off), every
//     element must be one too; a single element yields BoolValue, more
//     than one yields BoolListValue.
//     - Else every element is treated as a string (this always succeeds);
//     a single element yields StringValue, more than one yields
//     StringListValue.
//     A mismatch against the first element's inferred type (case 1 or 2)
//     is a loud error naming the offending element and explaining that
//     mixed types are not allowed.
func ParseCustomValue(raw string) (Value, error) {
	if trimASCIISpace(raw) == "" {
		return nil, fmt.Errorf("characteristics: custom value is empty")
	}

	rawElems := splitUnescapedCommas(raw)
	elems := make([]string, len(rawElems))
	for i, e := range rawElems {
		elem := trimASCIISpace(unescapeElement(e))
		if elem == "" {
			return nil, fmt.Errorf(
				"characteristics: custom value %q has an empty element at position %d (after splitting on commas, unescaping, and trimming whitespace); remove the extra comma or the blank entry",
				raw, i+1,
			)
		}
		elems[i] = elem
	}

	first := elems[0]

	if firstInt, err := strconv.ParseInt(first, 10, 64); err == nil {
		if len(elems) == 1 {
			return IntValue(firstInt), nil
		}
		ints := make([]int64, len(elems))
		ints[0] = firstInt
		for i := 1; i < len(elems); i++ {
			v, err := strconv.ParseInt(elems[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf(
					"characteristics: custom value %q: element %q at position %d is not an integer, but the first element %q is -- mixed types are not allowed in a comma-separated custom value (the first element decides the type for all of them)",
					raw, elems[i], i+1, first,
				)
			}
			ints[i] = v
		}
		return IntListValue(ints), nil
	}

	if firstBool, ok := yamlBools[first]; ok {
		if len(elems) == 1 {
			return BoolValue(firstBool), nil
		}
		bools := make([]bool, len(elems))
		bools[0] = firstBool
		for i := 1; i < len(elems); i++ {
			v, ok := yamlBools[elems[i]]
			if !ok {
				return nil, fmt.Errorf(
					"characteristics: custom value %q: element %q at position %d is not a recognized YAML boolean (true/True/TRUE/false/False/FALSE -- note yes/no/on/off are NOT recognized, they are strings), but the first element %q is -- mixed types are not allowed in a comma-separated custom value (the first element decides the type for all of them)",
					raw, elems[i], i+1, first,
				)
			}
			bools[i] = v
		}
		return BoolListValue(bools), nil
	}

	if len(elems) == 1 {
		return StringValue(elems[0]), nil
	}
	strs := make([]string, len(elems))
	copy(strs, elems)
	return StringListValue(strs), nil
}

// splitUnescapedCommas splits raw on commas that are not immediately
// preceded by an escaping backslash, leaving the escape sequences ("\," and
// "\\") intact in the returned elements for unescapeElement to resolve
// afterward. This two-pass approach (split first, unescape second) is what
// lets "\," survive as a literal comma inside one element instead of being
// treated as a delimiter.
func splitUnescapedCommas(raw string) []string {
	var elems []string
	var cur strings.Builder
	runes := []rune(raw)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\\' && i+1 < len(runes) && (runes[i+1] == ',' || runes[i+1] == '\\') {
			cur.WriteRune(c)
			cur.WriteRune(runes[i+1])
			i++
			continue
		}
		if c == ',' {
			elems = append(elems, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	elems = append(elems, cur.String())
	return elems
}

// unescapeElement resolves the two recognized escape sequences within a
// single already-split element: "\," becomes "," and "\\" becomes "\". Any
// other backslash (not followed by a comma or another backslash) is left
// exactly as-is -- only these two sequences carry special meaning.
func unescapeElement(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\\' && i+1 < len(runes) && (runes[i+1] == ',' || runes[i+1] == '\\') {
			b.WriteRune(runes[i+1])
			i++
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// trimASCIISpace trims leading/trailing ASCII whitespace (space, tab,
// newline, carriage return, vertical tab, form feed) only. Unlike
// strings.TrimSpace it does not treat other Unicode whitespace code points
// as trimmable, since a stray Unicode space inside an operator-typed value
// is far more likely to be an intentional (if odd) part of the value than a
// formatting artifact.
func trimASCIISpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		}
		return false
	})
}
