package monitor

import (
	"encoding/json"
	"log"
	"net"
	"runtime"
	"time"

	"argon-watch-go/internal/alerts"
	"argon-watch-go/internal/storage"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	netstat "github.com/shirou/gopsutil/v3/net"
)

type SystemMonitor struct {
	interval       time.Duration
	broadcast      func(string, interface{})
	storage        *storage.Storage
	alerts         *alerts.AlertEngine
	stopChan       chan struct{}
	prevNetStats   map[string]netstat.IOCountersStat
	prevDiskIO     disk.IOCountersStat
	lastUpdateTime time.Time
}

// Complete system metrics matching frontend expectations
type CompleteSystemMetrics struct {
	System       SystemInfo          `json:"system"`
	CPU          CPUMetrics          `json:"cpu"`
	Memory       MemoryMetrics       `json:"memory"`
	Disk         []DiskMetrics       `json:"disk"`
	Network      []NetworkMetrics    `json:"network"`
	DiskIO       DiskIOMetrics       `json:"diskIO"`
	Temperatures *TemperatureMetrics `json:"temperatures,omitempty"`
	DiskHealth   []DiskHealthMetrics `json:"diskHealth,omitempty"`
	Uptime       uint64              `json:"uptime"`
}

type SystemInfo struct {
	OS        string `json:"os"`
	Distro    string `json:"distro"`
	Hostname  string `json:"hostname"`
	IPAddress string `json:"ipAddress"`
}

type CPUMetrics struct {
	Load          float64    `json:"load"`
	LoadAverage1  float64    `json:"loadAverage1"`
	LoadAverage5  float64    `json:"loadAverage5"`
	LoadAverage15 float64    `json:"loadAverage15"`
	AvgSpeed      float64    `json:"avgSpeed"`
	Speed         float64    `json:"speed"`
	PhysicalCores int        `json:"physicalCores"`
	Cores         int        `json:"cores"`
	CoreLoads     []CoreLoad `json:"coreLoads"`
}

type CoreLoad struct {
	Load float64 `json:"load"`
}

type MemoryMetrics struct {
	Percentage     float64 `json:"percentage"`
	Used           uint64  `json:"used"`
	Active         uint64  `json:"active"`
	Cached         uint64  `json:"cached"`
	Buffers        uint64  `json:"buffers"`
	SwapUsed       uint64  `json:"swapUsed"`
	SwapTotal      uint64  `json:"swapTotal"`
	SwapPercentage float64 `json:"swapPercentage"`
	Total          uint64  `json:"total"`
}

type DiskMetrics struct {
	Fs        string  `json:"fs"`
	Mount     string  `json:"mount"`
	Size      uint64  `json:"size"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Use       float64 `json:"use"`
}

type NetworkMetrics struct {
	Iface     string `json:"iface"`
	RxSec     uint64 `json:"rx_sec"`
	TxSec     uint64 `json:"tx_sec"`
	Operstate string `json:"operstate"`
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
}

type DiskIOMetrics struct {
	RxSec uint64 `json:"rx_sec"`
	WxSec uint64 `json:"wx_sec"`
}

type TemperatureMetrics struct {
	Main float64       `json:"main"`
	GPU  []GPUTempInfo `json:"gpu,omitempty"`
}

type GPUTempInfo struct {
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	FanSpeed    float64 `json:"fanSpeed"`
}

type DiskHealthMetrics struct {
	Device      string  `json:"device"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Size        uint64  `json:"size"`
	SmartStatus string  `json:"smartStatus"`
	Temperature float64 `json:"temperature,omitempty"`
}

func NewSystemMonitor(interval time.Duration, broadcast func(string, interface{}), store *storage.Storage, alerts *alerts.AlertEngine) *SystemMonitor {
	return &SystemMonitor{
		interval:       interval,
		broadcast:      broadcast,
		storage:        store,
		alerts:         alerts,
		stopChan:       make(chan struct{}),
		prevNetStats:   make(map[string]netstat.IOCountersStat),
		lastUpdateTime: time.Now(),
	}
}

func (m *SystemMonitor) Start() {
	go m.loop()
}

func (m *SystemMonitor) Stop() {
	close(m.stopChan)
}

func (m *SystemMonitor) loop() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	m.collectMetrics()

	for {
		select {
		case <-ticker.C:
			m.collectMetrics()
		case <-m.stopChan:
			return
		}
	}
}

