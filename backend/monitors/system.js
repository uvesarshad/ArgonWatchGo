const si = require('systeminformation');

class SystemMonitor {
    constructor(wsHandler, interval = 2000, storage = null) {
        this.wsHandler = wsHandler;
        this.interval = interval;
        this.storage = storage;
        this.timeoutId = null;
        this.isRunning = false;
        this.staticData = null;
        this.lastDiskHealthUpdate = 0;
        this.diskHealthCache = [];
        this.tick = 0;
        this.cachedMetrics = {
            currentLoad: {},
            mem: {},
            fsSize: [],
            networkStats: [],
            cpuTemp: {},
            cpuCurrentSpeed: {},
            fsStats: {}
        };
        this.start();
    }

    async start() {
        if (this.isRunning) return;
        this.isRunning = true;
        
        await this.getStaticData();
        // Initial fetch of everything
        await this.updateMetrics(true); 
        this.loop();
    }

    // ... (stop and loop methods remain mostly the same, loop calls getData)

    async loop() {
        if (!this.isRunning) return;

        const startTime = Date.now();
        await this.getData();
        
        if (!this.isRunning) return;

        const executionTime = Date.now() - startTime;
        const delay = Math.max(100, this.interval - executionTime);

        this.timeoutId = setTimeout(() => this.loop(), delay);
    }

    async getStaticData() {
        try {
            const [osInfo, cpu, diskLayout, graphics] = await Promise.all([
                si.osInfo(),
                si.cpu(),
                si.diskLayout(),
                si.graphics()
            ]);

            // Get network interfaces for IP
            const networkInterfaces = await si.networkInterfaces();
            const primaryInterface = networkInterfaces.find(iface =>
                iface.ip4 && !iface.internal && iface.operstate === 'up'
            ) || networkInterfaces[0];

            this.staticData = {
                os: {
                    platform: osInfo.platform,
                    distro: osInfo.distro,
                    hostname: osInfo.hostname,
                    ipAddress: primaryInterface ? primaryInterface.ip4 : 'N/A'
                },
                cpu: {
                    manufacturer: cpu.manufacturer,
                    brand: cpu.brand,
                    cores: cpu.cores,
                    physicalCores: cpu.physicalCores,
                    speedMin: cpu.speedMin,
                    speedMax: cpu.speedMax
                },
                graphics: graphics,
                diskLayout: diskLayout
            };
            
            this.diskHealthCache = diskLayout.map(disk => ({
                device: disk.device,
                type: disk.type,
                name: disk.name,
                vendor: disk.vendor,
                size: disk.size,
                smartStatus: disk.smartStatus || 'unknown',
                temperature: disk.temperature || null
            }));

        } catch (error) {
            console.error('Error fetching static system data:', error);
        }
    }

    async updateMetrics(forceAll = false) {
        this.tick++;
        const promises = [];
        const updates = {};

        // Group 1: High frequency (Every 2s or tick % 1)
        // CPU Load is critical
        promises.push(si.currentLoad().then(res => { this.cachedMetrics.currentLoad = res; }));

        // Group 2: Medium frequency (Every 10s or tick % 5)
        if (forceAll || this.tick % 5 === 0) {
            promises.push(si.mem().then(res => { this.cachedMetrics.mem = res; }));
            promises.push(si.networkStats().then(res => { this.cachedMetrics.networkStats = res; }));
            promises.push(si.cpuTemperature().then(res => { this.cachedMetrics.cpuTemp = res; }));
            promises.push(si.cpuCurrentSpeed().then(res => { this.cachedMetrics.cpuCurrentSpeed = res; }));
            promises.push(si.fsStats().then(res => { this.cachedMetrics.fsStats = res; }));
        }

        // Group 3: Low frequency (Every 60s or tick % 30)
        if (forceAll || this.tick % 30 === 0) {
            promises.push(si.fsSize().then(res => { this.cachedMetrics.fsSize = res; }));
            
            // Also update SMART status here
            si.diskLayout().then(layout => {
                this.diskHealthCache = layout.map(disk => ({
                    device: disk.device,
                    type: disk.type,
                    name: disk.name,
                    vendor: disk.vendor,
                    size: disk.size,
                    smartStatus: disk.smartStatus || 'unknown',
                    temperature: disk.temperature || null
                }));
            }).catch(e => console.error('Error updating disk health:', e));
        }

        await Promise.all(promises);
    }

