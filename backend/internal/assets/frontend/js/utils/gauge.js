// Gauge chart utility
export class GaugeChart {
    constructor(canvasId, options = {}) {
        this.canvas = document.getElementById(canvasId);
        this.ctx = this.canvas.getContext('2d');
        this.value = 0;
        this.maxValue = options.maxValue || 100;
        this.label = options.label || '';
        this.color = options.color || '#3b82f6';

        this.resize();
    }

    resize() {
        const size = this.canvas.offsetWidth;
        this.canvas.width = size;
        this.canvas.height = size;
    }

    draw(value) {
        this.value = Math.min(value, this.maxValue);
        const ctx = this.ctx;
        const centerX = this.canvas.width / 2;
        const centerY = this.canvas.height / 2;
        const radius = Math.min(centerX, centerY) - 10;

        // Clear canvas
        ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);

        // Background arc
        ctx.beginPath();
        ctx.arc(centerX, centerY, radius, 0.75 * Math.PI, 2.25 * Math.PI);
        ctx.lineWidth = 15;
        ctx.strokeStyle = 'rgba(255, 255, 255, 0.1)';
        ctx.stroke();

        // Value arc
        const endAngle = 0.75 * Math.PI + (this.value / this.maxValue) * 1.5 * Math.PI;
        ctx.beginPath();
        ctx.arc(centerX, centerY, radius, 0.75 * Math.PI, endAngle);
        ctx.lineWidth = 15;
        ctx.lineCap = 'round';

        // Color based on value
        let color = this.color;
        if (this.value > 80) color = '#ef4444';
        else if (this.value > 50) color = '#f59e0b';

        ctx.strokeStyle = color;
        ctx.stroke();

        // Needle/arm
        const needleAngle = 0.75 * Math.PI + (this.value / this.maxValue) * 1.5 * Math.PI;
        const needleLength = radius - 20;
        const needleX = centerX + Math.cos(needleAngle) * needleLength;
        const needleY = centerY + Math.sin(needleAngle) * needleLength;

        ctx.beginPath();
        ctx.moveTo(centerX, centerY);
        ctx.lineTo(needleX, needleY);
        ctx.lineWidth = 3;
        ctx.strokeStyle = color;
        ctx.stroke();

        // Center circle
        ctx.beginPath();
        ctx.arc(centerX, centerY, radius - 25, 0, 2 * Math.PI);
        ctx.fillStyle = '#1e293b';
        ctx.fill();

        // Center value text
        ctx.fillStyle = '#f8fafc';
        ctx.font = 'bold 24px Inter';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillText(Math.round(this.value) + '%', centerX, centerY - 5);

        // Label text
        ctx.fillStyle = '#94a3b8';
        ctx.font = '12px Inter';
        ctx.fillText(this.label, centerX, centerY + 20);
    }

    update(value) {
        this.draw(value);
    }
}
