// Package model is a minimal client for the Anthropic Messages API.
// We only need: send a system prompt + a list of {role, content} turns,
// get back the assistant's text.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	endpoint         = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"
	defaultModel     = "claude-haiku-4-5"
	defaultMaxTok    = 8192
	logPath          = "logs/api.log"
)

var logMu sync.Mutex

func appendLog(reqBody, respBody []byte, status int) {
	logMu.Lock()
	defer logMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "===== %s status=%d =====\n--- request ---\n%s\n--- response ---\n%s\n\n",
		time.Now().Format(time.RFC3339Nano), status, reqBody, respBody)
}

// Client talks to the Anthropic Messages API.
type Client struct {
	APIKey     string
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

// New returns a Client using the given API key and sensible defaults.
func New(apiKey string) *Client {
	return NewWith(apiKey, defaultModel, defaultMaxTok)
}

// NewWith returns a Client with explicit model and max-tokens settings.
// Empty model or non-positive maxTok falls back to the defaults.
func NewWith(apiKey, model string, maxTok int) *Client {
	if model == "" {
		model = defaultModel
	}
	if maxTok <= 0 {
		maxTok = defaultMaxTok
	}
	return &Client{
		APIKey:     apiKey,
		Model:      model,
		MaxTokens:  maxTok,
		HTTPClient: &http.Client{Timeout: 180 * time.Second},
	}
}

// Message is one turn of the conversation handed to the model.
type Message struct {
	Role    string `json:"role"`    // "user" or "assistant"
	Content string `json:"content"`
}

type request struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
}

type apiContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Usage is the token accounting Anthropic returns for each call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Reply bundles the model's text output with the token usage for the call.
type Reply struct {
	Text       string
	Usage      Usage
	StopReason string
}

type apiResponse struct {
	Content    []apiContent `json:"content"`
	StopReason string       `json:"stop_reason"`
	Usage      Usage        `json:"usage"`
	Error      *apiError    `json:"error,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: %s", e.Type, e.Message) }

// Send issues one request and returns the model's text plus token usage.
func (c *Client) Send(ctx context.Context, system string, msgs []Message) (Reply, error) {
	if c.APIKey == "" {
		return Reply{}, errors.New("anthropic: missing API key")
	}
	body, err := json.Marshal(request{
		Model:     c.Model,
		System:    system,
		Messages:  msgs,
		MaxTokens: c.MaxTokens,
	})
	if err != nil {
		return Reply{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Reply{}, err
	}
	appendLog(body, raw, resp.StatusCode)

	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Reply{}, fmt.Errorf("anthropic: decode (status %d): %w; body=%s", resp.StatusCode, err, truncate(string(raw), 400))
	}
	if resp.StatusCode >= 400 {
		if out.Error != nil {
			return Reply{}, fmt.Errorf("anthropic: %s (status %d)", out.Error.Error(), resp.StatusCode)
		}
		return Reply{}, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}

	var sb bytes.Buffer
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return Reply{
		Text:       sb.String(),
		Usage:      out.Usage,
		StopReason: out.StopReason,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
