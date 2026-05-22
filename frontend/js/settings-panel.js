// Settings panel — consolidates everything that previously required
// editing config.json by hand:
//   • AI providers (key paste + Test connection)
//   • Telegram bot (bot token + chat IDs + Test message)
//   • GitHub PAT + watched repos
//   • Alert rules (read-only view of what's loaded; full editor is Phase 8)
//
// Renders as a slide-in panel from the right edge — same affordance as
// the AI chat sidebar so the muscle memory transfers. Admin-only:
// non-admin users see a "view-only" badge and the inputs disable.

const TABS = [
    { id: 'ai',       label: 'AI Providers' },
    { id: 'telegram', label: 'Telegram' },
    { id: 'github',   label: 'GitHub Actions' },
    { id: 'alerts',   label: 'Alert Rules' },
];

const state = {
    open: false,
    activeTab: 'ai',
    me: null,
    providers: [],
    serverConfig: null, // /api/config snapshot
};

function authHeaders(extra = {}) {
    const t = localStorage.getItem('auth_token');
    return { ...extra, ...(t ? { 'Authorization': `Bearer ${t}` } : {}) };
}

async function api(method, path, body) {
    const init = { method, headers: { ...authHeaders(), 'Content-Type': 'application/json' } };
    if (body !== undefined) init.body = JSON.stringify(body);
    const res = await fetch(path, init);
    if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(`${res.status} ${text || res.statusText}`);
    }
    if (res.status === 204) return null;
    return await res.json();
}

// ----- shell -----

function ensureRoot() {
    let root = document.getElementById('settings-root');
    if (root) return root;
    root = document.createElement('div');
    root.id = 'settings-root';
    document.body.appendChild(root);
    return root;
}

function open() {
    state.open = true;
    render();
    refresh().catch(e => console.warn('settings refresh', e));
}

function close() {
    state.open = false;
    render();
}

function isAdmin() {
    return state.me && (state.me.role === 'admin' || !state.me.role);
}

async function refresh() {
    try { state.me = await api('GET', '/api/auth/me'); }
    catch (e) { state.me = null; }
    try { const data = await api('GET', '/api/v2/ai/providers'); state.providers = data.providers || []; }
    catch (e) { state.providers = []; }
    try { state.serverConfig = await api('GET', '/api/config'); }
    catch (e) { state.serverConfig = null; }
    renderBody();
}

function render() {
    const root = ensureRoot();
    root.innerHTML = `
        <div class="settings-backdrop" data-close></div>
        <aside class="settings-panel" role="dialog" aria-label="Settings">
            <header class="settings-head">
                <h2>Settings</h2>
                <button type="button" class="icon-btn" id="settings-close" aria-label="Close">×</button>
            </header>
            <nav class="settings-tabs" id="settings-tabs"></nav>
            <div class="settings-body" id="settings-body">Loading…</div>
        </aside>
    `;
    root.classList.toggle('open', state.open);
    if (!state.open) return;
    document.getElementById('settings-close').addEventListener('click', close);
    root.querySelector('[data-close]').addEventListener('click', close);
    renderTabs();
    renderBody();
}

function renderTabs() {
    const nav = document.getElementById('settings-tabs');
    if (!nav) return;
    nav.innerHTML = TABS.map(t => `
        <button type="button" class="settings-tab ${t.id === state.activeTab ? 'active' : ''}" data-tab="${t.id}">${t.label}</button>
    `).join('');
    nav.querySelectorAll('.settings-tab').forEach(b => {
        b.addEventListener('click', () => { state.activeTab = b.dataset.tab; renderTabs(); renderBody(); });
    });
}

function renderBody() {
    const body = document.getElementById('settings-body');
    if (!body) return;
    if (!isAdmin() && state.me) {
        body.innerHTML = `<div class="settings-readonly">View-only: settings can only be changed by admin users.</div>`;
    } else {
        body.innerHTML = '';
    }
    switch (state.activeTab) {
        case 'ai':       renderAI(body); break;
        case 'telegram': renderTelegram(body); break;
        case 'github':   renderGithub(body); break;
        case 'alerts':   renderAlerts(body); break;
    }
}

// ----- AI tab -----

