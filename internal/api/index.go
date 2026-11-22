package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/embed"
	"github.com/wamra/specs-index/internal/indexer"
	"github.com/wamra/specs-index/internal/provider"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/vector"
)

// ErrNoSources is returned by Index when no source is registered.
var ErrNoSources = errors.New("no sources registered: run `specs-index add PATH` first (for example `specs-index add docs features`)")

// IndexOptions are the flags of index.
type IndexOptions struct {
	Reembed    bool
	Infer      bool
	AllowCloud bool
	NoEmbed    bool
}

// IndexFailure is a file that could not be indexed.
type IndexFailure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// EmbedSummary says what the embedding step did. State is none, ok,
// cloud_blocked, unreachable or mismatch; Note explains a skipped step.
type EmbedSummary struct {
	State    string `json:"state"`
	Embedded int    `json:"embedded"`
	Inferred int    `json:"inferred"`
	Note     string `json:"note"`
}

// IndexResult is the result of index.
type IndexResult struct {
	Scanned   int            `json:"scanned"`
	Parsed    int            `json:"parsed"`
	Unchanged int            `json:"unchanged"`
	Removed   int            `json:"removed"`
	Failed    int            `json:"failed"`
	Entities  int            `json:"entities"`
	Edges     int            `json:"edges"`
	Failures  []IndexFailure `json:"failures"`
	Embedding EmbedSummary   `json:"embedding"`
}

// Index brings the index in step with the sources, then embeds the chunks that
// need it. A missing, cloud-blocked or unreachable embedder, or one that differs
// from the embedder the stored vectors came from (state mismatch; the note
// names `index --reembed`), leaves the text index updated and is reported in
// Embedding, not as an error. Nothing is embedded on a mismatch.
func Index(ctx context.Context, st *store.Store, root string, cfg config.Config, o IndexOptions) (IndexResult, error) {
	if o.NoEmbed && (o.Reembed || o.Infer) {
		return IndexResult{}, fmt.Errorf("%w: --no-embed cannot be combined with --reembed or --infer", ErrInvalidArgument)
	}
	srcs, err := indexer.ListSources(st)
	if err != nil {
		return IndexResult{}, err
	}
	if len(srcs) == 0 {
		return IndexResult{}, ErrNoSources
	}
	rep, err := indexer.Run(ctx, st, root, cfg.Types)
	if err != nil {
		return IndexResult{}, err
	}
	res := IndexResult{
		Scanned: rep.Scanned, Parsed: rep.Parsed, Unchanged: rep.Unchanged, Removed: rep.Removed,
		Failed: rep.Failed, Entities: rep.Entities, Edges: rep.Edges,
		Failures: make([]IndexFailure, 0, len(rep.Failures)),
	}
	for _, f := range rep.Failures {
		res.Failures = append(res.Failures, IndexFailure{Path: f.Path, Reason: f.Reason})
	}
	res.Embedding = EmbedSummary{State: EmbedNone}
	if o.NoEmbed {
		res.Embedding.Note = "embedding skipped (--no-embed)"
		return res, nil
	}
	emb, state, note, err := SetupEmbedder(cfg, o.AllowCloud)
	if err != nil {
		return res, err
	}
	if o.Reembed {
		// Verify the embedder before touching any vector.
		switch state {
		case EmbedNone:
			return res, errors.New("--reembed needs an embedder, but none is configured; existing vectors were left untouched")
		case EmbedCloudBlocked:
			return res, fmt.Errorf("--reembed refused, existing vectors left untouched: %s", note)
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, perr := emb.Embed(pctx, []string{"reembed probe"})
		cancel()
		if perr != nil {
			return res, fmt.Errorf("--reembed refused, existing vectors left untouched: the embedder does not answer: %w", perr)
		}
	}
	res.Embedding.State = state
	switch state {
	case EmbedNone:
		if o.Infer {
			res.Embedding.Note = "no embedder is configured: --infer needs one"
		}
		return res, nil
	case EmbedCloudBlocked:
		res.Embedding.Note = "text-only index: " + note
		return res, nil
	}
	vs := vector.NewVec1(st.DB())
	if o.Reembed {
		if err := embed.Reembed(ctx, st, vs); err != nil {
			return res, err
		}
	}
	n, err := embed.Pending(ctx, st, emb, vs)
	switch {
	case errors.Is(err, provider.ErrDown):
		res.Embedding.State = EmbedUnreachable
		res.Embedding.Embedded = n
		res.Embedding.Note = fmt.Sprintf("text-only index: the embedder is unreachable (%v); run `specs-index index` again when it is up", err)
		return res, nil
	case errors.Is(err, embed.ErrFingerprintMismatch):
		res.Embedding.State = EmbedMismatch
		res.Embedding.Note = "text index updated, nothing embedded: " + err.Error()
		return res, nil
	case err != nil:
		return res, err
	}
	res.Embedding.Embedded = n
	if o.Infer {
		inf, err := embed.Infer(ctx, st, vs)
		if err != nil {
			return res, err
		}
		res.Embedding.Inferred = inf
	}
	return res, nil
}
