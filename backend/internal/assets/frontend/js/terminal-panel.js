// Terminal panel: xterm.js attached to a hub-minted session WebSocket.
//
// Flow when the user opens the panel:
//   1. POST /api/v2/servers/<currentServerId>/terminal → { sessionId }
//   2. Open ws://.../ws/terminal/<sessionId>
//   3. Wire xterm.js: keystrokes → {type:"input", data:"…"}
//                     resize → {type:"resize", cols, rows}
//                     server → {type:"output", data:<base64>} → term.write
//
// xterm.js + addon-fit load from CDN (matched to the dependency we ship
// in index.html). The panel itself is a simple toggle inside the
// per-server dashboard.

const XTERM_VERSION = '5.5.0';

let xtermLoaded = false;

// Lazy-load xterm.js the first time the user actually opens a terminal —
// keeps the cold-start payload small for the 80% of users who never
// touch the terminal.
async function ensureXtermLoaded() {
    if (xtermLoaded) return;
    await Promise.all([
        loadScript(`https://cdn.jsdelivr.net/npm/@xterm/xterm@${XTERM_VERSION}/lib/xterm.min.js`),
        loadStylesheet(`https://cdn.jsdelivr.net/npm/@xterm/xterm@${XTERM_VERSION}/css/xterm.min.css`),
    ]);
    await loadScript(`https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/lib/addon-fit.min.js`);
    xtermLoaded = true;
}

function loadScript(src) {
    return new Promise((res, rej) => {
        const s = document.createElement('script');
        s.src = src; s.onload = res; s.onerror = () => rej(new Error('script load: ' + src));
        document.head.appendChild(s);
    });
}
function loadStylesheet(href) {
    return new Promise((res, rej) => {
        const l = document.createElement('link');
        l.rel = 'stylesheet'; l.href = href; l.onload = res; l.onerror = () => rej(new Error('css load: ' + href));
        document.head.appendChild(l);
    });
}

const state = {
    term: null,
    fitAddon: null,
    ws: null,
    sessionId: null,
    serverId: null,
    container: null,
    statusEl: null,
};

async function mintSession(serverId) {
    const token = localStorage.getItem('auth_token');
    const headers = { 'Content-Type': 'application/json' };
    if (token) headers['Authorization'] = `Bearer ${token}`;
    const res = await fetch(`/api/v2/servers/${encodeURIComponent(serverId)}/terminal`, {
        method: 'POST', headers, body: '{}',
    });
    if (!res.ok) {
        const msg = await res.text();
        throw new Error(`mint terminal session: ${res.status} ${msg}`);
    }
    return await res.json();
}

function setStatus(text, kind) {
    if (!state.statusEl) return;
    state.statusEl.textContent = text;
    state.statusEl.dataset.kind = kind || '';
}

function teardown() {
    if (state.ws) {
        try { state.ws.close(); } catch (_) { /* ignore */ }
        state.ws = null;
    }
    if (state.term) {
        state.term.dispose();
        state.term = null;
        state.fitAddon = null;
    }
    state.sessionId = null;
}

