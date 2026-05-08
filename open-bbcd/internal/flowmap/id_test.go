package flowmap

import (
	"strings"
	"testing"
)

func TestSlugifySkillName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Place Order", "place-order"},
		{"  Trim me  ", "trim-me"},
		{"Order #42!", "order-42"},
		{"Send_email_alert", "send-email-alert"},
		{"camelCase ID", "camelcase-id"},
		{"Multiple   spaces", "multiple-spaces"},
		{"---hyphens---", "hyphens"},
		{"żółć", ""}, // non-ASCII collapses; caller guards empty
	}
	for _, tc := range tests {
		got := SlugifySkillName(tc.in)
		if got != tc.want {
			t.Errorf("SlugifySkillName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUniqueSkillID_NoCollision(t *testing.T) {
	taken := map[string]struct{}{"foo": {}, "bar": {}}
	got := UniqueSkillID("baz", taken)
	if got != "baz" {
		t.Errorf("UniqueSkillID = %q, want baz", got)
	}
}

func TestUniqueSkillID_CollisionAppendsDiscriminator(t *testing.T) {
	taken := map[string]struct{}{"foo": {}}
	got := UniqueSkillID("foo", taken)
	if !strings.HasPrefix(got, "foo-") || len(got) != len("foo-")+4 {
		t.Errorf("UniqueSkillID = %q, want foo-<4-hex-chars>", got)
	}
	if _, exists := taken[got]; exists {
		t.Errorf("UniqueSkillID returned an already-taken id: %q", got)
	}
}
