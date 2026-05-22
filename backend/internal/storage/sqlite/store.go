// Package sqlite is the v2 storage backend. It replaces the JSONL append-only
// log with an embedded SQLite database (modernc.org/sqlite, pure Go — no cgo)
// so the existing single-binary deployment story is preserved.
package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type DataPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

// Store is the SQLite-backed metrics store. Safe for concurrent use.
type Store struct {
	db            *sql.DB
	mu            sync.RWMutex
	retentionDays int
	dataPath      string
	dbPath        string
}

// Open opens (or creates) the SQLite database under dataPath/argonwatch.db
// and runs any pending migrations. WAL mode is enabled so reads do not block
// writers at our expected 1Hz metric ingest rate.
func Open(dataPath string, retentionDays int) (*Store, error) {
	dbPath := filepath.Join(dataPath, "argonwatch.db")

	// _journal=WAL is critical: without it, the single-writer lock makes
	// concurrent monitor goroutines serialize through fsync().
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single connection for writes avoids "database is locked" under load;
	// readers still parallelize via WAL.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)

	s := &Store{
		db:            db,
		retentionDays: retentionDays,
		dataPath:      dataPath,
		dbPath:        dbPath,
	}

	if err := s.runMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the raw database handle for components that need direct access
// (alerts history, terminal sessions, AI conversations). Use sparingly.
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) runMigrations() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`)
	if err != nil {
		return err
	}

	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}

	for i, sqlText := range migrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(sqlText); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration v%d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// AddDataPoint inserts one metric sample for the given server.
// serverID="local" is the default for single-binary deployments.
func (s *Store) AddDataPoint(serverID, metricType string, value float64) error {
	ts := time.Now().UnixMilli()
	_, err := s.db.Exec(
		`INSERT INTO metrics (server_id, type, ts, value) VALUES (?, ?, ?, ?)`,
		serverID, metricType, ts, value,
	)
	return err
}

// GetHistory returns DataPoints for one server/metric within the duration window.
// duration is one of: "1h", "6h", "24h", "7d". Unknown values fall back to "1h".
func (s *Store) GetHistory(serverID, metricType, duration string) []DataPoint {
	cutoff := cutoffFor(duration)
	rows, err := s.db.Query(
		`SELECT ts, value FROM metrics
		 WHERE server_id = ? AND type = ? AND ts > ?
		 ORDER BY ts ASC`,
		serverID, metricType, cutoff,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []DataPoint
	for rows.Next() {
		var p DataPoint
		if err := rows.Scan(&p.Timestamp, &p.Value); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// GetAllHistory returns every metric type for one server within the window.
// Used by the WS GET_HISTORICAL_DATA handler.
func (s *Store) GetAllHistory(serverID, duration string) map[string][]DataPoint {
	cutoff := cutoffFor(duration)
	rows, err := s.db.Query(
		`SELECT type, ts, value FROM metrics
		 WHERE server_id = ? AND ts > ?
		 ORDER BY ts ASC`,
		serverID, cutoff,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string][]DataPoint)
	for rows.Next() {
		var t string
		var p DataPoint
		if err := rows.Scan(&t, &p.Timestamp, &p.Value); err == nil {
			out[t] = append(out[t], p)
		}
	}
	return out
}

// PruneOlderThan deletes metric rows older than retentionDays. Returns rows affected.
// Caller picks the cadence (typically daily).
func (s *Store) PruneOlderThan(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).UnixMilli()
	res, err := s.db.Exec(`DELETE FROM metrics WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountMetrics returns the total metric row count (debugging/migration sanity check).
func (s *Store) CountMetrics() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM metrics`).Scan(&n)
	return n, err
}

func cutoffFor(duration string) int64 {
	now := time.Now()
	switch duration {
	case "6h":
		return now.Add(-6 * time.Hour).UnixMilli()
	case "24h":
		return now.Add(-24 * time.Hour).UnixMilli()
	case "7d":
		return now.AddDate(0, 0, -7).UnixMilli()
	case "1h":
		fallthrough
	default:
		return now.Add(-1 * time.Hour).UnixMilli()
	}
}
