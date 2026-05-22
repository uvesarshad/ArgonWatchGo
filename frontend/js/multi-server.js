// Multi-server tab manager. Renders the server tab strip + add-server
// modal, owns the "All servers" overview view, and coordinates with the
// legacy single-server dashboard by toggling the WebSocketClient's
// serverFilter.
//
// Phase 1 keeps app.js's per-server dashboard untouched. When the user is
// on the "All servers" view, the dashboard is hidden and the overview
// grid takes over; clicking a server card switches the filter and
// re-shows the dashboard.

const ALL_SERVERS = '__all__';
const GITHUB_TAB  = '__github__'; // virtual tab — routed to actions-panel.js

const state = {
    servers: [],          // [{ id, name, status, os, version, lastSeen }]
    currentId: ALL_SERVERS, // default landing per locked decision (§9.5)
    snapshots: new Map(), // id -> { cpu, memory, disk, network, lastSeen }
    ws: null,
    githubAvailable: false, // set true once initActionsPanel confirms the poller is up
    actions: null,          // dynamically imported module handle
};

function authHeaders() {
    const token = localStorage.getItem('auth_token');
    return token ? { 'Authorization': `Bearer ${token}` } : {};
}

async function fetchServers() {
    const res = await fetch('/api/v2/servers', { headers: authHeaders() });
    if (!res.ok) throw new Error(`fetch servers: ${res.status}`);
    state.servers = await res.json();
}

async function mintServer(name, tags) {
    const res = await fetch('/api/v2/servers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ name, tags }),
    });
    if (!res.ok) throw new Error(await res.text());
    return await res.json();
}

async function deleteServer(id) {
    const res = await fetch(`/api/v2/servers/${id}`, {
        method: 'DELETE',
        headers: authHeaders(),
    });
    if (!res.ok) throw new Error(await res.text());
}

function ensureSnapshot(serverId) {
    if (!state.snapshots.has(serverId)) {
        state.snapshots.set(serverId, { cpu: 0, memory: 0, disk: 0, network: 0, lastSeen: 0 });
    }
    return state.snapshots.get(serverId);
}

function ingestEnvelope(env) {
    if (!env || !env.type || !env.serverId) return;
    const snap = ensureSnapshot(env.serverId);
    snap.lastSeen = env.ts || Date.now();

    if (env.type === 'SYSTEM_METRICS' && env.payload) {
        const p = env.payload;
        if (p.cpu && typeof p.cpu.load === 'number') snap.cpu = p.cpu.load;
        if (p.memory && typeof p.memory.percentage === 'number') snap.memory = p.memory.percentage;
        if (Array.isArray(p.disk) && p.disk.length > 0) {
            // Use the first non-trivial partition as a representative figure.
            const main = p.disk.find(d => d.size > 0) || p.disk[0];
            if (main && typeof main.use === 'number') snap.disk = main.use;
        }
        if (Array.isArray(p.network)) {
            const total = p.network.reduce((acc, n) => acc + (n.rx_sec || 0) + (n.tx_sec || 0), 0);
            snap.network = total;
        }
        // Update server's online status — receiving metrics is the
        // ground truth, not the registry's stale view.
        const srv = state.servers.find(s => s.id === env.serverId);
        if (srv && srv.status !== 'online') {
            srv.status = 'online';
            renderTabs();
        }
    }

    if (state.currentId === ALL_SERVERS) renderOverview();
    else if (env.serverId === state.currentId) renderTabs(); // refresh status dot only
}

function setCurrentServer(id) {
    state.currentId = id;
    // Hide every primary view first; the chosen tab re-shows its own.
    hideAllPrimary();
    if (id === ALL_SERVERS) {
        state.ws.setServerFilter(null);
        showOverview();
    } else if (id === GITHUB_TAB) {
        state.ws.setServerFilter(null);
        if (state.actions) state.actions.show();
    } else {
        state.ws.setServerFilter(id);
        showDashboard();
        // Re-request historical data for the newly-selected server.
        state.ws.send('GET_HISTORICAL_DATA', { duration: '1h', serverId: id });
    }
    renderTabs();
    if (window.location.hash !== routeFor(id)) {
        window.location.hash = routeFor(id);
    }
}

function hideAllPrimary() {
    const dash = document.querySelector('.dashboard-container');
    const ov = document.getElementById('overview-container');
    const ac = document.getElementById('actions-container');
    if (dash) dash.style.display = 'none';
    if (ov) ov.style.display = 'none';
    if (ac) ac.style.display = 'none';
}

function routeFor(id) {
    if (id === ALL_SERVERS) return '#/overview';
    if (id === GITHUB_TAB)  return '#/github';
    return `#/server/${id}`;
}

