package api

import (
	"errors"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/embed"
)

// Embedding states reported by Index and Status.
const (
	EmbedNone         = "none"          // no embedder configured, or --no-embed
	EmbedOK           = "ok"            // embedder available
	EmbedCloudBlocked = "cloud_blocked" // cloud embedder, cloud not allowed
	EmbedUnreachable  = "unreachable"   // local embedder did not answer
	EmbedMismatch     = "mismatch"      // the index was embedded by a different embedder
)

// SetupEmbedder builds the configured embedder. The returned state is
// EmbedNone (nil embedder), EmbedOK, or EmbedCloudBlocked (nil embedder, note
// holds the refusal). Any other error (an invalid configuration) is returned.
func SetupEmbedder(cfg config.Config, allowCloud bool) (e embed.Embedder, state, note string, err error) {
	e, err = embed.New(cfg, allowCloud)
	switch {
	case errors.Is(err, embed.ErrCloudNotAllowed):
		return nil, EmbedCloudBlocked, err.Error(), nil
	case err != nil:
		return nil, "", "", err
	case e == nil:
		return nil, EmbedNone, "", nil
	}
	return e, EmbedOK, "", nil
}