func (m *SystemMonitor) collectMetrics() {
	now := time.Now()
	timeDelta := now.Sub(m.lastUpdateTime).Seconds()
	if timeDelta == 0 {
		timeDelta = 1 // Prevent division by zero
	}

	// System Info
	systemInfo := m.getSystemInfo()

	// CPU Metrics
	cpuMetrics := m.getCPUMetrics()

	// Memory Metrics
	memMetrics := m.getMemoryMetrics()

	// Disk Metrics
	diskMetrics := m.getDiskMetrics()

	// Network Metrics
	networkMetrics := m.getNetworkMetrics(timeDelta)

	// Disk I/O Metrics
	diskIOMetrics := m.getDiskIOMetrics(timeDelta)

	// Temperature Metrics (optional, may not be available on all systems)
	tempMetrics := m.getTemperatureMetrics()

	// Disk Health (optional)
	diskHealth := m.getDiskHealth()

	// Uptime
	uptime := getHostUptime()

	data := CompleteSystemMetrics{
		System:       systemInfo,
		CPU:          cpuMetrics,
		Memory:       memMetrics,
		Disk:         diskMetrics,
		Network:      networkMetrics,
		DiskIO:       diskIOMetrics,
		Temperatures: tempMetrics,
		DiskHealth:   diskHealth,
		Uptime:       uptime,
	}

	// Store historical data
	if m.storage != nil {
		m.storage.AddDataPoint("cpu", cpuMetrics.Load)
		m.storage.AddDataPoint("memory", memMetrics.Percentage)
	}

	// Check alerts
	if m.alerts != nil {
		var mapData map[string]interface{}
		bytes, _ := json.Marshal(data)
		json.Unmarshal(bytes, &mapData)
		m.alerts.CheckMetrics(mapData)
	}

	m.broadcast("SYSTEM_METRICS", data)
	m.lastUpdateTime = now
}

func (m *SystemMonitor) getSystemInfo() SystemInfo {
	info, _ := host.Info()

	hostname := info.Hostname
	if hostname == "" {
		hostname = "Unknown"
	}

	osName := info.OS
	if osName == "" {
		osName = runtime.GOOS
	}

	distro := info.Platform
	if distro == "" {
		distro = osName
	}

	ipAddress := m.getLocalIP()

	return SystemInfo{
		OS:        osName,
		Distro:    distro,
		Hostname:  hostname,
		IPAddress: ipAddress,
	}
}

func (m *SystemMonitor) getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "Unknown"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "Unknown"
}

func (m *SystemMonitor) getCPUMetrics() CPUMetrics {
	// Total CPU load
	totalPercent, err := cpu.Percent(0, false)
	cpuLoad := 0.0
	if err == nil && len(totalPercent) > 0 {
		cpuLoad = totalPercent[0]
	}

	// Per-core loads
	perCpuPercent, _ := cpu.Percent(0, true)
	coreLoads := make([]CoreLoad, len(perCpuPercent))
	for i, p := range perCpuPercent {
		coreLoads[i] = CoreLoad{Load: p}
	}

	// Load averages
	loadAvg, _ := load.Avg()
	loadAvg1 := 0.0
	loadAvg5 := 0.0
	loadAvg15 := 0.0
	if loadAvg != nil {
		loadAvg1 = loadAvg.Load1
		loadAvg5 = loadAvg.Load5
		loadAvg15 = loadAvg.Load15
	}

	// CPU info
	cpuInfo, _ := cpu.Info()
	cpuSpeed := 0.0
	if len(cpuInfo) > 0 {
		cpuSpeed = cpuInfo[0].Mhz / 1000.0 // Convert MHz to GHz
	}

	// Core counts
	physicalCores, _ := cpu.Counts(false)
	logicalCores, _ := cpu.Counts(true)

	return CPUMetrics{
		Load:          cpuLoad,
		LoadAverage1:  loadAvg1,
		LoadAverage5:  loadAvg5,
		LoadAverage15: loadAvg15,
		AvgSpeed:      cpuSpeed,
		Speed:         cpuSpeed,
		PhysicalCores: physicalCores,
		Cores:         logicalCores,
		CoreLoads:     coreLoads,
	}
}

