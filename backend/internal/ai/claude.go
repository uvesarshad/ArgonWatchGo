package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Default models for the Claude provider. Verified against the live
// API at session boot — if the configured model is rejected, we log
// a warning and fall back to the default for the next request.
const (
	defaultClaudeChat    = "claude-sonnet-4-6"
	defaultClaudeSummary = "claude-haiku-4-5-20251001"
	claudeAPIVersion     = "2023-06-01"
	claudeBaseURL        = "https://api.anthropic.com/v1"
)

// Claude implements Provider against the Anthropic Messages API.
type Claude struct {
	apiKey  string
	http    *http.Client
	baseURL string
}

func NewClaude(apiKey string) *Claude {
	return &Claude{
		apiKey:  apiKey,
		// 90s caters to slow multi-tool turns; the handler's own 2-min
		// context deadline is the outer bound.
		http: &http.Client{Timeout: 90 * time.Second},
		baseURL: claudeBaseURL,
	}
}

func (c *Claude) Name() string { return "claude" }

// claudeRequest mirrors the Anthropic Messages API request shape. We
// hand-roll the JSON rather than pull in the SDK so the binary stays
// small and we don't drag in transitive deps that conflict with
// google.golang.org/api (Gemini path).
type claudeRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []claudeMessage    `json:"messages"`
	Tools       []claudeTool       `json:"tools,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type       string                 `json:"type"`
	Text       string                 `json:"text,omitempty"`
	// tool_use (assistant requesting a tool call)
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Input      map[string]interface{} `json:"input,omitempty"`
	// tool_result (user delivering the tool output)
	ToolUseID  string                 `json:"tool_use_id,omitempty"`
	ResultText string                 `json:"content,omitempty"`
}

type claudeTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type claudeResponse struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content []claudeContent `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *claudeError `json:"error,omitempty"`
}

type claudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (c *Claude) Chat(ctx context.Context, messages []Message, tools []ToolDef, opts ChatOptions) (Response, error) {
	model := opts.Model
	if model == "" {
		model = defaultClaudeChat
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	req := claudeRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		System:      opts.System,
		Temperature: opts.Temperature,
		Messages:    toClaudeMessages(messages),
		Tools:       toClaudeTools(tools),
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", claudeAPIVersion)
	httpReq.Header.Set("x-api-key", c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return Response{}, ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return Response{}, fmt.Errorf("claude: %d %s", resp.StatusCode, string(rawBody))
	}

	var parsed claudeResponse
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("claude: decode response: %w (body=%s)", err, string(rawBody))
	}
	if parsed.Error != nil {
		return Response{}, errors.New("claude: " + parsed.Error.Message)
	}

	out := Response{
		Model: parsed.Model,
		Usage: Usage{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens},
	}
	for _, c := range parsed.Content {
		switch c.Type {
		case "text":
			out.Content += c.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:   c.ID,
				Name: c.Name,
				Args: c.Input,
			})
		}
	}
	return out, nil
}

func toClaudeMessages(in []Message) []claudeMessage {
	out := make([]claudeMessage, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case RoleSystem:
			// System prompts ride on the top-level System field, not as
			// a message — the API rejects role:"system" in messages.
			continue
		case RoleUser:
			out = append(out, claudeMessage{
				Role:    "user",
				Content: []claudeContent{{Type: "text", Text: m.Content}},
			})
		case RoleAssistant:
			parts := []claudeContent{}
			if m.Content != "" {
				parts = append(parts, claudeContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, claudeContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Args,
				})
			}
			out = append(out, claudeMessage{Role: "assistant", Content: parts})
		case RoleTool:
			// Tool results are user-role messages in Anthropic's schema.
			out = append(out, claudeMessage{
				Role: "user",
				Content: []claudeContent{{
					Type:       "tool_result",
					ToolUseID:  m.ToolUseID,
					ResultText: m.Content,
				}},
			})
		}
	}
	return out
}

func toClaudeTools(in []ToolDef) []claudeTool {
	out := make([]claudeTool, 0, len(in))
	for _, t := range in {
		out = append(out, claudeTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return out
}
