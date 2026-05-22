package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"argon-watch-go/internal/config"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type DatabaseMonitor struct {
	serverID  string
	databases []config.DatabaseConfig
	interval  time.Duration
	broadcast func(serverID, msgType string, data interface{})
	stopChan  chan struct{}
	prevStats map[string]databaseSnapshot
}

type DatabaseStatus struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Type         string           `json:"type"`
	Status       string           `json:"status"`       // "up", "down", "error"
	ResponseTime int64            `json:"responseTime"` // ms
	Message      string           `json:"message"`
	LastCheck    time.Time        `json:"lastCheck"`
	Version      string           `json:"version,omitempty"`
	Database     string           `json:"database,omitempty"`
	Metrics      *DatabaseMetrics `json:"metrics,omitempty"`
}

type DatabaseMetrics struct {
	Connections *DatabaseConnectionMetrics `json:"connections,omitempty"`
	Activity    *DatabaseActivityMetrics   `json:"activity,omitempty"`
	Storage     *DatabaseStorageMetrics    `json:"storage,omitempty"`
	Cache       *DatabaseCacheMetrics      `json:"cache,omitempty"`
	Memory      *DatabaseMemoryMetrics     `json:"memory,omitempty"`
}

type DatabaseConnectionMetrics struct {
	Current      int64 `json:"current,omitempty"`
	Available    int64 `json:"available,omitempty"`
	TotalCreated int64 `json:"totalCreated,omitempty"`
}

type DatabaseActivityMetrics struct {
	OperationsPerSecond   float64 `json:"operationsPerSecond,omitempty"`
	TransactionsPerSecond float64 `json:"transactionsPerSecond,omitempty"`
	Queries               int64   `json:"queries,omitempty"`
	Inserts               int64   `json:"inserts,omitempty"`
	Updates               int64   `json:"updates,omitempty"`
	Deletes               int64   `json:"deletes,omitempty"`
	Commands              int64   `json:"commands,omitempty"`
	Commits               int64   `json:"commits,omitempty"`
	Rollbacks             int64   `json:"rollbacks,omitempty"`
	TuplesReturned        int64   `json:"tuplesReturned,omitempty"`
	TuplesFetched         int64   `json:"tuplesFetched,omitempty"`
	TuplesInserted        int64   `json:"tuplesInserted,omitempty"`
	TuplesUpdated         int64   `json:"tuplesUpdated,omitempty"`
	TuplesDeleted         int64   `json:"tuplesDeleted,omitempty"`
}

type DatabaseStorageMetrics struct {
	DatabaseSizeBytes int64 `json:"databaseSizeBytes,omitempty"`
	DataSizeBytes     int64 `json:"dataSizeBytes,omitempty"`
	StorageSizeBytes  int64 `json:"storageSizeBytes,omitempty"`
	IndexSizeBytes    int64 `json:"indexSizeBytes,omitempty"`
	Collections       int64 `json:"collections,omitempty"`
	Indexes           int64 `json:"indexes,omitempty"`
}

type DatabaseCacheMetrics struct {
	HitRatio   float64 `json:"hitRatio,omitempty"`
	BlocksRead int64   `json:"blocksRead,omitempty"`
	BlocksHit  int64   `json:"blocksHit,omitempty"`
}

type DatabaseMemoryMetrics struct {
	ResidentBytes int64 `json:"residentBytes,omitempty"`
	VirtualBytes  int64 `json:"virtualBytes,omitempty"`
}

type databaseSnapshot struct {
	totalOps  int64
	checkedAt time.Time
}

type postgresDatabaseStats struct {
	NumBackends   int64
	Commits       int64
	Rollbacks     int64
	BlocksRead    int64
	BlocksHit     int64
	TuplesReturn  int64
	TuplesFetch   int64
	TuplesInsert  int64
	TuplesUpdate  int64
	TuplesDelete  int64
	DatabaseBytes int64
}

func NewDatabaseMonitor(serverID string, databases []config.DatabaseConfig, interval time.Duration, broadcast func(serverID, msgType string, data interface{})) *DatabaseMonitor {
	return &DatabaseMonitor{
		serverID:  serverID,
		databases: databases,
		interval:  interval,
		broadcast: broadcast,
		stopChan:  make(chan struct{}),
		prevStats: make(map[string]databaseSnapshot),
	}
}

func (m *DatabaseMonitor) Start() {
	if len(m.databases) == 0 {
		return
	}
	go m.loop()
}

func (m *DatabaseMonitor) Stop() {
	close(m.stopChan)
}

func (m *DatabaseMonitor) loop() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	m.checkAll()

	for {
		select {
		case <-ticker.C:
			m.checkAll()
		case <-m.stopChan:
			return
		}
	}
}

func (m *DatabaseMonitor) checkAll() {
	var results []DatabaseStatus
	for _, db := range m.databases {
		results = append(results, m.checkDatabase(db))
	}
	m.broadcast(m.serverID, "DATABASE_STATUS", results)
}

