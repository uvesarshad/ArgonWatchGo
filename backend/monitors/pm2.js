const pm2 = require('pm2');

class Pm2Monitor {
    constructor(wsHandler, interval = 5000) {
        this.wsHandler = wsHandler;
        this.interval = interval;
        this.timeoutId = null;
        this.connected = false;
        this.isRunning = false;
        this.start();
    }

    start() {
        if (this.isRunning) return;
        this.isRunning = true;
        this.loop();
    }

    stop() {
        this.isRunning = false;
        if (this.timeoutId) {
            clearTimeout(this.timeoutId);
            this.timeoutId = null;
        }
        pm2.disconnect();
    }

    async loop() {
        if (!this.isRunning) return;

        const startTime = Date.now();

        await new Promise(resolve => {
            if (!this.connected) {
                pm2.connect((err) => {
                    if (err) {
                        // PM2 might not be running or installed
                        // console.error('PM2 Connection Error:', err); 
                        // Be silent about connection errors to avoid log spam if PM2 isn't used
                        this.connected = false;
                        resolve();
                    } else {
                        this.connected = true;
                        this.getProcessList(resolve);
                    }
                });
            } else {
                this.getProcessList(resolve);
            }
        });

        if (!this.isRunning) return;

        const executionTime = Date.now() - startTime;
        const delay = Math.max(1000, this.interval - executionTime);
        this.timeoutId = setTimeout(() => this.loop(), delay);
    }

    getProcessList(callback) {
        pm2.list((err, list) => {
            if (err) {
                // Connection lost or other error
                // console.error('PM2 List Error:', err);
                this.connected = false;
                pm2.disconnect(); // Ensure clean slate
                callback();
                return;
            }

            const data = list.map(proc => ({
                name: proc.name,
                status: proc.pm2_env.status,
                pid: proc.pid,
                uptime: Date.now() - proc.pm2_env.pm_uptime,
                restarts: proc.pm2_env.restart_time,
                cpu: proc.monit ? proc.monit.cpu : 0,
                memory: proc.monit ? proc.monit.memory : 0
            }));

            this.wsHandler.broadcast('PM2_METRICS', data);
            callback();
        });
    }
}

module.exports = Pm2Monitor;
