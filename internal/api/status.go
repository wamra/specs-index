package api

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/embed"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/vector"
	"github.com/wamra/specs-index/internal/workspace"
)

// EmbedderInfo describes the configured embedder without contacting it.
// APIKeySet says only whether the variable named by APIKeyEnv has a value; the
// value is never exposed.
type EmbedderInfo struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	BaseURL     string `json:"base_url"`
	Cloud       bool   `json:"cloud"`
	CloudAllow  bool   `json:"cloud_allowed"`
	APIKeyEnv   string `json:"api_key_env"`
	APIKeySet   bool   `json:"api_key_set"`
	Error       string `json:"error"`
	Fingerprint string `json:"index_fingerprint"`
	Dims        int    `json:"index_dims"`
}

// StatusResult is the result of status.
type StatusResult struct {
	DB              string         `json:"db"`
	Sources         int            `json:"sources"`
	Documents       int            `json:"documents"`
	FailedDocs      int            `json:"failed_documents"`
	DocumentsByType map[string]int `json:"documents_by_type"`
	Entities        int            `json:"entities"`
	EntitiesByType  map[string]int `json:"entities_by_type"`
	EdgesExplicit   int            `json:"edges_explicit"`
	EdgesInferred   int            `json:"edges_inferred"`
	Chunks          int            `json:"chunks"`
	EmbeddedChunks  int            `json:"embedded_chunks"`
	IndexedAt       string         `json:"indexed_at"`
	Embedder        EmbedderInfo   `json:"embedder"`
	Environment     []EnvVarInfo   `json:"environment"`
}

func embedderInfo(cfg config.Config, allowCloud bool) EmbedderInfo {
	info := EmbedderInfo{
		Provider: cfg.Provider.Embed, Model: cfg.Model, BaseURL: cfg.BaseURL,
		APIKeyEnv: cfg.APIKeyEnv, CloudAllow: allowCloud || cfg.AllowCloud,
	}
	if cfg.APIKeyEnv != "" {
		info.APIKeySet = os.Getenv(cfg.APIKeyEnv) != ""
	}
	switch e, state, _, err := SetupEmbedder(cfg, allowCloud); {
	case err != nil:
		info.Error = err.Error()
	case state == EmbedCloudBlocked:
		info.Cloud = true
	case e != nil:
		info.Cloud = e.IsCloud()
	}
	return info
}

func countBy(ctx context.Context, db *sql.DB, q string) (map[string]int, int, error) {
	m := map[string]int{}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, 0, err
		}
		m[k] = n
		total += n
	}
	return m, total, rows.Err()
}

