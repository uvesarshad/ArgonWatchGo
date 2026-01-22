const express = require('express');
const http = require('http');
const path = require('path');
const cors = require('cors');
const fs = require('fs');
const WebSocketHandler = require('./utils/websocket');

// Load config
const configPath = path.join(__dirname, '../config/config.json');
let config = {};
try {
    config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
} catch (e) {
    console.error('Failed to load config:', e);
    process.exit(1);
}

const app = express();
const server = http.createServer(app);

// Middleware
app.use(cors());
app.use(express.json());
app.use(express.static(path.join(__dirname, '../frontend')));

// Initialize WebSocket
const wsHandler = new WebSocketHandler(server);

// Initialize Historical Storage
const HistoricalStorage = require('./storage/dbengine');
const storage = new HistoricalStorage(config.storage);

// Initialize Alert Engine
const AlertEngine = require('./alerts/alerts');
const Notifier = require('./alerts/notifier');
const alertEngine = new AlertEngine(config.alerts || {});
const notifier = new Notifier(config.notifications || {});

// Connect alert engine to notifier
alertEngine.on('alert:triggered', (alert, rule) => {
    notifier.notify(alert, rule);
    wsHandler.broadcast('ALERT_TRIGGERED', alert);
});

alertEngine.on('alert:resolved', (alert, rule) => {
    wsHandler.broadcast('ALERT_RESOLVED', alert);
});

// Initialize Service Monitor
let serviceMonitor = null;
if (config.services && config.services.length > 0) {
    const ServiceMonitor = require('./monitors/services');
    serviceMonitor = new ServiceMonitor(wsHandler, {
        services: config.services,
        interval: config.monitoring?.servicesInterval || 30000
    });
}

// Initialize Database Monitor
let databaseMonitor = null;
if (config.databases && config.databases.length > 0) {
    const DatabaseMonitor = require('./monitors/databases');
    databaseMonitor = new DatabaseMonitor(wsHandler, {
        databases: config.databases,
        interval: config.monitoring?.databaseInterval || 30000
    });
}

// Routes
app.get('/api/config', (req, res) => {
    // Only send non-sensitive config to frontend
    const safeConfig = {
        monitoring: config.monitoring,
        quickCommands: config.quickCommands,
        alerts: { enabled: config.alerts?.enabled || false },
        services: { enabled: config.services?.length > 0 }
    };
    res.json(safeConfig);
});

app.get('/api/history/:type', (req, res) => {
    const { type } = req.params;
    const { duration = '1h' } = req.query;

    if (storage) {
        const data = storage.getHistoricalData(type, duration);
        res.json(data);
    } else {
        res.json([]);
    }
});

// Alert Management API
app.get('/api/alerts', (req, res) => {
    res.json({
        rules: alertEngine.rules,
        active: alertEngine.getActiveAlerts()
    });
});

app.post('/api/alerts', (req, res) => {
    try {
        const rule = alertEngine.addRule(req.body);
        res.json({ success: true, rule });
    } catch (error) {
        res.status(400).json({ success: false, error: error.message });
    }
});

app.put('/api/alerts/:id', (req, res) => {
    try {
        const rule = alertEngine.addRule({ id: req.params.id, ...req.body });
        res.json({ success: true, rule });
    } catch (error) {
        res.status(400).json({ success: false, error: error.message });
    }
});

app.delete('/api/alerts/:id', (req, res) => {
    try {
        alertEngine.removeRule(req.params.id);
        res.json({ success: true });
    } catch (error) {
        res.status(400).json({ success: false, error: error.message });
    }
});

app.get('/api/alerts/history', (req, res) => {
    const limit = parseInt(req.query.limit) || 100;
    res.json(alertEngine.getHistory(limit));
});

app.post('/api/alerts/:id/acknowledge', (req, res) => {
    try {
        alertEngine.acknowledgeAlert(req.params.id);
        res.json({ success: true });
    } catch (error) {
        res.status(400).json({ success: false, error: error.message });
    }
});

