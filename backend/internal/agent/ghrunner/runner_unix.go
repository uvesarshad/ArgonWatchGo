//go:build !windows

// Package ghrunner detects and reports the health of a self-hosted
// GitHub Actions runner running on the same host as the agent.
//
// Phase 4 scope: read-only. We probe `systemctl is-active actions.runner.*`
// (the unit name pattern the official runner installer uses) and tail
// the last N lines of the configured log file. Start/stop/restart
// buttons land in Phase 4.5 once they can plug into the terminal RBAC
// path.
package ghrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"argon-watch-go/internal/config"
)

// Status is the per-poll snapshot pushed to the hub as GH_RUNNER_STATUS.
// Always populated — when the runner is missing we still send a row with
// Active=false so the UI can render "no runner installed" cleanly.
type Status struct {
	Active     bool      `json:"active"`     // systemctl is-active reports active
	Unit       string    `json:"unit"`       // resolved systemd unit, e.g. "actions.runner.foo-bar.service"
	LogPath    string    `json:"logPath"`
	LogTail    []string  `json:"logTail,omitempty"`
	OS         string    `json:"os"`
	LastError  string    `json:"lastError,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Poller probes the runner on a schedule and broadcasts the status via
// the agent's outbound queue (using the same envelope shape as the
// system monitors).
type Poller struct {
	cfg      config.GithubRunnerConfig
	interval time.Duration
	push     func(string, interface{}) // (msgType, payload) — agent serverID already bound
	stop     chan struct{}
}

func NewPoller(cfg config.GithubRunnerConfig, interval time.Duration, push func(string, interface{})) *Poller {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Poller{
		cfg:      cfg,
		interval: interval,
		push:     push,
		stop:     make(chan struct{}),
	}
}

func (p *Poller) Start(ctx context.Context) {
	go p.loop(ctx)
}

func (p *Poller) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

func (p *Poller) loop(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	p.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	s := Status{
		OS:        runtime.GOOS,
		LogPath:   p.cfg.LogPath,
		UpdatedAt: time.Now(),
	}

	unit, active, err := probeSystemd(ctx)
	s.Unit = unit
	s.Active = active
	if err != nil {
		s.LastError = err.Error()
	}

	if p.cfg.LogPath != "" {
		tail, err := tailFile(p.cfg.LogPath, 10)
		if err != nil && s.LastError == "" {
			s.LastError = err.Error()
		}
		s.LogTail = tail
	}

	if p.push != nil {
		p.push("GH_RUNNER_STATUS", s)
	}
}

// probeSystemd looks for any `actions.runner.*` unit and reports its
// activity state. Returns ("", false, nil) when none exists — that's
// the "no runner installed" path, not an error.
func probeSystemd(ctx context.Context) (unit string, active bool, err error) {
	// `systemctl list-units --type=service --no-legend 'actions.runner.*'`
	// is the cheapest reliable probe; busybox/init systems will fail
	// here, and that's fine — we just return "".
	cmd := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--no-legend", "--state=loaded", "actions.runner.*")
	var out bytes.Buffer
	cmd.Stdout = &out
	if runErr := cmd.Run(); runErr != nil {
		// `systemctl` missing or no matching units — both end up here.
		// Treat as "no runner present" rather than surfacing the noise.
		return "", false, nil
	}
	scanner := strings.SplitN(strings.TrimSpace(out.String()), "\n", 2)
	if len(scanner) == 0 || scanner[0] == "" {
		return "", false, nil
	}
	fields := strings.Fields(scanner[0])
	if len(fields) < 4 {
		return "", false, nil
	}
	unit = fields[0]
	// fields[3] is the SUB state: "running", "exited", "failed", etc.
	active = fields[3] == "running"
	return unit, active, nil
}

// tailFile reads the last n lines of a file without loading the whole
// thing — uses a small reverse-walk over the last 64KB which is more
// than enough for runner logs that rotate at 1MB.
func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const window = 65536
	size := fi.Size()
	offset := int64(0)
	if size > window {
		offset = size - window
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// Configured returns true when the agent config provides enough info to
// actually do anything useful. Callers use this to gate Start().
func Configured(cfg config.GithubRunnerConfig) bool {
	return cfg.RunnerPath != "" || cfg.LogPath != ""
}

// describeError is shared by the agent's wiring layer when it wants to
// surface a friendly "not configured" message without importing strings
// directly.
func describeError(reason string) string {
	return fmt.Sprintf("ghrunner: %s", reason)
}
