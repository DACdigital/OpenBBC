package flowmap

import (
	"fmt"
	"regexp"
	"strings"
)

// NormalizeMermaid rewrites a workflow mermaid string into the narrowed
// subset that ParseWorkflow accepts.
//
// Two transformations:
//
//   - Edge labels written as `a -->|label| b` become `a -- label --> b`.
//   - Parallel-fanout nodes (`id{{label}}`) are removed; every predecessor
//     edge is reconnected directly to every successor edge (cross-product).
//
// The result is validated with ParseWorkflow; if it still won't parse the
// returned error wraps ErrMermaidInvalid.
//
// Idempotent: a string that's already in the narrowed dialect round-trips
// through parse + serialize unchanged in semantics.
func NormalizeMermaid(src string) (string, error) {
	src = rewritePipeLabels(src)

	wf, parallels, err := parseLoose(src)
	if err != nil {
		return "", err
	}
	if len(parallels) > 0 {
		wf = bypassParallel(wf, parallels)
	}

	out := SerializeWorkflow(wf)
	if _, err := ParseWorkflow(out); err != nil {
		return "", fmt.Errorf("%w: normalized output is not parseable: %v", ErrMermaidInvalid, err)
	}
	return out, nil
}

// pipeLabelRe matches the `-->|label|` edge syntax. The capture is the
// label between the pipes. Replacement collapses to the `-- label -->`
// form the rest of the codebase accepts.
var pipeLabelRe = regexp.MustCompile(`-->\s*\|\s*([^|]+?)\s*\|`)

func rewritePipeLabels(src string) string {
	return pipeLabelRe.ReplaceAllString(src, "-- $1 -->")
}

// parallelTokenRe matches the parallel-fanout shape `id{{label}}` as a
// standalone token. Use this in absorbTokenLoose to recognise the shape.
var parallelTokenRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\{\{\s*([^{}]+?)\s*\}\}$`)

// parseLoose is a permissive variant of ParseWorkflow that additionally
// accepts `id{{label}}` parallel nodes. Parallel node ids are returned in
// a separate set so callers can transform them before serialization.
//
// The structure of the function mirrors ParseWorkflow line-by-line — the
// only difference is the extended token recogniser.
func parseLoose(src string) (ParsedWorkflow, map[string]bool, error) {
	parallels := map[string]bool{}

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
				return ParsedWorkflow{}, nil, fmt.Errorf("%w: unsupported orientation %q (want TD or LR)", ErrMermaidInvalid, rest)
			}
			header = true
			break
		}
		return ParsedWorkflow{}, nil, fmt.Errorf("%w: first non-blank line must be `flowchart TD` or `flowchart LR`", ErrMermaidInvalid)
	}
	if !header {
		return ParsedWorkflow{}, nil, fmt.Errorf("%w: no flowchart header found", ErrMermaidInvalid)
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
			if n, err := parseNodeTokenLoose(l, parallels); err == nil {
				if !parallels[n.ID] {
					nodes[n.ID] = n
				}
				continue
			}
			return ParsedWorkflow{}, nil, fmt.Errorf("%w: cannot parse line %q", ErrMermaidInvalid, l)
		}

		fID, err := absorbTokenLoose(from, nodes, parallels)
		if err != nil {
			return ParsedWorkflow{}, nil, fmt.Errorf("%w: %v", ErrMermaidInvalid, err)
		}
		tID, err := absorbTokenLoose(to, nodes, parallels)
		if err != nil {
			return ParsedWorkflow{}, nil, fmt.Errorf("%w: %v", ErrMermaidInvalid, err)
		}
		pending = append(pending, pendingEdge{from: fID, to: tID, label: label})
	}

	var edges []ParsedEdge
	for _, p := range pending {
		// Parallel-touching edges are kept in the graph for the bypass step
		// to consume; only refuse references to ids that were never declared
		// (and aren't bare parallel ids either).
		if _, ok := nodes[p.from]; !ok && !parallels[p.from] {
			return ParsedWorkflow{}, nil, fmt.Errorf("%w: edge endpoint %q not declared", ErrMermaidInvalid, p.from)
		}
		if _, ok := nodes[p.to]; !ok && !parallels[p.to] {
			return ParsedWorkflow{}, nil, fmt.Errorf("%w: edge endpoint %q not declared", ErrMermaidInvalid, p.to)
		}
		edges = append(edges, ParsedEdge{From: p.from, To: p.to, Label: p.label})
	}

	// Order nodes by first appearance in source — same heuristic ParseWorkflow uses.
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
			id := bareID(tok)
			if id == "" || parallels[id] {
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

	return ParsedWorkflow{Nodes: ordered, Edges: edges}, parallels, nil
}

// parseNodeTokenLoose accepts every shape parseNodeToken accepts plus the
// parallel `id{{label}}` shape. A parallel match registers the id in the
// `parallels` set and returns a synthetic ParsedNode (kind=skill, used
// only as a flag — it is filtered before serialization).
func parseNodeTokenLoose(token string, parallels map[string]bool) (ParsedNode, error) {
	t := strings.TrimSpace(token)
	if m := parallelTokenRe.FindStringSubmatch(t); m != nil {
		parallels[m[1]] = true
		return ParsedNode{ID: m[1], Kind: NodeSkill, Label: m[2]}, nil
	}
	return parseNodeToken(t)
}

func absorbTokenLoose(token string, nodes map[string]ParsedNode, parallels map[string]bool) (string, error) {
	if n, err := parseNodeTokenLoose(token, parallels); err == nil {
		if !parallels[n.ID] {
			nodes[n.ID] = n
		}
		return n.ID, nil
	}
	bare := strings.TrimSpace(token)
	if !bareIDRe.MatchString(bare) {
		return "", fmt.Errorf("malformed node token %q", token)
	}
	return bare, nil
}

func bareID(token string) string {
	if n, err := parseNodeToken(strings.TrimSpace(token)); err == nil {
		return n.ID
	}
	if m := parallelTokenRe.FindStringSubmatch(strings.TrimSpace(token)); m != nil {
		return m[1]
	}
	bare := strings.TrimSpace(token)
	if bareIDRe.MatchString(bare) {
		return bare
	}
	return ""
}

// bypassParallel removes every parallel node from wf and rewires the
// graph: for each parallel id p, every edge ending at p is paired with
// every edge starting from p to produce a new direct edge (cross-product).
// Edge labels prefer the outgoing branch's label; the incoming label is
// used only as a fallback.
func bypassParallel(wf ParsedWorkflow, parallels map[string]bool) ParsedWorkflow {
	preds := map[string][]ParsedEdge{}
	succs := map[string][]ParsedEdge{}
	var keep []ParsedEdge
	for _, e := range wf.Edges {
		switch {
		case parallels[e.To] && parallels[e.From]:
			// parallel→parallel chains aren't expected; drop.
		case parallels[e.To]:
			preds[e.To] = append(preds[e.To], e)
		case parallels[e.From]:
			succs[e.From] = append(succs[e.From], e)
		default:
			keep = append(keep, e)
		}
	}
	for pid := range parallels {
		for _, pe := range preds[pid] {
			for _, se := range succs[pid] {
				lbl := se.Label
				if lbl == "" {
					lbl = pe.Label
				}
				keep = append(keep, ParsedEdge{From: pe.From, To: se.To, Label: lbl})
			}
		}
	}

	var nodes []ParsedNode
	for _, n := range wf.Nodes {
		if parallels[n.ID] {
			continue
		}
		nodes = append(nodes, n)
	}
	return ParsedWorkflow{Nodes: nodes, Edges: keep}
}