function applyRoute() {
    const hash = window.location.hash || '#/overview';
    if (hash === '#/overview' || hash === '#/') {
        setCurrentServer(ALL_SERVERS);
        return;
    }
    if (hash === '#/github' && state.githubAvailable) {
        setCurrentServer(GITHUB_TAB);
        return;
    }
    const m = hash.match(/^#\/server\/([^/]+)/);
    if (m) {
        const id = decodeURIComponent(m[1]);
        if (state.servers.some(s => s.id === id)) {
            setCurrentServer(id);
            return;
        }
    }
    // Unknown route: fall back to overview.
    setCurrentServer(ALL_SERVERS);
}

// ---------- DOM rendering ----------

function renderTabs() {
    const root = document.getElementById('server-tabs');
    if (!root) return;
    root.innerHTML = '';

    // "All Servers" tab is always first.
    root.appendChild(makeTab(ALL_SERVERS, 'All Servers', null));

    // GitHub Actions tab — only when the poller is configured.
    if (state.githubAvailable) {
        root.appendChild(makeTab(GITHUB_TAB, 'GitHub Actions', 'online'));
    }

    for (const s of state.servers) {
        root.appendChild(makeTab(s.id, s.name, s.status));
    }

    const add = document.createElement('button');
    add.type = 'button';
    add.className = 'server-tab add-server-btn';
    add.textContent = '+ Add Server';
    add.addEventListener('click', openAddServerModal);
    root.appendChild(add);
}

function makeTab(id, name, status) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'server-tab';
    if (id === state.currentId) btn.classList.add('active');
    if (status) {
        const dot = document.createElement('span');
        dot.className = `tab-dot ${status === 'online' ? 'dot-online' : 'dot-offline'}`;
        btn.appendChild(dot);
    }
    const label = document.createElement('span');
    label.textContent = name;
    btn.appendChild(label);
    if (id !== '__all__' && id !== '__github__' && id !== 'local') {
        const close = document.createElement('span');
        close.className = 'tab-close';
        close.textContent = '×';
        close.title = 'Remove server';
        close.addEventListener('click', async (ev) => {
            ev.stopPropagation();
            if (!confirm(`Remove server "${name}"? Its token will be revoked.`)) return;
            try {
                await deleteServer(id);
                state.servers = state.servers.filter(s => s.id !== id);
                state.snapshots.delete(id);
                if (state.currentId === id) setCurrentServer(ALL_SERVERS);
                else renderTabs();
            } catch (e) {
                alert(`Failed to remove: ${e.message}`);
            }
        });
        btn.appendChild(close);
    }
    btn.addEventListener('click', () => setCurrentServer(id));
    return btn;
}

function showOverview() {
    const ov = document.getElementById('overview-container');
    if (ov) ov.style.display = '';
    renderOverview();
}

function showDashboard() {
    const dash = document.querySelector('.dashboard-container');
    if (dash) dash.style.display = '';
}

function renderOverview() {
    const root = document.getElementById('overview-grid');
    if (!root) return;
    root.innerHTML = '';

    if (state.servers.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'overview-empty';
        empty.innerHTML = `
            <h3>No servers connected yet</h3>
            <p>Click <strong>+ Add Server</strong> above to mint an enrollment token and connect your first agent.</p>
        `;
        root.appendChild(empty);
        return;
    }

    for (const s of state.servers) {
        const snap = state.snapshots.get(s.id) || { cpu: 0, memory: 0, disk: 0, network: 0 };
        const card = document.createElement('div');
        card.className = 'overview-card';
        card.innerHTML = `
            <div class="overview-card-header">
                <span class="tab-dot ${s.status === 'online' ? 'dot-online' : 'dot-offline'}"></span>
                <h3>${escapeHtml(s.name)}</h3>
                <span class="overview-card-os">${escapeHtml(s.os || '')}</span>
            </div>
            <div class="overview-metrics">
                ${metricRow('CPU',    snap.cpu)}
                ${metricRow('Memory', snap.memory)}
                ${metricRow('Disk',   snap.disk)}
                <div class="overview-metric">
                    <span class="overview-metric-label">Net I/O</span>
                    <span class="overview-metric-value">${formatBytes(snap.network)}/s</span>
                </div>
            </div>
            <div class="overview-card-footer">
                ${s.status === 'online' ? 'Online' : `Last seen ${formatRelative(s.lastSeen)}`}
            </div>
        `;
        card.addEventListener('click', () => setCurrentServer(s.id));
        root.appendChild(card);
    }
}

function metricRow(label, pct) {
    const p = Math.max(0, Math.min(100, pct || 0));
    const cls = p > 80 ? 'high' : p > 60 ? 'med' : 'low';
    return `
        <div class="overview-metric">
            <div class="overview-metric-head">
                <span class="overview-metric-label">${label}</span>
                <span class="overview-metric-value">${p.toFixed(1)}%</span>
            </div>
            <div class="overview-bar"><div class="overview-bar-fill ${cls}" style="width:${p}%"></div></div>
        </div>
    `;
}

// ---------- Add-server modal ----------

