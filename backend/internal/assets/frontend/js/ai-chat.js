// AI assistant chat sidebar. Phase 6 ships non-streaming chat against
// Claude or Gemini (chosen in Settings). Suggested commands rendered in
// tool results get a "Run in terminal" button that prefills the
// terminal panel — the locked plan §3 guarantees commands are NEVER
// auto-run.

const state = {
    open: false,
    sending: false,
    conversationId: null,
    provider: null,        // chosen provider name (claude / gemini)
    providers: [],         // [{ provider, modelChat, ... }]
    history: [],           // conversation list summaries
    messages: [],          // current conversation messages
    enabled: false,        // true once /api/v2/ai/providers returns 200
};

function authHeaders() {
    const token = localStorage.getItem('auth_token');
    return token ? { 'Authorization': `Bearer ${token}` } : {};
}

async function api(method, path, body) {
    const headers = { 'Content-Type': 'application/json', ...authHeaders() };
    const init = { method, headers };
    if (body !== undefined) init.body = JSON.stringify(body);
    const res = await fetch(path, init);
    if (!res.ok) {
        const text = await res.text();
        throw new Error(`${method} ${path}: ${res.status} ${text}`);
    }
    if (res.status === 204) return null;
    return await res.json();
}

async function fetchProviders() {
    try {
        const data = await api('GET', '/api/v2/ai/providers');
        state.providers = data.providers || [];
        state.enabled = true;
        if (!state.provider && state.providers.length > 0) {
            state.provider = state.providers[0].provider;
        }
        return true;
    } catch (e) {
        // 404 → AI feature off (auth disabled / no vault). Stay quiet.
        state.enabled = false;
        return false;
    }
}

async function fetchHistory() {
    try {
        const data = await api('GET', '/api/v2/ai/conversations');
        state.history = data.conversations || [];
    } catch (e) { state.history = []; }
}

async function loadConversation(id) {
    try {
        const c = await api('GET', `/api/v2/ai/conversations/${id}`);
        state.conversationId = c.id;
        state.messages = c.messages || [];
        state.provider = providerForModel(c.model) || state.provider;
        renderMessages();
        renderSidebar();
    } catch (e) {
        console.error('load conversation failed', e);
    }
}

function providerForModel(model) {
    if (!model) return null;
    if (model.toLowerCase().includes('claude')) return 'claude';
    if (model.toLowerCase().includes('gemini')) return 'gemini';
    return null;
}

async function sendMessage(text) {
    if (state.sending) return;
    if (!text || !text.trim()) return;
    state.sending = true;

    // Optimistic render so the user sees their message immediately.
    state.messages.push({ role: 'user', content: text });
    renderMessages();
    renderInput();

    try {
        const data = await api('POST', '/api/v2/ai/chat', {
            conversationId: state.conversationId,
            provider: state.provider || undefined,
            message: text,
        });
        state.conversationId = data.conversationId;
        state.messages = data.messages;
        renderMessages();
        // Refresh sidebar so the new title shows up.
        fetchHistory().then(renderSidebar);
    } catch (e) {
        state.messages.push({
            role: 'assistant',
            content: `_(error: ${escapeHtml(e.message)})_`,
        });
        renderMessages();
    } finally {
        state.sending = false;
        renderInput();
    }
}

async function deleteConversation(id) {
    if (!confirm('Delete this conversation?')) return;
    try {
        await api('DELETE', `/api/v2/ai/conversations/${id}`);
        if (state.conversationId === id) {
            state.conversationId = null;
            state.messages = [];
            renderMessages();
        }
        await fetchHistory();
        renderSidebar();
    } catch (e) {
        alert('Failed to delete: ' + e.message);
    }
}

function newConversation() {
    state.conversationId = null;
    state.messages = [];
    renderMessages();
    renderSidebar();
    const input = document.getElementById('ai-input');
    if (input) input.focus();
}

// ----- rendering -----

