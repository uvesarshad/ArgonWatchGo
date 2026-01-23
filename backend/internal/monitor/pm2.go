package monitor

import (
	"encoding/json"
	"os/exec"
	"time"
)

type PM2Monitor struct {
	interval  time.Duration
	broadcast func(string, interface{})
	stopChan  chan struct{}
}

// Better struct based on common PM2 JSON
type PM2ProcessInfo struct {
	Pid    int    `json:"pid"`
	Name   string `json:"name"`
	PmID   int    `json:"pm_id"`
	Monit  struct {
		Memory int `json:"memory"`
		Cpu    int `json:"cpu"`
	} `json:"monit"`
	Pm2Env struct {
		Status      string `json:"status"`
		Uptime      int64  `json:"pm_uptime"`
		RestartTime int    `json:"restart_time"`
		Instances   int    `json:"instances"`
		Version     string `json:"version"`
	} `json:"pm2_env"`
}

type PM2ProcessDTO struct {
	ID          int    `json:"id"`
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Uptime      int64  `json:"uptime"`
	Restarts    int    `json:"restarts"`
	CPU         int    `json:"cpu"`
	Memory      int64  `json:"memory"`
	MemoryHuman string `json:"memory_human"`
}

func NewPM2Monitor(interval time.Duration, broadcast func(string, interface{})) *PM2Monitor {
	return &PM2Monitor{
		interval:  interval,
		broadcast: broadcast,
		stopChan:  make(chan struct{}),
	}
}

func (m *PM2Monitor) Start() {
	go m.loop()
}

func (m *PM2Monitor) Stop() {
	close(m.stopChan)
}

func (m *PM2Monitor) loop() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	m.checkPM2()

	for {
		select {
		case <-ticker.C:
			m.checkPM2()
		case <-m.stopChan:
			return
		}
	}
}

func (m *PM2Monitor) checkPM2() {
	// Execute "pm2 jlist"
	cmd := exec.Command("pm2", "jlist")
	// If permissions are needed, user should run the binary with appropriate permissions
	output, err := cmd.Output()
	if err != nil {
		// If pm2 is not found or fails, broadcast empty or error?
		// Just return for now to avoid noise
		return 
	}

	var processes []PM2ProcessInfo
	if err := json.Unmarshal(output, &processes); err != nil {
		return
	}

	var results []PM2ProcessDTO
	for _, p := range processes {
		dto := PM2ProcessDTO{
			ID:       p.PmID,
			PID:      p.Pid,
			Name:     p.Name,
			Status:   p.Pm2Env.Status,
			Uptime:   p.Pm2Env.Uptime,
			Restarts: p.Pm2Env.RestartTime,
			CPU:      p.Monit.Cpu,
			Memory:   int64(p.Monit.Memory),
		}
		results = append(results, dto)
	}

	m.broadcast("PM2_STATUS", results)
}