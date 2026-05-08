package flowmap

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"unicode"
)

// SlugifySkillName lowercases s, replaces runs of non-ASCII-alphanumeric
// characters with single hyphens, and trims leading/trailing hyphens.
// Returns "" if no usable characters remain — callers must reject that case.
func SlugifySkillName(s string) string {
	var b strings.Builder
	prevHyphen := true // suppresses leading hyphens
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if unicode.IsSpace(r) || r == '-' || r == '_' {
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
		// other runes are dropped
	}
	return strings.TrimRight(b.String(), "-")
}

// UniqueSkillID returns base if it is not in taken; otherwise appends "-<4-hex>"
// using crypto/rand. Re-rolls on the (cosmically unlikely) chance of collision.
func UniqueSkillID(base string, taken map[string]struct{}) string {
	if _, exists := taken[base]; !exists {
		return base
	}
	for attempts := 0; attempts < 8; attempts++ {
		var buf [2]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand should never fail on Linux; if it does, fall back
			// to a deterministic suffix derived from base length.
			return base + "-" + hex.EncodeToString([]byte{byte(len(base)), 0xa5})
		}
		candidate := base + "-" + hex.EncodeToString(buf[:])
		if _, exists := taken[candidate]; !exists {
			return candidate
		}
	}
	// 8 collisions on a 16-bit suffix is extraordinary; surface a deterministic id.
	return base + "-conflict"
}