function renderShell() {
    const root = document.getElementById('ai-chat-root');
    if (!root) return;
    if (!state.enabled) { root.style.display = 'none'; return; }
    root.style.display = '';
    root.innerHTML = `
        <button type="button" class="ai-fab" id="ai-fab" title="AI Assistant">
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M3 12c0-5 4-9 9-9s9 4 9 9-4 9-9 9c-1.7 0-3.3-.5-4.7-1.3L3 21l1.3-4.3C3.5 15.3 3 13.7 3 12z"/>
            </svg>
        </button>
        <aside class="ai-panel" id="ai-panel" aria-hidden="true">
            <header class="ai-panel-head">
                <div class="ai-head-title">
                    <h3>AI Assistant</h3>
                    <select id="ai-provider-select" class="ai-provider-select"></select>
                </div>
                <div class="ai-head-actions">
                    <button type="button" id="ai-new-btn" title="New conversation">+</button>
                    <button type="button" id="ai-close-btn" title="Close">×</button>
                </div>
            </header>
            <div class="ai-panel-body">
                <nav class="ai-history" id="ai-history" aria-label="Past conversations"></nav>
                <section class="ai-conversation">
                    <div class="ai-messages" id="ai-messages"></div>
                    <form class="ai-input-row" id="ai-input-form">
                        <textarea id="ai-input" rows="2" placeholder="Ask about a server, alert, or metric…" autocomplete="off"></textarea>
                        <button type="submit" id="ai-send-btn">Send</button>
                    </form>
                </section>
            </div>
        </aside>
    `;
    document.getElementById('ai-fab').addEventListener('click', toggle);
    document.getElementById('ai-close-btn').addEventListener('click', () => setOpen(false));
    document.getElementById('ai-new-btn').addEventListener('click', newConversation);
    document.getElementById('ai-input-form').addEventListener('submit', (ev) => {
        ev.preventDefault();
        const ta = document.getElementById('ai-input');
        const text = ta.value;
        ta.value = '';
        sendMessage(text);
    });
    document.getElementById('ai-input').addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter' && !ev.shiftKey) {
            ev.preventDefault();
            document.getElementById('ai-input-form').requestSubmit();
        }
    });

    const sel = document.getElementById('ai-provider-select');
    sel.addEventListener('change', () => { state.provider = sel.value; });

    renderProviderSelect();
    renderSidebar();
    renderMessages();
    renderInput();
}

function renderProviderSelect() {
    const sel = document.getElementById('ai-provider-select');
    if (!sel) return;
    sel.innerHTML = state.providers.map(p => `
        <option value="${escapeAttr(p.provider)}" ${p.provider === state.provider ? 'selected' : ''}>
            ${escapeHtml(p.provider)}${p.modelChat ? ` · ${escapeHtml(p.modelChat)}` : ''}
        </option>
    `).join('') || '<option value="">No keys configured</option>';
}

function renderSidebar() {
    const root = document.getElementById('ai-history');
    if (!root) return;
    if (state.history.length === 0) {
        root.innerHTML = `<p class="ai-history-empty">No past chats yet.</p>`;
        return;
    }
    root.innerHTML = state.history.map(c => `
        <button type="button" class="ai-history-item ${c.id === state.conversationId ? 'active' : ''}" data-id="${escapeAttr(c.id)}">
            <span class="ai-history-title">${escapeHtml(c.title || 'untitled')}</span>
            <span class="ai-history-meta">${escapeHtml(c.model || '')} · ${formatRelative(c.updatedAt)}</span>
            <span class="ai-history-del" data-del="${escapeAttr(c.id)}" title="Delete">×</span>
        </button>
    `).join('');
    root.querySelectorAll('.ai-history-item').forEach(el => {
        el.addEventListener('click', (ev) => {
            if (ev.target.dataset.del) {
                ev.stopPropagation();
                deleteConversation(ev.target.dataset.del);
                return;
            }
            loadConversation(el.dataset.id);
        });
    });
}

function renderMessages() {
    const root = document.getElementById('ai-messages');
    if (!root) return;
    if (state.messages.length === 0) {
        root.innerHTML = `
            <div class="ai-empty">
                <p>Try:</p>
                <ul>
                    <li>"Why is CPU spiking on prod-web-01?"</li>
                    <li>"Show me the last 5 alerts"</li>
                    <li>"What workflow runs failed today?"</li>
                </ul>
            </div>`;
        return;
    }
    root.innerHTML = state.messages
        .map(messageHTML)
        .filter(Boolean)
        .join('');
    // Auto-scroll to the latest message.
    root.scrollTop = root.scrollHeight;
}

