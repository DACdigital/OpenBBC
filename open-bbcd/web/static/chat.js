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
  let displayBuf = '';
  let typingActive = false;
  let streamEnded = false;
  let onDrained = null;
  // Persisted assistant message ID captured from the first TEXT_MESSAGE_START.
  // Used post-finalize to fetch the feedback footer for the bubble.
  let currentAssistantMessageID = null;
  const toolCallElements = new Map();

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
    if (!text) return;
    input.value = '';
    sendBtn.disabled = true;
    input.disabled = true;

    appendUserBubble(text);
    startAssistantBubble();

    try {
      const resp = await fetch(`/agent_versions/${versionID}/chat/${sessionID}/turn`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ input: [{ type: 'text', text }] }),
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
      sendBtn.disabled = false;
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
    const md = document.createElement('div');
    md.className = 'md';
    md.innerHTML = renderMarkdown(text);
    content.appendChild(md);
    b.appendChild(content);
    log.appendChild(b);
    scheduleScroll();
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
