// Phase 7: ALERT_DIAGNOSIS toast.
//
// When the hub's AI diagnoser annotates a fresh alert, the result rides
// the same WebSocket as metrics. We surface it as a non-blocking toast
// in the bottom-right corner with the rule name + 2-line summary; click
// dismisses, auto-dismiss after 30 s.
//
// The legacy frontend doesn't have an alerts panel yet, so this is the
// minimal user-visible touchpoint. Phase 8+ wires diagnoses into a full
// alerts inbox.

const HOST_ID = 'argon-toasts';
const AUTO_DISMISS_MS = 30_000;

function ensureHost() {
    let host = document.getElementById(HOST_ID);
    if (host) return host;
    host = document.createElement('div');
    host.id = HOST_ID;
    host.className = 'argon-toasts';
    document.body.appendChild(host);
    return host;
}

function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[c]);
}

function renderMarkdownLite(s) {
    // Minimal — same shape as ai-chat.js's renderer but with no code
    // blocks since diagnoses are short prose.
    const escaped = escapeHtml(s || '');
    return escaped
        .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
        .replace(/`([^`\n]+)`/g, '<code>$1</code>')
        .replace(/\n/g, '<br>');
}

function showDiagnosis(d) {
    const host = ensureHost();
    const card = document.createElement('div');
    card.className = 'argon-toast argon-toast-diagnosis';
    card.innerHTML = `
        <div class="argon-toast-head">
            <span class="argon-toast-icon">📋</span>
            <strong>${escapeHtml(d.ruleName || 'Diagnosis')}</strong>
            <span class="argon-toast-meta">${escapeHtml(d.serverId || '')}</span>
            <button type="button" class="argon-toast-close" aria-label="Dismiss">×</button>
        </div>
        <div class="argon-toast-body">${renderMarkdownLite(d.summary || '')}</div>
    `;
    host.appendChild(card);
    requestAnimationFrame(() => card.classList.add('in'));

    const dismiss = () => {
        card.classList.remove('in');
        setTimeout(() => card.remove(), 220);
    };
    card.querySelector('.argon-toast-close').addEventListener('click', dismiss);
    setTimeout(dismiss, AUTO_DISMISS_MS);
}

export function initDiagnosisToast(ws) {
    if (!ws || typeof ws.onEnvelope !== 'function') return;
    ws.onEnvelope((env) => {
        if (env && env.type === 'ALERT_DIAGNOSIS' && env.payload) {
            showDiagnosis(env.payload);
        }
    });
}
