package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Server        ServerConfig        `json:"server"`
	Monitoring    MonitoringConfig    `json:"monitoring"`
	Storage       StorageConfig       `json:"storage"`
	Alerts        AlertsConfig        `json:"alerts"`
	Notifications NotificationsConfig `json:"notifications"`
	Services      []ServiceConfig     `json:"services"`
	Databases     []DatabaseConfig    `json:"databases"`
	GithubRunner  GithubRunnerConfig  `json:"githubRunner"`
	GitHub        GitHubConfig        `json:"github,omitempty"` // Phase 4: workflow runs dashboard
	PM2           PM2Config           `json:"pm2"`
	Terminal      TerminalConfig      `json:"terminal"`
	Auth          AuthConfig          `json:"auth"`
	Permissions   PermissionsConfig   `json:"permissions"`
	QuickCommands []QuickCommand      `json:"quickCommands"`
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

func (d *DatabaseConfig) UnmarshalJSON(data []byte) error {
	type rawDatabaseConfig struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Type     string          `json:"type"`
		Host     string          `json:"host"`
		Port     int             `json:"port"`
		User     string          `json:"user"`
		Username string          `json:"username"`
		Password string          `json:"password"`
		Database json.RawMessage `json:"database"`
	}

	var raw rawDatabaseConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	d.ID = raw.ID
	d.Name = raw.Name
	d.Type = normalizeDatabaseType(raw.Type)
	d.Host = raw.Host
	d.Port = raw.Port
	d.User = raw.User
	if d.User == "" {
		d.User = raw.Username
	}
	d.Password = raw.Password
	d.Database = decodeDatabaseName(raw.Database)

	return nil
}

func decodeDatabaseName(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}

	var asNumber int
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return strconv.Itoa(asNumber)
	}

	return ""
}

func normalizeDatabaseType(dbType string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "postgresql":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(dbType))
	}
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
	Enabled         bool   `json:"enabled"`
	JWTSecret       string `json:"jwtSecret"`
	TokenExpiration int    `json:"tokenExpiration"` // in hours
	UsersFile       string `json:"usersFile"`
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

// GenerateDefaultConfig creates a default configuration
func GenerateDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 3000,
			Host: "0.0.0.0",
		},
		Monitoring: MonitoringConfig{
			SystemInterval:   2000,
			RunnerInterval:   5000,
			PM2Interval:      5000,
			ServicesInterval: 30000,
		},
		Storage: StorageConfig{
			Enabled:       true,
			RetentionDays: 7,
			DataPath:      "./data",
		},
		Auth: AuthConfig{
			Enabled:         true,
			JWTSecret:       "CHANGE-THIS-TO-A-SECURE-RANDOM-SECRET-KEY",
			TokenExpiration: 24,
			UsersFile:       "./data/users.json",
		},
		Alerts: AlertsConfig{
			Enabled: true,
			Rules:   []AlertRule{},
		},
		Notifications: NotificationsConfig{
			Desktop: DesktopNotification{Enabled: false},
			Email:   EmailNotification{Enabled: false},
			Discord: WebhookNotification{Enabled: false},
			Slack:   WebhookNotification{Enabled: false},
		},
		Services:      []ServiceConfig{},
		Databases:     []DatabaseConfig{},
		GithubRunner:  GithubRunnerConfig{},
		PM2:           PM2Config{PM2User: "root"},
		Terminal:      TerminalConfig{Enabled: false, Shell: "/bin/bash", SessionTimeout: 3600000},
		Permissions:   PermissionsConfig{UseSudo: false, RunAsUser: "root"},
		QuickCommands: []QuickCommand{},
	}
}

// SaveConfig saves configuration to a file
func SaveConfig(cfg *Config, path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Sanitize returns a copy of the config with sensitive information masked
func (c *Config) Sanitize() *Config {
	// Create a deep copy (or just copy struct if simple, but we have slices)
	// For simplicity, we marshal/unmarshal or just manually copy relevant fields
	// Since we just want to mask specific fields for API response:

	clone := *c // Shallow copy

	// Mask Auth Secret
	clone.Auth.JWTSecret = "***HIDDEN***"

	// Mask SMTP Password
	clone.Notifications.Email.SMTP.Auth.Pass = "***HIDDEN***"

	// Mask Database Passwords if any
	// We'd need to deep copy the slice to avoid modifying original
	if len(c.Databases) > 0 {
		dbs := make([]DatabaseConfig, len(c.Databases))
		copy(dbs, c.Databases)
		for i := range dbs {
			dbs[i].Password = "***HIDDEN***"
		}
		clone.Databases = dbs
	}

	return &clone
}
