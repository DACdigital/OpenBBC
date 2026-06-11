// chat.js — vanilla JS chat client.
// Subscribes to AG-UI SSE stream from POST /agents/{id}/chat/{session_id}/turn.
//
// AG-UI Go SDK wire format (NOT W3C `event: TYPE` headers):
//   id: <event_id>
//   data: {"type":"<TYPE>", ...}
//   \n
//
// fetch + ReadableStream (not EventSource) because AG-UI is POST → SSE
// and EventSource is GET-only.
//
// Rendering policy:
//   - Streaming deltas accumulate as plain text in a <div class="md md-stream">.
//   - On bubble finish (RUN_FINISHED / fetch end), each md-stream div is parsed
//     through marked.parse() and replaced with rendered HTML.
//   - History (server-rendered) is parsed on DOMContentLoaded.
// Live markdown parsing per delta would thrash the DOM and look broken when a
// delta breaks mid-token (e.g. mid `**bold`).

(function () {
  console.info('[chat.js] loaded — AG-UI SSE client v3 (markdown + throttled scroll)');

  const log = document.getElementById('chat-log');
  if (!log) return;

  // Render all server-rendered history markdown blocks on load.
  renderAllMarkdown(log);

  const input = document.getElementById('chat-input');
  const sendBtn = document.getElementById('chat-send');
  const agentID = log.dataset.agentId;
  const sessionID = log.dataset.sessionId;
  if (!agentID || !sessionID) return;

  let currentAssistantTurn = null;
  const toolCallElements = new Map();

  sendBtn.addEventListener('click', send);
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      send();
    }
  });

  // Throttled scroller — coalesces multiple scroll requests per frame so a
  // rapid burst of text deltas does not pin the main thread on layout.
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
      const resp = await fetch(`/agents/${agentID}/chat/${sessionID}/turn`, {
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
      finishAssistantBubble();
      sendBtn.disabled = false;
      input.disabled = false;
      input.focus();
    }
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
      case 'TEXT_MESSAGE_START':
      case 'TEXT_MESSAGE_END':
      case 'RUN_FINISHED':
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

  function appendUserBubble(text) {
    const b = document.createElement('div');
    b.className = 'chat-bubble user';
    const role = document.createElement('div');
    role.className = 'role-label';
    role.textContent = '[user]';
    const content = document.createElement('div');
    content.className = 'content';
    const md = document.createElement('div');
    md.className = 'md';
    md.innerHTML = renderMarkdown(text);
    content.appendChild(md);
    b.appendChild(role);
    b.appendChild(content);
    log.appendChild(b);
    scheduleScroll();
  }

  function startAssistantBubble() {
    const b = document.createElement('div');
    b.className = 'chat-bubble assistant streaming';
    const role = document.createElement('div');
    role.className = 'role-label';
    role.textContent = '[assistant]';
    const content = document.createElement('div');
    content.className = 'content';
    const stream = newStreamSegment();
    content.appendChild(stream);
    const cursor = document.createElement('span');
    cursor.className = 'cursor';
    cursor.textContent = '▌';
    b.appendChild(role);
    b.appendChild(content);
    b.appendChild(cursor);
    log.appendChild(b);
    currentAssistantTurn = { bubble: b, content, stream };
    scheduleScroll();
  }

  // newStreamSegment creates a <div.md.md-stream> with a single text node child.
  // Deltas are appended to data on the text node — no element re-creation.
  function newStreamSegment() {
    const div = document.createElement('div');
    div.className = 'md md-stream';
    div.appendChild(document.createTextNode(''));
    return div;
  }

  function appendTextDelta(delta) {
    if (!currentAssistantTurn) startAssistantBubble();
    // If the most recent child of .content is a tool detail (not our stream
    // segment), start a fresh segment so text appears after the tool block.
    const last = currentAssistantTurn.content.lastChild;
    if (last !== currentAssistantTurn.stream) {
      const seg = newStreamSegment();
      currentAssistantTurn.content.appendChild(seg);
      currentAssistantTurn.stream = seg;
    }
    currentAssistantTurn.stream.firstChild.data += delta;
    scheduleScroll();
  }

  function finishAssistantBubble() {
    if (!currentAssistantTurn) return;
    currentAssistantTurn.bubble.classList.remove('streaming');
    const cursor = currentAssistantTurn.bubble.querySelector('.cursor');
    if (cursor) cursor.remove();
    // Parse each accumulated text segment as markdown.
    const segments = currentAssistantTurn.content.querySelectorAll('.md-stream');
    segments.forEach((seg) => {
      const raw = seg.textContent || '';
      seg.classList.remove('md-stream');
      seg.innerHTML = renderMarkdown(raw);
    });
    currentAssistantTurn = null;
    scheduleScroll();
  }

  function startToolCall(id, name) {
    if (!currentAssistantTurn) startAssistantBubble();
    const details = document.createElement('details');
    details.className = 'tool-call';
    const summary = document.createElement('summary');
    summary.textContent = `▸ ${name}(...)`;
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
    let short = el.args.textContent;
    if (short.length > 60) short = short.slice(0, 60) + '...';
    el.summary.textContent = `▸ ${el.name}(${short})`;
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

  // renderAllMarkdown walks every .md element under root and parses its raw
  // markdown (from data-raw if present, else textContent) into HTML. Called
  // once on page load for server-rendered history blocks.
  function renderAllMarkdown(root) {
    root.querySelectorAll('.md').forEach((el) => {
      if (el.classList.contains('md-stream')) return;
      const raw = el.dataset.raw != null ? el.dataset.raw : el.textContent;
      el.innerHTML = renderMarkdown(raw);
    });
  }
})();