func (m *SystemMonitor) getMemoryMetrics() MemoryMetrics {
	vm, err := mem.VirtualMemory()
	if err != nil {
		log.Printf("Error getting memory stats: %v", err)
		return MemoryMetrics{}
	}

	swap, err := mem.SwapMemory()
	swapUsed := uint64(0)
	swapTotal := uint64(0)
	swapPercentage := 0.0
	if err == nil {
		swapUsed = swap.Used
		swapTotal = swap.Total
		swapPercentage = swap.UsedPercent
	}

	return MemoryMetrics{
		Percentage:     vm.UsedPercent,
		Used:           vm.Used,
		Active:         vm.Active,
		Cached:         vm.Cached,
		Buffers:        vm.Buffers,
		SwapUsed:       swapUsed,
		SwapTotal:      swapTotal,
		SwapPercentage: swapPercentage,
		Total:          vm.Total,
	}
}

func (m *SystemMonitor) getDiskMetrics() []DiskMetrics {
	parts, err := disk.Partitions(false)
	var disks []DiskMetrics
	if err != nil {
		return disks
	}

	for _, p := range parts {
		usage, err := disk.Usage(p.Mountpoint)
		if err == nil {
			disks = append(disks, DiskMetrics{
				Fs:        p.Fstype,
				Mount:     p.Mountpoint,
				Size:      usage.Total,
				Used:      usage.Used,
				Available: usage.Free,
				Use:       usage.UsedPercent,
			})
		}
	}

	return disks
}

func (m *SystemMonitor) getNetworkMetrics(timeDelta float64) []NetworkMetrics {
	ioStats, err := netstat.IOCounters(true)
	var networks []NetworkMetrics
	if err != nil {
		return networks
	}

	for _, stat := range ioStats {
		rxSec := uint64(0)
		txSec := uint64(0)

		// Calculate speed based on delta
		if prev, exists := m.prevNetStats[stat.Name]; exists {
			rxDelta := stat.BytesRecv - prev.BytesRecv
			txDelta := stat.BytesSent - prev.BytesSent
			rxSec = uint64(float64(rxDelta) / timeDelta)
			txSec = uint64(float64(txDelta) / timeDelta)
		}

		// Store current stats for next iteration
		m.prevNetStats[stat.Name] = stat

		// Determine operational state (simplified)
		operstate := "up"
		if stat.BytesRecv == 0 && stat.BytesSent == 0 {
			operstate = "down"
		}

		networks = append(networks, NetworkMetrics{
			Iface:     stat.Name,
			RxSec:     rxSec,
			TxSec:     txSec,
			Operstate: operstate,
			RxErrors:  stat.Errin,
			TxErrors:  stat.Errout,
			RxDropped: stat.Dropin,
			TxDropped: stat.Dropout,
		})
	}

	return networks
}

func (m *SystemMonitor) getDiskIOMetrics(timeDelta float64) DiskIOMetrics {
	ioCounters, err := disk.IOCounters()
	if err != nil {
		return DiskIOMetrics{}
	}

	// Aggregate all disk I/O
	var totalRead, totalWrite uint64
	for _, io := range ioCounters {
		totalRead += io.ReadBytes
		totalWrite += io.WriteBytes
	}

	rxSec := uint64(0)
	wxSec := uint64(0)

	// Calculate speed based on delta
	if m.prevDiskIO.ReadBytes > 0 {
		readDelta := totalRead - m.prevDiskIO.ReadBytes
		writeDelta := totalWrite - m.prevDiskIO.WriteBytes
		rxSec = uint64(float64(readDelta) / timeDelta)
		wxSec = uint64(float64(writeDelta) / timeDelta)
	}

	// Store current stats for next iteration
	m.prevDiskIO.ReadBytes = totalRead
	m.prevDiskIO.WriteBytes = totalWrite

	return DiskIOMetrics{
		RxSec: rxSec,
		WxSec: wxSec,
	}
}

func (m *SystemMonitor) getTemperatureMetrics() *TemperatureMetrics {
	// Temperature monitoring is platform-specific and may not be available
	// This is a placeholder - actual implementation would use platform-specific libraries
	// For now, return nil to hide the temperature card
	return nil
}

func (m *SystemMonitor) getDiskHealth() []DiskHealthMetrics {
	// SMART disk health monitoring requires platform-specific tools
	// This is a placeholder - actual implementation would use smartctl or similar
	// For now, return empty array to hide the disk health card
	return nil
}

func getHostUptime() uint64 {
	info, _ := host.Info()
	return info.Uptime
}