function messageHTML(m) {
    if (m.role === 'system') return ''; // never shown
    if (m.role === 'tool') {
        // Tool result blocks render as a small collapsed pill with the
        // command-suggestion shortcut surfaced inline.
        const suggestion = extractSuggestion(m.content);
        if (suggestion) {
            return `<div class="ai-suggestion">
                <div class="ai-suggestion-cmd">${escapeHtml(suggestion.command)}</div>
                <div class="ai-suggestion-meta">on <code>${escapeHtml(suggestion.serverId)}</code> · ${escapeHtml(suggestion.rationale)}</div>
                <button type="button" class="ai-suggestion-run"
                    data-server="${escapeAttr(suggestion.serverId)}"
                    data-command="${escapeAttr(suggestion.command)}">Run in terminal →</button>
            </div>`;
        }
        return `<details class="ai-tool"><summary>${escapeHtml(m.name || 'tool')}</summary><pre>${escapeHtml(truncate(m.content, 1200))}</pre></details>`;
    }
    const klass = m.role === 'user' ? 'ai-msg ai-msg-user' : 'ai-msg ai-msg-assistant';
    return `<div class="${klass}">${renderMarkdown(m.content || '')}</div>`;
}

function extractSuggestion(toolBody) {
    try {
        const obj = JSON.parse(toolBody);
        if (obj && obj.kind === 'command_suggestion' && obj.command) {
            return { command: obj.command, serverId: obj.serverId || '', rationale: obj.rationale || '' };
        }
    } catch (e) { /* not JSON */ }
    return null;
}

// Run-in-terminal: delegated click handler. Cross-module wiring — we
// just set the args on window.argonRunInTerminal and let terminal-panel.js
// pick them up the next time it's opened.
document.addEventListener('click', (ev) => {
    const btn = ev.target.closest('.ai-suggestion-run');
    if (!btn) return;
    const server = btn.dataset.server;
    const command = btn.dataset.command;
    window.argonRunInTerminal = { server, command };
    // Switch to that server's tab if not already there.
    if (window.location.hash !== `#/server/${server}`) {
        window.location.hash = `#/server/${server}`;
    }
    // Surface a hint that the operator still must click Open.
    btn.textContent = 'Open terminal & paste ↑';
});

function renderInput() {
    const btn = document.getElementById('ai-send-btn');
    const ta = document.getElementById('ai-input');
    if (!btn || !ta) return;
    btn.disabled = state.sending;
    ta.disabled = state.sending;
    btn.textContent = state.sending ? 'Thinking…' : 'Send';
}

function setOpen(open) {
    state.open = open;
    const panel = document.getElementById('ai-panel');
    if (panel) {
        panel.setAttribute('aria-hidden', open ? 'false' : 'true');
        panel.classList.toggle('open', open);
    }
    if (open) document.getElementById('ai-input')?.focus();
}
function toggle() { setOpen(!state.open); }

// ----- markdown rendering -----

// Tiny inline renderer — covers code blocks, inline code, bold, italic,
// links, and line breaks. We deliberately don't pull in marked.js or
// similar to keep the cold-start payload small; the assistant's
// formatting needs aren't ambitious.
function renderMarkdown(src) {
    if (!src) return '';
    const escapedSrc = escapeHtml(src);
    let html = escapedSrc.replace(/```([a-z0-9_-]*)\n([\s\S]*?)```/g,
        (_, lang, body) => `<pre class="ai-codeblock"><code data-lang="${lang}">${body}</code></pre>`);
    html = html.replace(/`([^`\n]+)`/g, '<code>$1</code>');
    html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    html = html.replace(/\b_([^_\n]+)_\b/g, '<em>$1</em>');
    html = html.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g,
        '<a href="$2" target="_blank" rel="noopener">$1</a>');
    html = html.replace(/\n/g, '<br>');
    return html;
}

// ----- utilities -----

function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[c]);
}
function escapeAttr(s) { return escapeHtml(s); }

function truncate(s, n) {
    if (!s) return '';
    if (s.length <= n) return s;
    return s.slice(0, n) + '…';
}

function formatRelative(ts) {
    if (!ts) return '';
    const delta = Date.now() - ts;
    if (delta < 60_000) return `${Math.floor(delta / 1000)}s ago`;
    if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`;
    if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`;
    return `${Math.floor(delta / 86_400_000)}d ago`;
}

// ----- bootstrap -----

export async function initAIChat() {
    const ok = await fetchProviders();
    renderShell();
    if (!ok) return;
    await fetchHistory();
    renderSidebar();
}
