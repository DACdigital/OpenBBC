// Package llmextras parses OPENBBC_LLM_REQUEST_EXTRAS: an ordered list of
// rules that add extra HTTP headers and top-level JSON body fields to
// outbound LLM requests for models whose name matches a glob. aikdm reads the
// same variable with the same semantics (aikdm/aikdm/request_extras.py); the
// shared fixtures under testdata/llm-request-extras/ at the repo root pin
// that both reject and match identically.
//
// Header and body values may be credentials: nothing in this package, and
// nothing that uses it, logs them. Only header names and body keys are
// logged.
package llmextras

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// EnvVar is the environment variable holding the rules.
const EnvVar = "OPENBBC_LLM_REQUEST_EXTRAS"

// Rule is one validated entry. Index is its position in the array.
type Rule struct {
	Index   int
	Match   string
	Headers map[string]string
	Body    map[string]json.RawMessage
}

// reservedBodyKeys are top-level fields the adapters set themselves; Bifrost's
// ExtraParams merge would silently overwrite them.
var reservedBodyKeys = map[string]bool{
	"model": true, "messages": true, "tools": true, "tool_choice": true,
	"stream": true, "stream_options": true, "max_tokens": true,
	"max_completion_tokens": true, "system": true,
}

// reservedHeaders (lower-cased) carry credentials or framing the adapters own.
var reservedHeaders = map[string]bool{
	"authorization": true, "x-api-key": true, "x-goog-api-key": true,
	"anthropic-version": true, "content-type": true, "content-length": true,
	"host": true, "transfer-encoding": true,
}

// sjsonSyntax are characters with a meaning in sjson paths (the Anthropic
// SDK's WithJSONSet): a body key containing one would not mean the same top-
// level key on every adapter.
const sjsonSyntax = ".*?|#@\\"

