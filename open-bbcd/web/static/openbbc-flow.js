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

  // ---- Editor wiring ----
  //
  // Markup contract for a workflow-editor container:
  //   <div class="obf-editor"
  //        data-obf-editor
  //        data-agent-id="<uuid>"
  //        data-flow-id="<flow-id>"
  //        data-skills='["skill-id-1","skill-id-2",...]'>
  //     <div class="obf-toolbar">
  //       <button data-obf-add="skill">+ skill</button>
  //       <button data-obf-add="decision">+ decision</button>
  //       <button data-obf-add="end">+ end</button>
  //       <button data-obf-action="auto-layout">Auto-layout</button>
  //       <button data-obf-action="reset">Reset to source</button>
  //       <span class="obf-saved-indicator"></span>
  //     </div>
  //     <div class="obf-canvas"></div>
  //     <script type="application/json" data-obf-state>
  //       {"mermaid":"...","layout":{...}}
  //     </script>
  //   </div>

  const NODE_TEMPLATES = {
    start:    '<div class="obf-pill obf-start">start</div>',
    end:      '<div class="obf-pill obf-end">end</div>',
    skill:    '<div class="obf-rect obf-skill"><span class="obf-label">SKILL</span></div>',
    decision: '<div class="obf-diamond"><span class="obf-label">DECISION</span></div>',
  };

  function ensureUniqueId(base, taken) {
    if (!taken.has(base)) { taken.add(base); return base; }
    let i = 2;
    while (taken.has(`${base}_${i}`)) i++;
    const id = `${base}_${i}`;
    taken.add(id);
    return id;
  }

  // applyLayout: when no saved positions exist (or all are missing), use
  // Dagre to compute a tidy layered layout. Returns the layout map.
  function applyLayout(wf, layout) {
    if (window.dagre && (!layout || Object.keys(layout).length === 0)) {
      const g = new window.dagre.graphlib.Graph();
      g.setGraph({ rankdir: "TB", nodesep: 50, ranksep: 60 });
      g.setDefaultEdgeLabel(() => ({}));
      for (const n of wf.nodes) {
        const size = n.kind === "decision" ? { width: 140, height: 60 } : { width: 140, height: 44 };
        g.setNode(n.id, size);
      }
      for (const e of wf.edges) g.setEdge(e.from, e.to);
      window.dagre.layout(g);
      const out = {};
      for (const n of wf.nodes) {
        const node = g.node(n.id);
        out[n.id] = { x: Math.round(node.x - node.width / 2), y: Math.round(node.y - node.height / 2) };
      }
      return out;
    }
    return layout || {};
  }

  OpenBBCFlow.initEditor = function (root) {
    if (!root || root.dataset.obfReady === "1") return;
    root.dataset.obfReady = "1";

    const stateEl = root.querySelector("[data-obf-state]");
    if (!stateEl) { console.warn("[obf] no state element"); return; }
    let state;
    try { state = JSON.parse(stateEl.textContent); }
    catch (err) { console.error("[obf] state parse failed", err); return; }
    const skills = JSON.parse(root.dataset.skills || "[]");

    let wf;
    try { wf = OpenBBCFlow.parseMermaid(state.mermaid); }
    catch (err) { console.error("[obf] mermaid parse failed", err); return; }

    const layout = applyLayout(wf, state.layout);
    const dfState = OpenBBCFlow.drawflowFromWorkflow(wf, layout);

    const canvas = root.querySelector(".obf-canvas");
    if (!canvas) { console.warn("[obf] no canvas element"); return; }
    if (!window.Drawflow) { console.error("[obf] Drawflow library not loaded"); return; }

    const editor = new window.Drawflow(canvas);
    editor.start();
    for (const [k, tpl] of Object.entries(NODE_TEMPLATES)) {
      editor.registerNode(k, tpl, {}, {});
    }
    editor.import(dfState);

    refreshLabels(canvas, editor);

    const taken = new Set(wf.nodes.map((n) => n.id));

    function currentWorkflow() {
      const cur = editor.export();
      return OpenBBCFlow.workflowFromDrawflow(cur);
    }

    let saveTimer = null;
    const savedEl = root.querySelector(".obf-saved-indicator");

    function scheduleSave() {
      if (saveTimer) clearTimeout(saveTimer);
      saveTimer = setTimeout(doSave, 500);
    }

    async function doSave() {
      const next = currentWorkflow();
      const mermaid = OpenBBCFlow.serializeMermaid(next);
      const newLayout = {};
      const cur = editor.export().drawflow.Home.data;
      for (const k of Object.keys(cur)) {
        const dfn = cur[k];
        newLayout[dfn.data.mermaidId || `n${k}`] = { x: Math.round(dfn.pos_x), y: Math.round(dfn.pos_y) };
      }
      const url = `/agents/${root.dataset.agentId}/configure/flows/${root.dataset.flowId}/workflow`;
      try {
        const res = await fetch(url, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mermaid, layout: newLayout }),
        });
        if (!res.ok) throw new Error(`save failed: ${res.status}`);
        if (savedEl) savedEl.textContent = `Saved · ${new Date().toLocaleTimeString()}`;
      } catch (err) {
        console.error("[obf] save error", err);
        if (savedEl) savedEl.textContent = `Save error: ${err.message}`;
      }
    }

    ["nodeMoved", "nodeRemoved", "nodeDataChanged", "connectionCreated", "connectionRemoved"].forEach((evt) => {
      editor.on(evt, scheduleSave);
    });

    // Toolbar — add nodes.
    root.querySelectorAll("[data-obf-add]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const kind = btn.dataset.obfAdd;
        let mermaidId, label;
        if (kind === "skill") {
          const choice = window.prompt(`Skill id (one of):\n${skills.join("\n")}`);
          if (!choice || !skills.includes(choice)) return;
          mermaidId = ensureUniqueId("s_" + choice.replaceAll("-", "_"), taken);
          label = choice;
        } else if (kind === "decision") {
          const q = window.prompt("Decision question (e.g. 'cart empty?')");
          if (!q) return;
          mermaidId = ensureUniqueId("d", taken);
          label = q;
        } else if (kind === "end") {
          mermaidId = ensureUniqueId("e", taken);
          label = "end";
        } else {
          return;
        }
        const rect = canvas.getBoundingClientRect();
        const x = Math.round(rect.width / 2 - 70);
        const y = Math.round(rect.height / 2 - 22);
        editor.addNode(
          kind,                   // name (template)
          1, 1,                   // inputs, outputs
          x, y,                   // pos
          `obf-node obf-${kind}`, // class
          { mermaidId, label, edgeLabels: {} },
          kind                    // html (template ref)
        );
        refreshLabels(canvas, editor);
        scheduleSave();
      });
    });

    // Toolbar — actions.
    const autoBtn = root.querySelector('[data-obf-action="auto-layout"]');
    if (autoBtn) {
      autoBtn.addEventListener("click", () => {
        const cur = currentWorkflow();
        const newLayout = applyLayout(cur, {});
        const data = editor.export().drawflow.Home.data;
        for (const k of Object.keys(data)) {
          const mid = data[k].data.mermaidId;
          if (newLayout[mid]) {
            data[k].pos_x = newLayout[mid].x;
            data[k].pos_y = newLayout[mid].y;
          }
        }
        editor.import({ drawflow: { Home: { data } } });
        refreshLabels(canvas, editor);
        scheduleSave();
      });
    }

    const resetBtn = root.querySelector('[data-obf-action="reset"]');
    if (resetBtn) {
      resetBtn.addEventListener("click", () => {
        if (!window.confirm("Discard layout and recompute from the original mermaid?")) return;
        let original;
        try { original = OpenBBCFlow.parseMermaid(state.mermaid); }
        catch (err) { console.error("[obf] reset parse failed", err); return; }
        const newLayout = applyLayout(original, {});
        const fresh = OpenBBCFlow.drawflowFromWorkflow(original, newLayout);
        editor.clear();
        for (const [k, tpl] of Object.entries(NODE_TEMPLATES)) {
          editor.registerNode(k, tpl, {}, {});
        }
        editor.import(fresh);
        refreshLabels(canvas, editor);
        scheduleSave();
      });
    }
  };

  // refreshLabels: walk every rendered drawflow-node element and replace the
  // template placeholder text (e.g. "SKILL") with the actual mermaid label
  // pulled from the editor's data store.
  function refreshLabels(canvas, editor) {
    if (!editor) return;
    const data = editor.export().drawflow.Home.data;
    canvas.querySelectorAll(".drawflow-node").forEach((nodeEl) => {
      const id = nodeEl.getAttribute("id"); // "node-<num>"
      if (!id) return;
      const num = id.replace("node-", "");
      const dfn = data[num];
      if (!dfn) return;
      const label = nodeEl.querySelector(".obf-label");
      if (label && dfn.data && dfn.data.label) label.textContent = dfn.data.label;
    });
  }

  // Init on DOM ready + after htmx swaps.
  function scan(root) {
    (root || document).querySelectorAll("[data-obf-editor]").forEach(OpenBBCFlow.initEditor);
  }
  document.addEventListener("DOMContentLoaded", () => scan());
  document.body.addEventListener("htmx:afterSwap", (e) => scan(e.target));

  window.OpenBBCFlow = OpenBBCFlow;
})(window);
