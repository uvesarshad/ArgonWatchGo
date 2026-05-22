// v2: the wire envelope is { type, serverId, ts, payload }. The WSClient
// surfaces serverId to listeners and filters telemetry messages by the
// currently-selected server when serverFilter is set.

// Telemetry messages are scoped to a specific server. When a filter is
// active, only envelopes matching the filter are dispatched. Non-telemetry
// types (alerts, history snapshots, connection state) always pass through.
const TELEMETRY_TYPES = new Set([
    'SYSTEM_METRICS',
    'SERVICE_STATUS',
    'DATABASE_STATUS',
    'PM2_STATUS',
    'GH_RUNNER_STATUS',
    'LOG_TAIL',
    'TERMINAL_OUTPUT',
]);

export class WebSocketClient {
    constructor(url) {
        this.url = url;
        this.ws = null;
        this.listeners = new Map();
        this.envelopeListeners = new Set();
        this.reconnectAttempts = 0;
        this.maxReconnectAttempts = 5;

        // null = pass everything through; "local" or a uuid = telemetry must
        // match this server, other messages still pass.
        this.serverFilter = null;
    }

    setServerFilter(serverId) {
        this.serverFilter = serverId || null;
    }

    connect() {
        const token = localStorage.getItem('auth_token');
        const wsUrl = token ? `${this.url}?token=${encodeURIComponent(token)}` : this.url;

        this.ws = new WebSocket(wsUrl);

        this.ws.onopen = () => {
            console.log('Connected to server');
            this.reconnectAttempts = 0;
            this.notify('CONNECTION_STATUS', { status: 'connected' });
        };

        this.ws.onmessage = (event) => {
            try {
                const env = JSON.parse(event.data);
                // Mirror the envelope to wildcard listeners (used by the
                // multi-server tab manager to snapshot every server's
                // latest metrics regardless of the active filter).
                this.envelopeListeners.forEach(cb => cb(env));

                // Telemetry gating: when a filter is active, drop messages
                // for other servers so the per-server dashboard only sees
                // its own data.
                if (this.serverFilter && TELEMETRY_TYPES.has(env.type)) {
                    if (env.serverId && env.serverId !== this.serverFilter) return;
                }
                this.notify(env.type, env.payload, env);
            } catch (e) {
                console.error('Failed to parse message', e);
            }
        };

        this.ws.onclose = () => {
            console.log('Disconnected from server');
            this.notify('CONNECTION_STATUS', { status: 'disconnected' });
            this.handleReconnect();
        };

        this.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
        };
    }

    handleReconnect() {
        if (this.reconnectAttempts < this.maxReconnectAttempts) {
            this.reconnectAttempts++;
            setTimeout(() => this.connect(), 3000 * this.reconnectAttempts);
        }
    }

    on(type, callback) {
        if (!this.listeners.has(type)) {
            this.listeners.set(type, new Set());
        }
        this.listeners.get(type).add(callback);
    }

    // onEnvelope subscribes to every inbound envelope, bypassing the
    // server filter. Use sparingly — intended for the multi-server tab
    // manager that needs a live view of all connected agents.
    onEnvelope(callback) {
        this.envelopeListeners.add(callback);
        return () => this.envelopeListeners.delete(callback);
    }

    off(type, callback) {
        if (this.listeners.has(type)) {
            this.listeners.get(type).delete(callback);
        }
    }

    notify(type, payload, envelope) {
        if (this.listeners.has(type)) {
            this.listeners.get(type).forEach(callback => callback(payload, envelope));
        }
    }

    send(type, payload) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({ type, payload }));
        }
    }
}
