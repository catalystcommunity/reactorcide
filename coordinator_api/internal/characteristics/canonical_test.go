package characteristics

import (
	"bytes"
	"testing"
)

func TestCanonicalize_OrderIndependent(t *testing.T) {
	a := Characteristics{"os": StringValue("linux"), "arch": StringValue("amd64")}
	b := Characteristics{"arch": StringValue("amd64"), "os": StringValue("linux")}

	ca := Canonicalize(a)
	cb := Canonicalize(b)
	if !bytes.Equal(ca, cb) {
		t.Fatalf("canonical bytes differ by construction order:\n a=%x\n b=%x", ca, cb)
	}
	if CanonicalString(a) != CanonicalString(b) {
		t.Fatalf("canonical strings differ by construction order: %q vs %q", CanonicalString(a), CanonicalString(b))
	}
}

func TestCanonicalize_DifferentInputsDiffer(t *testing.T) {
	base := Characteristics{"os": StringValue("linux")}
	cases := map[string]Characteristics{
		"different os value": {"os": StringValue("windows")},
		"extra key":          {"os": StringValue("linux"), "arch": StringValue("amd64")},
		"different key":      {"platform": StringValue("linux")},
		"missing key":        {},
	}
	baseBytes := Canonicalize(base)
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(baseBytes, Canonicalize(c)) {
				t.Fatalf("expected differing canonical bytes for %s", name)
			}
		})
	}
}

func TestHash_StableAndOrderIndependent(t *testing.T) {
	a := Characteristics{"os": StringValue("linux"), "arch": StringValue("amd64")}
	b := Characteristics{"arch": StringValue("amd64"), "os": StringValue("linux")}

	h1 := Hash(a)
	h2 := Hash(a)
	h3 := Hash(b)
	if h1 != h2 {
		t.Fatalf("hash not stable across repeated calls: %s vs %s", h1, h2)
	}
	if h1 != h3 {
		t.Fatalf("hash not order-independent: %s vs %s", h1, h3)
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64-char hex sha256, got %d chars: %s", len(h1), h1)
	}
}

func TestHash_TypeSensitive(t *testing.T) {
	stringOne := Characteristics{"v": StringValue("1")}
	intOne := Characteristics{"v": IntValue(1)}
	boolTrue := Characteristics{"v": BoolValue(true)}
	// bool true classically encodes as 1 in many languages; make sure it
	// doesn't collide with IntValue(1) either.
	intOneAgain := Characteristics{"v": IntValue(1)}

	hStr := Hash(stringOne)
	hInt := Hash(intOne)
	hBool := Hash(boolTrue)

	if hStr == hInt {
		t.Fatalf("string \"1\" and int 1 must not hash the same: %s", hStr)
	}
	if hInt == hBool {
		t.Fatalf("int 1 and bool true must not hash the same: %s", hInt)
	}
	if hStr == hBool {
		t.Fatalf("string \"1\" and bool true must not hash the same: %s", hStr)
	}
	if Hash(intOneAgain) != hInt {
		t.Fatalf("identical int characteristics must hash identically")
	}
}

func TestHash_ScalarVsListDoNotCollide(t *testing.T) {
	scalar := Characteristics{"zone": StringValue("a")}
	list := Characteristics{"zone": StringListValue{"a"}}
	if Hash(scalar) == Hash(list) {
		t.Fatalf("scalar and single-element list characteristics must not hash the same")
	}
}

func TestHash_ListOrderMatters(t *testing.T) {
	// The canonical encoding preserves list element order as supplied; two
	// lists with the same elements in different order are different values.
	a := Characteristics{"zones": StringListValue{"a", "b"}}
	b := Characteristics{"zones": StringListValue{"b", "a"}}
	if Hash(a) == Hash(b) {
		t.Fatalf("expected different hashes for differently-ordered lists")
	}
}

func TestCharacteristicsJSON_RoundTrip(t *testing.T) {
	orig := Characteristics{
		"os":         StringValue("linux"),
		"arch":       StringValue("amd64"),
		"replicas":   IntValue(3),
		"gpu":        BoolValue(true),
		"zones":      StringListValue{"a", "b"},
		"priorities": IntListValue{1, 2, 3},
		"flags":      BoolListValue{true, false},
	}

	data, err := orig.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var round Characteristics
	if err := round.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if Hash(round) != Hash(orig) {
		t.Fatalf("round-tripped characteristics hash differs: got %s want %s\ndata=%s", Hash(round), Hash(orig), data)
	}
	assertCharacteristicsEqual(t, round, orig)
}

func TestCharacteristicsJSON_TypeDistinguishesStringVsIntVsBool(t *testing.T) {
	// A string "1", an int 1, and a bool true must produce distinguishable
	// JSON (never silently collapse to the same wire value).
	s := Characteristics{"v": StringValue("1")}
	i := Characteristics{"v": IntValue(1)}
	b := Characteristics{"v": BoolValue(true)}

	sd, _ := s.MarshalJSON()
	id, _ := i.MarshalJSON()
	bd, _ := b.MarshalJSON()

	if bytes.Equal(sd, id) || bytes.Equal(id, bd) || bytes.Equal(sd, bd) {
		t.Fatalf("expected distinct JSON encodings: string=%s int=%s bool=%s", sd, id, bd)
	}

	var rs, ri, rb Characteristics
	if err := rs.UnmarshalJSON(sd); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if err := ri.UnmarshalJSON(id); err != nil {
		t.Fatalf("unmarshal int: %v", err)
	}
	if err := rb.UnmarshalJSON(bd); err != nil {
		t.Fatalf("unmarshal bool: %v", err)
	}
	if _, ok := rs["v"].(StringValue); !ok {
		t.Fatalf("expected StringValue after round trip, got %T", rs["v"])
	}
	if _, ok := ri["v"].(IntValue); !ok {
		t.Fatalf("expected IntValue after round trip, got %T", ri["v"])
	}
	if _, ok := rb["v"].(BoolValue); !ok {
		t.Fatalf("expected BoolValue after round trip, got %T", rb["v"])
	}
}