function renderAI(body) {
    const editable = isAdmin();
    const claude = state.providers.find(p => p.provider === 'claude');
    const gemini = state.providers.find(p => p.provider === 'gemini');

    body.insertAdjacentHTML('beforeend', `
        <p class="settings-help">
            Paste an API key for at least one provider. Keys are encrypted at rest with
            AES-256-GCM and never returned by the API after storage.
        </p>
        ${providerCard('claude', 'Anthropic Claude', 'sk-ant-…', claude, editable)}
        ${providerCard('gemini', 'Google Gemini', 'AIza…', gemini, editable)}
    `);

    body.querySelectorAll('[data-save-provider]').forEach(btn => {
        btn.addEventListener('click', async () => {
            const provider = btn.dataset.saveProvider;
            const card = btn.closest('.settings-card');
            const apiKey = card.querySelector('[data-key]').value.trim();
            const chatModel = card.querySelector('[data-chat-model]').value.trim();
            if (!apiKey) { toast('Paste a key first', 'warn'); return; }
            btn.disabled = true; btn.textContent = 'Saving…';
            try {
                await api('POST', '/api/v2/ai/providers', { provider, apiKey, chatModel });
                toast(`${provider} key saved`, 'ok');
                card.querySelector('[data-key]').value = '';
                await refresh();
            } catch (e) {
                toast(`Save failed: ${e.message}`, 'error');
                btn.disabled = false; btn.textContent = 'Save';
            }
        });
    });
    body.querySelectorAll('[data-test-provider]').forEach(btn => {
        btn.addEventListener('click', async () => {
            const provider = btn.dataset.testProvider;
            const card = btn.closest('.settings-card');
            const apiKey = card.querySelector('[data-key]').value.trim();
            if (!apiKey) { toast('Paste a key first', 'warn'); return; }
            btn.disabled = true; btn.textContent = 'Testing…';
            try {
                await api('POST', `/api/v2/ai/providers/${provider}/test`, { provider, apiKey });
                toast(`${provider}: key valid ✓`, 'ok');
            } catch (e) {
                toast(`${provider} test failed: ${e.message}`, 'error');
            } finally {
                btn.disabled = false; btn.textContent = 'Test connection';
            }
        });
    });
    body.querySelectorAll('[data-delete-provider]').forEach(btn => {
        btn.addEventListener('click', async () => {
            const provider = btn.dataset.deleteProvider;
            if (!confirm(`Remove ${provider} key?`)) return;
            try {
                await api('DELETE', `/api/v2/ai/providers/${provider}`);
                toast(`${provider} key removed`, 'ok');
                await refresh();
            } catch (e) { toast(`Failed: ${e.message}`, 'error'); }
        });
    });
}

function providerCard(provider, label, placeholder, record, editable) {
    const hasKey = !!record;
    return `
        <section class="settings-card" data-provider="${provider}">
            <div class="settings-card-head">
                <h3>${escapeHtml(label)}</h3>
                <span class="settings-pill ${hasKey ? 'pill-ok' : 'pill-muted'}">${hasKey ? 'key set' : 'no key'}</span>
            </div>
            ${hasKey ? `
                <p class="settings-meta">Last updated ${formatRelative(record.updatedAt)}${record.modelChat ? ` · model <code>${escapeHtml(record.modelChat)}</code>` : ''}</p>
            ` : ''}
            <label class="settings-label">API key</label>
            <input type="password" data-key placeholder="${escapeAttr(placeholder)}" autocomplete="off" ${editable ? '' : 'disabled'}/>
            <label class="settings-label">Chat model (optional)</label>
            <input type="text" data-chat-model placeholder="${provider === 'claude' ? 'claude-sonnet-4-6' : 'gemini-2.0-flash'}" ${editable ? '' : 'disabled'}/>
            ${editable ? `
                <div class="settings-actions">
                    <button type="button" class="btn-secondary" data-test-provider="${provider}">Test connection</button>
                    ${hasKey ? `<button type="button" class="btn-ghost" data-delete-provider="${provider}">Delete</button>` : ''}
                    <button type="button" class="btn-primary" data-save-provider="${provider}">Save</button>
                </div>
            ` : ''}
        </section>
    `;
}

// ----- Telegram tab -----

function renderTelegram(body) {
    const tg = state.serverConfig?.notifications?.telegram || { enabled: false, chatIds: [] };
    body.insertAdjacentHTML('beforeend', `
        <p class="settings-help">
            Create a bot with <a href="https://t.me/BotFather" target="_blank" rel="noopener">@BotFather</a>,
            paste the token below, then list one or more chat IDs to deliver alerts to.
            Use <a href="https://t.me/userinfobot" target="_blank" rel="noopener">@userinfobot</a> to find your chat ID.
        </p>
        <section class="settings-card">
            <div class="settings-card-head">
                <h3>Telegram bot</h3>
                <span class="settings-pill ${tg.enabled ? 'pill-ok' : 'pill-muted'}">${tg.enabled ? 'enabled' : 'disabled'}</span>
            </div>
            <p class="settings-meta">
                ${tg.enabled ? `Currently delivering to ${(tg.chatIds || []).length} chat(s).` :
                    'Not yet configured. Edit <code>config.json</code> under <code>notifications.telegram</code> — Phase 5 keeps this read-only until the live config-edit endpoint lands (Phase 8).'}
            </p>
        </section>
    `);
}

