// Package staticid validates and derives operator-chosen static IDs for
// agents and agent versions (declarative-seed spec §1). The patterns here
// must stay identical to the CHECK constraints in migration 031.
package staticid

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

const maxDefaultLen = 90

var (
	validRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	uuidRe  = regexp.MustCompile(`^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$`)
	slugRe  = regexp.MustCompile(`[^a-z0-9]+`)
)

// Validate returns types.ErrInvalidStaticID (wrapped with the value) unless s
// matches the static-ID pattern and is not UUID-shaped.
func Validate(s string) error {
	if !validRe.MatchString(s) || uuidRe.MatchString(s) {
		return fmt.Errorf("%w: %q", types.ErrInvalidStaticID, s)
	}
	return nil
}

// IsUUID reports whether s is UUID-shaped (dashed or 32-hex), case-insensitive.
func IsUUID(s string) bool {
	return uuidRe.MatchString(strings.ToLower(s))
}

// Slug lower-cases name, replaces runs outside [a-z0-9] with "-", trims "-"
// from both ends and cuts to 90 chars. May return "".
func Slug(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if len(s) > maxDefaultLen {
		s = strings.Trim(s[:maxDefaultLen], "-")
	}
	return s
}

// AgentDefault is the default static ID for an agent name.
func AgentDefault(name string) string {
	s := Slug(name)
	if Validate(s) != nil {
		return "agent"
	}
	return s
}

// VersionDefault is "<agent static_id>_v<N>", with the agent part cut so the
// total is at most 90 chars.
func VersionDefault(agentStaticID string, n int) string {
	suffix := "_v" + strconv.Itoa(n)
	if keep := maxDefaultLen - len(suffix); len(agentStaticID) > keep {
		agentStaticID = agentStaticID[:keep]
	}
	return agentStaticID + suffix
}

// WithSuffix returns base for k < 2 and "base-k" otherwise (collision suffix).
func WithSuffix(base string, k int) string {
	if k < 2 {
		return base
	}
	return base + "-" + strconv.Itoa(k)
}
