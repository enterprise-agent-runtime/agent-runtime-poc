package jcs

import (
	"math"
	"testing"
)

// TestRFC8785Vectors uses the examples of RFC 8785 (§3.2.2 number table,
// §3.2.3 sorting example, Appendix B) plus the design's vectors (A04 §6.3).
func TestRFC8785Vectors(t *testing.T) {
	cases := []struct{ in, want string }{
		// RFC 8785 §3.2.2: structure, whitespace removal, literals.
		{`{ "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
		    "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
		    "literals": [null, true, false] }`,
			`{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`},
		// RFC 8785 §3.2.3: sorting by UTF-16 code units.
		{`{"\u20ac": "Euro Sign", "\r": "Carriage Return", "\ufb33": "Hebrew Letter Dalet With Dagesh",
		   "1": "One", "\ud83d\ude00": "Emoji: Grinning Face", "\u0080": "Control", "\u00f6": "Latin Small Letter O With Diaeresis"}`,
			// Escapes, not literals: editors may decompose U+FB33 into two code points.
			"{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\",\"\u20ac\":\"Euro Sign\",\"\U0001F600\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}"},
		// Design A04 vector (store-spec §3.5).
		{`{"b":0.061,"a":1e21,"c":100,"d":-0,"e":1.5e-7}`, `{"a":1e+21,"b":0.061,"c":100,"d":0,"e":1.5e-7}`},
		{`{"tool":"proc","operation":"exec","resource_pattern":"profile:install"}`, `{"operation":"exec","resource_pattern":"profile:install","tool":"proc"}`},
		{`[]`, `[]`},
		{`{}`, `{}`},
		{`"<a>&"`, `"<a>&"`},
		{`{"a":{"z":1,"y":[{"b":2,"a":1}]}}`, `{"a":{"y":[{"a":1,"b":2}],"z":1}}`},
	}
	for _, c := range cases {
		got, err := Transform([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("\n in  %s\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// TestNumber_ECMAScript checks the number serialization table of RFC 8785
// Appendix B (IEEE-754 values and their expected ES6 strings).
func TestNumber_ECMAScript(t *testing.T) {
	cases := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, c := range cases {
		got, err := Number(math.Float64frombits(c.bits))
		if err != nil || got != c.want {
			t.Errorf("%016x: got %q %v, want %q", c.bits, got, err, c.want)
		}
	}
	if _, err := Number(math.NaN()); err == nil {
		t.Error("NaN accepted")
	}
	if _, err := Number(math.Inf(1)); err == nil {
		t.Error("Inf accepted")
	}
}

func TestTransform_Rejects(t *testing.T) {
	for _, in := range []string{`{"a":1} {}`, `{"a":`, `{"a":1e999}`} {
		if _, err := Transform([]byte(in)); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestTransform_Idempotent(t *testing.T) {
	once, _ := Transform([]byte(`{"z":[1.0,2.50,"x"],"a":null}`))
	twice, _ := Transform(once)
	if string(once) != string(twice) {
		t.Fatalf("%s != %s", once, twice)
	}
	m, err := Marshal(map[string]any{"b": 1, "a": "x"})
	if err != nil || string(m) != `{"a":"x","b":1}` {
		t.Fatalf("Marshal = %s %v", m, err)
	}
}
