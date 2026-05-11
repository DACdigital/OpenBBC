package flowmap

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrMermaidInvalid wraps every parser failure so callers can use errors.Is.
var ErrMermaidInvalid = errors.New("mermaid flowchart is invalid")

// Recognised node shapes:
//
//	start([label]), end([label])  — stadium
//	id[label]                     — skill
//	id{label}                     — decision
//
// Recognised edges:
//
//	a --> b           (unlabeled)
//	a -- label --> b  (labeled, label is text — typically "yes" or "no")
//
// Out of scope (PR3):
//
//	id{{label}}       — parallel fanout (explicitly rejected)
//	a & b --> c       — fanout joins
//	a --|label|--> b  — alternate label syntax
var (
	stadiumRe  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\(\[\s*([^\]\[]+?)\s*\]\)$`)
	decisionRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\{\s*([^{}]+?)\s*\}$`)
	skillRe    = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\[\s*([^\]\[]+?)\s*\]$`)

	parallelRe = regexp.MustCompile(`\{\{[^{}]*\}\}`) // for explicit rejection

	labeledArrowRe = regexp.MustCompile(`^(.+?)\s+--\s+(.+?)\s+-->\s+(.+)$`)
	plainArrowRe   = regexp.MustCompile(`^(.+?)\s+-->\s+(.+)$`)

	bareIDRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseWorkflow reads a mermaid `flowchart TD/LR` string and returns
// structured nodes and edges. Errors wrap ErrMermaidInvalid.
//
// Strategy: two passes.
//
//	Pass 1 — collect declared nodes from any token that matches one of the
//	         node-shape patterns (declaration can happen on either side of
//	         an edge, e.g. `start([start]) --> s_a[place-order]`).
//	Pass 2 — collect edges, requiring both endpoints to be declared.
func ParseWorkflow(src string) (ParsedWorkflow, error) {
	if parallelRe.MatchString(src) {
		return ParsedWorkflow{}, fmt.Errorf("%w: parallel-fanout shape {{...}} is not supported in PR3", ErrMermaidInvalid)
	}

	lines := strings.Split(src, "\n")
	var header bool
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "flowchart ") {
			rest := strings.TrimSpace(strings.TrimPrefix(l, "flowchart"))
			if rest != "TD" && rest != "LR" {
				return ParsedWorkflow{}, fmt.Errorf("%w: unsupported orientation %q (want TD or LR)", ErrMermaidInvalid, rest)
			}
			header = true
			break
		}
		return ParsedWorkflow{}, fmt.Errorf("%w: first non-blank line must be `flowchart TD` or `flowchart LR`", ErrMermaidInvalid)
	}
	if !header {
		return ParsedWorkflow{}, fmt.Errorf("%w: no flowchart header found", ErrMermaidInvalid)
	}

	nodes := make(map[string]ParsedNode)
	type pendingEdge struct {
		from, to, label string
	}
	var pending []pendingEdge

	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "flowchart ") || strings.HasPrefix(l, "%%") {
			continue
		}

		from, label, to, ok := splitEdgeOrDecl(l)
		if !ok {
			if n, err := parseNodeToken(l); err == nil {
				nodes[n.ID] = n
				continue
			}
			return ParsedWorkflow{}, fmt.Errorf("%w: cannot parse line %q", ErrMermaidInvalid, l)
		}

		fID, err := absorbToken(from, nodes)
		if err != nil {
			return ParsedWorkflow{}, fmt.Errorf("%w: %v", ErrMermaidInvalid, err)
		}
		tID, err := absorbToken(to, nodes)
		if err != nil {
			return ParsedWorkflow{}, fmt.Errorf("%w: %v", ErrMermaidInvalid, err)
		}
		pending = append(pending, pendingEdge{from: fID, to: tID, label: label})
	}

	var edges []ParsedEdge
	for _, p := range pending {
		if _, ok := nodes[p.from]; !ok {
			return ParsedWorkflow{}, fmt.Errorf("%w: edge endpoint %q not declared", ErrMermaidInvalid, p.from)
		}
		if _, ok := nodes[p.to]; !ok {
			return ParsedWorkflow{}, fmt.Errorf("%w: edge endpoint %q not declared", ErrMermaidInvalid, p.to)
		}
		edges = append(edges, ParsedEdge{From: p.from, To: p.to, Label: p.label})
	}

	// Materialise nodes in declaration order (stable output for round-trip).
	seen := make(map[string]struct{}, len(nodes))
	var ordered []ParsedNode
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "flowchart ") || strings.HasPrefix(l, "%%") {
			continue
		}
		var tokens []string
		if from, _, to, ok := splitEdgeOrDecl(l); ok {
			tokens = []string{from, to}
		} else {
			tokens = []string{l}
		}
		for _, tok := range tokens {
			id, err := absorbToken(tok, map[string]ParsedNode{})
			if err != nil {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			if n, ok := nodes[id]; ok {
				seen[id] = struct{}{}
				ordered = append(ordered, n)
			}
		}
	}

	return ParsedWorkflow{Nodes: ordered, Edges: edges}, nil
}

func splitEdgeOrDecl(l string) (from, label, to string, ok bool) {
	if m := labeledArrowRe.FindStringSubmatch(l); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3]), true
	}
	if m := plainArrowRe.FindStringSubmatch(l); m != nil {
		return strings.TrimSpace(m[1]), "", strings.TrimSpace(m[2]), true
	}
	return "", "", "", false
}

func absorbToken(token string, nodes map[string]ParsedNode) (string, error) {
	if n, err := parseNodeToken(token); err == nil {
		nodes[n.ID] = n
		return n.ID, nil
	}
	bare := strings.TrimSpace(token)
	if !bareIDRe.MatchString(bare) {
		return "", fmt.Errorf("malformed node token %q", token)
	}
	return bare, nil
}

func parseNodeToken(token string) (ParsedNode, error) {
	t := strings.TrimSpace(token)
	if m := stadiumRe.FindStringSubmatch(t); m != nil {
		kind := NodeStart
		if strings.EqualFold(m[2], "end") {
			kind = NodeEnd
		}
		return ParsedNode{ID: m[1], Kind: kind, Label: m[2]}, nil
	}
	if m := decisionRe.FindStringSubmatch(t); m != nil {
		return ParsedNode{ID: m[1], Kind: NodeDecision, Label: m[2]}, nil
	}
	if m := skillRe.FindStringSubmatch(t); m != nil {
		return ParsedNode{ID: m[1], Kind: NodeSkill, Label: m[2]}, nil
	}
	return ParsedNode{}, fmt.Errorf("not a node shape: %q", token)
}
