// v2 radial gauge: minimal ring + inline sparkline.
//
// Replaces the v1 needle-style gauge with something that reads at a glance
// and gives context about the trend without a separate chart. Constructor
// signature is preserved (canvasId, { maxValue, color, label }) so the
// existing app.js call sites work unchanged. The trailing sparkline buffer
// is kept internal — every draw() call pushes the new value.

const SPARK_POINTS = 40;
const RING_THICKNESS = 6;       // px, scales with canvas
const SPARK_HEIGHT_FRAC = 0.18; // sparkline takes the bottom 18% of the canvas

export class GaugeChart {
    constructor(canvasId, options = {}) {
        this.canvas = document.getElementById(canvasId);
        if (!this.canvas) return;
        this.ctx = this.canvas.getContext('2d');
        this.value = 0;
        this.maxValue = options.maxValue || 100;
        this.label = options.label || '';
        this.color = options.color || '#3b82f6';
        this.history = [];

        this.resize();
        // Re-render on resize so the gauge stays crisp on rotate.
        window.addEventListener('resize', () => { this.resize(); this.draw(this.value); });
    }

    resize() {
        // Render at device pixel ratio so the ring stays sharp on HiDPI.
        const dpr = window.devicePixelRatio || 1;
        const size = Math.max(this.canvas.offsetWidth, 80);
        this.canvas.width = size * dpr;
        this.canvas.height = size * dpr;
        this.canvas.style.height = size + 'px';
        this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
        this.cssSize = size;
    }

    pushHistory(value) {
        this.history.push(value);
        if (this.history.length > SPARK_POINTS) this.history.shift();
    }

    draw(value) {
        if (!this.ctx) return;
        const v = Math.max(0, Math.min(value, this.maxValue));
        this.value = v;
        this.pushHistory(v);

        const ctx = this.ctx;
        const size = this.cssSize;
        const sparkBand = size * SPARK_HEIGHT_FRAC;
        const ringArea = size - sparkBand;
        const cx = size / 2;
        const cy = ringArea / 2;
        // Leave a bit of breathing room so the ring doesn't kiss the edge.
        const radius = Math.min(cx, cy) - RING_THICKNESS - 2;

        ctx.clearRect(0, 0, size, size);

        // Background ring.
        ctx.beginPath();
        ctx.arc(cx, cy, radius, 0, Math.PI * 2);
        ctx.lineWidth = RING_THICKNESS;
        ctx.strokeStyle = 'rgba(255, 255, 255, 0.08)';
        ctx.stroke();

        // Foreground ring: starts at top (−90°), sweeps clockwise.
        const pct = v / this.maxValue;
        const start = -Math.PI / 2;
        const end = start + pct * Math.PI * 2;
        ctx.beginPath();
        ctx.arc(cx, cy, radius, start, end);
        ctx.lineCap = 'round';
        ctx.strokeStyle = colorFor(v, this.color);
        ctx.stroke();

        // Sparkline along the bottom of the canvas.
        this.drawSpark(ctx, size, ringArea, sparkBand);
    }

    drawSpark(ctx, size, top, height) {
        if (this.history.length < 2) return;
        const pad = 4;
        const w = size - pad * 2;
        const max = this.maxValue || 100;
        const step = w / (SPARK_POINTS - 1);
        // Right-align so the latest sample is always at the right edge,
        // independent of how full the buffer is.
        const xOffset = pad + (SPARK_POINTS - this.history.length) * step;

        ctx.beginPath();
        for (let i = 0; i < this.history.length; i++) {
            const x = xOffset + i * step;
            const y = top + (height - 4) - (this.history[i] / max) * (height - 4);
            if (i === 0) ctx.moveTo(x, y);
            else ctx.lineTo(x, y);
        }
        ctx.lineWidth = 1.5;
        ctx.strokeStyle = colorFor(this.history[this.history.length - 1], this.color);
        ctx.globalAlpha = 0.7;
        ctx.stroke();
        ctx.globalAlpha = 1;
    }

    update(value) {
        this.draw(value);
    }
}

function colorFor(v, accent) {
    if (v > 85) return '#ef4444';
    if (v > 70) return '#f59e0b';
    return accent;
}
