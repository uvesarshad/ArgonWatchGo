// GitHub Actions panel. Lives as a special "github" entry in the
// server-tabs strip — multi-server.js looks for the ALL_SERVERS sentinel
// and the "github" sentinel and routes them to dedicated views.
//
// The panel is server-rendered once from /api/v2/github/runs on first
// open, then patched live whenever a GH_WORKFLOW_RUNS envelope arrives.

const GITHUB_SERVER_ID = 'github';

const state = {
    enabled: false,        // true once we've confirmed the poller is up
    runs: [],
    repos: [],
    rate: { limit: 0, remaining: 0, resetAt: '' },
    ws: null,
    container: null,
};

function authHeaders() {
    const token = localStorage.getItem('auth_token');
    return token ? { 'Authorization': `Bearer ${token}` } : {};
}

async function fetchSnapshot() {
    const res = await fetch('/api/v2/github/runs', { headers: authHeaders() });
    if (res.status === 404) {
        // Poller not wired (no github.enabled in config) — leave the
        // tab hidden so the user doesn't see an empty panel.
        state.enabled = false;
        return false;
    }
    if (!res.ok) {
        throw new Error(`GitHub snapshot: ${res.status}`);
    }
    const snap = await res.json();
    state.runs = snap.runs || [];
    state.repos = snap.repos || [];
    state.rate = snap.rate || state.rate;
    state.enabled = true;
    return true;
}

function ingest(env) {
    if (!env || env.type !== 'GH_WORKFLOW_RUNS' || env.serverId !== GITHUB_SERVER_ID) return;
    const snap = env.payload || {};
    state.runs = snap.runs || [];
    state.repos = snap.repos || [];
    state.rate = snap.rate || state.rate;
    if (isVisible()) render();
}

function isVisible() {
    return state.container && state.container.style.display !== 'none';
}

export function isEnabled() { return state.enabled; }

export async function initActionsPanel(ws) {
    state.ws = ws;
    state.container = document.getElementById('actions-container');
    if (!state.container) return;

    try {
        const ok = await fetchSnapshot();
        if (!ok) return;
    } catch (e) {
        console.warn('actions-panel: snapshot fetch failed', e);
        return;
    }

    ws.onEnvelope(ingest);
    render();
}

export function show() {
    if (!state.container) return;
    state.container.style.display = '';
    render();
}

export function hide() {
    if (!state.container) return;
    state.container.style.display = 'none';
}

function render() {
    if (!state.container) return;
    const rate = state.rate || {};
    const ratePct = rate.limit > 0 ? Math.round((rate.remaining / rate.limit) * 100) : 100;
    const reset = rate.resetAt ? new Date(rate.resetAt).toLocaleTimeString() : '—';

    state.container.innerHTML = `
        <section class="actions-header">
            <div>
                <h1>GitHub Actions</h1>
                <p class="actions-help">Workflow runs across ${state.repos.length} configured repo${state.repos.length === 1 ? '' : 's'}.</p>
            </div>
            <div class="actions-rate" title="GitHub API rate limit">
                <span>${rate.remaining || 0} / ${rate.limit || 0}</span>
                <small>resets ${reset}</small>
            </div>
        </section>
        <section class="actions-repos">
            ${state.repos.map(r => repoChip(r)).join('')}
        </section>
        <section class="actions-runs">
            ${state.runs.length === 0 ? emptyState() : runsTable(state.runs)}
        </section>
    `;
}

function repoChip(r) {
    const error = r.lastError ? ` data-error="${escapeAttr(r.lastError)}"` : '';
    return `<span class="repo-chip"${error}>
        <strong>${escapeHtml(r.fullName)}</strong>
        <small>${r.runCount} runs · ${formatRelative(r.lastFetched)}</small>
    </span>`;
}

function runsTable(runs) {
    const rows = runs.slice(0, 100).map(r => `
        <tr data-url="${escapeAttr(r.html_url || '')}">
            <td>${statusBadge(r)}</td>
            <td class="run-repo">${escapeHtml(r.repository?.fullName || '')}</td>
            <td class="run-name">${escapeHtml(r.name || '')}</td>
            <td class="run-branch">${escapeHtml(r.head_branch || '')}</td>
            <td class="run-actor">${escapeHtml(r.actor?.login || '')}</td>
            <td class="run-time" title="${escapeAttr(r.updated_at || '')}">${formatRelative(r.updated_at)}</td>
        </tr>
    `).join('');
    return `
        <table class="runs-table">
            <thead>
                <tr><th></th><th>Repo</th><th>Workflow</th><th>Branch</th><th>Actor</th><th>Updated</th></tr>
            </thead>
            <tbody>${rows}</tbody>
        </table>
    `;
}

function statusBadge(r) {
    let kind = 'pending';
    let label = r.status || '';
    if (r.status === 'completed') {
        kind = (r.conclusion === 'success') ? 'ok'
            : (r.conclusion === 'failure' || r.conclusion === 'timed_out') ? 'fail'
            : (r.conclusion === 'cancelled' || r.conclusion === 'skipped') ? 'skip'
            : 'pending';
        label = r.conclusion || 'completed';
    } else if (r.status === 'in_progress') {
        kind = 'running'; label = 'running';
    }
    return `<span class="run-badge run-badge-${kind}" title="${escapeAttr(label)}">${escapeHtml(label)}</span>`;
}

function emptyState() {
    return `<div class="actions-empty">
        <h3>No runs yet</h3>
        <p>Either nothing has run on the configured repos in the last fetch window, or the poller is still warming up.</p>
    </div>`;
}

// Delegated click → open run on GitHub. Keeps the table small; no per-
// row event handler attachment cost.
document.addEventListener('click', (ev) => {
    const tr = ev.target.closest('.runs-table tbody tr');
    if (!tr || !tr.dataset.url) return;
    window.open(tr.dataset.url, '_blank', 'noopener');
});

function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[c]);
}
function escapeAttr(s) { return escapeHtml(s); }

function formatRelative(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    if (isNaN(d.getTime())) return String(ts);
    const delta = Date.now() - d.getTime();
    if (delta < 60_000) return `${Math.max(1, Math.floor(delta / 1000))}s ago`;
    if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m ago`;
    if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`;
    return `${Math.floor(delta / 86_400_000)}d ago`;
}