app.post('/api/alerts/maintenance', (req, res) => {
    try {
        alertEngine.setMaintenanceMode(req.body.enabled);
        res.json({ success: true, maintenanceMode: req.body.enabled });
    } catch (error) {
        res.status(400).json({ success: false, error: error.message });
    }
});

// Service Monitoring API
app.get('/api/services', (req, res) => {
    if (serviceMonitor) {
        res.json(serviceMonitor.getStatuses());
    } else {
        res.json([]);
    }
});

app.post('/api/services', (req, res) => {
    if (serviceMonitor) {
        try {
            serviceMonitor.addService(req.body);
            res.json({ success: true });
        } catch (error) {
            res.status(400).json({ success: false, error: error.message });
        }
    } else {
        res.status(400).json({ success: false, error: 'Service monitor not initialized' });
    }
});

app.delete('/api/services/:id', (req, res) => {
    if (serviceMonitor) {
        try {
            serviceMonitor.removeService(req.params.id);
            res.json({ success: true });
        } catch (error) {
            res.status(400).json({ success: false, error: error.message });
        }
    } else {
        res.status(400).json({ success: false, error: 'Service monitor not initialized' });
    }
});

// Database Monitoring API
app.get('/api/databases', (req, res) => {
    if (databaseMonitor) {
        res.json(databaseMonitor.getStatuses());
    } else {
        res.json([]);
    }
});

app.post('/api/databases', (req, res) => {
    if (databaseMonitor) {
        try {
            databaseMonitor.addDatabase(req.body);
            res.json({ success: true });
        } catch (error) {
            res.status(400).json({ success: false, error: error.message });
        }
    } else {
        res.status(400).json({ success: false, error: 'Database monitor not initialized' });
    }
});

app.delete('/api/databases/:id', (req, res) => {
    if (databaseMonitor) {
        try {
            databaseMonitor.removeDatabase(req.params.id);
            res.json({ success: true });
        } catch (error) {
            res.status(400).json({ success: false, error: error.message });
        }
    } else {
        res.status(400).json({ success: false, error: 'Database monitor not initialized' });
    }
});

// Initialize Command Handler
const CommandHandler = require('./terminal/commands');
const commandHandler = new CommandHandler(wsHandler, config);

// Start Monitors
console.log('Starting monitors...');
const SystemMonitor = require('./monitors/system');
const systemMonitor = new SystemMonitor(wsHandler, config.monitoring.systemInterval, storage);

// Hook system monitor to alert engine
systemMonitor.on = systemMonitor.on || function () { }; // Ensure EventEmitter
setInterval(() => {
    // Get latest metrics and check alerts
    if (systemMonitor.lastData) {
        alertEngine.checkMetrics(systemMonitor.lastData);
    }
}, 5000); // Check alerts every 5 seconds



try {
    const Pm2Monitor = require('./monitors/pm2');
    const pm2Monitor = new Pm2Monitor(wsHandler, config.monitoring.pm2Interval);
} catch (e) {
    console.log('PM2 Monitor disabled (module not found or error)');
}

try {
    const RunnerMonitor = require('./monitors/github-runner');
    const runnerMonitor = new RunnerMonitor(wsHandler, config.githubRunner, config.monitoring.runnerInterval);
} catch (e) {
    console.log('GitHub Runner Monitor disabled (module not found or error)');
}

const PORT = config.server.port || 3000;
server.listen(PORT, '0.0.0.0', () => {
    console.log(`🚀 ArgonWatch Server running on port ${PORT}`);
    console.log(`📊 Monitoring: System, PM2, GitHub Runner`);
    if (serviceMonitor) console.log(`🔍 Service Monitoring: ${config.services.length} services`);
    if (alertEngine.rules.length > 0) console.log(`🚨 Alert Rules: ${alertEngine.rules.length} configured`);
});
