// chat.js — vanilla JS chat client.
// Subscribes to AG-UI SSE stream from POST /agent_versions/{version_id}/chat/{session_id}/turn.
//
// AG-UI Go SDK wire format (NOT W3C `event: TYPE` headers):
//   id: <event_id>
//   data: {"type":"<TYPE>", ...}
//   \n
//
// fetch + ReadableStream (not EventSource) because AG-UI is POST → SSE
// and EventSource is GET-only.
//
// Visual streaming model:
//   Anthropic ships text in 5-15 char chunks, not single tokens. Painting
//   raw chunks looks bursty. We instead buffer inbound deltas and drain
//   them at a target rate of ~one full buffer per 500ms — fast enough to
//   keep up with the network, smooth enough that the user perceives
//   continuous typing. When the stream ends we mark the bubble done; the
//   drain loop finalises (markdown render) once the buffer is empty.

(function () {
  console.info('[chat.js] loaded — AG-UI SSE client v4 (typewriter + avatars)');

  const log = document.getElementById('chat-log');
  if (!log) return;

  renderAllMarkdown(log);

  const input = document.getElementById('chat-input');
  const sendBtn = document.getElementById('chat-send');
  const versionID = log.dataset.versionId;
  const sessionID = log.dataset.sessionId;
  if (!versionID || !sessionID) return;

  let currentAssistantTurn = null;
  let currentUserBubble = null;
  let pendingAttachments = [];
  let uploadsInFlight = 0;
  let deletesInFlight = 0;
  let turnActive = false;
  let syncSeq = 0;
  let chipGen = 0; // bumped per chip added; lets a sync keep chips newer than its request
  let pendingLocked = false; // pending set frozen from send until the claim is decided
  let runStarted = false;
  let displayBuf = '';
  let typingActive = false;
  let streamEnded = false;
  let onDrained = null;
  // Persisted assistant message ID captured from the first TEXT_MESSAGE_START.
  // Used post-finalize to fetch the feedback footer for the bubble.
  let currentAssistantMessageID = null;
  const toolCallElements = new Map();

  // ---- Artifacts: attach, pending chips ------------------------------------
  // Server is authoritative: chips are rendered optimistically from upload
  // responses and re-synced from GET …/pending-artifacts after every turn.
  const chatBase = `/agent_versions/${versionID}/chat/${sessionID}`;
  const pendingBox = document.getElementById('pending-artifacts');
  const attachBtn = document.getElementById('chat-attach');
  const attachInput = document.getElementById('chat-attach-input');

  function chipLabel(p) {
    return p.filename || `file (${p.mime})`;
  }

  // Server error bodies are JSON {error} or plain text.
  function errText(body) {
    const t = (body || '').trim();
    try {
      const j = JSON.parse(t);
      if (j && typeof j.error === 'string' && j.error) return j.error;
    } catch (_) { /* not JSON */ }
    return t;
  }

  function updateSendEnabled() {
    sendBtn.disabled = uploadsInFlight > 0 || deletesInFlight > 0 || turnActive;
    if (attachBtn) attachBtn.disabled = pendingLocked || uploadsInFlight > 0;
  }

  function setPendingLocked(on) {
    pendingLocked = on;
    if (pendingBox) {
      pendingBox.querySelectorAll('.artifact-chip-remove').forEach((b) => { b.disabled = on; });
    }
    updateSendEnabled();
  }

  function addPendingChip(p) {
    if (!pendingBox || document.getElementById(`pending-artifact-${p.id}`)) return;
    const chip = document.createElement('span');
    chip.className = 'artifact-chip';
    chip.id = `pending-artifact-${p.id}`;
    const label = document.createElement('span');
    label.className = 'artifact-chip-label';
    label.textContent = chipLabel(p);
    const rm = document.createElement('button');
    rm.type = 'button';
    rm.className = 'artifact-chip-remove';
    rm.setAttribute('aria-label', `Remove ${chipLabel(p)}`);
    rm.dataset.deleteUrl = `${chatBase}/pending-artifacts/${p.id}`;
    rm.textContent = '×';
    rm.disabled = pendingLocked;
    chip.dataset.gen = String(++chipGen);
    chip.appendChild(label);
    chip.appendChild(rm);
    pendingBox.appendChild(chip);
  }

  function pendingLabels() {
    if (!pendingBox) return [];
    return [...pendingBox.querySelectorAll('.artifact-chip-label')].map((el) => el.textContent);
  }

  async function syncPendingChips() {
    if (!pendingBox) return;
    const seq = ++syncSeq;
    const startGen = chipGen;
    try {
      const resp = await fetch(`${chatBase}/pending-artifacts`, { headers: { Accept: 'application/json' } });
      if (!resp.ok) return;
      const body = await resp.json();
      if (seq !== syncSeq) return; // stale response
      // Merge: keep chips the server lists, drop absent ones (unless added
      // after this request started), add missing ones.
      const list = body.pending_artifacts || [];
      const ids = new Set(list.map((p) => `pending-artifact-${p.id}`));
      pendingBox.querySelectorAll('.artifact-chip').forEach((chip) => {
        if (!ids.has(chip.id) && Number(chip.dataset.gen || 0) <= startGen) chip.remove();
      });
      list.forEach(addPendingChip);
      if (!pendingBox.querySelector('.artifact-chip')) pendingBox.replaceChildren();
    } catch (err) {
      console.warn('[chat.js] pending-artifacts sync failed', err);
    }
  }

  // Mirrors llm.HumanBytes: binary units, one truncated decimal, ".0" dropped.
  function humanBytes(n) {
    n = Number(n) || 0;
    const units = [['GB', 1024 * 1024 * 1024], ['MB', 1024 * 1024], ['KB', 1024]];
    for (const [u, size] of units) {
      if (n >= size) {
        const whole = Math.floor(n / size);
        const frac = Math.floor(((n % size) * 10) / size);
        return `${whole}${frac ? '.' + frac : ''} ${u}`;
      }
    }
    return `${Math.floor(n)} B`;
  }

  // Mirrors the Go artifactHref: '' (not linkable) when store_id contains '/'
  // or any uri segment is '', '.' or '..'.
  function artifactHref(storeId, uri) {
    if (String(storeId).includes('/') || storeId === '.' || storeId === '..') return '';
    const segs = String(uri).split('/');
    for (const sg of segs) {
      if (sg === '' || sg === '.' || sg === '..') return '';
    }
    return `${chatBase}/artifacts/${encodeURIComponent(storeId)}/${segs.map(encodeURIComponent).join('/')}`;
  }

  function appendArtifactLink(v) {
    if (!v || !v.storeId || !v.uri) return;
    if (!currentAssistantTurn) startAssistantBubble();
    const label = `${v.filename || 'file'} (${v.mime || 'unknown'}, ${humanBytes(v.sizeBytes)})`;
    const href = artifactHref(v.storeId, v.uri);
    let el;
    if (href) {
      el = document.createElement('a');
      el.className = 'artifact-link';
      el.setAttribute('href', href);
      el.setAttribute('target', '_blank');
      el.setAttribute('rel', 'noopener');
    } else {
      el = document.createElement('span');
      el.className = 'artifact-link artifact-link-disabled';
    }
    el.setAttribute('title', label);
    el.textContent = `\u{1F4CE} ${label}`;
    flushDisplayBuf();
    currentAssistantTurn.content.appendChild(el);
    scheduleScroll();
  }

  // Paint any text still waiting in the typewriter buffer right now, so a
  // block appended next lands after it, not before.
  function flushDisplayBuf() {
    if (!currentAssistantTurn || displayBuf.length === 0) return;
    const last = currentAssistantTurn.content.lastChild;
    if (last !== currentAssistantTurn.stream) {
      const seg = newStreamSegment();
      currentAssistantTurn.content.appendChild(seg);
      currentAssistantTurn.stream = seg;
    }
    currentAssistantTurn.stream.firstChild.data += displayBuf;
    displayBuf = '';
  }

  // RUN_STARTED is emitted only after the user turn committed, so the pending
  // files are claimed: show them on the user bubble and empty the chips.
  function markAttachmentsClaimed() {
    if (currentUserBubble && pendingAttachments.length) {
      const wrap = document.createElement('div');
      wrap.className = 'user-attachments';
      pendingAttachments.forEach((label) => {
        const tag = document.createElement('span');
        tag.className = 'artifact-chip artifact-chip-sent';
        tag.textContent = `\u{1F4CE} ${label}`;
        wrap.appendChild(tag);
      });
      const content = currentUserBubble.querySelector('.content');
      if (content) content.appendChild(wrap);
    }
    pendingAttachments = [];
    runStarted = true;
    syncSeq++; // an older in-flight sync must not resurrect claimed chips
    if (pendingBox) pendingBox.replaceChildren();
    setPendingLocked(false);
  }

  async function uploadFile(file) {
    const fd = new FormData();
    fd.append('file', file, file.name);
    const resp = await fetch(`${chatBase}/artifacts`, { method: 'POST', body: fd });
    if (!resp.ok) {
      const body = await resp.text().catch(() => '');
      throw new Error(`HTTP ${resp.status}${body ? ': ' + errText(body) : ''}`);
    }
    addPendingChip(await resp.json());
  }

  if (attachBtn && attachInput) {
    attachBtn.addEventListener('click', () => attachInput.click());
    attachInput.addEventListener('change', async () => {
      const files = [...attachInput.files];
      attachInput.value = '';
      if (pendingLocked) return; // pending set frozen while a turn is being claimed
      attachBtn.disabled = true;
      for (const f of files) {
        uploadsInFlight++;
        updateSendEnabled();
        try {
          await uploadFile(f);
        } catch (err) {
          showError(`Upload of ${f.name} failed: ${err.message || err}`);
        } finally {
          uploadsInFlight--;
          updateSendEnabled();
        }
      }
      updateSendEnabled();
    });
  }

  if (pendingBox) {
    pendingBox.addEventListener('click', async (e) => {
      const btn = e.target.closest('.artifact-chip-remove');
      if (!btn || !btn.dataset.deleteUrl) return;
      // A chip whose DELETE is in flight must not be captured as "claimed".
      deletesInFlight++;
      updateSendEnabled();
      try {
        await removeChip(btn);
      } finally {
        deletesInFlight--;
        updateSendEnabled();
      }
    });

    async function removeChip(btn) {
      btn.disabled = true;
      let resp = null;
      try {
        resp = await fetch(btn.dataset.deleteUrl, { method: 'DELETE' });
      } catch (_) { /* network error: fall through */ }
      // 204 removed; 404 already gone; 409 already consumed by a turn —
      // in every case the chip is stale, so drop it and re-sync.
      if (resp && (resp.ok || resp.status === 404 || resp.status === 409)) {
        syncSeq++; // an in-flight sync GET must not resurrect this chip
        btn.closest('.artifact-chip').remove();
        // Drop any leftover whitespace so #pending-artifacts:empty matches.
        if (!pendingBox.querySelector('.artifact-chip')) pendingBox.replaceChildren();
        if (!resp.ok) syncPendingChips();
        return;
      }
      btn.disabled = pendingLocked;
      let detail = '';
      if (resp) {
        const t = errText(await resp.text().catch(() => ''));
        detail = `: HTTP ${resp.status}${t ? ' ' + t : ''}`;
      }
      showError(`Could not remove file${detail}`);
    }
  }

  sendBtn.addEventListener('click', send);
  // Enter sends; Shift+Enter inserts a newline (standard chat textarea behavior).
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  });

  let scrollPending = false;
  function scheduleScroll() {
    if (scrollPending) return;
    scrollPending = true;
    requestAnimationFrame(() => {
      scrollPending = false;
      log.scrollTop = log.scrollHeight;
    });
  }

  async function send() {
    const text = input.value.trim();
    const attached = pendingLabels();
    if (!text && attached.length === 0) return;
    if (uploadsInFlight > 0 || deletesInFlight > 0 || turnActive) return;
    input.value = '';
    turnActive = true;
    runStarted = false;
    setPendingLocked(true);
    input.disabled = true;

    try {
      currentUserBubble = appendUserBubble(text);
      pendingAttachments = attached;
      startAssistantBubble();
      const resp = await fetch(`/agent_versions/${versionID}/chat/${sessionID}/turn`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ input: text ? [{ type: 'text', text }] : [] }),
      });
      if (!resp.ok) {
        const body = await resp.text().catch(() => '');
        showError(`HTTP ${resp.status}${body ? ': ' + body.trim() : ''}`);
        return;
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split('\n');
        buf = lines.pop();
        for (const line of lines) {
          processSSELine(line);
        }
      }
      if (buf) processSSELine(buf);
    } catch (err) {
      console.error('[chat.js] fetch error', err);
      showError(err.message || String(err));
    } finally {
      streamEnded = true;
      // Wait for the typewriter to drain its buffer before finalising the
      // bubble (markdown render + re-enable input). Otherwise we'd cut off
      // mid-sentence visually.
      await waitForDrain();
      finalizeAssistantBubble();
      // Artifact-only send that never started: drop the empty user bubble.
      if (!runStarted && currentUserBubble && !currentUserBubble.querySelector('.md')) {
        currentUserBubble.remove();
      }
      currentUserBubble = null;
      pendingAttachments = [];
      setPendingLocked(false);
      syncPendingChips();
      turnActive = false;
      updateSendEnabled();
      input.disabled = false;
      input.focus();
    }
  }

  function waitForDrain() {
    return new Promise((resolve) => {
      if (!typingActive && displayBuf.length === 0) {
        resolve();
        return;
      }
      onDrained = resolve;
    });
  }

  let pendingData = '';
  function processSSELine(line) {
    if (line.startsWith('data:')) {
      pendingData += line.slice(5).trim();
      return;
    }
    if (line === '' && pendingData) {
      let obj = null;
      try {
        obj = JSON.parse(pendingData);
      } catch (e) {
        console.error('[chat.js] JSON.parse failed for', pendingData, e);
      }
      pendingData = '';
      if (obj && obj.type) {
        handleEvent(obj.type, obj);
      }
    }
  }

  function handleEvent(type, data) {
    switch (type) {
      case 'RUN_STARTED':
        markAttachmentsClaimed();
        break;
      case 'CUSTOM':
        if (data.name === 'ARTIFACT_REF') appendArtifactLink(data.value);
        break;
      case 'TEXT_MESSAGE_END':
      case 'RUN_FINISHED':
        break;
      case 'TEXT_MESSAGE_START':
        // AG-UI carries the persisted assistant message id on start events.
        // Capture the FIRST one only — a single turn can produce multiple
        // assistant messages (text → tool_use → tool_result → text) but
        // buildMessageViews merges them into one bubble anchored at the
        // first id, so feedback attaches there too.
        if (!currentAssistantMessageID && data.messageId) {
          currentAssistantMessageID = data.messageId;
        }
        break;
      case 'TEXT_MESSAGE_CONTENT':
        appendTextDelta(data.delta || '');
        break;
      case 'TOOL_CALL_START':
        startToolCall(data.toolCallId, data.toolCallName);
        break;
      case 'TOOL_CALL_ARGS':
        appendToolArgs(data.toolCallId, data.delta || '');
        break;
      case 'TOOL_CALL_END':
        finishToolCall(data.toolCallId);
        break;
      case 'TOOL_CALL_RESULT':
        appendToolResult(data.toolCallId, data.content);
        break;
      case 'RUN_ERROR':
        showError(data.message || 'unknown error');
        break;
      default:
        console.debug('[chat.js] unhandled event', type, data);
    }
  }

  function appendTextDelta(delta) {
    if (!currentAssistantTurn) startAssistantBubble();
    displayBuf += delta;
    if (!typingActive) startTyping();
  }

  // Typewriter loop: drain displayBuf at ~one full buffer per 500ms.
  // Chars per frame = ceil(bufLen / 30) so a 30-char backlog drains 1/frame,
  // a 300-char backlog drains 10/frame — both empty in roughly the same
  // wall time, keeping the feel consistent regardless of arrival burstiness.
  function startTyping() {
    typingActive = true;
    const tick = () => {
      if (!currentAssistantTurn) {
        typingActive = false;
        signalDrained();
        return;
      }
      if (displayBuf.length === 0) {
        typingActive = false;
        signalDrained();
        return;
      }
      const chunkSize = Math.max(1, Math.ceil(displayBuf.length / 30));
      const chunk = displayBuf.slice(0, chunkSize);
      displayBuf = displayBuf.slice(chunkSize);

      // If the most recent child of .content is a tool detail (not our stream
      // segment), start a fresh segment so text appears after the tool block.
      const last = currentAssistantTurn.content.lastChild;
      if (last !== currentAssistantTurn.stream) {
        const seg = newStreamSegment();
        currentAssistantTurn.content.appendChild(seg);
        currentAssistantTurn.stream = seg;
      }
      currentAssistantTurn.stream.firstChild.data += chunk;
      scheduleScroll();
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }

  function signalDrained() {
    if (onDrained) {
      const fn = onDrained;
      onDrained = null;
      fn();
    }
  }

  function appendUserBubble(text) {
    const b = document.createElement('div');
    b.className = 'chat-bubble user';
    b.appendChild(buildHeader('user'));
    const content = document.createElement('div');
    content.className = 'content';
    if (text) {
      const md = document.createElement('div');
      md.className = 'md';
      md.innerHTML = renderMarkdown(text);
      content.appendChild(md);
    }
    b.appendChild(content);
    log.appendChild(b);
    scheduleScroll();
    return b;
  }

  function startAssistantBubble() {
    streamEnded = false;
    const b = document.createElement('div');
    b.className = 'chat-bubble assistant streaming';
    b.appendChild(buildHeader('assistant'));
    const content = document.createElement('div');
    content.className = 'content';
    const stream = newStreamSegment();
    content.appendChild(stream);
    b.appendChild(content);
    log.appendChild(b);
    currentAssistantTurn = { bubble: b, content, stream };
    scheduleScroll();
  }

  // Header: role-coloured avatar circle + role text. Matches the structure
  // produced server-side for history (see view.html template).
  function buildHeader(role) {
    const h = document.createElement('div');
    h.className = 'bubble-header';
    const avatar = document.createElement('div');
    avatar.className = `avatar avatar-${role}`;
    avatar.textContent = role.charAt(0).toUpperCase();
    const name = document.createElement('div');
    name.className = 'role-name';
    name.textContent = role;
    h.appendChild(avatar);
    h.appendChild(name);
    return h;
  }

  function newStreamSegment() {
    const div = document.createElement('div');
    div.className = 'md md-stream';
    div.appendChild(document.createTextNode(''));
    return div;
  }

  function finalizeAssistantBubble() {
    if (!currentAssistantTurn) return;
    currentAssistantTurn.bubble.classList.remove('streaming');
    const segments = currentAssistantTurn.content.querySelectorAll('.md-stream');
    segments.forEach((seg) => {
      const raw = seg.textContent || '';
      seg.classList.remove('md-stream');
      seg.innerHTML = renderMarkdown(raw);
    });
    // Fetch and inject the feedback footer for this bubble. Done async so
    // it doesn't block scroll settling; failures are logged and skipped
    // (page refresh will render it via the server template on next load).
    const bubble = currentAssistantTurn.bubble;
    const msgID = currentAssistantMessageID;
    if (msgID) {
      const url = `/agent_versions/${versionID}/chat/${sessionID}/messages/${msgID}/feedback`;
      fetch(url, { headers: { 'Accept': 'text/html' } })
        .then((r) => (r.ok ? r.text() : null))
        .then((html) => {
          if (!html) return;
          const wrap = document.createElement('div');
          wrap.innerHTML = html.trim();
          const footer = wrap.firstElementChild;
          if (footer) {
            bubble.appendChild(footer);
            if (window.htmx) window.htmx.process(footer);
          }
        })
        .catch((err) => console.warn('[chat.js] feedback footer fetch failed', err));
    }
    currentAssistantMessageID = null;
    currentAssistantTurn = null;
    scheduleScroll();
  }

  function startToolCall(id, name) {
    if (!currentAssistantTurn) startAssistantBubble();
    const details = document.createElement('details');
    details.className = 'tool-call';
    const summary = document.createElement('summary');
    summary.textContent = `▸ ${name}(…)`;
    const args = document.createElement('pre');
    args.className = 'args';
    details.appendChild(summary);
    details.appendChild(args);
    currentAssistantTurn.content.appendChild(details);
    toolCallElements.set(id, { summary, args, name });
    scheduleScroll();
  }

  function appendToolArgs(id, delta) {
    const el = toolCallElements.get(id);
    if (!el) return;
    el.args.textContent += delta;
  }

  function finishToolCall(id) {
    const el = toolCallElements.get(id);
    if (!el) return;
    // Hide args in summary; user expands details to inspect. Matches the
    // persisted-view rendering in chat/view.html.
    el.summary.textContent = `▸ ${el.name}(…)`;
  }

  function appendToolResult(id, content) {
    if (!currentAssistantTurn) return;
    const details = document.createElement('details');
    details.className = 'tool-result';
    const summary = document.createElement('summary');
    const isMocked = typeof content === 'string' && content.includes('"_mocked":true');
    if (isMocked) summary.classList.add('mocked');
    summary.textContent = `▸ result${isMocked ? ' (mocked)' : ''}`;
    const pre = document.createElement('pre');
    try {
      const parsed = JSON.parse(content);
      pre.textContent = JSON.stringify(parsed, null, 2);
    } catch (_) {
      pre.textContent = String(content || '');
    }
    details.appendChild(summary);
    details.appendChild(pre);
    currentAssistantTurn.content.appendChild(details);
    scheduleScroll();
  }

  function showError(msg) {
    const b = document.createElement('div');
    b.className = 'chat-error';
    b.textContent = `Error: ${msg}`;
    log.appendChild(b);
    scheduleScroll();
  }

  function renderMarkdown(text) {
    if (typeof window.marked === 'undefined') return escapeHTML(text);
    try {
      return window.marked.parse(text, { breaks: true, gfm: true });
    } catch (e) {
      console.error('[chat.js] markdown render failed', e);
      return escapeHTML(text);
    }
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');
  }

  function renderAllMarkdown(root) {
    root.querySelectorAll('.md').forEach((el) => {
      if (el.classList.contains('md-stream')) return;
      const raw = el.dataset.raw != null ? el.dataset.raw : el.textContent;
      el.innerHTML = renderMarkdown(raw);
    });
  }

  // Inline session-title editor: pencil swaps display→form; Save PATCHes
  // /title; Cancel reverts. Empty input clears the title to "Untitled".
  initSessionTitleEditor();
  function initSessionTitleEditor() {
    const wrap = document.querySelector('.session-title');
    if (!wrap) return;
    const display = wrap.querySelector('.session-title-display');
    const text = wrap.querySelector('.session-title-text');
    const editBtn = wrap.querySelector('.session-title-edit');
    const form = wrap.querySelector('.session-title-form');
    const input = wrap.querySelector('.session-title-input');
    const cancelBtn = wrap.querySelector('.session-title-cancel');
    const untitledText = wrap.dataset.untitledText || 'Untitled session';

    function toEdit() {
      input.value = text.classList.contains('is-empty') ? '' : text.textContent.trim();
      display.hidden = true;
      form.hidden = false;
      input.focus();
      input.select();
    }
    function toDisplay() {
      display.hidden = false;
      form.hidden = true;
    }

    editBtn.addEventListener('click', toEdit);
    cancelBtn.addEventListener('click', toDisplay);
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.preventDefault(); toDisplay(); }
    });
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const newTitle = input.value.trim();
      try {
        const res = await fetch(`/agent_versions/${versionID}/chat/${sessionID}/title`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ title: newTitle }),
        });
        if (!res.ok) {
          throw new Error(`HTTP ${res.status}`);
        }
        const body = await res.json();
        const saved = (body && typeof body.title === 'string') ? body.title : newTitle;
        if (saved) {
          text.textContent = saved;
          text.classList.remove('is-empty');
        } else {
          text.textContent = untitledText;
          text.classList.add('is-empty');
        }
        toDisplay();
      } catch (err) {
        console.error('[chat.js] title update failed', err);
        alert('Could not update title: ' + err.message);
      }
    });
  }
})();

