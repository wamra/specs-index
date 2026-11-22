// Package embed builds embedders from the configuration (enforcing the cloud
// gate), embeds the chunks that need it, and derives vector-based
// suggestions: inferred links.
package embed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/provider"
	"github.com/wamra/specs-index/internal/vector"
)

// Errors callers match with errors.Is.
var (
	// ErrCloudNotAllowed: the provider is not local and cloud use was not
	// allowed (allow_cloud in the config, or --allow-cloud). It is returned
	// before any network I/O.
	ErrCloudNotAllowed = errors.New("cloud embedding provider not allowed")
	// ErrFingerprintMismatch: the configured embedder differs from the one the
	// index was embedded with; vectors from two models are never mixed.
	ErrFingerprintMismatch = errors.New("embedder fingerprint mismatch")
	// ErrNotEmbedded: the index holds no embeddings yet.
	ErrNotEmbedded = errors.New("the index has no embeddings")
)

// MaxChunkRunes is the longest text sent to a provider: the provider drivers
// cut each chunk to provider.MaxChunk runes (2000) before the document prefix
// is added. One chunk per entity; a longer chunk is truncated, not split.
const MaxChunkRunes = provider.MaxChunk

// batchSize is the number of chunks embedded per request.
const batchSize = 32

// Meta keys under which the index pins its embedder.
const (
	metaFingerprint = "embed_fingerprint"
	metaDims        = "embed_dims"
)

// Embedder turns text into vectors.
type Embedder interface {
	// Embed embeds documents (chunk texts), with the model's document prefix.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedQuery embeds one search query, with the model's query prefix.
	EmbedQuery(ctx context.Context, q string) ([]float32, error)
	// Fingerprint is provider|model|dimension|document prefix|version. The
	// dimension is part of it, so it is only complete once known (see Detect).
	Fingerprint() string
	// IsCloud reports whether the text leaves this machine.
	IsCloud() bool
}

// clientHook lets tests redirect the provider's HTTP client; nil in production.
var clientHook func(*http.Client)

type embedder struct {
	p     provider.Embedder
	cloud bool
}

func (e *embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vs, err := e.p.Embed(ctx, texts, provider.DocPrefixOf(e.p))
	if err != nil {
		return nil, err
	}
	if len(vs) != len(texts) {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs", provider.ErrMalformed, len(vs), len(texts))
	}
	return vs, nil
}

func (e *embedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	return provider.Query(ctx, e.p, q)
}

func (e *embedder) Fingerprint() string { return e.p.Fingerprint() }
func (e *embedder) IsCloud() bool       { return e.cloud }

// Detect learns the vector size of a model whose dimension is neither
// configured nor known, with one probe request.
func (e *embedder) Detect(ctx context.Context) error {
	if e.p.Dims() != 0 {
		return nil
	}
	_, err := e.p.Embed(ctx, []string{"dimension probe"}, "")
	return err
}

// fingerprintOf returns e's complete fingerprint, probing the dimension first
// when it is unknown.
func fingerprintOf(ctx context.Context, e Embedder) (string, error) {
	if d, ok := e.(interface{ Detect(context.Context) error }); ok {
		if err := d.Detect(ctx); err != nil {
			return "", err
		}
	}
	return e.Fingerprint(), nil
}