    async getData() {
        try {
            // Ensure static data is loaded
            if (!this.staticData) {
                await this.getStaticData();
            }

            await this.updateMetrics();

            const {
                currentLoad,
                mem,
                fsSize,
                networkStats,
                cpuTemp,
                cpuCurrentSpeed,
                fsStats
            } = this.cachedMetrics;

            const data = {
                system: {
                    ...this.staticData.os,
                    uptime: si.time().uptime
                },
                cpu: {
                    ...this.staticData.cpu,
                    speed: cpuCurrentSpeed.avg, // Update current speed
                    // Overall load
                    load: currentLoad.currentLoad,
                    loadUser: currentLoad.currentLoadUser,
                    loadSystem: currentLoad.currentLoadSystem,
                    loadIdle: currentLoad.currentLoadIdle,
                    // Per-core loads
                    coreLoads: currentLoad.cpus ? currentLoad.cpus.map(core => ({
                        load: core.load,
                        loadUser: core.loadUser,
                        loadSystem: core.loadSystem,
                        loadIdle: core.loadIdle
                    })) : [],
                    // Current speeds per core
                    coreSpeeds: cpuCurrentSpeed.cores || [],
                    avgSpeed: cpuCurrentSpeed.avg,
                    // Load averages (Linux/Mac)
                    loadAverage1: currentLoad.avgLoad || 0,
                    loadAverage5: currentLoad.avgLoad5 || 0,
                    loadAverage15: currentLoad.avgLoad15 || 0
                },
                memory: {
                    total: mem.total || 0,
                    free: mem.free || 0,
                    used: mem.used || 0,
                    active: mem.active || 0,
                    available: mem.available || 0,
                    percentage: mem.total > 0 ? (mem.active / mem.total) * 100 : 0,
                    // Swap memory
                    swapTotal: mem.swaptotal || 0,
                    swapUsed: mem.swapused || 0,
                    swapFree: mem.swapfree || 0,
                    swapPercentage: mem.swaptotal > 0 ? (mem.swapused / mem.swaptotal) * 100 : 0,
                    // Cache and buffers
                    buffers: mem.buffers || 0,
                    cached: mem.cached || 0,
                    slab: mem.slab || 0
                },
                disk: (fsSize || []).map(drive => ({
                    fs: drive.fs,
                    type: drive.type,
                    size: drive.size,
                    used: drive.used,
                    use: drive.use,
                    mount: drive.mount,
                    // Find matching I/O stats
                    rw_sec: drive.rw_sec || 0,
                    r_sec: drive.r_sec || 0,
                    w_sec: drive.w_sec || 0
                })),
                // Disk I/O statistics
                diskIO: fsStats ? {
                    rx: fsStats.rx || 0,
                    wx: fsStats.wx || 0,
                    tx: fsStats.tx || 0,
                    rx_sec: fsStats.rx_sec || 0,
                    wx_sec: fsStats.wx_sec || 0,
                    tx_sec: fsStats.tx_sec || 0,
                    ms: fsStats.ms || 0
                } : null,
                network: (networkStats || []).map(iface => ({
                    iface: iface.iface,
                    rx_bytes: iface.rx_bytes,
                    tx_bytes: iface.tx_bytes,
                    rx_sec: iface.rx_sec,
                    tx_sec: iface.tx_sec,
                    operstate: iface.operstate,
                    // Errors and packet loss
                    rx_errors: iface.rx_errors || 0,
                    tx_errors: iface.tx_errors || 0,
                    rx_dropped: iface.rx_dropped || 0,
                    tx_dropped: iface.tx_dropped || 0
                })),
                // Temperature sensors
                temperatures: {
                    main: cpuTemp.main || null,
                    cores: cpuTemp.cores || [],
                    max: cpuTemp.max || null,
                    // GPU temperatures
                    gpu: this.staticData.graphics.controllers && this.staticData.graphics.controllers.length > 0
                        ? this.staticData.graphics.controllers.map(gpu => ({
                            model: gpu.model,
                            temperature: gpu.temperatureGpu || null,
                            temperatureMemory: gpu.temperatureMemory || null,
                            fanSpeed: gpu.fanSpeed || null,
                            utilizationGpu: gpu.utilizationGpu || null,
                            utilizationMemory: gpu.utilizationMemory || null,
                            memoryTotal: gpu.memoryTotal || null,
                            memoryUsed: gpu.memoryUsed || null,
                            memoryFree: gpu.memoryFree || null
                        }))
                        : []
                },
                // Disk health (SMART status) - Cached
                diskHealth: this.diskHealthCache,
                uptime: si.time().uptime
            };

            // Store historical data (enhanced)
            if (this.storage) {
                this.storage.addDataPoint('cpu', currentLoad.currentLoad);
                if (mem.total > 0) {
                     this.storage.addDataPoint('memory', (mem.active / mem.total) * 100);
                }

                // Store temperature if available
                if (cpuTemp.main) {
                    this.storage.addDataPoint('cpu_temp', cpuTemp.main);
                }

                // Store disk I/O
                if (fsStats && fsStats.rx_sec !== undefined) {
                    this.storage.addDataPoint('disk_read', fsStats.rx_sec / 1024 / 1024); // MB/s
                    this.storage.addDataPoint('disk_write', fsStats.wx_sec / 1024 / 1024); // MB/s
                }

                // Store network I/O
                if (networkStats && networkStats.length > 0) {
                    const totalRx = networkStats.reduce((sum, n) => sum + (n.rx_sec || 0), 0);
                    const totalTx = networkStats.reduce((sum, n) => sum + (n.tx_sec || 0), 0);
                    this.storage.addDataPoint('network_rx', totalRx / 1024 / 1024); // MB/s
                    this.storage.addDataPoint('network_tx', totalTx / 1024 / 1024); // MB/s
                }
            }

            this.lastData = data;
            this.wsHandler.broadcast('SYSTEM_METRICS', data);
        } catch (error) {
            console.error('Error fetching system metrics:', error);
        }
    }
}

module.exports = SystemMonitor;