// ----- GitHub tab -----

function renderGithub(body) {
    const gh = state.serverConfig?.github || { enabled: false, repos: [] };
    body.insertAdjacentHTML('beforeend', `
        <p class="settings-help">
            Get a Personal Access Token at <a href="https://github.com/settings/tokens" target="_blank" rel="noopener">github.com/settings/tokens</a>
            with <code>repo</code> + <code>workflow</code> scopes. Edit <code>config.json</code> under <code>github</code> to enable.
        </p>
        <section class="settings-card">
            <div class="settings-card-head">
                <h3>GitHub Actions poller</h3>
                <span class="settings-pill ${gh.enabled ? 'pill-ok' : 'pill-muted'}">${gh.enabled ? 'enabled' : 'disabled'}</span>
            </div>
            <p class="settings-meta">
                ${gh.enabled
                    ? `Watching ${(gh.repos || []).length} repo(s).`
                    : 'Disabled. ETag-cached polls run every <code>pollInterval</code> ms (default 60s) when enabled.'}
            </p>
        </section>
    `);
}

// ----- Alerts tab -----

function renderAlerts(body) {
    const rules = state.serverConfig?.alerts?.rules || [];
    body.insertAdjacentHTML('beforeend', `
        <p class="settings-help">
            Alert rules are loaded from <code>config.json</code>. Live editor lands in Phase 8.
        </p>
        <section class="settings-card">
            <div class="settings-card-head">
                <h3>${rules.length} rule(s) loaded</h3>
            </div>
            ${rules.length === 0 ? `
                <div class="settings-empty">No rules configured. Add <code>alerts.rules</code> in <code>config.json</code> to start receiving notifications.</div>
            ` : `
                <table class="settings-table">
                    <thead><tr><th>Name</th><th>Metric</th><th>Threshold</th><th>Severity</th><th>Servers</th></tr></thead>
                    <tbody>
                        ${rules.map(r => `
                            <tr>
                                <td>${escapeHtml(r.name || r.id)}</td>
                                <td><code>${escapeHtml(r.metric || '')}</code></td>
                                <td>${escapeHtml(r.condition || '')} ${escapeHtml(String(r.threshold ?? ''))}</td>
                                <td>${escapeHtml(r.severity || '')}</td>
                                <td>${(r.serverIds && r.serverIds.length) ? r.serverIds.map(escapeHtml).join(', ') : 'all'}</td>
                            </tr>
                        `).join('')}
                    </tbody>
                </table>
            `}
        </section>
    `);
}

// ----- small helpers -----

function toast(message, kind) {
    const host = document.getElementById('argon-toasts') || (() => {
        const h = document.createElement('div'); h.id = 'argon-toasts'; h.className = 'argon-toasts';
        document.body.appendChild(h); return h;
    })();
    const card = document.createElement('div');
    card.className = `argon-toast argon-toast-${kind || 'info'}`;
    card.innerHTML = `<div class="argon-toast-body">${escapeHtml(message)}</div>`;
    host.appendChild(card);
    requestAnimationFrame(() => card.classList.add('in'));
    setTimeout(() => { card.classList.remove('in'); setTimeout(() => card.remove(), 220); }, 4000);
}

function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));
}
function escapeAttr(s) { return escapeHtml(s); }
function formatRelative(ts) {
    if (!ts) return 'never';
    const delta = Date.now() - ts;
    if (delta < 60_000) return `${Math.floor(delta / 1000)}s ago`;
    if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`;
    if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`;
    return `${Math.floor(delta / 86_400_000)}d ago`;
}

// ----- public API -----

export function initSettingsPanel() {
    ensureRoot();
    const btn = document.getElementById('settings-btn');
    if (btn) btn.addEventListener('click', open);

    // Keyboard shortcut: ',' opens settings (mirrors VS Code / GitHub).
    document.addEventListener('keydown', (ev) => {
        if (ev.key === 'Escape' && state.open) { close(); return; }
        if (ev.key === ',' && (ev.ctrlKey || ev.metaKey)) {
            ev.preventDefault();
            state.open ? close() : open();
        }
    });
}

// Allow other modules to open the panel programmatically (e.g. AI chat
// when no provider key exists yet).
export function openSettings(tab) {
    if (tab && TABS.some(t => t.id === tab)) state.activeTab = tab;
    open();
}