// Feedback criteria editor: hooks all .feedback-criteria-editor blocks on
// the page. Add/remove rows, serialise to the sibling hidden input as JSON
// on submit. Called from feedback form onsubmit handlers.
function serialiseCriteria(form) {
  form.querySelectorAll('.feedback-criteria-editor').forEach(editor => {
    const targetName = editor.dataset.targetInput;
    const target = form.querySelector(`input[name="${targetName}"]`);
    if (!target) return;
    const items = [...editor.querySelectorAll('.feedback-criterion-row input')]
      .map(i => i.value.trim()).filter(Boolean);
    target.value = JSON.stringify(items);
  });
}

document.addEventListener('click', (e) => {
  const addBtn = e.target.closest('.btn-add-criterion');
  if (addBtn) {
    const editor = addBtn.closest('.feedback-criteria-editor');
    const rows = editor.querySelector('.feedback-criteria-rows');
    const row = document.createElement('div');
    row.className = 'feedback-criterion-row';
    row.innerHTML = '<input type="text" class="field-input" placeholder="e.g. Refuses to disclose PII">' +
      '<button type="button" class="btn-remove-criterion">✕</button>';
    rows.appendChild(row);
    row.querySelector('input').focus();
    return;
  }
  const rmBtn = e.target.closest('.btn-remove-criterion');
  if (rmBtn) {
    rmBtn.closest('.feedback-criterion-row').remove();
  }
});