function openAddServerModal() {
    const modal = document.getElementById('add-server-modal');
    if (!modal) return;
    modal.innerHTML = `
        <div class="modal-backdrop"></div>
        <div class="modal-card">
            <h2>Add Server</h2>
            <p class="modal-help">Mint a one-time token, then paste the install command on the target host.</p>
            <div id="add-server-step1">
                <label for="add-server-name">Server name</label>
                <input id="add-server-name" type="text" placeholder="prod-web-01" autocomplete="off" />
                <div class="modal-actions">
                    <button type="button" class="btn-secondary" data-close>Cancel</button>
                    <button type="button" class="btn-primary" id="add-server-create">Generate token</button>
                </div>
            </div>
            <div id="add-server-step2" hidden></div>
        </div>
    `;
    modal.classList.add('open');
    modal.querySelectorAll('[data-close]').forEach(el => el.addEventListener('click', closeAddServerModal));
    modal.querySelector('.modal-backdrop').addEventListener('click', closeAddServerModal);

    const nameInput = modal.querySelector('#add-server-name');
    nameInput.focus();
    modal.querySelector('#add-server-create').addEventListener('click', async () => {
        const name = nameInput.value.trim();
        if (!name) { nameInput.focus(); return; }
        try {
            const result = await mintServer(name, {});
            renderTokenStep(modal, result);
            // Optimistically add to the in-memory list; real reconcile happens via WS.
            state.servers.push(result.server);
            renderTabs();
        } catch (e) {
            alert(`Failed to mint server: ${e.message}`);
        }
    });
}

function renderTokenStep(modal, result) {
    modal.querySelector('#add-server-step1').hidden = true;
    const step2 = modal.querySelector('#add-server-step2');
    step2.hidden = false;
    step2.innerHTML = `
        <p><strong>Token created.</strong> Copy one of the commands below and run it on the target host. This is the only time the token will be shown.</p>

        <label>Linux / macOS</label>
        <pre class="install-cmd" data-copy>${escapeHtml(result.installLinux)}</pre>

        <label>Windows (PowerShell, admin)</label>
        <pre class="install-cmd" data-copy>${escapeHtml(result.installWindows)}</pre>

        <details class="install-manual">
            <summary>Manual / advanced</summary>
            <p>Run directly with the binary:</p>
            <pre class="install-cmd" data-copy>argon-watch --mode=agent --hub=${escapeHtml(result.manualHubUrl)} --token=${escapeHtml(result.manualToken)} --config="" </pre>
            <p>Or write <code>agent.config.json</code>:</p>
            <pre class="install-cmd" data-copy>${escapeHtml(JSON.stringify({
                hubUrl: result.manualHubUrl,
                serverId: result.manualServerId,
                token: result.manualToken,
            }, null, 2))}</pre>
        </details>

        <div class="modal-actions">
            <button type="button" class="btn-primary" data-close>Done</button>
        </div>
    `;
    step2.querySelectorAll('pre[data-copy]').forEach(pre => {
        pre.addEventListener('click', () => {
            navigator.clipboard.writeText(pre.textContent).then(() => {
                pre.classList.add('copied');
                setTimeout(() => pre.classList.remove('copied'), 800);
            });
        });
    });
    step2.querySelectorAll('[data-close]').forEach(el => el.addEventListener('click', closeAddServerModal));
}

function closeAddServerModal() {
    const modal = document.getElementById('add-server-modal');
    if (modal) modal.classList.remove('open');
}

// ---------- helpers ----------

function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[c]);
}

function formatBytes(n) {
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(n < 10 ? 1 : 0)} ${units[i]}`;
}

function formatRelative(ts) {
    if (!ts) return 'never';
    const delta = Date.now() - ts;
    if (delta < 60_000) return `${Math.floor(delta / 1000)}s ago`;
    if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`;
    if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`;
    return `${Math.floor(delta / 86_400_000)}d ago`;
}

// ---------- bootstrap ----------

export async function initMultiServer(ws) {
    state.ws = ws;
    try {
        await fetchServers();
    } catch (e) {
        console.error('multi-server: initial server fetch failed', e);
    }

    // Probe GitHub Actions poller. The /api/v2/github/runs endpoint
    // 404s when github.enabled=false, in which case we leave the tab
    // hidden — the user shouldn't see chrome for a feature that's off.
    try {
        const actions = await import('./actions-panel.js');
        await actions.initActionsPanel(ws);
        if (actions.isEnabled()) {
            state.githubAvailable = true;
            state.actions = actions;
        }
    } catch (e) {
        console.warn('multi-server: actions panel unavailable', e);
    }

    ws.onEnvelope(ingestEnvelope);
    window.addEventListener('hashchange', applyRoute);
    renderTabs();
    applyRoute();

    // Refresh registry view every 15s — picks up status changes when no
    // metrics are flowing (e.g. an agent went offline).
    setInterval(async () => {
        try {
            await fetchServers();
            renderTabs();
            if (state.currentId === ALL_SERVERS) renderOverview();
        } catch (e) { /* ignore — transient */ }
    }, 15_000);
}
