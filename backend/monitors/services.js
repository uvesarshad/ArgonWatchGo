const axios = require('axios');
const { exec } = require('child_process');
const { promisify } = require('util');
const execAsync = promisify(exec);

class ServiceMonitor {
    constructor(wsHandler, config = {}) {
        this.wsHandler = wsHandler;
        this.services = config.services || [];
        this.interval = config.interval || 30000;
        this.results = new Map();
        this.timeoutId = null;
        this.isRunning = false;

        if (this.services.length > 0) {
            this.start();
        }
    }

    start() {
        if (this.isRunning) return;
        this.isRunning = true;
        console.log(`Starting service monitoring for ${this.services.length} services...`);
        this.loop();
    }

    stop() {
        this.isRunning = false;
        if (this.timeoutId) {
            clearTimeout(this.timeoutId);
            this.timeoutId = null;
        }
    }

    async loop() {
        if (!this.isRunning) return;

        const startTime = Date.now();
        await this.checkAll();

        if (!this.isRunning) return;

        const executionTime = Date.now() - startTime;
        const delay = Math.max(1000, this.interval - executionTime);
        this.timeoutId = setTimeout(() => this.loop(), delay);
    }

    async checkAll() {
        const promises = this.services.map(service => this.checkService(service));
        await Promise.allSettled(promises);

        // Broadcast results
        const results = Array.from(this.results.values());
        this.wsHandler.broadcast('SERVICE_STATUS', results);
    }

    async checkService(service) {
        const startTime = Date.now();
        let result = {
            id: service.id || service.name,
            name: service.name,
            type: service.type,
            status: 'unknown',
            responseTime: null,
            message: '',
            lastCheck: new Date().toISOString()
        };

        try {
            switch (service.type) {
                case 'http':
                case 'https':
                    result = await this.checkHTTP(service, startTime);
                    break;
                case 'tcp':
                    result = await this.checkTCP(service, startTime);
                    break;
                case 'ping':
                    result = await this.checkPing(service, startTime);
                    break;
                case 'process':
                    result = await this.checkProcess(service, startTime);
                    break;
                default:
                    result.message = 'Unknown service type';
            }
        } catch (error) {
            result.status = 'error';
            result.message = error.message;
        }

        this.results.set(service.name, result);
        return result;
    }

    // Check HTTP/HTTPS endpoint
    async checkHTTP(service, startTime) {
        const timeout = service.timeout || 5000;

        try {
            const response = await axios.get(service.url, {
                timeout,
                validateStatus: () => true // Don't throw on any status
            });

            const responseTime = Date.now() - startTime;
            const expectedStatus = service.expectedStatus || 200;
            const isHealthy = response.status === expectedStatus;

            return {
                id: service.id || service.name,
                name: service.name,
                type: service.type,
                url: service.url,
                status: isHealthy ? 'up' : 'degraded',
                responseTime,
                statusCode: response.status,
                message: isHealthy ? 'OK' : `Expected ${expectedStatus}, got ${response.status}`,
                lastCheck: new Date().toISOString()
            };
        } catch (error) {
            return {
                id: service.id || service.name,
                name: service.name,
                type: service.type,
                url: service.url,
                status: 'down',
                responseTime: Date.now() - startTime,
                message: error.message,
                lastCheck: new Date().toISOString()
            };
        }
    }

    // Check TCP port
    async checkTCP(service, startTime) {
        const net = require('net');
        const timeout = service.timeout || 5000;

        return new Promise((resolve) => {
            const socket = new net.Socket();
            let resolved = false;

            const cleanup = () => {
                if (!resolved) {
                    resolved = true;
                    socket.destroy();
                }
            };

            socket.setTimeout(timeout);

            socket.on('connect', () => {
                cleanup();
                resolve({
                    id: service.id || service.name,
                    name: service.name,
                    type: service.type,
                    host: service.host,
                    port: service.port,
                    status: 'up',
                    responseTime: Date.now() - startTime,
                    message: 'Port is open',
                    lastCheck: new Date().toISOString()
                });
            });

            socket.on('error', (error) => {
                cleanup();
                resolve({
                    id: service.id || service.name,
                    name: service.name,
                    type: service.type,
                    host: service.host,
                    port: service.port,
                    status: 'down',
                    responseTime: Date.now() - startTime,
                    message: error.message,
                    lastCheck: new Date().toISOString()
                });
            });

            socket.on('timeout', () => {
                cleanup();
                resolve({
                    id: service.id || service.name,
                    name: service.name,
                    type: service.type,
                    host: service.host,
                    port: service.port,
                    status: 'down',
                    responseTime: Date.now() - startTime,
                    message: 'Connection timeout',
                    lastCheck: new Date().toISOString()
                });
            });

            socket.connect(service.port, service.host);
        });
    }

    // Check ping
    async checkPing(service, startTime) {
        const host = service.host;
        const isWindows = process.platform === 'win32';
        const pingCmd = isWindows ? `ping -n 1 ${host}` : `ping -c 1 ${host}`;

        try {
            await execAsync(pingCmd);
            return {
                id: service.id || service.name,
                name: service.name,
                type: service.type,
                host: host,
                status: 'up',
                responseTime: Date.now() - startTime,
                message: 'Host is reachable',
                lastCheck: new Date().toISOString()
            };
        } catch (error) {
            return {
                id: service.id || service.name,
                name: service.name,
                type: service.type,
                host: host,
                status: 'down',
                responseTime: Date.now() - startTime,
                message: 'Host is unreachable',
                lastCheck: new Date().toISOString()
            };
        }
    }

    // Check if process is running
    async checkProcess(service, startTime) {
        const processName = service.processName;
        const isWindows = process.platform === 'win32';
        const checkCmd = isWindows
            ? `tasklist /FI "IMAGENAME eq ${processName}" /NH`
            : `pgrep -x ${processName}`;

        try {
            const { stdout } = await execAsync(checkCmd);
            const isRunning = isWindows
                ? stdout.toLowerCase().includes(processName.toLowerCase())
                : stdout.trim().length > 0;

            return {
                id: service.id || service.name,
                name: service.name,
                type: service.type,
                processName: processName,
                status: isRunning ? 'up' : 'down',
                responseTime: Date.now() - startTime,
                message: isRunning ? 'Process is running' : 'Process not found',
                lastCheck: new Date().toISOString()
            };
        } catch (error) {
            return {
                id: service.id || service.name,
                name: service.name,
                type: service.type,
                processName: processName,
                status: 'down',
                responseTime: Date.now() - startTime,
                message: 'Process not found',
                lastCheck: new Date().toISOString()
            };
        }
    }

    // Add service to monitor
    addService(service) {
        service.id = service.id || service.name;
        this.services.push(service);
        this.checkService(service); // Immediate check
    }

    // Remove service
    removeService(serviceId) {
        this.services = this.services.filter(s => (s.id || s.name) !== serviceId);
        this.results.delete(serviceId);
    }

    // Get all service statuses
    getStatuses() {
        return Array.from(this.results.values());
    }
}

module.exports = ServiceMonitor;
