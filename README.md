# ArgonWatchGo - Server Monitor

A lightweight, self-hostable server monitoring tool with comprehensive metrics and minimal resource usage.

## Features

### ✅ **Enhanced System Resource Monitoring**
- **Real-time CPU monitoring** with per-core loads and load averages (1m, 5m, 15m)
- **Detailed memory metrics** including active, cached, buffers, and swap usage
- **Disk I/O performance** tracking with read/write speeds (MB/s)
- **Network interface monitoring** with bandwidth, errors, and packet loss tracking
- **Live charts and visualizations** with color-coded status indicators
- **Historical data storage** (7 days retention) for trend analysis

### 🌡️ **Hardware Health Monitoring**
- **Temperature sensors** for CPU, GPU, and disk drives
- **SMART disk health** status and failure prediction
- **GPU metrics** including utilization, VRAM usage, and fan speeds
- **Fan speed monitoring** for cooling system health
- **Color-coded warnings** (Green < 60°C, Yellow < 80°C, Red > 80°C)

### ⚙️ **Detailed CPU Insights**
- **Per-core CPU loads** with individual utilization bars
- **Current CPU frequency** monitoring across all cores
- **Load averages** for system load trend analysis
- **Physical and logical core** count display

### 💾 **Advanced Memory Analytics**
- **Active memory** usage tracking
- **Cache and buffer** memory breakdown
- **Swap memory** utilization and percentage
- **Real-time memory** allocation visualization

### 💿 **Disk Performance & Health**
- **Real-time I/O speeds** (read/write MB/s)
- **SMART health status** for early failure detection
- **Disk temperature** monitoring per drive
- **Usage percentage** across all partitions

### 🌐 **Network Interface Details**
- **Per-interface statistics** (RX/TX speeds)
- **Error counters** (RX errors, TX errors)
- **Dropped packet** tracking
- **Interface status** monitoring (up/down)

### 📊 **PM2 Process Management**
- View all PM2 processes in a table
- Monitor status, uptime, restarts, CPU, and memory

### ⚡ **Service & Database Monitoring**
- **Services**: HTTP, TCP, Ping, and Process checks
- **Databases**: MongoDB, PostgreSQL, MySQL, Redis connection checks

## Installation

### Prerequisites
- None! (The binary contains everything)
- (Optional) PM2 for process monitoring

### Setup

1. **Download the latest release** for your platform (Windows/Linux).

2. **Create a `config.json`** file (see Configuration section).

3. **Run the executable:**
   ```bash
   # Linux
   ./argon-watch-go-linux
   
   # Windows
   argon-watch-go.exe
   ```

4. **Access the dashboard** at `http://localhost:3000`

## Build from Source

1. **Install Go 1.21+**

2. **Clone the repository**

3. **Build:**
   ```bash
   cd backend
   go mod tidy
   go build -o argon-watch-go ./cmd/server
   ```

## Configuration

Edit `config.json`:

```json
{
  "server": {
    "port": 3000,
    "host": "0.0.0.0"
  },
  "monitoring": {
    "systemInterval": 2000,
    "servicesInterval": 30000
  },
  "services": [
    {
       "name": "My Website",
       "type": "http",
       "url": "https://example.com"
    }
  ],
  "databases": [
    {
       "name": "Main DB",
       "type": "postgres",
       "host": "localhost",
       "port": 5432,
       "user": "postgres",
       "password": "password",
       "database": "mydb"
    }
  ],
  "alerts": {
      "enabled": true,
      "rules": [
          {
              "id": "cpu-high",
              "metric": "cpu.load",
              "condition": ">",
              "threshold": 90,
              "notifications": ["email", "discord"]
          }
      ]
  }
}
```

## Resource Usage

- **Memory**: ~10-15MB RAM
- **CPU**: <1% average load
- **Disk**: Minimal (append-only logs)

## License

MIT
