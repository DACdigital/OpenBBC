// openbbc-flow.js — workflow editor for the configurator.
//
// Bridges:
//   - mermaid `flowchart TD` string (server-side source of truth)
//   - Drawflow JSON state (in-memory)
//
// Mirrors the Go parser/serializer in internal/flowmap/. The supported
// dialect is intentionally small:
//   - flowchart TD or flowchart LR header
//   - node shapes: id([label]) start/end, id[label] skill, id{label} decision
//   - edges:       a --> b, a -- label --> b
//
// Out of scope: {{label}} parallel-fanout, & joins, --|label|-- syntax.

(function (window) {
  "use strict";

  const OpenBBCFlow = {};

  // ---- Mermaid parser ----
  // Returns { nodes: [{id, kind, label}], edges: [{from, to, label}] }
  // Throws Error("...") on parse failure.

  const stadiumRe  = /^([A-Za-z_][A-Za-z0-9_]*)\s*\(\[\s*([^\]\[]+?)\s*\]\)$/;
  const decisionRe = /^([A-Za-z_][A-Za-z0-9_]*)\s*\{\s*([^{}]+?)\s*\}$/;
  const skillRe    = /^([A-Za-z_][A-Za-z0-9_]*)\s*\[\s*([^\]\[]+?)\s*\]$/;
  const idRe       = /^[A-Za-z_][A-Za-z0-9_]*$/;
  const labeledArrowRe = /^(.+?)\s+--\s+(.+?)\s+-->\s+(.+)$/;
  const plainArrowRe   = /^(.+?)\s+-->\s+(.+)$/;

  function parseNodeToken(token) {
    const t = token.trim();
    let m;
    if ((m = t.match(stadiumRe))) {
      const kind = m[2].toLowerCase() === "end" ? "end" : "start";
      return { id: m[1], kind, label: m[2] };
    }
    if ((m = t.match(decisionRe)))  return { id: m[1], kind: "decision", label: m[2] };
    if ((m = t.match(skillRe)))     return { id: m[1], kind: "skill",    label: m[2] };
    return null;
  }

  function absorbToken(token, nodes) {
    const parsed = parseNodeToken(token);
    if (parsed) {
      nodes.set(parsed.id, parsed);
      return parsed.id;
    }
    const bare = token.trim();
    if (!idRe.test(bare)) throw new Error(`malformed node token: ${token}`);
    return bare;
  }

  function splitEdge(line) {
    let m = line.match(labeledArrowRe);
    if (m) return { from: m[1].trim(), label: m[2].trim(), to: m[3].trim() };
    m = line.match(plainArrowRe);
    if (m) return { from: m[1].trim(), label: "", to: m[2].trim() };
    return null;
  }

  OpenBBCFlow.parseMermaid = function (src) {
    if (/\{\{[^{}]*\}\}/.test(src)) {
      throw new Error("parallel-fanout {{...}} is not supported");
    }
    const lines = src.split("\n");
    let header = false;
    for (const raw of lines) {
      const l = raw.trim();
      if (!l) continue;
      if (l.startsWith("flowchart ")) {
        const rest = l.substring("flowchart".length).trim();
        if (rest !== "TD" && rest !== "LR") throw new Error(`unsupported orientation: ${rest}`);
        header = true;
        break;
      }
      throw new Error("first non-blank line must be `flowchart TD` or `flowchart LR`");
    }
    if (!header) throw new Error("no flowchart header found");

    const nodes = new Map();
    const pending = [];
    for (const raw of lines) {
      const l = raw.trim();
      if (!l || l.startsWith("flowchart ") || l.startsWith("%%")) continue;
      const e = splitEdge(l);
      if (e) {
        const f = absorbToken(e.from, nodes);
        const t = absorbToken(e.to, nodes);
        pending.push({ from: f, to: t, label: e.label });
      } else if (parseNodeToken(l)) {
        const n = parseNodeToken(l);
        nodes.set(n.id, n);
      } else {
        throw new Error(`cannot parse line: ${l}`);
      }
    }
    for (const p of pending) {
      if (!nodes.has(p.from)) throw new Error(`edge endpoint ${p.from} not declared`);
      if (!nodes.has(p.to))   throw new Error(`edge endpoint ${p.to} not declared`);
    }
    return { nodes: Array.from(nodes.values()), edges: pending };
  };

  // ---- Mermaid serializer ----
  // Inverse of parseMermaid. Deterministic output: nodes in declaration
  // order, edges in input order.

  function formatNode(n) {
    if (n.kind === "start" || n.kind === "end")   return `${n.id}([${n.label}])`;
    if (n.kind === "decision")                    return `${n.id}{${n.label}}`;
    return `${n.id}[${n.label}]`;
  }

  OpenBBCFlow.serializeMermaid = function (wf) {
    const out = ["flowchart TD"];
    for (const n of wf.nodes) out.push("  " + formatNode(n));
    for (const e of wf.edges) {
      out.push(e.label ? `  ${e.from} -- ${e.label} --> ${e.to}` : `  ${e.from} --> ${e.to}`);
    }
    return out.join("\n") + "\n";
  };

  // ---- Drawflow JSON ↔ ParsedWorkflow bridge ----
  // The Drawflow library uses its own state shape:
  //   { drawflow: { Home: { data: { <num>: { id, name, data: {...}, class, pos_x, pos_y, ... } } } } }
  // We encode each node's domain payload in `data` and expose `name` as the
  // Drawflow node-type identifier registered via `editor.registerNode`.

  OpenBBCFlow.drawflowFromWorkflow = function (wf, layout) {
    layout = layout || {};
    const data = {};
    const idToNum = new Map();
    let next = 1;
    for (const n of wf.nodes) {
      idToNum.set(n.id, next++);
    }
    for (const n of wf.nodes) {
      const num = idToNum.get(n.id);
      const pos = layout[n.id] || { x: 40 + (num - 1) * 200, y: 40 };
      data[num] = {
        id: num,
        name: n.kind,
        data: { mermaidId: n.id, label: n.label },
        class: `obf-node obf-${n.kind}`,
        html: n.kind,
        typenode: false,
        inputs: { input_1: { connections: [] } },
        outputs: { output_1: { connections: [] } },
        pos_x: pos.x,
        pos_y: pos.y,
      };
    }
    for (const e of wf.edges) {
      const fromNum = idToNum.get(e.from);
      const toNum = idToNum.get(e.to);
      if (fromNum == null || toNum == null) continue;
      data[fromNum].outputs.output_1.connections.push({ node: String(toNum), output: "input_1" });
      data[toNum].inputs.input_1.connections.push({ node: String(fromNum), input: "output_1" });
      if (e.label) {
        data[fromNum].data.edgeLabels = data[fromNum].data.edgeLabels || {};
        data[fromNum].data.edgeLabels[toNum] = e.label;
      }
    }
    return { drawflow: { Home: { data } } };
  };

  OpenBBCFlow.workflowFromDrawflow = function (df) {
    const data = df.drawflow.Home.data;
    const nodes = [];
    const edges = [];
    const keys = Object.keys(data).sort((a, b) => Number(a) - Number(b));
    for (const k of keys) {
      const dfn = data[k];
      nodes.push({
        id: dfn.data.mermaidId || `n${k}`,
        kind: dfn.name,
        label: dfn.data.label || dfn.name,
      });
    }
    const idForKey = (k) => data[k].data.mermaidId || `n${k}`;
    for (const k of keys) {
      const dfn = data[k];
      const labels = (dfn.data && dfn.data.edgeLabels) || {};
      for (const conn of dfn.outputs.output_1.connections) {
        edges.push({
          from: idForKey(k),
          to: idForKey(conn.node),
          label: labels[conn.node] || "",
        });
      }
    }
    return { nodes, edges };
  };

  window.OpenBBCFlow = OpenBBCFlow;
})(window);
