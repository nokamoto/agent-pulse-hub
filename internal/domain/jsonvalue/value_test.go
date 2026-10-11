package jsonvalue

import (
	"bytes"
	"testing"
)

func TestParseRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
	}{
		{name: "duplicate key", data: []byte(`{"x":1,"x":2}`)},
		{name: "nested duplicate key", data: []byte(`{"x":{"y":1,"y":2}}`)},
		{name: "escaped duplicate key", data: []byte(`{"x":1,"\u0078":2}`)},
		{name: "unpaired high surrogate", data: []byte(`"\ud800"`)},
		{name: "unpaired low surrogate", data: []byte(`"\udc00"`)},
		{name: "invalid UTF-8", data: []byte{'"', 0xff, '"'}},
		{name: "leading zero", data: []byte(`01`)},
		{name: "empty fraction", data: []byte(`1.`)},
		{name: "empty exponent", data: []byte(`1e`)},
		{name: "trailing data", data: []byte(`true false`)},
		{name: "unescaped control", data: []byte{'"', '\n', '"'}},
		{name: "BOM", data: []byte("\xef\xbb\xbf{}")},
		{name: "trailing array comma", data: []byte(`[1,]`)},
		{name: "trailing object comma", data: []byte(`{"a":1,}`)},
		{name: "invalid surrogate pair", data: []byte(`"\ud800\u0041"`)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(test.data, 0); err == nil {
				t.Fatal("Parse accepted invalid JSON")
			}
		})
	}
}

func TestCanonicalPreservesExactDecimalDistinctions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		equivalent  bool
	}{
		{left: `9007199254740992`, right: `9007199254740993`},
		{left: `1e100000000000000000000`, right: `10e99999999999999999999`, equivalent: true},
		{left: `1.00000000000000000001`, right: `1`},
		{left: `-0e99999999999999999999`, right: `0`, equivalent: true},
	} {
		left, err := Parse([]byte(test.left), 0)
		if err != nil {
			t.Fatal(err)
		}
		right, err := Parse([]byte(test.right), 0)
		if err != nil {
			t.Fatal(err)
		}
		if left.Equivalent(right) != test.equivalent {
			t.Errorf("%s and %s equivalence", test.left, test.right)
		}
	}
}

func TestParseEnforcesMaximumSize(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(`"long"`), 5); err == nil {
		t.Fatal("Parse accepted input above the maximum")
	}
}

func TestCanonicalIgnoresObjectOrderAndExactNumberSpelling(t *testing.T) {
	t.Parallel()
	first, err := Parse([]byte(`{"z":[1.00,{"n":-0.0}],"a":1e2}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse([]byte(`{"a":100,"z":[1,{"n":0}]}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equivalent(second) {
		t.Fatalf("canonical mismatch:\n%s\n%s", first.Canonical(), second.Canonical())
	}
}

func TestCanonicalPreservesArrayOrder(t *testing.T) {
	t.Parallel()
	first, _ := Parse([]byte(`[1,2]`), 0)
	second, _ := Parse([]byte(`[2,1]`), 0)
	if first.Equivalent(second) {
		t.Fatal("canonical comparison ignored array order")
	}
}

func TestMarshalPreservesExactNumberTokens(t *testing.T) {
	t.Parallel()
	value, err := Parse([]byte(`{"n":123456789012345678901234567890.0001,"s":"line\n\u2028"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := value.Marshal()
	want := []byte(`{"n":123456789012345678901234567890.0001,"s":"line\n` + "\u2028" + `"}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
	if _, err := Parse(got, 0); err != nil {
		t.Fatalf("Marshal() produced invalid JSON: %v", err)
	}
}

func TestUnicodeSurrogatePair(t *testing.T) {
	t.Parallel()
	value, err := Parse([]byte(`"\ud83d\ude80"`), 0)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := ParseString(value)
	if !ok || text != "🚀" {
		t.Fatalf("ParseString() = %q, %v", text, ok)
	}
}
