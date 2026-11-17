package chat

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestCanonicalizeClientContext(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"keys sorted at every depth", `{"b":1,"a":{"d":2,"c":[{"z":1,"y":2}]}}`, `{"a":{"c":[{"y":2,"z":1}],"d":2},"b":1}`},
		{"no insignificant whitespace", "{ \"a\" : [ 1 , 2 ] }\n", `{"a":[1,2]}`},
		{"leading whitespace", " \t\r\n{\"a\":1}", `{"a":1}`},
		{"no HTML escaping", `{"h":"<b>&amp;</b>"}`, `{"h":"<b>&amp;</b>"}`},
		{"number literals preserved", `{"a":1.0,"b":1e3,"c":123456789012345678901234567890,"d":-0}`, `{"a":1.0,"b":1e3,"c":123456789012345678901234567890,"d":-0}`},
		{"U+2028 escaped", "{\"s\":\"a b\"}", `{"s":"a\u2028b"}`},
		{"duplicate keys: last wins (top)", `{"a":1,"a":2}`, `{"a":2}`},
		{"duplicate keys: last wins (nested)", `{"o":{"k":"x","k":"y"}}`, `{"o":{"k":"y"}}`},
		{"duplicate object keys: whole last value", `{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`},
		{"lone surrogate becomes U+FFFD", `{"s":"\ud800"}`, "{\"s\":\"�\"}"},
		{"invalid UTF-8 becomes U+FFFD", "{\"s\":\"\xff\"}", "{\"s\":\"�\"}"},
		{"empty object", `{}`, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalizeClientContext(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
			// Idempotent: canonicalizing the canonical form changes nothing.
			again, err := CanonicalizeClientContext(json.RawMessage(got))
			if err != nil || again != got {
				t.Fatalf("not idempotent: %q, %v", again, err)
			}
		})
	}
}

func TestCanonicalizeClientContext_NotObject(t *testing.T) {
	for _, in := range []string{`[]`, `[1]`, `"s"`, `42`, `true`, `null`, ``} {
		_, err := CanonicalizeClientContext(json.RawMessage(in))
		if !errors.Is(err, types.ErrClientContextInvalid) || err.Error() != "invalid client_context: must be a JSON object" {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}

func TestCanonicalizeClientContext_NUL(t *testing.T) {
	for _, in := range []string{`{"a\u0000":1}`, `{"a":"x\u0000"}`, `{"a":["\u0000"]}`, `{"a":{"b\u0000":true}}`} {
		_, err := CanonicalizeClientContext(json.RawMessage(in))
		if !errors.Is(err, types.ErrClientContextInvalid) || err.Error() != `invalid client_context: contains \u0000` {
			t.Errorf("%s: err = %v", in, err)
		}
	}
}

func TestRenderClientContext(t *testing.T) {
	if got := RenderClientContext(`{"a":"<x>"}`); got != `<client_context>{"a":"<x>"}</client_context>` {
		t.Fatalf("got %q", got)
	}
}
