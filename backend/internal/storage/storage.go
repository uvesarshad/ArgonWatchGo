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
)

type DataPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

type LogEntry struct {
	Type      string  `json:"type"`
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

type Storage struct {
	mu            sync.RWMutex
	fileMu        sync.Mutex
	data          map[string][]DataPoint
	enabled       bool
	retentionDays int
	dataPath      string
	dataFile      string
	file          *os.File
}

func NewStorage(cfg config.StorageConfig) *Storage {
	// Resolve path
	// Assuming cfg.DataPath is relative to executable or config?
	// JS: path.resolve(__dirname, '../../', config.dataPath)
	// We'll stick to a simple path relative to CWD for now or use absolute if provided

	s := &Storage{
		data:          make(map[string][]DataPoint),
		enabled:       cfg.Enabled,
		retentionDays: cfg.RetentionDays,
		dataPath:      cfg.DataPath,
		dataFile:      filepath.Join(cfg.DataPath, "metrics.jsonl"),
	}

	if s.enabled {
		s.ensureDataDirectory()
		s.loadData()
		s.initWriter()
	}

	return s
}

func (s *Storage) ensureDataDirectory() {
	if _, err := os.Stat(s.dataPath); os.IsNotExist(err) {
		os.MkdirAll(s.dataPath, 0755)
	}
}

func (s *Storage) initWriter() {
	f, err := os.OpenFile(s.dataFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Error opening metrics file: %v", err)
		return
	}
	s.file = f
}

func (s *Storage) loadData() {
	file, err := os.Open(s.dataFile)
	if err != nil {
		// File might not exist yet
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays).UnixMilli()

	count := 0
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil {
			if entry.Timestamp > cutoff {
				s.data[entry.Type] = append(s.data[entry.Type], DataPoint{
					Timestamp: entry.Timestamp,
					Value:     entry.Value,
				})
				count++
			}
		}
	}
	log.Printf("Loaded %d historical data points", count)
}

func (s *Storage) AddDataPoint(metricType string, value float64) {
	if !s.enabled {
		return
	}

	ts := time.Now().UnixMilli()
	point := DataPoint{Timestamp: ts, Value: value}

	s.mu.Lock()
	s.data[metricType] = append(s.data[metricType], point)

	// Simple memory pruning (every 1000 items or so? or just check length)
	// Keep last N items? 7 days * 24h * 60m * 60s / 2s = ~300k items max
	// Let's cap at 50k for safety per metric for now
	if len(s.data[metricType]) > 50000 {
		s.data[metricType] = s.data[metricType][5000:] // Remove oldest 5000
	}
	s.mu.Unlock()

	// Async write to file
	go s.writeToFile(metricType, ts, value)
}

func (s *Storage) writeToFile(metricType string, ts int64, value float64) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()

	if s.file == nil {
		return
	}

	entry := LogEntry{
		Type:      metricType,
		Timestamp: ts,
		Value:     value,
	}

	bytes, _ := json.Marshal(entry)
	// Write with newline
	if _, err := s.file.Write(append(bytes, '\n')); err != nil {
		log.Printf("Error writing to storage: %v", err)
	}
}
func (s *Storage) GetHistory(metricType string, duration string) []DataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	points, ok := s.data[metricType]
	if !ok {
		return []DataPoint{}
	}

	// Filter based on duration
	now := time.Now()
	var cutoff time.Time

	switch duration {
	case "1h":
		cutoff = now.Add(-1 * time.Hour)
	case "6h":
		cutoff = now.Add(-6 * time.Hour)
	case "24h":
		cutoff = now.Add(-24 * time.Hour)
	case "7d":
		cutoff = now.AddDate(0, 0, -7)
	default:
		cutoff = now.Add(-1 * time.Hour)
	}

	cutoffMs := cutoff.UnixMilli()

	// Find index (optimization)
	// For now linear filter is fine for Go speed
	var result []DataPoint
	for _, p := range points {
		if p.Timestamp > cutoffMs {
			result = append(result, p)
		}
	}
	return result
}

func (s *Storage) GetAllHistory(duration string) map[string][]DataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]DataPoint)
	for metricType := range s.data {
		result[metricType] = s.GetHistory(metricType, duration)
	}
	return result
}
