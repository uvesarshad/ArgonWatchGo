package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Default models for the Gemini provider.
const (
	defaultGeminiChat    = "gemini-2.0-flash"
	defaultGeminiSummary = "gemini-2.0-flash-lite"
	geminiBaseURL        = "https://generativelanguage.googleapis.com/v1beta"
)

// Gemini implements Provider against Google's generateContent endpoint.
type Gemini struct {
	apiKey  string
	http    *http.Client
	baseURL string
}

func NewGemini(apiKey string) *Gemini {
	return &Gemini{
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 90 * time.Second},
		baseURL: geminiBaseURL,
	}
}

func (g *Gemini) Name() string { return "gemini" }

// geminiRequest mirrors generateContent. We keep enough surface area
// for tool/function calling and a single system_instruction.
type geminiRequest struct {
	Contents          []geminiContent     `json:"contents"`
	Tools             []geminiTool        `json:"tools,omitempty"`
	SystemInstruction *geminiContent      `json:"system_instruction,omitempty"`
	GenerationConfig  *geminiGenConfig    `json:"generationConfig,omitempty"`
}

type geminiGenConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                 `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall    `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResp    `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type geminiFunctionResp struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

func (g *Gemini) Chat(ctx context.Context, messages []Message, tools []ToolDef, opts ChatOptions) (Response, error) {
	model := opts.Model
	if model == "" {
		model = defaultGeminiChat
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	req := geminiRequest{
		Contents:         toGeminiContents(messages),
		Tools:            toGeminiTools(tools),
		GenerationConfig: &geminiGenConfig{Temperature: opts.Temperature, MaxOutputTokens: maxTokens},
	}
	if opts.System != "" {
		req.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: opts.System}},
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}

	// API key goes on the query string; same as Google's official SDK.
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", g.baseURL, model, g.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.http.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Response{}, ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return Response{}, fmt.Errorf("gemini: %d %s", resp.StatusCode, string(rawBody))
	}

	var parsed geminiResponse
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	if parsed.Error != nil {
		return Response{}, errors.New("gemini: " + parsed.Error.Message)
	}

	out := Response{
		Model: model,
		Usage: Usage{
			InputTokens:  parsed.UsageMetadata.PromptTokenCount,
			OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
		},
	}
	if len(parsed.Candidates) == 0 {
		return out, nil
	}
	textBuf := strings.Builder{}
	for _, p := range parsed.Candidates[0].Content.Parts {
		if p.Text != "" {
			textBuf.WriteString(p.Text)
		}
		if p.FunctionCall != nil {
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				// Gemini doesn't return a per-call ID; synthesise one
				// from the function name so the conversation log can
				// pair calls with results deterministically.
				ID:   "gemini_" + p.FunctionCall.Name,
				Name: p.FunctionCall.Name,
				Args: p.FunctionCall.Args,
			})
		}
	}
	out.Content = textBuf.String()
	return out, nil
}

func toGeminiContents(in []Message) []geminiContent {
	out := make([]geminiContent, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case RoleSystem:
			continue // hoisted to systemInstruction at call time
		case RoleUser:
			out = append(out, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Content}},
			})
		case RoleAssistant:
			parts := []geminiPart{}
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
					Name: tc.Name,
					Args: tc.Args,
				}})
			}
			out = append(out, geminiContent{Role: "model", Parts: parts})
		case RoleTool:
			// Gemini tool results are user-role function responses;
			// the response payload must be an object, not a string.
			var resp map[string]interface{}
			if err := json.Unmarshal([]byte(m.Content), &resp); err != nil {
				resp = map[string]interface{}{"result": m.Content}
			}
			out = append(out, geminiContent{
				Role: "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFunctionResp{
					Name:     m.Name,
					Response: resp,
				}}},
			})
		}
	}
	return out
}

func toGeminiTools(in []ToolDef) []geminiTool {
	if len(in) == 0 {
		return nil
	}
	decls := make([]geminiFuncDecl, 0, len(in))
	for _, t := range in {
		decls = append(decls, geminiFuncDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Schema,
		})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}
