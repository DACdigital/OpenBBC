package seed

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// placeholderRe matches, leftmost-first, the escape `$${`, a placeholder
// `${NAME}`, or any other `${` — the last one is malformed.
var placeholderRe = regexp.MustCompile(`\$\$\{|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$\{`)

const malformedPlaceholder = "${"

// malformedMsg is the problem text for a `${` that is neither ${NAME} nor
// escaped; it does not echo the value (it may hold a secret).
const malformedMsg = "malformed placeholder: every ${ must be ${NAME} (NAME = [A-Za-z_][A-Za-z0-9_]*) or escaped as $${"

// Interpolate substitutes ${VAR} in every string leaf under each
// Backend.Config (recursively). `$${` becomes a literal `${`. Nothing outside
// backend configs is touched. Missing variables and malformed placeholders
// are reported together.
func Interpolate(s *Set, lookup func(string) (string, bool)) error {
	var errs []error
	for i := range s.Backends {
		b := &s.Backends[i]
		for _, k := range sortedKeys(b.Config) {
			malformed := false
			b.Config[k] = interpValue(b.Config[k], func(str string) string {
				return placeholderRe.ReplaceAllStringFunc(str, func(m string) string {
					if m == "$${" {
						return "${"
					}
					if m == malformedPlaceholder {
						malformed = true
						return m
					}
					name := m[2 : len(m)-1]
					val, ok := lookup(name)
					if !ok {
						errs = append(errs, fmt.Errorf("%s: backend %s: missing environment variable %s", b.Source, b.Name, name))
						return m
					}
					return val
				})
			})
			if malformed {
				errs = append(errs, fmt.Errorf("%s: backend %s: config key %s: %s", b.Source, b.Name, k, malformedMsg))
			}
		}
	}
	return errors.Join(errs...)
}

// malformedPlaceholderErrors reports, per backend config key, a `${` that is
// not a valid placeholder or escape. Validate runs it on the raw file, so
// `seed validate --no-env` catches it without the environment.
func malformedPlaceholderErrors(s *Set) []error {
	var errs []error
	for _, b := range s.Backends {
		for _, k := range sortedKeys(b.Config) {
			malformed := false
			interpValue(b.Config[k], func(str string) string {
				for _, m := range placeholderRe.FindAllString(str, -1) {
					if m == malformedPlaceholder {
						malformed = true
					}
				}
				return str
			})
			if malformed {
				errs = append(errs, fmt.Errorf("%s: backend %s: config key %s: %s", b.Source, b.Name, k, malformedMsg))
			}
		}
	}
	return errs
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func interpValue(v any, f func(string) string) any {
	switch t := v.(type) {
	case string:
		return f(t)
	case map[string]any:
		for k, val := range t {
			t[k] = interpValue(val, f)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = interpValue(val, f)
		}
		return t
	}
	return v
}

// RequiredEnv returns the sorted, unique variable names referenced by
// ${VAR} placeholders under backend configs (escaped `$${` excluded).
func RequiredEnv(s *Set) []string {
	seen := map[string]bool{}
	for _, b := range s.Backends {
		for _, v := range b.Config {
			interpValue(v, func(str string) string {
				for _, m := range placeholderRe.FindAllStringSubmatch(str, -1) {
					if m[1] != "" {
						seen[m[1]] = true
					}
				}
				return str
			})
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
