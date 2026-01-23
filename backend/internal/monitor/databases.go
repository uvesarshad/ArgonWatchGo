package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"argon-watch-go/internal/config"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type DatabaseMonitor struct {
	databases []config.DatabaseConfig
	interval  time.Duration
	broadcast func(string, interface{})
	stopChan  chan struct{}
}

type DatabaseStatus struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Status       string    `json:"status"`       // "up", "down", "error"
	ResponseTime int64     `json:"responseTime"` // ms
	Message      string    `json:"message"`
	LastCheck    time.Time `json:"lastCheck"`
}

func NewDatabaseMonitor(databases []config.DatabaseConfig, interval time.Duration, broadcast func(string, interface{})) *DatabaseMonitor {
	return &DatabaseMonitor{
		databases: databases,
		interval:  interval,
		broadcast: broadcast,
		stopChan:  make(chan struct{}),
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
	m.broadcast("DATABASE_STATUS", results)
}

func (m *DatabaseMonitor) checkDatabase(db config.DatabaseConfig) DatabaseStatus {
	startTime := time.Now()
	res := DatabaseStatus{
		ID:        db.ID,
		Name:      db.Name,
		Type:      db.Type,
		LastCheck: startTime,
	}

	if res.ID == "" {
		res.ID = db.Name
	}

	timeout := 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var err error

	switch db.Type {
	case "mongodb":
		uri := fmt.Sprintf("mongodb://%s:%d", db.Host, db.Port)
		if db.User != "" {
			uri = fmt.Sprintf("mongodb://%s:%s@%s:%d", db.User, db.Password, db.Host, db.Port)
		}

		client, connErr := mongo.Connect(ctx, options.Client().ApplyURI(uri))
		if connErr != nil {
			err = connErr
		} else {
			defer client.Disconnect(ctx)
			err = client.Ping(ctx, readpref.Primary())
		}

	case "postgres":
		dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", db.User, db.Password, db.Host, db.Port, db.Database)
		pool, connErr := pgxpool.New(ctx, dsn)
		if connErr != nil {
			err = connErr
		} else {
			defer pool.Close()
			err = pool.Ping(ctx)
		}

	case "mysql":
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", db.User, db.Password, db.Host, db.Port, db.Database)
		conn, connErr := sql.Open("mysql", dsn)
		if connErr != nil {
			err = connErr
		} else {
			defer conn.Close()
			err = conn.PingContext(ctx)
		}

	case "redis":
		addr := fmt.Sprintf("%s:%d", db.Host, db.Port)
		rdb := redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: db.Password, // no password set
			DB:       0,           // use default DB
		})
		defer rdb.Close()
		_, err = rdb.Ping(ctx).Result()

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
		res.Message = "Connected"
	}

	return res
}
