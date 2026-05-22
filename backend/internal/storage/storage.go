// Package storage is the metrics persistence facade. In v2 it delegates to an
// embedded SQLite database (internal/storage/sqlite). The old JSONL-on-disk
// log is auto-migrated to SQLite on first launch and then archived to
// metrics.jsonl.v1.bak so no historical data is lost.
//
// Public API (AddDataPoint, GetHistory, GetAllHistory) is preserved so
// existing callers keep working. New callers should use the scoped methods
// (AddDataPointScoped, GetHistoryScoped) to target a specific server.
package storage

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"argon-watch-go/internal/config"
	"argon-watch-go/internal/storage/sqlite"
)

// DataPoint mirrors sqlite.DataPoint so callers can ignore the inner package.
type DataPoint = sqlite.DataPoint

// LocalServerID is the implicit server ID used for the in-process monitors
// that ship inside a single-binary hub install. Multi-server agents use
// their own IDs (set during enrollment, see internal/hub).
const LocalServerID = "local"

type Storage struct {
	mu       sync.RWMutex
	store    *sqlite.Store
	enabled  bool
	dataPath string

	retentionDays int
}

func NewStorage(cfg config.StorageConfig) *Storage {
	s := &Storage{
		enabled:       cfg.Enabled,
		dataPath:      cfg.DataPath,
		retentionDays: cfg.RetentionDays,
	}

	if !s.enabled {
		return s
	}

	if err := os.MkdirAll(cfg.DataPath, 0755); err != nil {
		log.Printf("storage: mkdir %s: %v — storage disabled", cfg.DataPath, err)
		s.enabled = false
		return s
	}

	store, err := sqlite.Open(cfg.DataPath, cfg.RetentionDays)
	if err != nil {
		log.Printf("storage: sqlite open failed: %v — storage disabled", err)
		s.enabled = false
		return s
	}
	s.store = store

	// One-shot migration from v1 JSONL log if present.
	if imported, err := s.importJSONL(); err != nil {
		log.Printf("storage: JSONL migration failed: %v (continuing — new writes still work)", err)
	} else if imported > 0 {
		log.Printf("storage: imported %d data points from metrics.jsonl into SQLite", imported)
	}

	// Background retention prune. Daily cadence — cheap.
	go s.pruneLoop()

	return s
}

// AddDataPoint records a metric for the implicit local server. Kept for
// backward compatibility with the v1 monitors that have no concept of
// server ID.
func (s *Storage) AddDataPoint(metricType string, value float64) {
	s.AddDataPointScoped(LocalServerID, metricType, value)
}

// AddDataPointScoped records a metric for the named server.
func (s *Storage) AddDataPointScoped(serverID, metricType string, value float64) {
	if !s.enabled || s.store == nil {
		return
	}
	if err := s.store.AddDataPoint(serverID, metricType, value); err != nil {
		// One-off log; do not spam. SQLite errors here usually indicate disk
		// pressure or a closed DB during shutdown.
		log.Printf("storage: write %s/%s failed: %v", serverID, metricType, err)
	}
}

// GetHistory returns local-server history (v1 API).
func (s *Storage) GetHistory(metricType string, duration string) []DataPoint {
	return s.GetHistoryScoped(LocalServerID, metricType, duration)
}

func (s *Storage) GetHistoryScoped(serverID, metricType, duration string) []DataPoint {
	if !s.enabled || s.store == nil {
		return nil
	}
	return s.store.GetHistory(serverID, metricType, duration)
}

// GetAllHistory returns local-server history grouped by metric type (v1 API).
func (s *Storage) GetAllHistory(duration string) map[string][]DataPoint {
	return s.GetAllHistoryScoped(LocalServerID, duration)
}

func (s *Storage) GetAllHistoryScoped(serverID, duration string) map[string][]DataPoint {
	if !s.enabled || s.store == nil {
		return map[string][]DataPoint{}
	}
	return s.store.GetAllHistory(serverID, duration)
}

// SQLiteStore exposes the underlying handle for callers that need direct
// access (alerts history, terminal sessions, AI conversations).
func (s *Storage) SQLiteStore() *sqlite.Store {
	return s.store
}

func (s *Storage) Close() error {
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *Storage) pruneLoop() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		if s.store == nil {
			return
		}
		n, err := s.store.PruneOlderThan(s.retentionDays)
		if err != nil {
			log.Printf("storage: prune failed: %v", err)
		} else if n > 0 {
			log.Printf("storage: pruned %d rows older than %d days", n, s.retentionDays)
		}
	}
}

// importJSONL reads any pre-v2 metrics.jsonl in the data directory and bulk-
// inserts the rows into SQLite under serverID="local". On success the file
// is renamed to metrics.jsonl.v1.bak so the import never runs twice.
//
// Returns the number of rows inserted (0 if no file or nothing to do).
func (s *Storage) importJSONL() (int, error) {
	if s.store == nil {
		return 0, nil
	}

	jsonlPath := filepath.Join(s.dataPath, "metrics.jsonl")
	f, err := os.Open(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	type legacyEntry struct {
		Type      string  `json:"type"`
		Timestamp int64   `json:"timestamp"`
		Value     float64 `json:"value"`
	}

	cutoff := time.Now().AddDate(0, 0, -s.retentionDays).UnixMilli()

	db := s.store.DB()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT INTO metrics (server_id, type, ts, value) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	// Default scanner buffer is 64KB — fine for one JSON line per metric.
	for scanner.Scan() {
		var e legacyEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Timestamp <= cutoff {
			continue
		}
		if _, err := stmt.Exec(LocalServerID, e.Type, e.Timestamp, e.Value); err != nil {
			continue
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Archive so we don't re-import on the next boot. Close the file handle
	// first (Windows can't rename an open file).
	f.Close()
	backupPath := filepath.Join(s.dataPath, "metrics.jsonl.v1.bak")
	if err := os.Rename(jsonlPath, backupPath); err != nil {
		log.Printf("storage: imported JSONL but rename to .v1.bak failed: %v", err)
	}

	return count, nil
}
