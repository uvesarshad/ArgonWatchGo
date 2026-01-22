const fs = require('fs');
const path = require('path');
const readline = require('readline');

class HistoricalStorage {
    constructor(config) {
        this.enabled = config.enabled;
        this.retentionDays = config.retentionDays || 7;
        this.dataPath = path.resolve(__dirname, '../../', config.dataPath || '../data');
        this.dataFile = path.join(this.dataPath, 'metrics.jsonl');
        // One point every 2 seconds roughly.
        // We will keep memory usage constrained but disk usage is append-only until rotation.
        this.maxDataPointsInMemory = 60 * 60 * 24 * this.retentionDays / 2; 
        
        this.data = {
            cpu: [],
            memory: [],
            network_rx: [],
            network_tx: [],
            disk_read: [],
            disk_write: [],
            cpu_temp: []
        };
        
        this.writeStream = null;

        if (this.enabled) {
            this.ensureDataDirectory();
            this.loadData().then(() => {
                this.initWriteStream();
            });
        }
    }

    ensureDataDirectory() {
        if (!fs.existsSync(this.dataPath)) {
            fs.mkdirSync(this.dataPath, { recursive: true });
        }
    }

    initWriteStream() {
        // Open file for appending
        this.writeStream = fs.createWriteStream(this.dataFile, { flags: 'a' });
        this.writeStream.on('error', (err) => {
            console.error('Error writing to metrics file:', err);
        });
    }

    async loadData() {
        try {
            if (!fs.existsSync(this.dataFile)) {
                return;
            }

            const fileStream = fs.createReadStream(this.dataFile);
            const rl = readline.createInterface({
                input: fileStream,
                crlfDelay: Infinity
            });

            const cutoff = Date.now() - (this.retentionDays * 24 * 60 * 60 * 1000);

            for await (const line of rl) {
                if (!line.trim()) continue;
                try {
                    const record = JSON.parse(line);
                    // record is { type, timestamp, value }
                    if (record.timestamp > cutoff) {
                        if (!this.data[record.type]) {
                            this.data[record.type] = [];
                        }
                        this.data[record.type].push({
                            timestamp: record.timestamp,
                            value: record.value
                        });
                    }
                } catch (e) {
                    // Ignore bad lines
                }
            }
            
            console.log('Historical data loaded.');

        } catch (e) {
            console.error('Failed to load historical data:', e);
        }
    }

    addDataPoint(type, value) {
        if (!this.enabled) return;

        const timestamp = Date.now();
        const point = { timestamp, value };

        // Update in-memory cache
        if (!this.data[type]) {
            this.data[type] = [];
        }
        this.data[type].push(point);

        // Prune in-memory cache if too large (keep it lightweight)
        if (this.data[type].length > this.maxDataPointsInMemory) {
            // Remove oldest 10%
            const removeCount = Math.floor(this.maxDataPointsInMemory * 0.1);
            this.data[type].splice(0, removeCount);
        }

        // Persist to disk asynchronously
        if (this.writeStream) {
            const entry = JSON.stringify({ type, ...point }) + '\n';
            const canWrite = this.writeStream.write(entry);
            // If buffer is full, we could handle backpressure, but for this volume it's rarely an issue.
        }
    }

    getHistoricalData(type, duration = '1h') {
        if (!this.enabled || !this.data[type]) return [];

        const now = Date.now();
        let cutoff;

        switch (duration) {
            case '1h':
                cutoff = now - (60 * 60 * 1000);
                break;
            case '6h':
                cutoff = now - (6 * 60 * 60 * 1000);
                break;
            case '24h':
                cutoff = now - (24 * 60 * 60 * 1000);
                break;
            case '7d':
                cutoff = now - (7 * 24 * 60 * 60 * 1000);
                break;
            default:
                cutoff = now - (60 * 60 * 1000);
        }

        // Use binary search for performance if array is sorted (it is because we append)
        // For simplicity, findIndex is okay for now, or just filter.
        // Optimization: Find the first index > cutoff
        const data = this.data[type];
        // Simple optimization: check if first element is already within range
        if (data.length > 0 && data[0].timestamp > cutoff) {
            return data;
        }
        
        return data.filter(p => p.timestamp > cutoff);
    }
}

module.exports = HistoricalStorage;
