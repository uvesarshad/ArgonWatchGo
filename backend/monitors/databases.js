const { MongoClient } = require('mongodb');
const mysql = require('mysql2/promise');
const { Client: PostgresClient } = require('pg');
const redis = require('redis');

class DatabaseMonitor {
    constructor(wsHandler, config = {}) {
        this.wsHandler = wsHandler;
        this.databases = config.databases || [];
        this.interval = config.interval || 30000;
        this.results = new Map();
        this.connections = new Map();
        this.timeoutId = null;
        this.isRunning = false;

        if (this.databases.length > 0) {
            this.start();
        }
    }

    start() {
        if (this.isRunning) return;
        this.isRunning = true;
        console.log(`🗄️  Starting database monitoring for ${this.databases.length} databases...`);
        this.loop();
    }

    stop() {
        this.isRunning = false;
        if (this.timeoutId) {
            clearTimeout(this.timeoutId);
            this.timeoutId = null;
        }
        // Close all connections
        this.connections.forEach((conn, id) => {
            this.closeConnection(id);
        });
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
        const promises = this.databases.map(db => this.checkDatabase(db));
        await Promise.allSettled(promises);

        // Broadcast results
        const results = Array.from(this.results.values());
        this.wsHandler.broadcast('DATABASE_STATUS', results);
    }

    async checkDatabase(db) {
        const startTime = Date.now();
        let result = {
            id: db.id || db.name,
            name: db.name,
            type: db.type,
            host: db.host,
            status: 'unknown',
            responseTime: null,
            metrics: {},
            message: '',
            lastCheck: new Date().toISOString()
        };

        try {
            switch (db.type.toLowerCase()) {
                case 'mongodb':
                    result = await this.checkMongoDB(db, startTime);
                    break;
                case 'mysql':
                    result = await this.checkMySQL(db, startTime);
                    break;
                case 'postgresql':
                case 'postgres':
                    result = await this.checkPostgreSQL(db, startTime);
                    break;
                case 'redis':
                    result = await this.checkRedis(db, startTime);
                    break;
                default:
                    result.message = 'Unsupported database type';
            }
        } catch (error) {
            result.status = 'error';
            result.message = error.message;
            result.responseTime = Date.now() - startTime;
        }

        this.results.set(db.id || db.name, result);
        return result;
    }

    // MongoDB monitoring
    async checkMongoDB(db, startTime) {
        const connectionString = db.connectionString ||
            `mongodb://${db.username ? db.username + ':' + db.password + '@' : ''}${db.host}:${db.port || 27017}/${db.database || 'admin'}`;

        const client = new MongoClient(connectionString, {
            serverSelectionTimeoutMS: 5000,
            connectTimeoutMS: 5000
        });

        try {
            await client.connect();
            const admin = client.db().admin();

            // Get server status
            const serverStatus = await admin.serverStatus();
            const dbStats = await client.db(db.database || 'admin').stats();

            const responseTime = Date.now() - startTime;

            const result = {
                id: db.id || db.name,
                name: db.name,
                type: 'mongodb',
                host: db.host,
                status: 'up',
                responseTime,
                metrics: {
                    version: serverStatus.version,
                    uptime: Math.floor(serverStatus.uptime / 60), // minutes
                    connections: {
                        current: serverStatus.connections.current,
                        available: serverStatus.connections.available,
                        total: serverStatus.connections.totalCreated
                    },
                    operations: {
                        insert: serverStatus.opcounters.insert,
                        query: serverStatus.opcounters.query,
                        update: serverStatus.opcounters.update,
                        delete: serverStatus.opcounters.delete
                    },
                    memory: {
                        resident: Math.round(serverStatus.mem.resident),
                        virtual: Math.round(serverStatus.mem.virtual)
                    },
                    database: {
                        collections: dbStats.collections,
                        dataSize: Math.round(dbStats.dataSize / 1024 / 1024), // MB
                        storageSize: Math.round(dbStats.storageSize / 1024 / 1024), // MB
                        indexes: dbStats.indexes
                    }
                },
                message: 'Connected',
                lastCheck: new Date().toISOString()
            };

            await client.close();
            return result;
        } catch (error) {
            throw new Error(`MongoDB connection failed: ${error.message}`);
        }
    }

