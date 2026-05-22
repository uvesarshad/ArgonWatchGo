// Package ai is the v2 AI assistant: provider-agnostic chat with tool
// calling against the hub's telemetry. Phase 6 ships Claude (Anthropic)
// + Gemini (Google) provider implementations behind one Provider
// interface; the chat REST endpoint + tool router live in this package
// so adding a third provider (Phase 6.5) only touches one file.
package ai

import (
	"context"
	"errors"
)

// Role is the conversation-role enum. Mirrors Anthropic/OpenAI/Gemini
// conventions; we map provider-specific names at the edge.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a transport-neutral chat message. Tool calls and tool
// results both ride here so the provider impls don't fork between
// "text turn" and "tool turn" paths in the conversation log.
type Message struct {
	Role      Role        `json:"role"`
	Content   string      `json:"content,omitempty"`
	ToolCalls []ToolCall  `json:"toolCalls,omitempty"` // assistant requesting tool execution
	ToolUseID string      `json:"toolUseId,omitempty"` // for tool-result turns
	Name      string      `json:"name,omitempty"`      // for tool-result turns (the tool that ran)
}

// ToolCall is one tool invocation requested by the model. Args is JSON-
// marshallable; providers convert from their native shape to this.
type ToolCall struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Args  map[string]interface{} `json:"args"`
}

// ToolDef describes a tool to the model. Schema follows JSON Schema
// draft-07; both Claude (input_schema) and Gemini (parameters with
// OpenAPI subset) accept this shape with minor field renames.
type ToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Schema      map[string]interface{} `json:"schema"`
}

// Response is one full assistant turn. If ToolCalls is non-empty the
// caller is expected to execute them and feed results back as
// RoleTool messages in the next turn.
type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	Model     string     `json:"model"`
	Usage     Usage      `json:"usage,omitempty"`
}

// Usage exposes provider-reported token counts so the budget tracker
// (Phase 6.5) can enforce per-user caps without a separate API call.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Provider is the seam every chat backend implements. Streaming is on
// the interface but Phase 6 only wires the non-streaming Chat path
// (the UI gets a full response per request). Stream lands in Phase 6.5.
type Provider interface {
	Name() string
	Chat(ctx context.Context, messages []Message, tools []ToolDef, opts ChatOptions) (Response, error)
}

// ChatOptions are per-call knobs. Zero values pick sensible defaults
// per provider (Sonnet for Claude, Flash for Gemini).
type ChatOptions struct {
	Model       string
	MaxTokens   int
	Temperature float64
	System      string // injected as a top-level system prompt; not a Message entry
}

// ErrUnauthorized is returned by providers when the configured API key
// is rejected. Surfaced separately from generic errors so the UI can
// prompt the user to refresh credentials.
var ErrUnauthorized = errors.New("ai: provider rejected the API key")

// ErrNoProvider is returned when the user hits /chat but the configured
// default provider has no key yet.
var ErrNoProvider = errors.New("ai: no provider configured (set an API key in Settings)")
