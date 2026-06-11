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

(function () {
  console.info('[chat.js] loaded — AG-UI SSE client v2');

  const log = document.getElementById('chat-log');
  if (!log) return;

  const input = document.getElementById('chat-input');
  const sendBtn = document.getElementById('chat-send');
  const agentID = log.dataset.agentId;
  const sessionID = log.dataset.sessionId;
  if (!agentID || !sessionID) return;

  // currentAssistantTurn holds per-turn DOM refs. textNode is a dedicated
  // child of `.content` so streaming deltas never wipe sibling <details>
  // elements (which textContent += would do).
  let currentAssistantTurn = null;
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
      if (buf) processSSELine(buf); // flush trailing partial
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
        // No render side-effect; cursor cleanup happens in finishAssistantBubble.
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
    // Dedicated text node — deltas append here without touching sibling
    // tool-detail elements that we'll add inside `.content` later.
    const textNode = document.createTextNode('');
    content.appendChild(textNode);
    const cursor = document.createElement('span');
    cursor.className = 'cursor';
    cursor.textContent = '▌';
    b.appendChild(role);
    b.appendChild(content);
    b.appendChild(cursor);
    log.appendChild(b);
    currentAssistantTurn = { bubble: b, content, textNode };
    b.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }

  function appendTextDelta(delta) {
    if (!currentAssistantTurn) startAssistantBubble();
    // If a tool detail was just rendered, deltas should start a fresh text run
    // after it — otherwise the text would visually merge into the tool block.
    const last = currentAssistantTurn.content.lastChild;
    if (last && last.nodeType !== Node.TEXT_NODE) {
      const t = document.createTextNode('');
      currentAssistantTurn.content.appendChild(t);
      currentAssistantTurn.textNode = t;
    }
    currentAssistantTurn.textNode.data += delta;
    currentAssistantTurn.bubble.scrollIntoView({ block: 'end' });
  }

  function finishAssistantBubble() {
    if (!currentAssistantTurn) return;
    currentAssistantTurn.bubble.classList.remove('streaming');
    const cursor = currentAssistantTurn.bubble.querySelector('.cursor');
    if (cursor) cursor.remove();
    currentAssistantTurn = null;
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
  }

  function showError(msg) {
    const b = document.createElement('div');
    b.className = 'chat-error';
    b.textContent = `Error: ${msg}`;
    log.appendChild(b);
    b.scrollIntoView({ block: 'end' });
  }
})();