    // MySQL monitoring
    async checkMySQL(db, startTime) {
        const connection = await mysql.createConnection({
            host: db.host,
            port: db.port || 3306,
            user: db.username,
            password: db.password,
            database: db.database || 'mysql',
            connectTimeout: 5000
        });

        try {
            // Get server status
            const [statusRows] = await connection.query('SHOW GLOBAL STATUS');
            const [variablesRows] = await connection.query('SHOW GLOBAL VARIABLES LIKE "version"');
            const [processRows] = await connection.query('SHOW PROCESSLIST');

            const responseTime = Date.now() - startTime;

            // Parse status into object
            const status = {};
            statusRows.forEach(row => {
                status[row.Variable_name] = row.Value;
            });

            const result = {
                id: db.id || db.name,
                name: db.name,
                type: 'mysql',
                host: db.host,
                status: 'up',
                responseTime,
                metrics: {
                    version: variablesRows[0].Value,
                    uptime: Math.floor(parseInt(status.Uptime) / 60), // minutes
                    connections: {
                        current: processRows.length,
                        max: parseInt(status.Max_used_connections),
                        total: parseInt(status.Connections)
                    },
                    queries: {
                        total: parseInt(status.Queries),
                        slow: parseInt(status.Slow_queries || 0),
                        perSecond: (parseInt(status.Queries) / parseInt(status.Uptime)).toFixed(2)
                    },
                    threads: {
                        connected: parseInt(status.Threads_connected),
                        running: parseInt(status.Threads_running),
                        cached: parseInt(status.Threads_cached)
                    },
                    traffic: {
                        bytesReceived: Math.round(parseInt(status.Bytes_received) / 1024 / 1024), // MB
                        bytesSent: Math.round(parseInt(status.Bytes_sent) / 1024 / 1024) // MB
                    }
                },
                message: 'Connected',
                lastCheck: new Date().toISOString()
            };

            await connection.end();
            return result;
        } catch (error) {
            await connection.end();
            throw new Error(`MySQL connection failed: ${error.message}`);
        }
    }

    // PostgreSQL monitoring
    async checkPostgreSQL(db, startTime) {
        const client = new PostgresClient({
            host: db.host,
            port: db.port || 5432,
            user: db.username,
            password: db.password,
            database: db.database || 'postgres',
            connectionTimeoutMillis: 5000
        });

        try {
            await client.connect();

            // Get server stats
            const versionResult = await client.query('SELECT version()');
            const statsResult = await client.query(`
                SELECT 
                    numbackends as connections,
                    xact_commit as commits,
                    xact_rollback as rollbacks,
                    blks_read as blocks_read,
                    blks_hit as blocks_hit,
                    tup_returned as tuples_returned,
                    tup_fetched as tuples_fetched,
                    tup_inserted as tuples_inserted,
                    tup_updated as tuples_updated,
                    tup_deleted as tuples_deleted
                FROM pg_stat_database 
                WHERE datname = $1
            `, [db.database || 'postgres']);

            const dbSizeResult = await client.query(`
                SELECT pg_database_size($1) as size
            `, [db.database || 'postgres']);

            const responseTime = Date.now() - startTime;
            const stats = statsResult.rows[0];

            const result = {
                id: db.id || db.name,
                name: db.name,
                type: 'postgresql',
                host: db.host,
                status: 'up',
                responseTime,
                metrics: {
                    version: versionResult.rows[0].version.split(' ')[1],
                    connections: parseInt(stats.connections),
                    transactions: {
                        commits: parseInt(stats.commits),
                        rollbacks: parseInt(stats.rollbacks),
                        ratio: ((parseInt(stats.commits) / (parseInt(stats.commits) + parseInt(stats.rollbacks))) * 100).toFixed(2) + '%'
                    },
                    cache: {
                        blocksRead: parseInt(stats.blocks_read),
                        blocksHit: parseInt(stats.blocks_hit),
                        hitRatio: ((parseInt(stats.blocks_hit) / (parseInt(stats.blocks_read) + parseInt(stats.blocks_hit))) * 100).toFixed(2) + '%'
                    },
                    tuples: {
                        returned: parseInt(stats.tuples_returned),
                        fetched: parseInt(stats.tuples_fetched),
                        inserted: parseInt(stats.tuples_inserted),
                        updated: parseInt(stats.tuples_updated),
                        deleted: parseInt(stats.tuples_deleted)
                    },
                    database: {
                        size: Math.round(parseInt(dbSizeResult.rows[0].size) / 1024 / 1024) // MB
                    }
                },
                message: 'Connected',
                lastCheck: new Date().toISOString()
            };

            await client.end();
            return result;
        } catch (error) {
            await client.end();
            throw new Error(`PostgreSQL connection failed: ${error.message}`);
        }
    }

