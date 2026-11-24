package seed

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// placeholderRe matches either the escape `$${` or a placeholder `${NAME}`.
var placeholderRe = regexp.MustCompile(`\$\$\{|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Interpolate substitutes ${VAR} in every string leaf under each
// Backend.Config (recursively). `$${` becomes a literal `${`. Nothing outside
// backend configs is touched. Missing variables are reported together.
func Interpolate(s *Set, lookup func(string) (string, bool)) error {
	var errs []error
	for i := range s.Backends {
		b := &s.Backends[i]
		for k, v := range b.Config {
			b.Config[k] = interpValue(v, func(str string) string {
				return placeholderRe.ReplaceAllStringFunc(str, func(m string) string {
					if m == "$${" {
						return "${"
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
		}
	}
	return errors.Join(errs...)
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
