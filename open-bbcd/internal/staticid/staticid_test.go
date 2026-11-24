package staticid

import (
	"errors"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		in    string
		valid bool
	}{
		{"sales", true},
		{"sales_v7", true},
		{"a.b-c", true},
		{"", false},
		{"Sales", false},
		{"-x", false},
		{strings.Repeat("a", 101), false},
		{"0123456789abcdef0123456789abcdef", false},
		{"01234567-89ab-cdef-0123-456789abcdef", false},
	}
	for _, tc := range tests {
		err := Validate(tc.in)
		if tc.valid && err != nil {
			t.Errorf("Validate(%q) = %v, want nil", tc.in, err)
		}
		if !tc.valid && !errors.Is(err, types.ErrInvalidStaticID) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidStaticID", tc.in, err)
		}
	}
}

func TestIsUUID(t *testing.T) {
	for _, s := range []string{
		"01234567-89ab-cdef-0123-456789abcdef",
		"01234567-89AB-CDEF-0123-456789ABCDEF",
		"0123456789abcdef0123456789abcdef",
	} {
		if !IsUUID(s) {
			t.Errorf("IsUUID(%q) = false, want true", s)
		}
	}
	if IsUUID("sales") {
		t.Error("IsUUID(sales) = true")
	}
}

func TestSlug(t *testing.T) {
	if got := Slug("Sales Assistant!"); got != "sales-assistant" {
		t.Errorf("Slug = %q", got)
	}
	if got := Slug("Ąę"); got != "" {
		t.Errorf("Slug(Ąę) = %q, want empty", got)
	}
	if got := AgentDefault("Ąę"); got != "agent" {
		t.Errorf("AgentDefault(Ąę) = %q", got)
	}
	if got := AgentDefault("Sales Assistant"); got != "sales-assistant" {
		t.Errorf("AgentDefault = %q", got)
	}
	if got := len(Slug(strings.Repeat("a", 200))); got != 90 {
		t.Errorf("Slug len = %d, want 90", got)
	}
}

func TestVersionDefault_Truncates(t *testing.T) {
	got := VersionDefault(strings.Repeat("a", 89), 7)
	if len(got) > 90 || !strings.HasSuffix(got, "_v7") {
		t.Errorf("VersionDefault = %q (len %d)", got, len(got))
	}
	if got := VersionDefault("sales", 3); got != "sales_v3" {
		t.Errorf("VersionDefault = %q", got)
	}
}

func TestWithSuffix(t *testing.T) {
	if got := WithSuffix("x", 2); got != "x-2" {
		t.Errorf("WithSuffix = %q", got)
	}
	if got := WithSuffix("x", 1); got != "x" {
		t.Errorf("WithSuffix(1) = %q", got)
	}
}