// Status reports the index counts and the configured embedder. It makes no
// network call.
func Status(ctx context.Context, st *store.Store, root string, cfg config.Config) (StatusResult, error) {
	db := st.DB()
	r := StatusResult{DB: DisplayPath(root, workspace.DBPath(root))}
	var err error
	if r.DocumentsByType, r.Documents, err = countBy(ctx, db, `SELECT type, count(*) FROM documents GROUP BY type`); err != nil {
		return r, err
	}
	if r.EntitiesByType, r.Entities, err = countBy(ctx, db, `SELECT type, count(*) FROM entities GROUP BY type`); err != nil {
		return r, err
	}
	var failed, embedded sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sources`).Scan(&r.Sources); err != nil {
		return r, err
	}
	if err := db.QueryRowContext(ctx, `SELECT SUM(status = 'failed') FROM documents`).Scan(&failed); err != nil {
		return r, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(explicit = 1), 0), COALESCE(SUM(explicit = 0), 0) FROM edges`).Scan(&r.EdgesExplicit, &r.EdgesInferred); err != nil {
		return r, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*), SUM(COALESCE(embed_fingerprint, '') <> '') FROM chunks`).Scan(&r.Chunks, &embedded); err != nil {
		return r, err
	}
	r.FailedDocs, r.EmbeddedChunks = int(failed.Int64), int(embedded.Int64)
	if r.IndexedAt, err = st.Meta("indexed_at"); err != nil {
		return r, err
	}
	r.Embedder = embedderInfo(cfg, false)
	if r.Embedder.Fingerprint, r.Embedder.Dims, err = embed.Stored(ctx, st); err != nil {
		return r, err
	}
	r.Environment = EnvInfo()
	return r, nil
}

// Check statuses reported by Doctor.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
	CheckSkip = "skip"
)

// DoctorCheck is one doctor check.
type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// DoctorResult is the result of doctor; OK is false when any check failed
// (warnings and skips do not fail it).
type DoctorResult struct {
	OK     bool          `json:"ok"`
	Checks []DoctorCheck `json:"checks"`
}

// Doctor checks the index: it opens, passes integrity_check, FTS5 and vec1
// answer a query, the row counts of chunks and chunk_fts (and of embedded
// chunks and chunk_vec) agree, and the embedder answers. An unreachable
// embedder is a warning; a cloud embedder that is not allowed is not contacted.
// probeTimeout bounds the embedder probe.
func Doctor(ctx context.Context, root string, cfg config.Config, allowCloud bool, probeTimeout time.Duration) DoctorResult {
	r := DoctorResult{OK: true, Checks: []DoctorCheck{}}
	add := func(name, status, format string, a ...any) {
		if status == CheckFail {
			r.OK = false
		}
		r.Checks = append(r.Checks, DoctorCheck{Name: name, Status: status, Detail: fmt.Sprintf(format, a...)})
	}
	dbp := workspace.DBPath(root)
	st, err := OpenExisting(root)
	if err != nil {
		add("index", CheckFail, "%v", err)
		return r
	}
	defer st.Close()
	db := st.DB()
	add("index", CheckOK, "opened %s", DisplayPath(root, dbp))

	var integ string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integ); err != nil {
		add("integrity", CheckFail, "%v", err)
	} else if integ != "ok" {
		add("integrity", CheckFail, "PRAGMA integrity_check: %s", integ)
	} else {
		add("integrity", CheckOK, "PRAGMA integrity_check is ok")
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'specs'`).Scan(&n); err != nil {
		add("fts5", CheckFail, "full-text query failed: %v", err)
	} else {
		add("fts5", CheckOK, "full-text query answered")
	}

	var chunks, ftsRows, embedded, vecRows int
	var emb64 sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT count(*), SUM(COALESCE(embed_fingerprint, '') <> '') FROM chunks`).Scan(&chunks, &emb64); err != nil {
		add("chunks", CheckFail, "%v", err)
	}
	embedded = int(emb64.Int64)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM chunk_fts`).Scan(&ftsRows); err != nil {
		add("chunk_fts", CheckFail, "%v", err)
	} else if ftsRows != chunks {
		add("chunk_fts", CheckFail, "chunks has %d rows but chunk_fts has %d; run `specs-index index` (or delete the index and re-index)", chunks, ftsRows)
	} else {
		add("chunk_fts", CheckOK, "chunks and chunk_fts both have %d rows", chunks)
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM chunk_vec`).Scan(&vecRows); err != nil {
		add("vec1", CheckFail, "vector table query failed: %v", err)
	} else {
		_, dims, _ := embed.Stored(ctx, st)
		if vecRows > 0 && dims > 0 {
			q := make([]float32, dims)
			q[0] = 1
			if _, err := vector.NewVec1(db).KNN(ctx, q, 1, vector.Filter{}); err != nil {
				add("vec1", CheckFail, "nearest-neighbour query failed: %v", err)
			} else {
				add("vec1", CheckOK, "nearest-neighbour query answered (%d dimensions)", dims)
			}
		} else {
			add("vec1", CheckOK, "vector table answered a query (empty)")
		}
		if embedded != vecRows {
			add("chunk_vec", CheckFail, "%d chunks are marked embedded but chunk_vec has %d rows; run `specs-index index --reembed`", embedded, vecRows)
		} else {
			add("chunk_vec", CheckOK, "%d embedded chunks, %d vector rows", embedded, vecRows)
		}
	}

	emb, state, note, err := SetupEmbedder(cfg, allowCloud)
	switch {
	case err != nil:
		add("embedder", CheckFail, "%v", err)
	case state == EmbedNone:
		add("embedder", CheckSkip, "not configured: search is full text only")
	case state == EmbedCloudBlocked:
		add("embedder", CheckSkip, "skipped (cloud not allowed): %s", note)
	default:
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		_, perr := emb.Embed(pctx, []string{"doctor probe"})
		cancel()
		if perr != nil {
			add("embedder", CheckWarn, "%s %s is not reachable: %v", cfg.Provider.Embed, cfg.Model, perr)
		} else {
			add("embedder", CheckOK, "%s %s answered", cfg.Provider.Embed, cfg.Model)
		}
	}
	return r
}
