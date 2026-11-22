package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// dimTracker holds configured or detected dimensions.
type dimTracker struct{ n atomic.Int64 }

func (d *dimTracker) get() int { return int(d.n.Load()) }

// check validates the vectors' dimensions against the known size, or records
// the size of the first vector when none is known.
func (d *dimTracker) check(vecs [][]float32) error {
	for _, v := range vecs {
		if len(v) == 0 {
			return fmt.Errorf("%w: empty embedding", ErrMalformed)
		}
		if !d.n.CompareAndSwap(0, int64(len(v))) && int(d.n.Load()) != len(v) {
			return fmt.Errorf("%w: embedding dimension %d, expected %d", ErrBroken, len(v), d.n.Load())
		}
	}
	return nil
}

func capAll(texts []string, prefix string) []string {
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = prefix + CapRunes(t, MaxChunk)
	}
	return in
}

// OllamaEmbedder talks to Ollama's /api/embed.
type OllamaEmbedder struct {
	BaseURL, Model string
	HTTP           *http.Client
	docPrefix      string
	queryPrefix    string
	dims           dimTracker
}

func newOllamaEmbedder(c EmbedConfig) *OllamaEmbedder {
	e := &OllamaEmbedder{BaseURL: c.BaseURL, Model: c.Model, HTTP: newHTTP(), docPrefix: *c.DocPrefix, queryPrefix: *c.QueryPrefix}
	e.dims.n.Store(int64(c.Dims))
	return e
}

func (e *OllamaEmbedder) DocPrefix() string   { return e.docPrefix }
func (e *OllamaEmbedder) QueryPrefix() string { return e.queryPrefix }
func (e *OllamaEmbedder) Dims() int           { return e.dims.get() }
func (e *OllamaEmbedder) Fingerprint() string {
	return Fingerprint(Ollama, e.Model, e.Dims(), e.docPrefix)
}

// Query embeds one search query with the query prefix.
func (e *OllamaEmbedder) Query(ctx context.Context, q string) ([]float32, error) {
	return Query(ctx, e, q)
}

// Embed embeds texts, each capped to 2000 runes before prefix is prepended.
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string, prefix string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": capAll(texts, prefix), "truncate": true, "keep_alive": "15m"})
	resp, err := request(ctx, e.HTTP, http.MethodPost, e.BaseURL+"/api/embed", "", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return nil, err
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: decode: %v", ErrMalformed, err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs", ErrMalformed, len(out.Embeddings), len(texts))
	}
	if err := e.dims.check(out.Embeddings); err != nil {
		return nil, err
	}
	return out.Embeddings, nil
}

// Health lists Ollama's models (GET /api/tags, 2 s) and checks the model.
func (e *OllamaEmbedder) Health(ctx context.Context) error {
	return ollamaHealth(ctx, e.HTTP, e.BaseURL, e.Model)
}

func ollamaHealth(ctx context.Context, hc *http.Client, base, model string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := request(ctx, hc, http.MethodGet, base+"/api/tags", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return err
	}
	var body struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrMalformed, err)
	}
	var names []string
	for _, m := range body.Models {
		names = append(names, m.Name, m.Model)
	}
	if !listed(names, model) {
		return fmt.Errorf("model %s not listed at %s", model, base)
	}
	return nil
}

// OllamaSummarizer talks to Ollama's /api/chat.
type OllamaSummarizer struct {
	BaseURL, Model string
	Temperature    float64
	HTTP           *http.Client
}

func newOllamaSummarizer(c SummarizeConfig) *OllamaSummarizer {
	return &OllamaSummarizer{BaseURL: c.BaseURL, Model: c.Model, Temperature: *c.Temperature, HTTP: newHTTP()}
}

func (s *OllamaSummarizer) Fingerprint() string { return Ollama + ":" + s.Model }

// Summarize returns the model's reply. Connection errors and HTTP
// 404/408/429/5xx wrap ErrDown; a request timeout and other failures are
// *ItemError.
func (s *OllamaSummarizer) Summarize(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	body, _ := json.Marshal(map[string]any{
		"model":      s.Model,
		"messages":   []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"stream":     false,
		"think":      false,
		"keep_alive": "15m",
		"options":    map[string]any{"temperature": s.Temperature, "num_predict": maxTokens},
	})
	resp, err := request(ctx, s.HTTP, http.MethodPost, s.BaseURL+"/api/chat", "", body)
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
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &ItemError{"bad response"}
	}
	return strings.TrimSpace(out.Message.Content), nil
}

func (s *OllamaSummarizer) Health(ctx context.Context) error {
	return ollamaHealth(ctx, s.HTTP, s.BaseURL, s.Model)
}