    // Redis monitoring
    async checkRedis(db, startTime) {
        const client = redis.createClient({
            socket: {
                host: db.host,
                port: db.port || 6379,
                connectTimeout: 5000
            },
            password: db.password,
            database: db.database || 0
        });

        try {
            await client.connect();

            // Get server info
            const info = await client.info();
            const dbSize = await client.dbSize();

            const responseTime = Date.now() - startTime;

            // Parse info string
            const infoObj = {};
            info.split('\r\n').forEach(line => {
                if (line && !line.startsWith('#')) {
                    const [key, value] = line.split(':');
                    if (key && value) {
                        infoObj[key] = value;
                    }
                }
            });

            const result = {
                id: db.id || db.name,
                name: db.name,
                type: 'redis',
                host: db.host,
                status: 'up',
                responseTime,
                metrics: {
                    version: infoObj.redis_version,
                    uptime: Math.floor(parseInt(infoObj.uptime_in_seconds) / 60), // minutes
                    connections: {
                        current: parseInt(infoObj.connected_clients),
                        total: parseInt(infoObj.total_connections_received),
                        rejected: parseInt(infoObj.rejected_connections || 0)
                    },
                    memory: {
                        used: Math.round(parseInt(infoObj.used_memory) / 1024 / 1024), // MB
                        peak: Math.round(parseInt(infoObj.used_memory_peak) / 1024 / 1024), // MB
                        rss: Math.round(parseInt(infoObj.used_memory_rss) / 1024 / 1024) // MB
                    },
                    stats: {
                        totalCommands: parseInt(infoObj.total_commands_processed),
                        opsPerSec: parseInt(infoObj.instantaneous_ops_per_sec),
                        keys: dbSize,
                        evictedKeys: parseInt(infoObj.evicted_keys || 0),
                        expiredKeys: parseInt(infoObj.expired_keys || 0)
                    },
                    keyspace: {
                        hits: parseInt(infoObj.keyspace_hits || 0),
                        misses: parseInt(infoObj.keyspace_misses || 0),
                        hitRate: infoObj.keyspace_hits && infoObj.keyspace_misses ?
                            ((parseInt(infoObj.keyspace_hits) / (parseInt(infoObj.keyspace_hits) + parseInt(infoObj.keyspace_misses))) * 100).toFixed(2) + '%' : 'N/A'
                    }
                },
                message: 'Connected',
                lastCheck: new Date().toISOString()
            };

            await client.disconnect();
            return result;
        } catch (error) {
            await client.disconnect();
            throw new Error(`Redis connection failed: ${error.message}`);
        }
    }

    closeConnection(dbId) {
        const conn = this.connections.get(dbId);
        if (conn) {
            try {
                if (conn.close) conn.close();
                if (conn.end) conn.end();
                if (conn.disconnect) conn.disconnect();
            } catch (e) {
                // Ignore close errors
            }
            this.connections.delete(dbId);
        }
    }

    // Add database to monitor
    addDatabase(db) {
        db.id = db.id || db.name;
        this.databases.push(db);
        this.checkDatabase(db); // Immediate check
    }

    // Remove database
    removeDatabase(dbId) {
        this.databases = this.databases.filter(d => (d.id || d.name) !== dbId);
        this.results.delete(dbId);
        this.closeConnection(dbId);
    }

    // Get all database statuses
    getStatuses() {
        return Array.from(this.results.values());
    }
}

module.exports = DatabaseMonitor;
