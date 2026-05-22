// v2 chart enhancements layered on top of the existing Chart.js instances:
//
//   • Brush-to-zoom + click-drag pan via chartjs-plugin-zoom
//   • Range selector (1h / 6h / 24h / 7d) that re-pulls scoped history
//   • Anomaly band: rolling z-score, |z|>3 highlighted as a second dataset
//
// Stays opt-in so the existing app.js call sites need only one new line
// per chart:
//
//   enhanceChart(this.charts.cpu, { canvasId: 'cpu-chart', metric: 'cpu' });
//
// The current server ID is read off window.argonApp at fetch time so the
// chart automatically rebinds when the user switches server tabs.

const RANGES = [
    { id: '1h',  label: '1H' },
    { id: '6h',  label: '6H' },
    { id: '24h', label: '24H' },
    { id: '7d',  label: '7D' },
];

// Track per-chart state out of band so we don't pollute the Chart instance.
const chartState = new WeakMap();

let zoomRegistered = false;
function ensureZoomRegistered() {
    if (zoomRegistered) return;
    // The plugin attaches itself to window.ChartZoom (UMD). It's already
    // registered by chartjs-plugin-zoom's UMD bundle, but Chart.js v4
    // requires explicit registration when plugins are loaded after the
    // Chart constructor was already used. Guard for missing plugin so
    // offline previews don't crash.
    if (typeof window !== 'undefined' && window.Chart && window.ChartZoom) {
        try { window.Chart.register(window.ChartZoom); } catch (_) { /* no-op */ }
    }
    zoomRegistered = true;
}

export function enhanceChart(chart, opts) {
    if (!chart) return;
    const { canvasId, metric, color = '#3b82f6' } = opts;
    if (chartState.has(chart)) return; // idempotent

    ensureZoomRegistered();

    // Inject the range selector + zoom-reset above the chart canvas.
    const canvas = document.getElementById(canvasId);
    if (canvas && canvas.parentElement) {
        const bar = buildControlBar(metric);
        canvas.parentElement.insertBefore(bar.el, canvas);
        bar.onRangeChange = (range) => loadRange(chart, metric, range);
        bar.onReset = () => {
            if (chart.resetZoom) chart.resetZoom();
        };
    }

    // Configure brush-to-zoom on the X axis.
    if (window.ChartZoom && chart.options) {
        chart.options.plugins = chart.options.plugins || {};
        chart.options.plugins.zoom = {
            limits: { x: { minRange: 30 * 1000 } }, // can't zoom in below 30s
            zoom: {
                wheel: { enabled: true, modifierKey: 'ctrl' },
                drag:  { enabled: true, backgroundColor: 'rgba(59, 130, 246, 0.15)' },
                mode: 'x',
            },
            pan: {
                enabled: true,
                mode: 'x',
                modifierKey: 'shift',
            },
        };
        chart.update('none');
    }

    // Add a second (hidden by default) dataset for anomaly markers.
    if (chart.data && chart.data.datasets) {
        chart.data.datasets.push({
            label: 'Anomaly',
            data: [],
            type: 'line',
            showLine: false,
            pointRadius: 4,
            pointBackgroundColor: '#ef4444',
            pointBorderColor: '#ef4444',
            order: -1,
        });
        chart.update('none');
    }

    chartState.set(chart, { metric, range: '1h', color });
}

// recomputeAnomalies should be called by callers when the primary dataset
// changes. Computes a rolling z-score over the last N values and replaces
// the anomaly dataset.
//
// Window size of 60 strikes the right balance: long enough for the baseline
// to stabilize on a 1Hz feed, short enough that a real regime change
// quickly becomes the new normal.
export function recomputeAnomalies(chart) {
    if (!chart || !chart.data || !chart.data.datasets) return;
    const primary = chart.data.datasets[0];
    const anomalyDS = chart.data.datasets[chart.data.datasets.length - 1];
    if (!primary || !anomalyDS || anomalyDS.label !== 'Anomaly') return;

    const values = primary.data;
    if (!Array.isArray(values) || values.length < 10) {
        anomalyDS.data = [];
        return;
    }

    const N = Math.min(60, values.length);
    const window = values.slice(-N);
    const mean = window.reduce((a, b) => a + b, 0) / N;
    const variance = window.reduce((a, b) => a + (b - mean) ** 2, 0) / N;
    const std = Math.sqrt(variance);

    if (std < 0.5) {
        // Essentially flat — anything would be a spurious "anomaly". Skip.
        anomalyDS.data = values.map(() => null);
        return;
    }

    anomalyDS.data = values.map(v => {
        const z = (v - mean) / std;
        return Math.abs(z) > 3 ? v : null;
    });
}

function buildControlBar(metricName) {
    const el = document.createElement('div');
    el.className = 'chart-controls';
    el.setAttribute('role', 'toolbar');
    el.setAttribute('aria-label', `${metricName} chart controls`);

    const ranges = document.createElement('div');
    ranges.className = 'range-selector';
    let activeBtn = null;
    let onRangeChange = () => {};

    for (const r of RANGES) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'range-btn';
        btn.textContent = r.label;
        btn.dataset.range = r.id;
        if (r.id === '1h') {
            btn.classList.add('active');
            activeBtn = btn;
        }
        btn.addEventListener('click', () => {
            if (activeBtn) activeBtn.classList.remove('active');
            btn.classList.add('active');
            activeBtn = btn;
            onRangeChange(r.id);
        });
        ranges.appendChild(btn);
    }

    const reset = document.createElement('button');
    reset.type = 'button';
    reset.className = 'chart-reset-btn';
    reset.title = 'Reset zoom';
    reset.setAttribute('aria-label', 'Reset zoom');
    reset.textContent = '↺';

    let onReset = () => {};
    reset.addEventListener('click', () => onReset());

    el.appendChild(ranges);
    el.appendChild(reset);

    // Returning a thin handle so the caller can wire callbacks AFTER
    // we've appended the controls (lets callers reference the chart
    // instance without circular setup).
    const handle = { el };
    Object.defineProperty(handle, 'onRangeChange', {
        set(fn) { onRangeChange = fn; }
    });
    Object.defineProperty(handle, 'onReset', {
        set(fn) { onReset = fn; }
    });
    return handle;
}

async function loadRange(chart, metric, range) {
    const serverId = (window.argonApp && window.argonApp.currentServerId) || 'local';
    const token = localStorage.getItem('auth_token');
    const headers = token ? { 'Authorization': `Bearer ${token}` } : {};
    const url = `/api/v2/servers/${encodeURIComponent(serverId)}/history/${metric}?duration=${range}`;

    try {
        const res = await fetch(url, { headers });
        if (!res.ok) throw new Error(`fetch ${url}: ${res.status}`);
        const points = await res.json();
        applyPoints(chart, points || []);
        const state = chartState.get(chart);
        if (state) state.range = range;
    } catch (e) {
        console.error('chart-enhancer: load range failed', e);
    }
}

function applyPoints(chart, points) {
    if (!Array.isArray(points)) points = [];
    const labels = points.map(p => formatLabel(p.timestamp));
    const values = points.map(p => p.value);

    chart.data.labels = labels;
    chart.data.datasets[0].data = values;
    recomputeAnomalies(chart);
    // Reset any zoom state since the underlying domain changed.
    if (chart.resetZoom) chart.resetZoom();
    chart.update('none');
}

function formatLabel(ts) {
    const d = new Date(ts);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}
