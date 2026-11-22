package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/wamra/specs-index/internal/api"
)

func cmdIndex(args []string, out, errw io.Writer) int {
	var c common
	var o api.IndexOptions
	fs := newFlags("index", errw, &c)
	fs.BoolVar(&o.Reembed, "reembed", false, "discard every vector and embed all chunks again")
	fs.BoolVar(&o.Infer, "infer", false, "after embedding, store suggested implements/verified_by links (never counted as coverage)")
	fs.BoolVar(&o.AllowCloud, "allow-cloud", false, "allow a cloud embedding provider for this run")
	fs.BoolVar(&o.NoEmbed, "no-embed", false, "index text only; do not embed")
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 0 {
		return usageErr(errw, "index takes no arguments (it indexes the registered sources; see `add`)")
	}
	if o.NoEmbed && (o.Reembed || o.Infer) {
		return usageErr(errw, "index: --no-embed cannot be combined with --reembed or --infer")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(errw, err)
	}
	root, err := c.resolveRoot()
	if err != nil {
		return fail(errw, err)
	}
	// Without an index there are no sources either: explain before creating
	// the database or touching the git exclude file.
	st, err := api.OpenExisting(root)
	if errors.Is(err, api.ErrNoIndex) {
		return fail(errw, api.ErrNoSources)
	}
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	excludeState(root, errw)
	release, err := api.LockIndex(context.Background(), root, errw)
	if err != nil {
		return fail(errw, err)
	}
	defer release()
	r, err := api.Index(context.Background(), st, root, cfg, o)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	fmt.Fprintf(out, "scanned %d files: %d parsed, %d unchanged, %d removed, %d failed\n", r.Scanned, r.Parsed, r.Unchanged, r.Removed, r.Failed)
	fmt.Fprintf(out, "%d entities, %d edges\n", r.Entities, r.Edges)
	for _, f := range r.Failures {
		fmt.Fprintf(out, "  failed: %s: %s\n", f.Path, f.Reason)
	}
	e := r.Embedding
	switch {
	case e.Note != "":
		fmt.Fprintf(out, "embedding: %s\n", e.Note)
	case e.State == api.EmbedNone:
		fmt.Fprintln(out, "embedding: no embedder configured; the index is text-only")
	default:
		fmt.Fprintf(out, "embedding: %d chunks embedded", e.Embedded)
		if o.Infer {
			fmt.Fprintf(out, ", %d inferred links stored", e.Inferred)
		}
		fmt.Fprintln(out)
	}
	return 0
}
