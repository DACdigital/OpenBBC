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
        // Drawflow import with typenode:false assigns html to innerHTML verbatim.
        html: NODE_TEMPLATES[n.kind] || n.kind,
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
  //        data-version-id="<uuid>"
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
    decision: '<div class="obf-diamond">' +
              '<svg viewBox="0 0 140 80" preserveAspectRatio="none">' +
              '<polygon points="70,1 139,40 70,79 1,40" fill="#161b22" stroke="#d29922" stroke-width="2"/>' +
              '</svg>' +
              '<span class="obf-label">DECISION</span></div>',
  };

  function ensureUniqueId(base, taken) {
    if (!taken.has(base)) { taken.add(base); return base; }
    let i = 2;
    while (taken.has(`${base}_${i}`)) i++;
    const id = `${base}_${i}`;
    taken.add(id);
    return id;
  }

  // applyLayout always runs Dagre against the current workflow — there is
  // no "saved positions" code path. Any layout the backend may have stored
  // is intentionally ignored so reload, Auto-layout and Reset to source
  // produce identical results. Generous nodesep/ranksep give Drawflow's
  // bezier curves enough room to bend without crossing obstacle nodes,
  // since edge rendering is left to the library (no waypoint overrides).
  // The graph is horizontally centred in the canvas.
  function applyLayout(wf, canvas) {
    if (!window.dagre) return {};
    const g = new window.dagre.graphlib.Graph();
    g.setGraph({ rankdir: "TB", nodesep: 100, ranksep: 120, edgesep: 40 });
    g.setDefaultEdgeLabel(() => ({}));
    for (const n of wf.nodes) g.setNode(n.id, nodeSize(n));
    for (const e of wf.edges) g.setEdge(e.from, e.to);
    window.dagre.layout(g);
    const graphWidth = g.graph().width || 0;
    const canvasWidth = canvas ? canvas.getBoundingClientRect().width : 0;
    const offsetX = Math.max(40, Math.round((canvasWidth - graphWidth) / 2));
    const offsetY = 40;
    const positions = {};
    for (const n of wf.nodes) {
      const node = g.node(n.id);
      positions[n.id] = {
        x: Math.round(node.x - node.width / 2) + offsetX,
        y: Math.round(node.y - node.height / 2) + offsetY,
      };
    }
    return positions;
  }

  // nodeSize approximates the rendered size of each kind for the Dagre
  // layout. Reasonable estimates avoid wasted space; skill width tracks
  // label length so long ids don't collide with neighbours.
  function nodeSize(n) {
    switch (n.kind) {
      case "start":
      case "end":
        return { width: 70, height: 36 };
      case "decision":
        return { width: 140, height: 90 };
      default:
        return { width: Math.max(90, n.label.length * 7 + 30), height: 36 };
    }
  }

  // Modal: replaces window.prompt / window.confirm with in-page dialogs
  // (blurred backdrop). All three return a Promise; the caller awaits.
  //
  //   Modal.confirm({title, message, confirmText, cancelText, danger}) -> bool
  //   Modal.prompt ({title, message, placeholder, defaultValue})        -> string|null
  //   Modal.select ({title, items, placeholder, emptyText})              -> string|null
  const Modal = (function () {
    function makeOverlay() {
      const o = document.createElement("div");
      o.className = "obf-modal-overlay";
      return o;
    }
    function makeModal(title) {
      const m = document.createElement("div");
      m.className = "obf-modal";
      m.setAttribute("role", "dialog");
      m.setAttribute("aria-modal", "true");
      const h = document.createElement("h3");
      h.textContent = title;
      m.appendChild(h);
      return m;
    }
    function makeBtn(label, cls) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "obf-modal-btn" + (cls ? " " + cls : "");
      b.textContent = label;
      return b;
    }
    function makeActions(/* ...buttons */) {
      const a = document.createElement("div");
      a.className = "obf-modal-actions";
      for (let i = 0; i < arguments.length; i++) a.appendChild(arguments[i]);
      return a;
    }
    function present(modal, onKey) {
      const overlay = makeOverlay();
      overlay.appendChild(modal);
      document.body.appendChild(overlay);
      function keyHandler(e) { onKey(e); }
      document.addEventListener("keydown", keyHandler, true);
      return function dismiss() {
        document.removeEventListener("keydown", keyHandler, true);
        overlay.remove();
      };
    }

    function confirmDlg(opts) {
      return new Promise(function (resolve) {
        const modal = makeModal(opts.title || "Confirm");
        if (opts.message) {
          const p = document.createElement("p");
          p.className = "obf-modal-message";
          p.textContent = opts.message;
          modal.appendChild(p);
        }
        const cancel = makeBtn(opts.cancelText || "Cancel");
        const ok = makeBtn(opts.confirmText || "Confirm",
          opts.danger ? "obf-modal-btn-danger" : "obf-modal-btn-primary");
        modal.appendChild(makeActions(cancel, ok));
        const dismiss = present(modal, function (e) {
          if (e.key === "Escape") { e.preventDefault(); dismiss(); resolve(false); }
          else if (e.key === "Enter") { e.preventDefault(); dismiss(); resolve(true); }
        });
        cancel.addEventListener("click", function () { dismiss(); resolve(false); });
        ok.addEventListener("click", function () { dismiss(); resolve(true); });
        ok.focus();
      });
    }

    function promptDlg(opts) {
      return new Promise(function (resolve) {
        const modal = makeModal(opts.title || "Input");
        if (opts.message) {
          const p = document.createElement("p");
          p.className = "obf-modal-message";
          p.textContent = opts.message;
          modal.appendChild(p);
        }
        const input = document.createElement("input");
        input.type = "text";
        input.className = "obf-modal-input";
        input.placeholder = opts.placeholder || "";
        input.value = opts.defaultValue || "";
        modal.appendChild(input);
        const cancel = makeBtn("Cancel");
        const ok = makeBtn("OK", "obf-modal-btn-primary");
        modal.appendChild(makeActions(cancel, ok));
        function commit() {
          const v = input.value.trim();
          dismiss();
          resolve(v || null);
        }
        const dismiss = present(modal, function (e) {
          if (e.key === "Escape") { e.preventDefault(); dismiss(); resolve(null); }
          else if (e.key === "Enter") { e.preventDefault(); commit(); }
        });
        cancel.addEventListener("click", function () { dismiss(); resolve(null); });
        ok.addEventListener("click", commit);
        setTimeout(function () { input.focus(); }, 0);
      });
    }

    function selectDlg(opts) {
      return new Promise(function (resolve) {
        const items = (opts.items || []).slice();
        const modal = makeModal(opts.title || "Select");
        const input = document.createElement("input");
        input.type = "text";
        input.className = "obf-modal-input";
        input.placeholder = opts.placeholder || "Filter…";
        modal.appendChild(input);
        const list = document.createElement("div");
        list.className = "obf-modal-list";
        modal.appendChild(list);
        const cancel = makeBtn("Cancel");
        modal.appendChild(makeActions(cancel));

        let filtered = items.slice();
        let active = 0;
        function render() {
          list.innerHTML = "";
          if (filtered.length === 0) {
            const empty = document.createElement("div");
            empty.className = "obf-modal-empty";
            empty.textContent = items.length === 0
              ? (opts.emptyText || "No items.")
              : "No matches.";
            list.appendChild(empty);
            return;
          }
          filtered.forEach(function (item, i) {
            const row = document.createElement("div");
            row.className = "obf-modal-item" + (i === active ? " active" : "");
            row.textContent = item;
            row.addEventListener("click", function () { commit(item); });
            list.appendChild(row);
          });
          const activeEl = list.querySelector(".obf-modal-item.active");
          if (activeEl) activeEl.scrollIntoView({ block: "nearest" });
        }
        function applyFilter() {
          const q = input.value.trim().toLowerCase();
          filtered = q
            ? items.filter(function (i) { return i.toLowerCase().includes(q); })
            : items.slice();
          active = 0;
          render();
        }
        function commit(value) { dismiss(); resolve(value || null); }
        const dismiss = present(modal, function (e) {
          if (e.key === "Escape") { e.preventDefault(); dismiss(); resolve(null); }
          else if (e.key === "ArrowDown") { e.preventDefault(); active = Math.min(filtered.length - 1, active + 1); render(); }
          else if (e.key === "ArrowUp")   { e.preventDefault(); active = Math.max(0, active - 1); render(); }
          else if (e.key === "Enter")     { e.preventDefault(); if (filtered[active]) commit(filtered[active]); }
        });
        cancel.addEventListener("click", function () { dismiss(); resolve(null); });
        input.addEventListener("input", applyFilter);
        applyFilter();
        setTimeout(function () { input.focus(); }, 0);
      });
    }

    return { confirm: confirmDlg, prompt: promptDlg, select: selectDlg };
  })();

  // installVerticalCurvature monkey-patches Drawflow's createCurvature so
  // connection paths use vertical bezier control points (cp Y offset from
  // start/end Y) instead of the library's horizontal default. Required for
  // edges to look natural between top-input / bottom-output ports.
  function installVerticalCurvature(editor) {
    editor.createCurvature = function (sx, sy, ex, ey, curvature) {
      const dy = ey - sy;
      const offset = Math.abs(dy) * curvature;
      const sign = dy >= 0 ? 1 : -1;
      const cp1y = sy + offset * sign;
      const cp2y = ey - offset * sign;
      return " M " + sx + " " + sy +
             " C " + sx + " " + cp1y +
             " " + ex + " " + cp2y +
             " " + ex + " " + ey;
    };
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
    const readOnly = root.dataset.obfReadonly === "1";

    let wf;
    try { wf = OpenBBCFlow.parseMermaid(state.mermaid); }
    catch (err) { console.error("[obf] mermaid parse failed", err); return; }

    const canvas = root.querySelector(".obf-canvas");
    if (!canvas) { console.warn("[obf] no canvas element"); return; }
    if (!window.Drawflow) { console.error("[obf] Drawflow library not loaded"); return; }

    const positions = applyLayout(wf, canvas);
    const dfState = OpenBBCFlow.drawflowFromWorkflow(wf, positions);

    const editor = new window.Drawflow(canvas);
    installVerticalCurvature(editor);
    editor.start();
    // "view" lets the user pan/zoom the canvas but blocks node edits and
    // new connections — "fixed" would lock pan too.
    if (readOnly) editor.editor_mode = "view";
    editor.import(dfState);

    refreshLabels(canvas, editor);
    refreshEdgeLabels(canvas, editor);

    const taken = new Set(wf.nodes.map((n) => n.id));

    function currentWorkflow() {
      const cur = editor.export();
      return OpenBBCFlow.workflowFromDrawflow(cur);
    }

    let saveTimer = null;
    const savedEl = root.querySelector(".obf-saved-indicator");

    function scheduleSave() {
      if (readOnly) return;
      if (saveTimer) clearTimeout(saveTimer);
      saveTimer = setTimeout(doSave, 500);
    }

    async function doSave() {
      const next = currentWorkflow();
      const mermaid = OpenBBCFlow.serializeMermaid(next);
      const url = `/agents/${root.dataset.agentId}/configure/architecture/flows/${root.dataset.flowId}/workflow`;
      try {
        const res = await fetch(url, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mermaid }),
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
      editor.on(evt, () => refreshEdgeLabels(canvas, editor));
    });

    // Toolbar — add nodes.
    root.querySelectorAll("[data-obf-add]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const kind = btn.dataset.obfAdd;
        let mermaidId, label;
        if (kind === "skill") {
          const choice = await Modal.select({
            title: "Add skill node",
            placeholder: "Filter skills…",
            items: skills,
            emptyText: "No skills configured for this agent.",
          });
          if (!choice) return;
          mermaidId = ensureUniqueId("s_" + choice.replaceAll("-", "_"), taken);
          label = choice;
        } else if (kind === "decision") {
          const q = await Modal.prompt({
            title: "Add decision node",
            message: "Question the decision asks. Two outgoing edges should be labelled yes/no.",
            placeholder: "e.g. cart empty?",
          });
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
          kind,                          // name
          1, 1,                          // inputs, outputs
          x, y,                          // pos
          `obf-node obf-${kind}`,        // class
          { mermaidId, label, edgeLabels: {} },
          NODE_TEMPLATES[kind] || kind   // html (raw markup, typenode defaults false)
        );
        refreshLabels(canvas, editor);
        refreshEdgeLabels(canvas, editor);
        scheduleSave();
      });
    });

    // Toolbar — actions.
    const autoBtn = root.querySelector('[data-obf-action="auto-layout"]');
    if (autoBtn) {
      autoBtn.addEventListener("click", () => {
        const cur = currentWorkflow();
        const newLayout = applyLayout(cur, canvas);
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
        refreshEdgeLabels(canvas, editor);
        scheduleSave();
      });
    }

    const resetBtn = root.querySelector('[data-obf-action="reset"]');
    if (resetBtn) {
      resetBtn.addEventListener("click", async () => {
        const ok = await Modal.confirm({
          title: "Reset to source",
          message: "Discard your edits and rebuild the workflow from the discovered mermaid?",
          confirmText: "Reset",
          danger: true,
        });
        if (!ok) return;
        let original;
        try { original = OpenBBCFlow.parseMermaid(state.mermaid); }
        catch (err) { console.error("[obf] reset parse failed", err); return; }
        const newLayout = applyLayout(original, canvas);
        const fresh = OpenBBCFlow.drawflowFromWorkflow(original, newLayout);
        editor.clear();
        editor.import(fresh);
        refreshLabels(canvas, editor);
        refreshEdgeLabels(canvas, editor);
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

  // refreshEdgeLabels: for every connection SVG, inject (or update) a <text>
  // element at the path's midpoint showing the edge label (e.g. "yes" / "no"
  // from a decision branch). Drawflow doesn't render edge labels itself.
  // Must be called after import and after any event that moves a node or
  // adds/removes a connection — the path geometry changes each time.
  function refreshEdgeLabels(canvas, editor) {
    if (!editor) return;
    const data = editor.export().drawflow.Home.data;
    const SVG_NS = "http://www.w3.org/2000/svg";
    canvas.querySelectorAll(".connection").forEach((conn) => {
      let fromId = null, toId = null;
      for (const c of conn.classList) {
        let m = c.match(/^node_in_node-(\d+)$/);
        if (m) toId = m[1];
        m = c.match(/^node_out_node-(\d+)$/);
        if (m) fromId = m[1];
      }
      const existing = conn.querySelector("text.obf-edge-label");
      if (existing) existing.remove();
      if (!fromId || !toId) return;
      const fromNode = data[fromId];
      const label = fromNode && fromNode.data && fromNode.data.edgeLabels && fromNode.data.edgeLabels[toId];
      if (!label) return;
      const path = conn.querySelector(".main-path");
      if (!path) return;
      let mid;
      try {
        const len = path.getTotalLength();
        if (len === 0) return; // path geometry not yet computed
        mid = path.getPointAtLength(len / 2);
      } catch (e) { return; }
      const text = document.createElementNS(SVG_NS, "text");
      text.setAttribute("class", "obf-edge-label");
      text.setAttribute("x", mid.x);
      text.setAttribute("y", mid.y);
      text.setAttribute("text-anchor", "middle");
      text.setAttribute("dominant-baseline", "middle");
      text.textContent = label;
      conn.appendChild(text);
    });
  }

  // Inject a one-off SVG <defs> containing the arrowhead marker referenced
  // by the connection stroke's `marker-end` CSS rule. Doing this once on
  // first init keeps marker IDs unique across htmx swaps.
  function ensureArrowMarker() {
    if (document.getElementById("obf-arrow-defs")) return;
    const NS = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(NS, "svg");
    svg.id = "obf-arrow-defs";
    svg.setAttribute("width", "0");
    svg.setAttribute("height", "0");
    svg.style.position = "absolute";
    const defs = document.createElementNS(NS, "defs");
    const marker = document.createElementNS(NS, "marker");
    marker.setAttribute("id", "obf-arrow");
    marker.setAttribute("viewBox", "0 0 10 10");
    marker.setAttribute("refX", "10");
    marker.setAttribute("refY", "5");
    marker.setAttribute("markerWidth", "7");
    marker.setAttribute("markerHeight", "7");
    marker.setAttribute("orient", "auto");
    marker.setAttribute("markerUnits", "strokeWidth");
    const path = document.createElementNS(NS, "path");
    path.setAttribute("d", "M 0 0 L 10 5 L 0 10 z");
    path.setAttribute("fill", "#58a6ff");
    marker.appendChild(path);
    defs.appendChild(marker);
    svg.appendChild(defs);
    document.body.appendChild(svg);
  }

  // Init on DOM ready + after htmx swaps.
  function scan(root) {
    ensureArrowMarker();
    (root || document).querySelectorAll("[data-obf-editor]").forEach(OpenBBCFlow.initEditor);
  }
  document.addEventListener("DOMContentLoaded", () => scan());
  document.body.addEventListener("htmx:afterSwap", (e) => scan(e.target));

  window.OpenBBCFlow = OpenBBCFlow;
})(window);