func (m *DatabaseMonitor) checkDatabase(db config.DatabaseConfig) DatabaseStatus {
	startTime := time.Now()
	res := DatabaseStatus{
		ID:        db.ID,
		Name:      db.Name,
		Type:      db.Type,
		LastCheck: startTime,
		Database:  db.Database,
	}

	if res.ID == "" {
		res.ID = db.Name
	}

	timeout := 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var err error

	switch normalizeDatabaseType(db.Type) {
	case "mongodb":
		err = m.collectMongoDBMetrics(ctx, db, &res)
	case "postgres":
		err = m.collectPostgresMetrics(ctx, db, &res)
	case "mysql":
		err = m.checkMySQL(ctx, db)
	case "redis":
		err = m.checkRedis(ctx, db)
	default:
		res.Status = "unknown"
		res.Message = "Unknown database type"
		return res
	}

	res.ResponseTime = time.Since(startTime).Milliseconds()

	if err != nil {
		res.Status = "down"
		res.Message = err.Error()
	} else {
		res.Status = "up"
		if res.Message == "" {
			res.Message = "Connected"
		}
	}

	return res
}

func normalizeDatabaseType(dbType string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "postgresql":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(dbType))
	}
}

func (m *DatabaseMonitor) collectMongoDBMetrics(ctx context.Context, db config.DatabaseConfig, res *DatabaseStatus) error {
	clientOpts := options.Client().ApplyURI(fmt.Sprintf("mongodb://%s:%d", db.Host, db.Port))
	if db.User != "" {
		clientOpts.SetAuth(options.Credential{
			Username: db.User,
			Password: db.Password,
		})
	}

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return err
	}

	targetDB := db.Database
	if targetDB == "" {
		targetDB = "admin"
	}

	adminDB := client.Database("admin")
	var buildInfo bson.M
	if err := adminDB.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&buildInfo); err != nil {
		return err
	}

	var serverStatus bson.M
	if err := adminDB.RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&serverStatus); err != nil {
		return err
	}

	var dbStats bson.M
	if err := client.Database(targetDB).RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&dbStats); err != nil {
		return err
	}

	opCount := totalMongoOperations(serverStatus)
	opsPerSecond := m.recordOpsRate(res.ID, opCount, res.LastCheck)

	res.Version = nestedString(buildInfo, "version")
	res.Database = targetDB
	res.Metrics = buildMongoMetrics(serverStatus, dbStats, opsPerSecond)
	res.Message = mongoStatusSummary(res.Metrics)
	return nil
}

func (m *DatabaseMonitor) collectPostgresMetrics(ctx context.Context, db config.DatabaseConfig, res *DatabaseStatus) error {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", db.User, db.Password, db.Host, db.Port, db.Database)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}

	var version string
	if err := pool.QueryRow(ctx, "select version()").Scan(&version); err != nil {
		return err
	}

	var currentDB string
	var stats postgresDatabaseStats
	err = pool.QueryRow(ctx, `
		SELECT
			current_database(),
			COALESCE(numbackends, 0),
			COALESCE(xact_commit, 0),
			COALESCE(xact_rollback, 0),
			COALESCE(blks_read, 0),
			COALESCE(blks_hit, 0),
			COALESCE(tup_returned, 0),
			COALESCE(tup_fetched, 0),
			COALESCE(tup_inserted, 0),
			COALESCE(tup_updated, 0),
			COALESCE(tup_deleted, 0),
			COALESCE(pg_database_size(current_database()), 0)
		FROM pg_stat_database
		WHERE datname = current_database()
	`).Scan(
		&currentDB,
		&stats.NumBackends,
		&stats.Commits,
		&stats.Rollbacks,
		&stats.BlocksRead,
		&stats.BlocksHit,
		&stats.TuplesReturn,
		&stats.TuplesFetch,
		&stats.TuplesInsert,
		&stats.TuplesUpdate,
		&stats.TuplesDelete,
		&stats.DatabaseBytes,
	)
	if err != nil {
		return err
	}

	txCount := stats.Commits + stats.Rollbacks
	txPerSecond := m.recordOpsRate(res.ID, txCount, res.LastCheck)

	res.Version = version
	res.Database = currentDB
	res.Metrics = buildPostgresMetrics(stats, txPerSecond)
	res.Message = postgresStatusSummary(res.Metrics)
	return nil
}

func (m *DatabaseMonitor) checkMySQL(ctx context.Context, db config.DatabaseConfig) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", db.User, db.Password, db.Host, db.Port, db.Database)
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.PingContext(ctx)
}

func (m *DatabaseMonitor) checkRedis(ctx context.Context, db config.DatabaseConfig) error {
	addr := fmt.Sprintf("%s:%d", db.Host, db.Port)
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: db.Password,
		DB:       0,
	})
	defer rdb.Close()

	_, err := rdb.Ping(ctx).Result()
	return err
}

