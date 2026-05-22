package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// HubConfig is the v2 hub-mode configuration shape. It is a strict subset of
// the existing Config plus a few hub-only sections (agents registry, AI,
// telegram). The v1 Config struct remains the canonical loader for Phase 0
// so existing config.json files keep working unchanged.
type HubConfig struct {
	Server        ServerConfig        `json:"server"`
	Storage       StorageConfig       `json:"storage"`
	Auth          AuthConfig          `json:"auth"`
	Alerts        AlertsConfig        `json:"alerts"`
	Notifications NotificationsConfig `json:"notifications"`

	// Hub-only sections, all optional and ignored by v1 callers.
	AI       AIConfig       `json:"ai,omitempty"`
	Telegram TelegramConfig `json:"telegram,omitempty"`
	GitHub   GitHubConfig   `json:"github,omitempty"`

	// Legacy in-process monitoring settings are now owned by the
	// implicit "local" agent (see AgentConfig). They are mirrored here
	// only so v1 config.json files can be migrated cleanly.
	Monitoring MonitoringConfig `json:"monitoring,omitempty"`
}

// AgentConfig is the on-disk shape for an agent install. The hub generates
// one of these (with a token baked in) for each `+ Add server` action.
type AgentConfig struct {
	HubURL       string             `json:"hubUrl"`
	Token        string             `json:"token"`
	ServerID     string             `json:"serverId,omitempty"`
	Name         string             `json:"name,omitempty"`
	Tags         map[string]string  `json:"tags,omitempty"`
	Monitoring   MonitoringConfig   `json:"monitoring,omitempty"`
	Services     []ServiceConfig    `json:"services,omitempty"`
	Databases    []DatabaseConfig   `json:"databases,omitempty"`
	PM2          PM2Config          `json:"pm2,omitempty"`
	Terminal     TerminalConfig     `json:"terminal,omitempty"`
	Permissions  PermissionsConfig  `json:"permissions,omitempty"`
	GithubRunner GithubRunnerConfig `json:"githubRunner,omitempty"` // Phase 4: self-hosted runner detection
}

// AIConfig holds provider keys and policy. Keys are stored encrypted at rest;
// the on-disk shape carries ciphertext, not plaintext.
type AIConfig struct {
	Enabled         bool         `json:"enabled"`
	DefaultProvider string       `json:"defaultProvider,omitempty"` // "claude" | "gemini"
	Providers       []AIProvider `json:"providers,omitempty"`
	MonthlyBudget   int          `json:"monthlyBudgetTokens,omitempty"`
	RetentionDays   int          `json:"retentionDays,omitempty"` // conversation prune window
}

type AIProvider struct {
	Name           string `json:"name"` // "claude" | "gemini"
	APIKeyCipher   string `json:"apiKeyCipher,omitempty"`
	ChatModel      string `json:"chatModel,omitempty"`
	SummaryModel   string `json:"summaryModel,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"` // for self-hosted / proxied endpoints
}

// TelegramConfig is the Phase 5 notification channel.
type TelegramConfig struct {
	Enabled       bool     `json:"enabled"`
	BotTokenCipher string  `json:"botTokenCipher,omitempty"`
	ChatIDs       []string `json:"chatIds,omitempty"`
}

// GitHubConfig drives the Phase 4 Actions integration. Supports either PAT
// or GitHub App auth via the discriminated `auth.type` field.
type GitHubConfig struct {
	Enabled      bool         `json:"enabled"`
	Auth         GitHubAuth   `json:"auth"`
	Repos        []string     `json:"repos,omitempty"`
	PollInterval int          `json:"pollInterval,omitempty"`
}

type GitHubAuth struct {
	Type             string `json:"type"`                       // "pat" | "app"
	Token            string `json:"token,omitempty"`            // PAT (plaintext until Phase 6 vault lands)
	TokenCipher      string `json:"tokenCipher,omitempty"`      // PAT (Phase 6 ciphertext)
	AppID            int64  `json:"appId,omitempty"`            // App
	InstallationID   int64  `json:"installationId,omitempty"`   // App
	PrivateKey       string `json:"privateKey,omitempty"`       // App (Phase 6 will move to PrivateKeyCipher)
	PrivateKeyCipher string `json:"privateKeyCipher,omitempty"` // App
}

// LoadHubConfig reads a v2 hub config file.
func LoadHubConfig(path string) (*HubConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg HubConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadAgentConfig reads a v2 agent config file.
func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// MigrateLegacyConfig converts a v1 Config into a paired HubConfig + local
// AgentConfig. The legacy file at oldPath is read but not modified; callers
// are responsible for writing the outputs and backing up the original.
//
// Wired into main.go in Phase 1 once the multi-server UI is ready to consume
// the new shape. Defined now so Phase 0 ships the types end-to-end.
func MigrateLegacyConfig(oldPath string) (*HubConfig, *AgentConfig, error) {
	cfg, err := LoadConfig(oldPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read legacy config: %w", err)
	}

	hub := &HubConfig{
		Server:        cfg.Server,
		Storage:       cfg.Storage,
		Auth:          cfg.Auth,
		Alerts:        cfg.Alerts,
		Notifications: cfg.Notifications,
		Monitoring:    cfg.Monitoring,
	}

	local := &AgentConfig{
		ServerID:    "local",
		Name:        "Local",
		Monitoring:  cfg.Monitoring,
		Services:    cfg.Services,
		Databases:   cfg.Databases,
		PM2:         cfg.PM2,
		Terminal:    cfg.Terminal,
		Permissions: cfg.Permissions,
	}

	return hub, local, nil
}

// WriteMigratedConfig writes the migrated config pair to the conventional
// locations and renames the legacy file to config.json.v1.bak.
func WriteMigratedConfig(legacyPath string, hub *HubConfig, local *AgentConfig) error {
	dir := filepath.Dir(legacyPath)

	hubPath := filepath.Join(dir, "hub.config.json")
	agentDir := filepath.Join(dir, "agents")
	agentPath := filepath.Join(agentDir, "local.agent.json")

	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return err
	}

	if err := writeJSON(hubPath, hub); err != nil {
		return fmt.Errorf("write hub config: %w", err)
	}
	if err := writeJSON(agentPath, local); err != nil {
		return fmt.Errorf("write local agent config: %w", err)
	}
	if err := os.Rename(legacyPath, legacyPath+".v1.bak"); err != nil {
		return fmt.Errorf("backup legacy config: %w", err)
	}
	return nil
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
