package monitor

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"argon-watch-go/internal/config"

	"github.com/shirou/gopsutil/v3/process"
)

type ServiceMonitor struct {
	services  []config.ServiceConfig
	interval  time.Duration
	broadcast func(string, interface{})
	stopChan  chan struct{}
}

type ServiceStatus struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	LastCheck    time.Time `json:"lastCheck"`
	URL          string    `json:"url,omitempty"`
	Host         string    `json:"host,omitempty"`
	Port         int       `json:"port,omitempty"`
	ProcessName  string    `json:"processName,omitempty"`
	ResponseTime int64     `json:"responseTime"`
	Status       string    `json:"status"`
	Message      string    `json:"message"`
}

func NewServiceMonitor(services []config.ServiceConfig, interval time.Duration, broadcast func(string, interface{})) *ServiceMonitor {
	return &ServiceMonitor{
		services:  services,
		interval:  interval,
		broadcast: broadcast,
		stopChan:  make(chan struct{}),
	}
}

func (m *ServiceMonitor) Start() {
	go m.loop()
}

func (m *ServiceMonitor) Stop() {
	close(m.stopChan)
}

func (m *ServiceMonitor) loop() {
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

func (m *ServiceMonitor) checkAll() {
	var results []ServiceStatus
	for _, svc := range m.services {
		results = append(results, m.checkService(svc))
	}
	m.broadcast("SERVICE_STATUS", results)
}

func (m *ServiceMonitor) checkService(svc config.ServiceConfig) ServiceStatus {
	startTime := time.Now()
	res := ServiceStatus{
		ID:        svc.ID,
		Name:      svc.Name,
		Type:      svc.Type,
		LastCheck: startTime,
		// Fill defaults
		URL:         svc.URL,
		Host:        svc.Host,
		Port:        svc.Port,
		ProcessName: svc.ProcessName,
	}

	if res.ID == "" {
		res.ID = svc.Name
	}

	timeout := time.Duration(5000) * time.Millisecond
	if svc.Timeout > 0 {
		timeout = time.Duration(svc.Timeout) * time.Millisecond
	}

	switch svc.Type {
	case "http", "https":
		client := http.Client{Timeout: timeout}
		resp, reqErr := client.Get(svc.URL)
		res.ResponseTime = time.Since(startTime).Milliseconds()
		if reqErr != nil {
			res.Status = "down"
			res.Message = reqErr.Error()
		} else {
			defer resp.Body.Close()
			expected := 200
			if svc.ExpectedStatus > 0 {
				expected = svc.ExpectedStatus
			}
			if resp.StatusCode == expected {
				res.Status = "up"
				res.Message = "OK"
			} else {
				res.Status = "degraded"
				res.Message = fmt.Sprintf("Expected %d, got %d", expected, resp.StatusCode)
			}
		}

	case "tcp":
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", svc.Host, svc.Port), timeout)
		res.ResponseTime = time.Since(startTime).Milliseconds()
		if dialErr != nil {
			res.Status = "down"
			res.Message = dialErr.Error()
		} else {
			conn.Close()
			res.Status = "up"
			res.Message = "Port is open"
		}

	case "ping":
		// Simple ping implementation using exec
		host := svc.Host
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("ping", "-n", "1", "-w", fmt.Sprintf("%d", timeout.Milliseconds()), host)
		} else {
			cmd = exec.Command("ping", "-c", "1", "-W", fmt.Sprintf("%d", timeout.Milliseconds()/1000), host)
		}

		runErr := cmd.Run()
		res.ResponseTime = time.Since(startTime).Milliseconds()
		if runErr != nil {
			res.Status = "down"
			res.Message = "Host unreachable"
		} else {
			res.Status = "up"
			res.Message = "Host is reachable"
		}

	case "process":
		// Check if process exists using gopsutil
		found := false
		procs, _ := process.Processes()
		nameToCheck := strings.ToLower(svc.ProcessName)

		for _, p := range procs {
			name, _ := p.Name()
			if strings.Contains(strings.ToLower(name), nameToCheck) {
				found = true
				break
			}
		}
		res.ResponseTime = time.Since(startTime).Milliseconds()
		if found {
			res.Status = "up"
			res.Message = "Process is running"
		} else {
			res.Status = "down"
			res.Message = "Process not found"
		}

	default:
		res.Status = "unknown"
		res.Message = "Unknown service type"
	}

	return res
}
