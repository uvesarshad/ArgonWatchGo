package ai

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"argon-watch-go/internal/crypto"

	"github.com/google/uuid"
)

// Store persists AI keys + conversations. Keys are vault-encrypted at
// rest; conversations are plaintext JSON because the prompt + response
// content is what makes /export useful for the user.
type Store struct {
	db    *sql.DB
	vault *crypto.Vault
}

func NewStore(db *sql.DB, vault *crypto.Vault) (*Store, error) {
	if db == nil {
		return nil, errors.New("ai/store: nil db")
	}
	if vault == nil {
		return nil, errors.New("ai/store: nil vault")
	}
	if err := initKeyTable(db); err != nil {
		return nil, err
	}
	return &Store{db: db, vault: vault}, nil
}

// initKeyTable is idempotent so we don't bake a v2 schema bump in just
// for the ai_keys side table. Phase 0 already created ai_conversations.
func initKeyTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS ai_keys (
			provider     TEXT PRIMARY KEY,
			cipher       TEXT NOT NULL,
			model_chat   TEXT,
			model_summary TEXT,
			updated_at   INTEGER NOT NULL
		)
	`)
	return err
}

// ----- API keys -----

// ProviderRecord is the redacted view returned to the browser. The
// plaintext key NEVER leaves the server after Set returns.
type ProviderRecord struct {
	Provider      string `json:"provider"`
	HasKey        bool   `json:"hasKey"`
	ModelChat     string `json:"modelChat,omitempty"`
	ModelSummary  string `json:"modelSummary,omitempty"`
	UpdatedAt     int64  `json:"updatedAt"`
}

func (s *Store) SetKey(provider, apiKey, chatModel, summaryModel string) error {
	if provider == "" || apiKey == "" {
		return errors.New("provider and apiKey are required")
	}
	cipher, err := s.vault.Encrypt(apiKey)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO ai_keys (provider, cipher, model_chat, model_summary, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(provider) DO UPDATE SET
		   cipher=excluded.cipher,
		   model_chat=excluded.model_chat,
		   model_summary=excluded.model_summary,
		   updated_at=excluded.updated_at`,
		provider, cipher, chatModel, summaryModel, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) DeleteKey(provider string) error {
	_, err := s.db.Exec(`DELETE FROM ai_keys WHERE provider = ?`, provider)
	return err
}

func (s *Store) GetKey(provider string) (apiKey, chatModel, summaryModel string, err error) {
	var cipher string
	row := s.db.QueryRow(`SELECT cipher, COALESCE(model_chat,''), COALESCE(model_summary,'') FROM ai_keys WHERE provider = ?`, provider)
	if err = row.Scan(&cipher, &chatModel, &summaryModel); err != nil {
		return "", "", "", err
	}
	apiKey, err = s.vault.Decrypt(cipher)
	return apiKey, chatModel, summaryModel, err
}

func (s *Store) ListProviders() ([]ProviderRecord, error) {
	rows, err := s.db.Query(`SELECT provider, COALESCE(model_chat,''), COALESCE(model_summary,''), updated_at FROM ai_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderRecord{}
	for rows.Next() {
		var r ProviderRecord
		r.HasKey = true
		if err := rows.Scan(&r.Provider, &r.ModelChat, &r.ModelSummary, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ----- Conversations -----

// Conversation is the JSON shape stored in ai_conversations.messages.
// We persist the rendered Message slice rather than the raw provider
// payload so export is portable across model migrations.
type Conversation struct {
	ID        string    `json:"id"`
	User      string    `json:"user"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	StartedAt int64     `json:"startedAt"`
	UpdatedAt int64     `json:"updatedAt"`
}

// SaveConversation upserts. New conversations get an ID minted here.
func (s *Store) SaveConversation(c *Conversation) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UnixMilli()
	if c.StartedAt == 0 {
		c.StartedAt = now
	}
	c.UpdatedAt = now

	body, err := json.Marshal(c.Messages)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO ai_conversations (id, user, started_at, updated_at, model, title, messages)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   updated_at=excluded.updated_at,
		   model=excluded.model,
		   title=excluded.title,
		   messages=excluded.messages`,
		c.ID, c.User, c.StartedAt, c.UpdatedAt, c.Model, c.Title, string(body),
	)
	return err
}

func (s *Store) GetConversation(id, user string) (*Conversation, error) {
	row := s.db.QueryRow(`SELECT id, user, started_at, updated_at, COALESCE(model,''), COALESCE(title,''), messages FROM ai_conversations WHERE id = ? AND user = ?`, id, user)
	var c Conversation
	var msgs string
	if err := row.Scan(&c.ID, &c.User, &c.StartedAt, &c.UpdatedAt, &c.Model, &c.Title, &msgs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(msgs), &c.Messages); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListConversations(user string, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.Query(
		`SELECT id, user, started_at, updated_at, COALESCE(model,''), COALESCE(title,'')
		 FROM ai_conversations
		 WHERE user = ?
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		user, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.User, &c.StartedAt, &c.UpdatedAt, &c.Model, &c.Title); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) DeleteConversation(id, user string) error {
	_, err := s.db.Exec(`DELETE FROM ai_conversations WHERE id = ? AND user = ?`, id, user)
	return err
}

// PruneOlderThan deletes conversations untouched for more than `days`
// days, per the locked decision §9.3 (30-day retention, exportable).
// Wired as a daily cron from main.go (Phase 6.5).
func (s *Store) PruneOlderThan(days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	res, err := s.db.Exec(`DELETE FROM ai_conversations WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
