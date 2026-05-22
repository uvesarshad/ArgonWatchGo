//go:build windows

// Windows stub for the self-hosted runner detector. The runner ships
// as a Windows service on this platform; Phase 4.5 will probe via
// `Get-Service actions.runner.*` to mirror the Linux behavior.
package ghrunner

import (
	"context"
	"runtime"
	"time"

	"argon-watch-go/internal/config"
)

type Status struct {
	Active    bool      `json:"active"`
	Unit      string    `json:"unit"`
	LogPath   string    `json:"logPath"`
	LogTail   []string  `json:"logTail,omitempty"`
	OS        string    `json:"os"`
	LastError string    `json:"lastError,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Poller struct{}

func NewPoller(cfg config.GithubRunnerConfig, interval time.Duration, push func(string, interface{})) *Poller {
	// Best-effort: surface a single "not supported" status so the UI
	// shows something instead of leaving the panel forever empty.
	if push != nil {
		push("GH_RUNNER_STATUS", Status{
			OS:        runtime.GOOS,
			LogPath:   cfg.LogPath,
			LastError: "Windows runner detection ships in Phase 4.5",
			UpdatedAt: time.Now(),
		})
	}
	return &Poller{}
}

func (p *Poller) Start(ctx context.Context) {}
func (p *Poller) Stop()                     {}

func Configured(cfg config.GithubRunnerConfig) bool {
	return cfg.RunnerPath != "" || cfg.LogPath != ""
}
