// Package provider defines the model-provider interfaces (embeddings and
// summarization), the shared error classification and the drivers: ollama,
// openai (any OpenAI-compatible server: LM Studio, vLLM, OpenAI, Voyage),
// anthropic (summaries) and bedrock (embeddings and summaries).
package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Embedder turns texts into vectors. prefix is prepended to every text (the
// caller passes the document or the query prefix, see Prefixer).
type Embedder interface {
	Embed(ctx context.Context, texts []string, prefix string) ([][]float32, error)
	// Dims is the vector size: configured, known for the model, or detected
	// on the first Embed call. It is 0 until one of those happened.
	Dims() int
	// Fingerprint is "<provider>|<model>|<dims>|<doc_prefix>|v1".
	Fingerprint() string
	Health(ctx context.Context) error
}

// Summarizer produces a short text from a system and a user message.
type Summarizer interface {
	Summarize(ctx context.Context, system, user string, maxTokens int) (string, error)
	// Fingerprint is "<provider>:<model>" (recorded in summaries.model).
	Fingerprint() string
	Health(ctx context.Context) error
}

// Prefixer is implemented by embedders that know their document and query
// prefixes.
type Prefixer interface {
	DocPrefix() string
	QueryPrefix() string
}

// DocPrefixOf returns e's document prefix ("" when e is not a Prefixer).
func DocPrefixOf(e Embedder) string {
	if p, ok := e.(Prefixer); ok {
		return p.DocPrefix()
	}
	return ""
}

// QueryPrefixOf returns e's query prefix ("" when e is not a Prefixer).
func QueryPrefixOf(e Embedder) string {
	if p, ok := e.(Prefixer); ok {
		return p.QueryPrefix()
	}
	return ""
}

// Query embeds one search query with e's query prefix.
func Query(ctx context.Context, e Embedder, q string) ([]float32, error) {
	v, err := e.Embed(ctx, []string{q}, QueryPrefixOf(e))
	if err != nil {
		return nil, err
	}
	if len(v) != 1 {
		return nil, fmt.Errorf("%w: got %d embeddings for 1 query", ErrMalformed, len(v))
	}
	return v[0], nil
}

// Error classes shared by all drivers.
var (
	// ErrDown: the provider is unreachable or refusing service (connection
	// refused, 5xx, 404, 408, 429). Nothing is parked; the work stays pending.
	// A request timeout is per item and retryable: embedders report it as
	// ErrDown, summarizers as ItemError "timeout", which the summarize run
	// skips (never parks) and counts toward stopping the run.
	ErrDown = errors.New("model provider unreachable")
	// ErrBroken: the provider answers but systemically wrongly (wrong
	// dimensions, malformed responses).
	ErrBroken = errors.New("embedder is failing systemically")
	// ErrAuth: the server rejected the credentials (HTTP 401/403). Systemic:
	// nothing is parked and the work stays pending. Errors of this class are
	// *AuthError values.
	ErrAuth = errors.New("authentication failed")
	// ErrItem: the failure is specific to the input (400, 413, other 4xx).
	// Errors of this class are *ItemError values and match errors.Is(err, ErrItem).
	ErrItem = errors.New("input-specific failure")
	// ErrMalformed is the malformed-response flavor of ErrBroken (it also
	// matches errors.Is(err, ErrBroken)); a drain bisects it instead of
	// stopping, because one bad batch may be caused by its input.
	ErrMalformed error = &classError{"bad response", ErrBroken}
)

type classError struct {
	msg   string
	class error
}

func (e *classError) Error() string        { return e.msg }
func (e *classError) Is(target error) bool { return target == e.class }

// ItemError is an ErrItem failure with a reason (stored as "failed:<reason>"
// for summaries).
type ItemError struct{ Reason string }

func (e *ItemError) Error() string        { return "item: " + e.Reason }
func (e *ItemError) Is(target error) bool { return target == ErrItem }

// AuthError is an ErrAuth failure with the HTTP status.
type AuthError struct{ Code int }

func (e *AuthError) Error() string {
	if e.Code == 0 {
		return "authentication failed"
	}
	return fmt.Sprintf("authentication failed (HTTP %d)", e.Code)
}
func (e *AuthError) Is(target error) bool { return target == ErrAuth }

// AuthCode returns the HTTP status of an ErrAuth error (0 otherwise).
func AuthCode(err error) int {
	var ae *AuthError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

// IsSystemic reports whether err must stop a drain instead of being bisected.
func IsSystemic(err error) bool {
	return errors.Is(err, ErrDown) || errors.Is(err, ErrAuth) || (errors.Is(err, ErrBroken) && !errors.Is(err, ErrMalformed))
}

// MaxChunk caps embedded text (runes) before the prefix is prepended.
const MaxChunk = 2000

// CapRunes truncates s to n runes.
func CapRunes(s string, n int) string {
	if len(s) <= n { // byte length <= n implies rune count <= n
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func newHTTP() *http.Client { return &http.Client{Timeout: 120 * time.Second} }

func trimBase(base string) string { return strings.TrimRight(strings.TrimSpace(base), "/") }

// request sends method url with an optional JSON body and bearer key. A
// transport failure is ErrDown (wrapping the cause, so url.Error timeouts stay
// visible to errors.As); a cancelled ctx returns ctx.Err().
func request(ctx context.Context, hc *http.Client, method, u, key string, body []byte) (*http.Response, error) {
	var hdr http.Header
	if key != "" {
		hdr = http.Header{"Authorization": {"Bearer " + key}}
	}
	return requestWith(ctx, hc, method, u, hdr, body)
}

// requestWith is request with explicit extra headers.
func requestWith(ctx context.Context, hc *http.Client, method, u string, hdr http.Header, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %w", ErrDown, err)
	}
	return resp, nil
}

// statusError classifies a non-2xx status and drains the body. 2xx returns nil.
func statusError(resp *http.Response) error {
	code := resp.StatusCode
	if code >= 200 && code < 300 {
		return nil
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case code == 401, code == 403:
		return &AuthError{code}
	case code >= 500, code == 404, code == 408, code == 429:
		return fmt.Errorf("%w: HTTP %d", ErrDown, code)
	}
	return &ItemError{fmt.Sprintf("http %d", code)}
}

func isTimeout(err error) bool {
	var ue *url.Error
	return errors.As(err, &ue) && ue.Timeout()
}

// listed reports whether name appears in names, matching "model" and
// "model:tag".
func listed(names []string, model string) bool {
	for _, n := range names {
		if n == model || strings.HasPrefix(n, model+":") {
			return true
		}
	}
	return false
}