async function openTerminal(serverId) {
    if (!serverId || serverId === '__all__') {
        setStatus('Pick a server tab first.', 'warn');
        return;
    }

    teardown();
    setStatus('Allocating session…', 'pending');

    try {
        await ensureXtermLoaded();
    } catch (e) {
        setStatus('Failed to load xterm.js (network).', 'error');
        return;
    }

    let session;
    try {
        session = await mintSession(serverId);
    } catch (e) {
        setStatus(e.message, 'error');
        return;
    }

    state.serverId = serverId;
    state.sessionId = session.sessionId;

    // Build xterm.
    const term = new window.Terminal({
        cursorBlink: true,
        fontFamily: 'JetBrains Mono, monospace',
        fontSize: 13,
        theme: {
            background: '#0b1220',
            foreground: '#e2e8f0',
            cursor: '#3b82f6',
            selectionBackground: 'rgba(59, 130, 246, 0.3)',
        },
        // 'canvas' is sharper than 'dom' but heavier; defer to default
        // (canvas) which xterm picks based on browser capability.
        scrollback: 4000,
        convertEol: true,
    });
    const fitAddon = new window.FitAddon.FitAddon();
    term.loadAddon(fitAddon);

    const host = document.getElementById('terminal-host');
    host.innerHTML = '';
    term.open(host);
    fitAddon.fit();
    state.term = term;
    state.fitAddon = fitAddon;

    // Open the browser WS. JWT in query so the standard reverse-proxy
    // WS config keeps working without extra headers.
    const token = localStorage.getItem('auth_token') || '';
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${proto}//${window.location.host}/ws/terminal/${session.sessionId}?token=${encodeURIComponent(token)}`);
    state.ws = ws;

    ws.onopen = () => {
        setStatus('Connected', 'ok');
        // Push the agent the initial size so the shell renders correctly.
        const { cols, rows } = term;
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
        term.focus();
    };

    ws.onmessage = (ev) => {
        try {
            const msg = JSON.parse(ev.data);
            if (msg.type === 'output' && typeof msg.data === 'string') {
                // data is base64 from the Go side ([]byte JSON-encoded).
                const bytes = b64ToBytes(msg.data);
                term.write(bytes);
            } else if (msg.type === 'closed') {
                setStatus('Session ended.', 'warn');
                term.write('\r\n\x1b[33m[session closed by hub]\x1b[0m\r\n');
            }
        } catch (e) { /* ignore non-JSON frames */ }
    };

    ws.onerror = () => setStatus('Connection error.', 'error');
    ws.onclose = () => setStatus('Disconnected.', 'warn');

    // Input: send every keystroke as base64-encoded data so binary keys
    // (arrow keys, ctrl-c) survive the JSON trip.
    term.onData((data) => {
        if (ws.readyState !== WebSocket.OPEN) return;
        ws.send(JSON.stringify({
            type: 'input',
            // Use bytesToB64 so binary input (UTF-8 high bytes, ctrl
            // chars) round-trips losslessly.
            data: bytesToB64(new TextEncoder().encode(data)),
        }));
    });

    // Resize: ResizeObserver triggers a fit, which updates cols/rows
    // and sends the new size to the agent.
    const ro = new ResizeObserver(() => {
        if (!state.fitAddon) return;
        try { state.fitAddon.fit(); } catch (_) { /* container hidden */ }
        if (ws.readyState !== WebSocket.OPEN) return;
        const { cols, rows } = term;
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
    });
    ro.observe(host);
}

function b64ToBytes(s) {
    const raw = atob(s);
    const out = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
}

function bytesToB64(bytes) {
    let bin = '';
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin);
}

// Public entry point — wired from app.js after the DOM is ready.
export function initTerminalPanel() {
    state.container = document.getElementById('terminal-card');
    state.statusEl = document.getElementById('terminal-status');
    if (!state.container) return;

    const openBtn = document.getElementById('terminal-open-btn');
    const closeBtn = document.getElementById('terminal-close-btn');

    if (openBtn) {
        openBtn.addEventListener('click', () => {
            const sid = (window.argonApp && window.argonApp.currentServerId) || 'local';
            openTerminal(sid);
        });
    }
    if (closeBtn) {
        closeBtn.addEventListener('click', () => {
            teardown();
            setStatus('Closed.', '');
        });
    }

    // Mobile keyboard toolbar: send a single byte or escape sequence so
    // touch users can hit Tab / Esc / Ctrl-C without a real keyboard.
    document.querySelectorAll('#terminal-keybar [data-key]').forEach(btn => {
        btn.addEventListener('click', () => {
            const ws = state.ws;
            if (!ws || ws.readyState !== WebSocket.OPEN) return;
            const key = btn.dataset.key;
            // Translation table — keep small + obvious. Anything beyond
            // these falls through unchanged.
            const map = {
                'tab': '\t', 'esc': '\x1b', 'ctrl-c': '\x03', 'ctrl-d': '\x04',
                'up': '\x1b[A', 'down': '\x1b[B', 'right': '\x1b[C', 'left': '\x1b[D',
            };
            const data = map[key] || key;
            ws.send(JSON.stringify({
                type: 'input',
                data: bytesToB64(new TextEncoder().encode(data)),
            }));
            if (state.term) state.term.focus();
        });
    });
}
