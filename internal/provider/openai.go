package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// openAIBase returns base without a trailing "/v1" so that "{base}/v1/..."
// works whether the user wrote http://host:1234 or http://host:1234/v1.
func openAIBase(base string) string {
	return strings.TrimSuffix(trimBase(base), "/v1")
}

func apiKey(env string) string {
	if env == "" {
		return ""
	}
	return os.Getenv(env)
}

// bearer returns the Authorization header for key (nil for no key).
func bearer(key string) http.Header {
	if key == "" {
		return nil
	}
	return http.Header{"Authorization": {"Bearer " + key}}
}

// OpenAIEmbedder talks to an OpenAI-compatible POST /v1/embeddings.
type OpenAIEmbedder struct {
	BaseURL, Model, APIKeyEnv string
	HTTP                      *http.Client
	docPrefix, queryPrefix    string
	dims                      dimTracker
}

func newOpenAIEmbedder(c EmbedConfig) *OpenAIEmbedder {
	e := &OpenAIEmbedder{BaseURL: openAIBase(c.BaseURL), Model: c.Model, APIKeyEnv: c.APIKeyEnv, HTTP: newHTTP(), docPrefix: *c.DocPrefix, queryPrefix: *c.QueryPrefix}
	e.dims.n.Store(int64(c.Dims))
	return e
}

func (e *OpenAIEmbedder) DocPrefix() string   { return e.docPrefix }
func (e *OpenAIEmbedder) QueryPrefix() string { return e.queryPrefix }
func (e *OpenAIEmbedder) Dims() int           { return e.dims.get() }
func (e *OpenAIEmbedder) Fingerprint() string {
	return Fingerprint(OpenAI, e.Model, e.Dims(), e.docPrefix)
}

// Query embeds one search query with the query prefix.
func (e *OpenAIEmbedder) Query(ctx context.Context, q string) ([]float32, error) {
	return Query(ctx, e, q)
}

// Embed embeds texts (capped to 2000 runes before the prefix is prepended).
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string, prefix string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": capAll(texts, prefix)})
	resp, err := requestRetry(ctx, e.HTTP, http.MethodPost, e.BaseURL+"/v1/embeddings", bearer(apiKey(e.APIKeyEnv)), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: decode: %v", ErrMalformed, err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs", ErrMalformed, len(out.Data), len(texts))
	}
	sort.SliceStable(out.Data, func(i, j int) bool {
		a, b := out.Data[i].Index, out.Data[j].Index
		return a != nil && b != nil && *a < *b
	})
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	if err := e.dims.check(vecs); err != nil {
		return nil, err
	}
	return vecs, nil
}

func (e *OpenAIEmbedder) Health(ctx context.Context) error {
	return openAIHealth(ctx, e.HTTP, e.BaseURL, e.APIKeyEnv, e.Model, func(ctx context.Context) error {
		_, err := e.Embed(ctx, []string{"health probe"}, "")
		return err
	})
}

// openAIHealth lists /v1/models and checks the model. A key that may not list
// models (403) is "healthy but restricted": probe runs a one-item request
// instead.
func openAIHealth(ctx context.Context, hc *http.Client, base, keyEnv, model string, probe func(context.Context) error) error {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := request(ctx, hc, http.MethodGet, base+"/v1/models", apiKey(keyEnv), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		if AuthCode(err) == http.StatusForbidden {
			pctx, pcancel := context.WithTimeout(parent, 15*time.Second)
			defer pcancel()
			return probe(pctx)
		}
		return err
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrMalformed, err)
	}
	var names []string
	for _, m := range body.Data {
		names = append(names, m.ID)
	}
	if !listed(names, model) {
		return fmt.Errorf("model %s not listed at %s", model, base)
	}
	return nil
}

// OpenAISummarizer talks to an OpenAI-compatible POST /v1/chat/completions.
type OpenAISummarizer struct {
	BaseURL, Model, APIKeyEnv string
	Temperature               float64
	HTTP                      *http.Client
}

func newOpenAISummarizer(c SummarizeConfig) *OpenAISummarizer {
	return &OpenAISummarizer{BaseURL: openAIBase(c.BaseURL), Model: c.Model, APIKeyEnv: c.APIKeyEnv, Temperature: *c.Temperature, HTTP: newHTTP()}
}

func (s *OpenAISummarizer) Fingerprint() string { return OpenAI + ":" + s.Model }

// Summarize returns choices[0].message.content. Connection errors and HTTP
// 404/408/429/5xx wrap ErrDown (429 is retried with backoff first), 401/403 are ErrAuth; a request timeout and other failures
// are *ItemError.
func (s *OpenAISummarizer) Summarize(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	body, _ := json.Marshal(map[string]any{
		"model":       s.Model,
		"messages":    []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"max_tokens":  maxTokens,
		"temperature": s.Temperature,
	})
	resp, err := requestRetry(ctx, s.HTTP, http.MethodPost, s.BaseURL+"/v1/chat/completions", bearer(apiKey(s.APIKeyEnv)), body)
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
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &ItemError{"bad response"}
	}
	if len(out.Choices) == 0 {
		return "", &ItemError{"bad response"}
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func (s *OpenAISummarizer) Health(ctx context.Context) error {
	return openAIHealth(ctx, s.HTTP, s.BaseURL, s.APIKeyEnv, s.Model, func(ctx context.Context) error {
		_, err := s.Summarize(ctx, "Reply with one word.", "ping", 1)
		return err
	})
}