func (m *DatabaseMonitor) recordOpsRate(id string, totalOps int64, checkedAt time.Time) float64 {
	prev, ok := m.prevStats[id]
	m.prevStats[id] = databaseSnapshot{totalOps: totalOps, checkedAt: checkedAt}
	if !ok || totalOps < prev.totalOps {
		return 0
	}

	return calculatePerSecond(totalOps-prev.totalOps, checkedAt.Sub(prev.checkedAt))
}

func calculatePerSecond(delta int64, elapsed time.Duration) float64 {
	if delta <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(delta) / elapsed.Seconds()
}

func buildMongoMetrics(serverStatus, dbStats bson.M, opsPerSecond float64) *DatabaseMetrics {
	return &DatabaseMetrics{
		Connections: &DatabaseConnectionMetrics{
			Current:      nestedInt64(serverStatus, "connections", "current"),
			Available:    nestedInt64(serverStatus, "connections", "available"),
			TotalCreated: nestedInt64(serverStatus, "connections", "totalCreated"),
		},
		Activity: &DatabaseActivityMetrics{
			OperationsPerSecond: opsPerSecond,
			Queries:             nestedInt64(serverStatus, "opcounters", "query"),
			Inserts:             nestedInt64(serverStatus, "opcounters", "insert"),
			Updates:             nestedInt64(serverStatus, "opcounters", "update"),
			Deletes:             nestedInt64(serverStatus, "opcounters", "delete"),
			Commands:            nestedInt64(serverStatus, "opcounters", "command"),
		},
		Storage: &DatabaseStorageMetrics{
			DataSizeBytes:    nestedInt64(dbStats, "dataSize"),
			StorageSizeBytes: nestedInt64(dbStats, "storageSize"),
			IndexSizeBytes:   nestedInt64(dbStats, "indexSize"),
			Collections:      nestedInt64(dbStats, "collections"),
			Indexes:          nestedInt64(dbStats, "indexes"),
		},
		Memory: &DatabaseMemoryMetrics{
			ResidentBytes: nestedInt64(serverStatus, "mem", "resident") * 1024 * 1024,
			VirtualBytes:  nestedInt64(serverStatus, "mem", "virtual") * 1024 * 1024,
		},
	}
}

func buildPostgresMetrics(stats postgresDatabaseStats, transactionsPerSecond float64) *DatabaseMetrics {
	return &DatabaseMetrics{
		Connections: &DatabaseConnectionMetrics{
			Current: stats.NumBackends,
		},
		Activity: &DatabaseActivityMetrics{
			TransactionsPerSecond: transactionsPerSecond,
			Commits:               stats.Commits,
			Rollbacks:             stats.Rollbacks,
			TuplesReturned:        stats.TuplesReturn,
			TuplesFetched:         stats.TuplesFetch,
			TuplesInserted:        stats.TuplesInsert,
			TuplesUpdated:         stats.TuplesUpdate,
			TuplesDeleted:         stats.TuplesDelete,
		},
		Storage: &DatabaseStorageMetrics{
			DatabaseSizeBytes: stats.DatabaseBytes,
		},
		Cache: &DatabaseCacheMetrics{
			HitRatio:   calculateRatio(stats.BlocksHit, stats.BlocksRead+stats.BlocksHit),
			BlocksRead: stats.BlocksRead,
			BlocksHit:  stats.BlocksHit,
		},
	}
}

func calculateRatio(part, total int64) float64 {
	if part <= 0 || total <= 0 {
		return 0
	}
	return (float64(part) / float64(total)) * 100
}

func totalMongoOperations(serverStatus bson.M) int64 {
	return nestedInt64(serverStatus, "opcounters", "insert") +
		nestedInt64(serverStatus, "opcounters", "query") +
		nestedInt64(serverStatus, "opcounters", "update") +
		nestedInt64(serverStatus, "opcounters", "delete") +
		nestedInt64(serverStatus, "opcounters", "command")
}

func mongoStatusSummary(metrics *DatabaseMetrics) string {
	if metrics == nil || metrics.Connections == nil {
		return "Connected"
	}

	return fmt.Sprintf(
		"%d active connections, %.1f ops/sec",
		metrics.Connections.Current,
		metrics.Activity.OperationsPerSecond,
	)
}

func postgresStatusSummary(metrics *DatabaseMetrics) string {
	if metrics == nil || metrics.Connections == nil {
		return "Connected"
	}

	return fmt.Sprintf(
		"%d active connections, %.1f tx/sec",
		metrics.Connections.Current,
		metrics.Activity.TransactionsPerSecond,
	)
}

func nestedString(doc bson.M, path ...string) string {
	value := nestedValue(doc, path...)
	if str, ok := value.(string); ok {
		return str
	}
	return ""
}

func nestedInt64(doc bson.M, path ...string) int64 {
	value := nestedValue(doc, path...)
	switch v := value.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	default:
		return 0
	}
}

func nestedValue(doc bson.M, path ...string) interface{} {
	var current interface{} = doc
	for _, key := range path {
		m, ok := current.(bson.M)
		if !ok {
			return nil
		}
		current, ok = m[key]
		if !ok {
			return nil
		}
	}
	return current
}
