// Minimal service worker. Two jobs:
//   1. Cache the app shell so the UI loads instantly on repeat visits and
//      survives a flaky connection long enough to show "Disconnected".
//   2. NEVER cache /api/* or /ws/* — those are live data and must hit the
//      network every time, falling back to a clear error rather than
//      serving stale metrics.

const SHELL_CACHE = 'argonwatch-shell-v2';
const SHELL_ASSETS = [
    '/',
    '/index.html',
    '/css/theme.css',
    '/css/styles.css',
    '/css/graph-tabs.css',
    '/css/graphs-grid.css',
    '/css/multi-server.css',
    '/css/charts-v2.css',
    '/css/terminal.css',
    '/css/actions.css',
    '/css/ai-chat.css',
    '/css/toasts.css',
    '/css/refined.css',
    '/css/responsive.css',
    '/js/app.js',
    '/js/multi-server.js',
    '/js/theme.js',
    '/js/session-check.js',
    '/js/chart-enhancer.js',
    '/js/terminal-panel.js',
    '/js/actions-panel.js',
    '/js/ai-chat.js',
    '/js/diagnosis-toast.js',
    '/js/settings-panel.js',
    '/js/utils/gauge.js',
    '/js/utils/websocket.js',
    '/manifest.json',
    '/icons/icon-192.svg',
    '/icons/icon-512.svg',
];

self.addEventListener('install', (event) => {
    event.waitUntil(
        caches.open(SHELL_CACHE).then((cache) => cache.addAll(SHELL_ASSETS))
            .then(() => self.skipWaiting())
            .catch((e) => console.warn('SW install: shell precache partial', e))
    );
});

self.addEventListener('activate', (event) => {
    // Drop any stale shell caches from previous deploys.
    event.waitUntil(
        caches.keys().then((keys) => Promise.all(
            keys.filter((k) => k !== SHELL_CACHE).map((k) => caches.delete(k))
        )).then(() => self.clients.claim())
    );
});

self.addEventListener('fetch', (event) => {
    const req = event.request;
    const url = new URL(req.url);

    // Never cache API or WebSocket traffic. Network-only, no fallback —
    // a 503 from a real failure is more honest than stale telemetry.
    if (url.pathname.startsWith('/api/') ||
        url.pathname.startsWith('/ws') ||
        url.pathname.startsWith('/agent') ||
        url.pathname.startsWith('/install.')) {
        return; // let the browser handle it directly
    }

    // Shell + static assets: cache-first, fall through to network, then
    // update the cache opportunistically so the next visit is fresh.
    if (req.method === 'GET') {
        event.respondWith(
            caches.match(req).then((cached) => {
                const networked = fetch(req).then((res) => {
                    if (res && res.status === 200) {
                        const copy = res.clone();
                        caches.open(SHELL_CACHE).then((c) => c.put(req, copy));
                    }
                    return res;
                }).catch(() => cached);
                return cached || networked;
            })
        );
    }
});