// Parse validates raw (the variable's value). Empty or whitespace-only means
// no rules. Every error starts with "OPENBBC_LLM_REQUEST_EXTRAS: " and, for a
// problem inside one rule, names it as "rule <i>: <field>: …".
func Parse(raw string) ([]Rule, error) {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 {
		return nil, nil
	}
	if !json.Valid(trimmed) {
		var v any
		err := json.Unmarshal(trimmed, &v)
		return nil, fmt.Errorf("%s: invalid JSON: %v", EnvVar, err)
	}
	if trimmed[0] != '[' {
		return nil, fmt.Errorf("%s: top level must be a JSON array", EnvVar)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %v", EnvVar, err)
	}
	rules := make([]Rule, 0, len(items))
	for i, item := range items {
		r, err := parseRule(i, item)
		if err != nil {
			return nil, fmt.Errorf("%s: rule %d: %w", EnvVar, i, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

func parseRule(i int, raw json.RawMessage) (Rule, error) {
	if !isObject(raw) {
		return Rule{}, fmt.Errorf("must be an object with match and headers and/or body")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Rule{}, fmt.Errorf("must be an object: %v", err)
	}
	var unknown []string
	for k := range fields {
		if k != "match" && k != "headers" && k != "body" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Rule{}, fmt.Errorf("%s: unknown key (allowed: match, headers, body)", unknown[0])
	}

	r := Rule{Index: i}
	if err := json.Unmarshal(fields["match"], &r.Match); fields["match"] == nil || err != nil || r.Match == "" {
		return Rule{}, fmt.Errorf("match: required, a non-empty string")
	}
	if h, ok := fields["headers"]; ok {
		headers, err := parseHeaders(h)
		if err != nil {
			return Rule{}, err
		}
		r.Headers = headers
	}
	if b, ok := fields["body"]; ok {
		body, err := parseBody(b)
		if err != nil {
			return Rule{}, err
		}
		r.Body = body
	}
	if len(r.Headers) == 0 && len(r.Body) == 0 {
		return Rule{}, fmt.Errorf("neither headers nor body is set (an empty object counts as absent)")
	}
	return r, nil
}

func parseHeaders(raw json.RawMessage) (map[string]string, error) {
	if !isObject(raw) {
		return nil, fmt.Errorf("headers: must be an object of header name to string value")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("headers: %v", err)
	}
	out := make(map[string]string, len(m))
	for _, name := range sortedKeys(m) {
		if name == "" {
			return nil, fmt.Errorf("headers: empty header name")
		}
		if !isToken(name) {
			return nil, fmt.Errorf("headers.%s: not a valid header name (RFC 7230 token)", name)
		}
		if reservedHeaders[strings.ToLower(name)] {
			return nil, fmt.Errorf("headers.%s: reserved header, set by the adapter", name)
		}
		var v string
		if t := bytes.TrimSpace(m[name]); len(t) == 0 || t[0] != '"' || json.Unmarshal(t, &v) != nil {
			return nil, fmt.Errorf("headers.%s: value must be a string", name)
		}
		out[name] = v
	}
	return out, nil
}

func parseBody(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if !isObject(raw) {
		return nil, fmt.Errorf("body: must be a JSON object")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("body: %v", err)
	}
	for _, k := range sortedKeys(m) {
		if k == "" {
			return nil, fmt.Errorf("body: empty key")
		}
		if reservedBodyKeys[k] {
			return nil, fmt.Errorf("body.%s: reserved key, set by the adapter", k)
		}
		if strings.ContainsAny(k, sjsonSyntax) {
			return nil, fmt.Errorf("body.%s: key must not contain any of . * ? | # @ \\", k)
		}
	}
	return m, nil
}

// isToken reports whether s is an RFC 7230 token (a valid header field name).
func isToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HeaderNames returns the rule's header names, sorted (never nil).
func (r *Rule) HeaderNames() []string { return sortedKeys(r.Headers) }

// BodyKeys returns the rule's top-level body keys, sorted (never nil).
func (r *Rule) BodyKeys() []string { return sortedKeys(r.Body) }

// BodyValues decodes the body into a fresh map on every call, so a caller
// that mutates it cannot affect later requests. Numbers stay json.Number,
// keeping their exact text when re-encoded.
func (r *Rule) BodyValues() map[string]any {
	if len(r.Body) == 0 {
		return nil
	}
	out := make(map[string]any, len(r.Body))
	for k, raw := range r.Body {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		_ = dec.Decode(&v) // validated by Parse
		out[k] = v
	}
	return out
}

// Match reports whether pattern matches the whole of s. Syntax: '*' matches
// any run of characters (including none and including '/'), '?' exactly one
// character; every other character, including '[', ']' and '\', is literal.
// Case-sensitive. Deliberately not path.Match: aikdm implements the same
// syntax and the two must agree.
func Match(pattern, s string) bool {
	p, str := []rune(pattern), []rune(s)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == str[si]):
			pi++
			si++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// Resolve returns the first rule whose glob matches model, or nil.
func Resolve(rules []Rule, model string) *Rule {
	for i := range rules {
		if Match(rules[i].Match, model) {
			return &rules[i]
		}
	}
	return nil
}

// LogRules writes the startup lines: one per rule (index, match, header
// names, body keys — never values), then which rule is active for the
// configured model. Nothing is logged when there are no rules.
func LogRules(logger *slog.Logger, rules []Rule, active *Rule) {
	if len(rules) == 0 {
		return
	}
	for i := range rules {
		r := &rules[i]
		logger.Info("llm request extras rule",
			slog.Int("index", r.Index),
			slog.String("match", r.Match),
			slog.Any("headers", r.HeaderNames()),
			slog.Any("body_keys", r.BodyKeys()))
	}
	if active == nil {
		logger.Info("llm request extras inactive", slog.String("reason", "no rule matches the configured model"))
		return
	}
	logger.Info("llm request extras active", slog.Int("rule", active.Index), slog.String("match", active.Match))
}