// New builds the embedder for cfg. Provider "none" (or empty) returns a nil
// Embedder and a nil error. The cloud decision is made from the configuration
// alone, before any network I/O: Bedrock, Anthropic and any provider whose
// base URL is not on a loopback host (localhost, 127.0.0.0/8, ::1) is cloud,
// and returns ErrCloudNotAllowed unless allowCloud or cfg.AllowCloud is set.
func New(cfg config.Config, allowCloud bool) (Embedder, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider.Embed))
	if name == "" || name == config.None {
		return nil, nil
	}
	ec := provider.EmbedConfig{
		Provider: name, BaseURL: cfg.BaseURL, Model: cfg.Model, Dims: cfg.Dims,
		APIKeyEnv: cfg.APIKeyEnv, Region: cfg.Region, Profile: cfg.Profile,
	}
	res := ec.Resolved()
	cloud := isCloud(res)
	if cloud && !allowCloud && !cfg.AllowCloud {
		where := "AWS"
		if res.BaseURL != "" {
			// never echo credentials embedded in the URL
			if u, err := url.Parse(res.BaseURL); err == nil {
				where = u.Redacted()
			} else {
				where = "(unparseable URL)"
			}
		}
		return nil, fmt.Errorf("%w: provider %q at %s would send document text off this machine; set \"allow_cloud\": true in the config (or SPECS_INDEX_ALLOW_CLOUD=true), or pass --allow-cloud to index or doctor",
			ErrCloudNotAllowed, res.Provider, where)
	}
	pe, err := provider.NewEmbedder(ec)
	if err != nil {
		return nil, err
	}
	var hc *http.Client
	switch p := pe.(type) {
	case *provider.OllamaEmbedder:
		hc = p.HTTP
	case *provider.OpenAIEmbedder:
		hc = p.HTTP
	}
	if hc != nil {
		if !allowCloud && !cfg.AllowCloud {
			// The gate also covers redirects: a local endpoint must not be able
			// to bounce the document text to another host.
			hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				if !loopback(req.URL.String()) {
					return fmt.Errorf("%w: redirect to %s refused", ErrCloudNotAllowed, req.URL.Redacted())
				}
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				return nil
			}
		}
		if clientHook != nil {
			clientHook(hc)
		}
	}
	return &embedder{p: pe, cloud: cloud}, nil
}

// isCloud reports whether the resolved config sends text off this machine.
func isCloud(c provider.EmbedConfig) bool {
	switch c.Provider {
	case provider.Ollama, provider.OpenAI:
		return !loopback(c.BaseURL)
	}
	return true // bedrock, anthropic, anything else
}

