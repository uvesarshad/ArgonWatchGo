package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"argon-watch-go/internal/auth"
)

// Service is the chat orchestrator. It picks a provider from the
// store, drives the tool-use loop (Phase 6 runs at most 4 tool calls
// per turn — enough for most diagnostics without runaway), and
// persists the conversation through Store.
type Service struct {
	store *Store
	tools *ToolExecutor
}

func NewService(store *Store, tools *ToolExecutor) *Service {
	return &Service{store: store, tools: tools}
}

// ChatRequest is the shape POST /api/v2/ai/chat accepts.
type ChatRequest struct {
	ConversationID string `json:"conversationId,omitempty"`
	Provider       string `json:"provider,omitempty"` // "claude" | "gemini"; empty → first configured
	Model          string `json:"model,omitempty"`
	UserMessage    string `json:"message"`
}

// ChatResult is what /api/v2/ai/chat returns. The full updated message
// history rides back so the browser doesn't need to track state — same
// pattern Anthropic + OpenAI clients use.
type ChatResult struct {
	ConversationID string    `json:"conversationId"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	Usage          Usage     `json:"usage"`
	ToolUses       int       `json:"toolUses"`
}

const systemPrompt = `You are the embedded operations assistant for ArgonWatchGo, a self-hosted server monitor. You are helping the operator who runs this hub.

Style: Concise, evidence-based, never speculative. When the user asks "why is X happening", call the relevant tools to inspect real telemetry instead of guessing.

Tool use: You have read-only access to live telemetry via the provided tools. Always prefer calling a tool over fabricating data. If a tool errors, surface the error to the user verbatim — do not retry blindly.

Suggestions: When you propose a remediation, use the suggest_command tool. NEVER claim you have run a command — you cannot. The UI surfaces a "Run in terminal" button the operator must click.

Tone: Direct, no filler. Bullet lists for multi-fact answers. Code blocks for commands.`

// Chat runs one user → assistant exchange, recursively executing tool
// calls until the model returns a final text response (or hits the
// per-turn budget).
func (s *Service) Chat(ctx context.Context, claims *auth.Claims, req ChatRequest) (ChatResult, error) {
	if strings.TrimSpace(req.UserMessage) == "" {
		return ChatResult{}, errors.New("ai: empty message")
	}

	provider, providerName, modelChat, err := s.pickProvider(req.Provider)
	if err != nil {
		return ChatResult{}, err
	}
	if req.Model != "" {
		modelChat = req.Model
	}

	// Load (or start) the conversation. New conversations seed an empty
	// message list; existing ones replay the entire history every turn —
	// fine at Phase 6 sizes (≤ 30d retention), revisit when we add summary
	// compression later.
	user := claims.Username
	var conv *Conversation
	if req.ConversationID != "" {
		conv, err = s.store.GetConversation(req.ConversationID, user)
		if err != nil {
			// Treat "not found" as "start a new conversation under the
			// requested ID" rather than surfacing the SQL error.
			conv = &Conversation{ID: req.ConversationID, User: user}
		}
	}
	if conv == nil {
		conv = &Conversation{User: user}
	}
	if conv.Title == "" {
		conv.Title = truncate(req.UserMessage, 60)
	}
	conv.Model = modelChat

	// Append the new user turn.
	conv.Messages = append(conv.Messages, Message{Role: RoleUser, Content: req.UserMessage})

	tools := s.tools.Defs()
	totalUsage := Usage{}
	const maxToolRounds = 4 // safety against tool-loop runaways
	rounds := 0

	for {
		resp, err := provider.Chat(ctx, conv.Messages, tools, ChatOptions{
			Model:  modelChat,
			System: systemPrompt,
		})
		if err != nil {
			return ChatResult{}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		conv.Messages = append(conv.Messages, Message{
			Role:      RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			break
		}
		rounds++
		if rounds > maxToolRounds {
			conv.Messages = append(conv.Messages, Message{
				Role:    RoleAssistant,
				Content: "_(tool-use loop exceeded budget; stopping early)_",
			})
			break
		}

		// Execute every tool call and append the results as RoleTool
		// messages. Provider impls handle the role->wire mapping.
		for _, tc := range resp.ToolCalls {
			result, _ := s.tools.Execute(ctx, tc, claims)
			conv.Messages = append(conv.Messages, Message{
				Role:      RoleTool,
				ToolUseID: tc.ID,
				Name:      tc.Name,
				Content:   result,
			})
		}
	}

	if err := s.store.SaveConversation(conv); err != nil {
		// Persistence failure shouldn't break the response — log via
		// caller. Returned conversation still has an ID the client
		// can resume against.
		return ChatResult{
			ConversationID: conv.ID,
			Provider:       providerName,
			Model:          modelChat,
			Messages:       conv.Messages,
			Usage:          totalUsage,
			ToolUses:       rounds,
		}, fmt.Errorf("ai: chat ok but persist failed: %w", err)
	}

	return ChatResult{
		ConversationID: conv.ID,
		Provider:       providerName,
		Model:          modelChat,
		Messages:       conv.Messages,
		Usage:          totalUsage,
		ToolUses:       rounds,
	}, nil
}

// pickProvider returns a live provider instance + its name + the chat
// model to use. Falls back to the first configured provider if `pref`
// is empty.
func (s *Service) pickProvider(pref string) (Provider, string, string, error) {
	providers, err := s.store.ListProviders()
	if err != nil {
		return nil, "", "", err
	}
	if len(providers) == 0 {
		return nil, "", "", ErrNoProvider
	}
	pick := pref
	if pick == "" {
		pick = providers[0].Provider
	}
	apiKey, chatModel, _, err := s.store.GetKey(pick)
	if err != nil {
		return nil, "", "", fmt.Errorf("provider %q: %w", pick, err)
	}
	switch pick {
	case "claude":
		if chatModel == "" {
			chatModel = defaultClaudeChat
		}
		return NewClaude(apiKey), "claude", chatModel, nil
	case "gemini":
		if chatModel == "" {
			chatModel = defaultGeminiChat
		}
		return NewGemini(apiKey), "gemini", chatModel, nil
	default:
		return nil, "", "", fmt.Errorf("unknown provider: %s", pick)
	}
}

// TestProvider verifies an API key by making the cheapest possible
// chat call (a 1-token "ping"). Used by the Settings UI to validate
// keys before storing them.
func TestProvider(ctx context.Context, provider, apiKey string) error {
	var p Provider
	switch provider {
	case "claude":
		p = NewClaude(apiKey)
	case "gemini":
		p = NewGemini(apiKey)
	default:
		return fmt.Errorf("unknown provider: %s", provider)
	}
	_, err := p.Chat(ctx, []Message{
		{Role: RoleUser, Content: "ping"},
	}, nil, ChatOptions{MaxTokens: 8})
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
