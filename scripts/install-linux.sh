#!/bin/bash

# ArgonWatchGo Linux Installation Script
# This script installs ArgonWatchGo as a systemd service

set -e

echo "==================================="
echo "ArgonWatchGo Installation Script"
echo "==================================="
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then 
    echo "Error: This script must be run as root (use sudo)"
    exit 1
fi

# Configuration
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/argon-watch-go"
DATA_DIR="/var/lib/argon-watch-go/data"
SERVICE_FILE="/etc/systemd/system/argon-watch-go.service"
BINARY_NAME="argon-watch-go"
DOWNLOAD_URL="https://github.com/montr-studio/ArgonWatchGo/releases/latest/download/argon-watch-go-linux"

# Create directories
echo "Creating directories..."
mkdir -p "$CONFIG_DIR"
mkdir -p "$DATA_DIR"

# Download binary
echo "Downloading ArgonWatchGo..."
if command -v wget &> /dev/null; then
    wget -O "$INSTALL_DIR/$BINARY_NAME" "$DOWNLOAD_URL"
elif command -v curl &> /dev/null; then
    curl -L -o "$INSTALL_DIR/$BINARY_NAME" "$DOWNLOAD_URL"
else
    echo "Error: Neither wget nor curl is installed. Please install one of them."
    exit 1
fi

# Make binary executable
chmod +x "$INSTALL_DIR/$BINARY_NAME"

# Generate random JWT secret
echo "Generating secure JWT secret..."
if command -v openssl &> /dev/null; then
    JWT_SECRET=$(openssl rand -base64 32)
else
    JWT_SECRET=$(cat /dev/urandom | tr -dc 'a-zA-Z0-9' | fold -w 32 | head -n 1)
fi

# Create default config if it doesn't exist
if [ ! -f "$CONFIG_DIR/config.json" ]; then
    echo "Creating default configuration..."
    cat > "$CONFIG_DIR/config.json" <<EOF
{
  "server": {
    "port": 3000,
    "host": "0.0.0.0"
  },
  "monitoring": {
    "systemInterval": 2000,
    "runnerInterval": 5000,
    "pm2Interval": 5000,
    "servicesInterval": 30000
  },
  "storage": {
    "enabled": true,
    "retentionDays": 7,
    "dataPath": "/var/lib/argon-watch-go/data"
  },
  "auth": {
    "enabled": true,
    "jwtSecret": "${JWT_SECRET}",
    "tokenExpiration": 24,
    "usersFile": "/var/lib/argon-watch-go/data/users.json"
  },
  "alerts": {
    "enabled": true,
    "rules": []
  },
  "notifications": {
    "desktop": {
      "enabled": false
    },
    "email": {
      "enabled": false
    },
    "discord": {
      "enabled": false
    },
    "slack": {
      "enabled": false
    }
  },
  "services": [],
  "databases": [],
  "githubRunner": {
    "runnerPath": "",
    "logPath": "",
    "runnerUser": ""
  },
  "pm2": {
    "pm2User": "root"
  },
  "terminal": {
    "enabled": false,
    "shell": "/bin/bash",
    "sessionTimeout": 3600000
  },
  "permissions": {
    "useSudo": false,
    "runAsUser": "root"
  },
  "quickCommands": []
}
EOF
fi

# Create systemd service
echo "Creating systemd service..."
cat > "$SERVICE_FILE" <<'EOF'
[Unit]
Description=ArgonWatchGo Server Monitor
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/etc/argon-watch-go
ExecStart=/usr/local/bin/argon-watch-go
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd
echo "Reloading systemd..."
systemctl daemon-reload

# Enable service
echo "Enabling service to start on boot..."
systemctl enable argon-watch-go

# Start service
echo "Starting ArgonWatchGo..."
systemctl start argon-watch-go

echo ""
echo "==================================="
echo "Installation Complete!"
echo "==================================="
echo ""
echo "ArgonWatchGo has been installed and started."
echo ""
echo "Configuration file: $CONFIG_DIR/config.json"
echo "Data directory: $DATA_DIR"
echo ""
echo "⚠️  IMPORTANT NEXT STEPS:"
echo "1. Edit $CONFIG_DIR/config.json and change the jwtSecret to a secure random string"
echo "2. Restart the service: sudo systemctl restart argon-watch-go"
echo "3. Access the dashboard at http://YOUR_SERVER_IP:3000"
echo "4. Complete the initial setup wizard to create your admin account"
echo ""
echo "Useful commands:"
echo "  Check status: sudo systemctl status argon-watch-go"
echo "  View logs: sudo journalctl -u argon-watch-go -f"
echo "  Restart: sudo systemctl restart argon-watch-go"
echo "  Stop: sudo systemctl stop argon-watch-go"
echo ""
