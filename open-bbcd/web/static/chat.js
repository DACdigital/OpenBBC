// chat.js — vanilla JS chat client.
// Subscribes to AG-UI SSE stream from POST /agents/{id}/chat/{session_id}/turn.
//
// AG-UI events on the wire:
//   event: TYPE
//   data: <JSON>
//   \n
//
// fetch + ReadableStream (not EventSource) because AG-UI is POST → SSE
// and EventSource is GET-only.

(function () {
  const log = document.getElementById('chat-log');
  if (!log) return;

  const input = document.getElementById('chat-input');
  const sendBtn = document.getElementById('chat-send');
  const agentID = log.dataset.agentId;
  const sessionID = log.dataset.sessionId;
  if (!agentID || !sessionID) return;

  let currentAssistantBubble = null;
  const toolCallElements = new Map();

  sendBtn.addEventListener('click', send);
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      send();
    }
  });

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
        // Read the response body — the server puts the actual error message
        // there. Without this, the user only sees "HTTP 500" with no hint of
        // what went wrong (missing env var, DB issue, etc.).
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
    } catch (err) {
      showError(err.message || String(err));
    } finally {
      finishAssistantBubble();
      sendBtn.disabled = false;
      input.disabled = false;
      input.focus();
    }
  }

  let pendingEvent = null;
  let pendingData = '';
  function processSSELine(line) {
    if (line.startsWith('event:')) {
      pendingEvent = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      pendingData += line.slice(5).trim();
    } else if (line === '' && pendingEvent) {
      handleEvent(pendingEvent, pendingData);
      pendingEvent = null;
      pendingData = '';
    }
  }

  function handleEvent(type, dataStr) {
    let data = {};
    try { data = JSON.parse(dataStr); } catch (_e) {}

    switch (type) {
      case 'TEXT_MESSAGE_START':
        break;
      case 'TEXT_MESSAGE_CONTENT':
        appendTextDelta(data.delta || '');
        break;
      case 'TEXT_MESSAGE_END':
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
      case 'RUN_FINISHED':
        break;
      case 'RUN_ERROR':
        showError(data.message || 'unknown error');
        break;
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
    content.textContent = text;
    b.appendChild(role);
    b.appendChild(content);
    log.appendChild(b);
    b.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }

  function startAssistantBubble() {
    const b = document.createElement('div');
    b.className = 'chat-bubble assistant streaming';
    const role = document.createElement('div');
    role.className = 'role-label';
    role.textContent = '[assistant]';
    const content = document.createElement('div');
    content.className = 'content';
    const cursor = document.createElement('span');
    cursor.className = 'cursor';
    cursor.textContent = '▌';
    b.appendChild(role);
    b.appendChild(content);
    b.appendChild(cursor);
    log.appendChild(b);
    currentAssistantBubble = b;
    b.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }

  function appendTextDelta(delta) {
    if (!currentAssistantBubble) startAssistantBubble();
    const content = currentAssistantBubble.querySelector('.content');
    content.textContent += delta;
    currentAssistantBubble.scrollIntoView({ block: 'end' });
  }

  function finishAssistantBubble() {
    if (!currentAssistantBubble) return;
    currentAssistantBubble.classList.remove('streaming');
    const cursor = currentAssistantBubble.querySelector('.cursor');
    if (cursor) cursor.remove();
    currentAssistantBubble = null;
  }

  function startToolCall(id, name) {
    if (!currentAssistantBubble) startAssistantBubble();
    const details = document.createElement('details');
    details.className = 'tool-call';
    const summary = document.createElement('summary');
    summary.textContent = `▸ ${name}(...)`;
    const args = document.createElement('pre');
    args.className = 'args';
    details.appendChild(summary);
    details.appendChild(args);
    currentAssistantBubble.querySelector('.content').appendChild(details);
    toolCallElements.set(id, { summary, args, name });
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
    if (!currentAssistantBubble) return;
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
    currentAssistantBubble.querySelector('.content').appendChild(details);
  }

  function showError(msg) {
    const b = document.createElement('div');
    b.className = 'chat-error';
    b.textContent = `Error: ${msg}`;
    log.appendChild(b);
    b.scrollIntoView({ block: 'end' });
  }
})();