// Sync submit-button enabled state to whether the feedback form has at
// least one non-empty criterion. Runs on click (add/remove) and on input.
function syncFeedbackSubmit(editor) {
  const form = editor.closest('form');
  if (!form) return;
  const submit = form.querySelector('button[type="submit"]');
  if (!submit) return;
  const filled = [...editor.querySelectorAll('.feedback-criterion-row input')]
    .some(i => i.value.trim().length > 0);
  submit.disabled = !filled;
  submit.title = filled ? '' : 'Add at least one acceptance criterion';
  const hint = editor.querySelector('.feedback-criteria-hint');
  if (hint) hint.hidden = filled;
}

// Disable submit on initial render of any newly-shown feedback form
// (they start with zero criteria, so submit must be disabled).
document.addEventListener('click', (e) => {
  const btn = e.target.closest('.thumb-up, .thumb-down');
  if (!btn) return;
  // The form was just un-hidden by the inline onclick; find it and sync.
  setTimeout(() => {
    const parent = btn.closest('.feedback-footer');
    if (!parent) return;
    parent.querySelectorAll('.feedback-criteria-editor').forEach(syncFeedbackSubmit);
  }, 0);
});

// Re-sync when rows are added/removed or when input changes.
document.addEventListener('click', (e) => {
  const target = e.target.closest('.btn-add-criterion, .btn-remove-criterion');
  if (!target) return;
  const editor = target.closest('.feedback-criteria-editor');
  if (editor) setTimeout(() => syncFeedbackSubmit(editor), 0);
});
document.addEventListener('input', (e) => {
  const input = e.target.closest('.feedback-criterion-row input');
  if (!input) return;
  const editor = input.closest('.feedback-criteria-editor');
  if (editor) syncFeedbackSubmit(editor);
});