// loopback reports whether base names this machine: localhost, an address in
// 127.0.0.0/8, or ::1.
func loopback(base string) bool {
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func mismatch(stored, have string) error {
	return fmt.Errorf("%w: the index was embedded with %q but the configured embedder is %q; run `index --reembed` to re-embed everything, restore the old embedder settings, or search with `--mode fts` meanwhile",
		ErrFingerprintMismatch, stored, have)
}

// CheckFingerprint refuses to use e on this index when it differs from the
// embedder the vectors came from: ErrFingerprintMismatch (the message names
// `index --reembed`), or ErrNotEmbedded when there are no vectors yet.
func CheckFingerprint(ctx context.Context, st interface{ Meta(string) (string, error) }, e Embedder) error {
	stored, err := st.Meta(metaFingerprint)
	if err != nil {
		return err
	}
	if stored == "" {
		return ErrNotEmbedded
	}
	have, err := fingerprintOf(ctx, e)
	if err != nil {
		return err
	}
	if have != stored {
		return mismatch(stored, have)
	}
	return nil
}

// Stored returns the fingerprint and vector size the index is pinned to
// ("" and 0 before the first embedding).
func Stored(ctx context.Context, st interface{ Meta(string) (string, error) }) (fingerprint string, dims int, err error) {
	if fingerprint, err = st.Meta(metaFingerprint); err != nil {
		return "", 0, err
	}
	d, err := st.Meta(metaDims)
	if err != nil {
		return "", 0, err
	}
	if d != "" {
		dims, _ = strconv.Atoi(d)
	}
	return fingerprint, dims, nil
}

// Store is the part of *store.Store that embed uses.
type Store interface {
	DB() *sql.DB
	Meta(key string) (string, error)
	SetMeta(key, value string) error
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

type pendingChunk struct {
	id          int64
	text        string
	sha         sql.NullString
	typ, status string
}

// Pending embeds every chunk whose embed_fingerprint differs from e's (new and
// changed chunks have an empty one) or that has no stored vector, stores the
// vectors, refreshes the type/status metadata of vectors whose entity changed
// without its text changing, and drops vectors without a chunk. It returns the
// number of chunks embedded. If the index was embedded by a different embedder
// it refuses with ErrFingerprintMismatch before sending anything.
func Pending(ctx context.Context, st Store, e Embedder, vs vector.Store) (int, error) {
	if e == nil {
		return 0, errors.New("no embedder is configured")
	}
	stored, err := st.Meta(metaFingerprint)
	if err != nil {
		return 0, err
	}
	var fp string
	if stored != "" {
		if fp, err = fingerprintOf(ctx, e); err != nil {
			return 0, err
		}
		if fp != stored {
			return 0, mismatch(stored, fp)
		}
	}
	db := st.DB()
	metas, err := vs.Metas(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id, c.text, c.text_sha256, c.embed_fingerprint, e.type, COALESCE(e.status, '')
		FROM chunks c JOIN entities e ON e.id = c.entity_id ORDER BY c.id`)
	if err != nil {
		return 0, err
	}
	var todo []pendingChunk
	seen := map[int64]bool{}
	type metaFix struct {
		id          int64
		typ, status string
	}
	var fixes []metaFix
	for rows.Next() {
		var c pendingChunk
		var cfp sql.NullString
		if err := rows.Scan(&c.id, &c.text, &c.sha, &cfp, &c.typ, &c.status); err != nil {
			rows.Close()
			return 0, err
		}
		seen[c.id] = true
		m, hasVec := metas[c.id]
		if cfp.String == "" || (fp != "" && cfp.String != fp) || !hasVec {
			todo = append(todo, c)
		} else if m[0] != norm(c.typ) || m[1] != norm(c.status) {
			fixes = append(fixes, metaFix{c.id, c.typ, c.status})
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for id := range metas {
		if !seen[id] {
			if err := vs.Delete(ctx, id); err != nil {
				return 0, err
			}
		}
	}
	for _, f := range fixes {
		if err := vs.SetMeta(ctx, f.id, f.typ, f.status); err != nil {
			return 0, err
		}
	}
	if len(todo) == 0 {
		return 0, pin(st, fp, 0)
	}
	if fp == "" {
		if fp, err = fingerprintOf(ctx, e); err != nil {
			return 0, err
		}
	}
	n := 0
	for i := 0; i < len(todo); i += batchSize {
		batch := todo[i:min(i+batchSize, len(todo))]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.text
		}
		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			return n, fmt.Errorf("embed: %w", err)
		}
		if err := pin(st, fp, len(vecs[0])); err != nil {
			return n, err
		}
		for j, c := range batch {
			if err := vs.Upsert(ctx, c.id, vecs[j], c.typ, c.status); err != nil {
				return n, err
			}
			// Only mark the chunk done if its text is still the one embedded.
			if _, err := db.ExecContext(ctx, `UPDATE chunks SET embed_fingerprint = ? WHERE id = ? AND text_sha256 IS ?`,
				fp, c.id, c.sha); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// pin records the embedder fingerprint (and vector size, when known) in meta.
func pin(st Store, fp string, dims int) error {
	if fp == "" {
		return nil
	}
	if err := st.SetMeta(metaFingerprint, fp); err != nil {
		return err
	}
	if dims > 0 {
		return st.SetMeta(metaDims, strconv.Itoa(dims))
	}
	return nil
}

// Reembed discards every vector and fingerprint so that the next Pending
// embeds all chunks with the configured embedder (index --reembed).
func Reembed(ctx context.Context, st Store, vs vector.Store) error {
	if _, err := st.DB().ExecContext(ctx, `UPDATE chunks SET embed_fingerprint = ''`); err != nil {
		return err
	}
	if err := vs.Reset(ctx); err != nil {
		return err
	}
	if err := st.SetMeta(metaFingerprint, ""); err != nil {
		return err
	}
	return st.SetMeta(metaDims, "")
}
