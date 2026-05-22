package storage

import (
	"os"
	"path/filepath"
	"testing"

	"argon-watch-go/internal/config"
)

func newTempStore(t *testing.T) (*Storage, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewStorage(config.StorageConfig{
		Enabled:       true,
		RetentionDays: 7,
		DataPath:      dir,
	})
	if s == nil || !s.enabled {
		t.Fatalf("storage failed to initialize")
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestStorage_RoundTrip(t *testing.T) {
	s, _ := newTempStore(t)

	// v1 API: implicit local server.
	s.AddDataPoint("cpu", 42.5)
	s.AddDataPoint("cpu", 50.0)
	s.AddDataPoint("memory", 71.2)

	got := s.GetHistory("cpu", "1h")
	if len(got) != 2 {
		t.Fatalf("cpu: want 2 points, got %d", len(got))
	}
	if got[0].Value != 42.5 || got[1].Value != 50.0 {
		t.Fatalf("cpu values out of order or wrong: %+v", got)
	}

	all := s.GetAllHistory("1h")
	if len(all["cpu"]) != 2 || len(all["memory"]) != 1 {
		t.Fatalf("all-history: %+v", all)
	}
}

func TestStorage_ScopedIsolation(t *testing.T) {
	s, _ := newTempStore(t)

	s.AddDataPointScoped("server-a", "cpu", 10)
	s.AddDataPointScoped("server-b", "cpu", 90)

	a := s.GetHistoryScoped("server-a", "cpu", "1h")
	b := s.GetHistoryScoped("server-b", "cpu", "1h")

	if len(a) != 1 || a[0].Value != 10 {
		t.Fatalf("server-a leaked or empty: %+v", a)
	}
	if len(b) != 1 || b[0].Value != 90 {
		t.Fatalf("server-b leaked or empty: %+v", b)
	}

	// v1 API must NOT see other servers' data.
	if got := s.GetHistory("cpu", "1h"); len(got) != 0 {
		t.Fatalf("v1 GetHistory leaked non-local data: %+v", got)
	}
}

func TestStorage_JSONLMigration(t *testing.T) {
	dir := t.TempDir()

	// Plant a v1 JSONL log with two valid rows + one garbage line.
	jsonl := filepath.Join(dir, "metrics.jsonl")
	contents := `{"type":"cpu","timestamp":` + nowMs() + `,"value":12.3}` + "\n" +
		`not json` + "\n" +
		`{"type":"memory","timestamp":` + nowMs() + `,"value":55.5}` + "\n"
	if err := os.WriteFile(jsonl, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewStorage(config.StorageConfig{
		Enabled:       true,
		RetentionDays: 7,
		DataPath:      dir,
	})
	if s == nil || !s.enabled {
		t.Fatalf("storage failed to initialize")
	}
	t.Cleanup(func() { _ = s.Close() })

	// JSONL should be archived after import.
	if _, err := os.Stat(jsonl); !os.IsNotExist(err) {
		t.Fatalf("metrics.jsonl should be renamed after import (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "metrics.jsonl.v1.bak")); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	// Imported rows should land under the local server.
	cpu := s.GetHistory("cpu", "1h")
	mem := s.GetHistory("memory", "1h")
	if len(cpu) != 1 || cpu[0].Value != 12.3 {
		t.Fatalf("cpu not imported: %+v", cpu)
	}
	if len(mem) != 1 || mem[0].Value != 55.5 {
		t.Fatalf("memory not imported: %+v", mem)
	}
}

// nowMs returns the current time in ms as a string. Kept local to avoid
// dragging time imports into the test file just for one helper.
func nowMs() string {
	return itoa(timeNowMillis())
}

func itoa(n int64) string {
	// Minimal int64-to-string, avoids fmt for a tighter test binary.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
