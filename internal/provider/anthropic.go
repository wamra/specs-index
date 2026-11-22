package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// anthropicVersion is the anthropic-version header value.
const anthropicVersion = "2023-06-01"

// AnthropicSummarizer talks to the Anthropic Messages API (POST /v1/messages).
// The API key is read from the environment variable APIKeyEnv on every call.
type AnthropicSummarizer struct {
	BaseURL, Model, APIKeyEnv string
	Temperature               float64
	HTTP                      *http.Client
}

func newAnthropicSummarizer(c SummarizeConfig) *AnthropicSummarizer {
	return &AnthropicSummarizer{BaseURL: openAIBase(c.BaseURL), Model: c.Model, APIKeyEnv: c.APIKeyEnv, Temperature: *c.Temperature, HTTP: newHTTP()}
}

func (s *AnthropicSummarizer) Fingerprint() string { return Anthropic + ":" + s.Model }

func (s *AnthropicSummarizer) headers() http.Header {
	h := http.Header{"Anthropic-Version": {anthropicVersion}}
	if k := os.Getenv(s.APIKeyEnv); k != "" && s.APIKeyEnv != "" {
		h["X-Api-Key"] = []string{k}
	}
	return h
}

// Summarize returns the text of the first text block of the reply. 401/403
// are ErrAuth; 429 is retried with backoff (honoring retry-after) and then
// ErrDown; 5xx (including 529 overloaded_error) and 404/408 are ErrDown; other
// 4xx (400: prompt too long, invalid request) are *ItemError; a request
// timeout is the ItemError "timeout".
func (s *AnthropicSummarizer) Summarize(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	body, _ := json.Marshal(map[string]any{
		"model":       s.Model,
		"max_tokens":  maxTokens,
		"temperature": s.Temperature,
		"system":      system,
		"messages":    []map[string]string{{"role": "user", "content": user}},
	})
	resp, err := requestRetry(ctx, s.HTTP, http.MethodPost, s.BaseURL+"/v1/messages", s.headers(), body)
	if err != nil {
		if isTimeout(err) {
			return "", &ItemError{"timeout"}
		}
		return "", err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &ItemError{"bad response"}
	}
	for _, b := range out.Content {
		if b.Type == "" || b.Type == "text" {
			return strings.TrimSpace(b.Text), nil
		}
	}
	return "", &ItemError{"bad response"}
}

// Health calls GET /v1/models (free): it proves reachability and that the key
// is accepted. The model is not looked up (the listing is paginated).
func (s *AnthropicSummarizer) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := requestWith(ctx, s.HTTP, http.MethodGet, s.BaseURL+"/v1/models?limit=1", s.headers(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return statusError(resp)
}
