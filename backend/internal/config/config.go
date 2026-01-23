package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	Server       ServerConfig       `json:"server"`
	Monitoring   MonitoringConfig   `json:"monitoring"`
	Storage      StorageConfig      `json:"storage"`
	Alerts       AlertsConfig       `json:"alerts"`
	Notifications NotificationsConfig `json:"notifications"`
	Services     []ServiceConfig    `json:"services"`
	Databases    []DatabaseConfig   `json:"databases"`
	GithubRunner GithubRunnerConfig `json:"githubRunner"`
	PM2          PM2Config          `json:"pm2"`
	Terminal     TerminalConfig     `json:"terminal"`
	Auth         AuthConfig         `json:"auth"`
	Permissions  PermissionsConfig  `json:"permissions"`
	QuickCommands []QuickCommand    `json:"quickCommands"`
}

type ServerConfig struct {
	Port int    `json:"port"`
	Host string `json:"host"`
}

type MonitoringConfig struct {
	SystemInterval   int `json:"systemInterval"`
	RunnerInterval   int `json:"runnerInterval"`
	PM2Interval      int `json:"pm2Interval"`
	ServicesInterval int `json:"servicesInterval"`
}

type StorageConfig struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retentionDays"`
	DataPath      string `json:"dataPath"`
}

type AlertsConfig struct {
	Enabled bool        `json:"enabled"`
	Rules   []AlertRule `json:"rules"`
}

type AlertRule struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Metric        string   `json:"metric"`
	Condition     string   `json:"condition"`
	Threshold     float64  `json:"threshold"`
	Duration      int      `json:"duration"`
	Severity      string   `json:"severity"`
	Enabled       bool     `json:"enabled"`
	Notifications []string `json:"notifications"`
}

type NotificationsConfig struct {
	Desktop DesktopNotification `json:"desktop"`
	Email   EmailNotification   `json:"email"`
	Discord WebhookNotification `json:"discord"`
	Slack   WebhookNotification `json:"slack"`
}

type DesktopNotification struct {
	Enabled bool `json:"enabled"`
}

type EmailNotification struct {
	Enabled bool       `json:"enabled"`
	SMTP    SMTPConfig `json:"smtp"`
	From    string     `json:"from"`
	To      []string   `json:"to"`
}

type SMTPConfig struct {
	Host   string     `json:"host"`
	Port   int        `json:"port"`
	Secure bool       `json:"secure"`
	Auth   AuthDetail `json:"auth"`
}

type AuthDetail struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

type WebhookNotification struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhookUrl"`
}

type ServiceConfig struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"` // http, tcp, ping, process
	URL            string `json:"url"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	ProcessName    string `json:"processName"`
	Timeout        int    `json:"timeout"`
	ExpectedStatus int    `json:"expectedStatus"`
}

type DatabaseConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"` // mongodb, postgres, mysql, redis
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
}

type GithubRunnerConfig struct {
	RunnerPath string `json:"runnerPath"`
	LogPath    string `json:"logPath"`
	RunnerUser string `json:"runnerUser"`
}

type PM2Config struct {
	PM2User string `json:"pm2User"`
}

type TerminalConfig struct {
	Enabled        bool   `json:"enabled"`
	Shell          string `json:"shell"`
	SessionTimeout int    `json:"sessionTimeout"`
}

type AuthConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

type PermissionsConfig struct {
	UseSudo   bool   `json:"useSudo"`
	RunAsUser string `json:"runAsUser"`
}

type QuickCommand struct {
	Name                 string `json:"name"`
	Command              string `json:"command"`
	RequiresConfirmation bool   `json:"requiresConfirmation"`
	Description          string `json:"description"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cfg Config
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&cfg)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}