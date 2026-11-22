package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/wamra/specs-index/internal/api"
)

func cmdSearch(args []string, out, errw io.Writer) int {
	var c common
	var o api.SearchOptions
	fs := newFlags("search", errw, &c)
	fs.StringVar(&o.Mode, "mode", "auto", "auto, exact, fts, vector or hybrid")
	fs.IntVar(&o.K, "k", 10, "number of results")
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return usageErr(errw, "search: give a QUERY (use -- before a query that starts with -)")
	}
	if o.K < 1 {
		return usageErr(errw, "search: --k must be at least 1")
	}
	if err := api.ValidateSearchOptions(o); err != nil {
		return usageErr(errw, "search: %v", err)
	}
	query := strings.Join(pos, " ")
	cfg, err := loadConfig()
	if err != nil {
		return fail(errw, err)
	}
	st, root, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	r, err := api.Search(context.Background(), st, root, query, cfg, o)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	if r.Notice != "" {
		fmt.Fprintf(errw, "specs-index: %s\n", r.Notice)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(out, "note: %s\n", n)
	}
	if len(r.Hits) == 0 {
		fmt.Fprintln(out, "no results")
		return 0
	}
	for _, h := range r.Hits {
		fmt.Fprintf(out, "%s  %s  %s\n", h.Key, h.Type, h.Title)
		fmt.Fprintf(out, "  %s:%d-%d", h.Path, h.StartLine, h.EndLine)
		if h.Status != "" {
			fmt.Fprintf(out, "  status=%s", h.Status)
		}
		if h.Stale {
			fmt.Fprint(out, "  (stale: the file changed since indexing; run `specs-index index`)")
		}
		fmt.Fprintln(out)
	}
	return 0
}

func cmdShow(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("show", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return usageErr(errw, "show: give exactly one KEY")
	}
	st, root, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	r, err := api.Show(context.Background(), st, root, pos[0])
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	fmt.Fprintf(out, "%s  %s  %s\n", r.Key, r.Type, r.Title)
	fmt.Fprintf(out, "%s:%d-%d", r.Path, r.StartLine, r.EndLine)
	if r.Status != "" {
		fmt.Fprintf(out, "  status=%s", r.Status)
	}
	if r.Priority != "" {
		fmt.Fprintf(out, "  priority=%s", r.Priority)
	}
	if len(r.Tags) > 0 {
		fmt.Fprintf(out, "  tags=%s", strings.Join(r.Tags, ","))
	}
	if r.Stale {
		fmt.Fprint(out, "  (stale: the file changed since indexing; run `specs-index index`)")
	}
	fmt.Fprintln(out)
	if r.Source != "" {
		fmt.Fprintln(out, "---")
		fmt.Fprintln(out, r.Source)
		fmt.Fprintln(out, "---")
	}
	printEdges := func(label string, edges []api.EdgeInfo) {
		for _, e := range edges {
			fmt.Fprintf(out, "%s %s %s", label, e.Kind, e.Key)
			if e.Missing {
				fmt.Fprint(out, " (undefined)")
			}
			if !e.Explicit {
				fmt.Fprint(out, " (inferred)")
			}
			if e.Path != "" {
				fmt.Fprintf(out, "  %s:%d", e.Path, e.Line)
			}
			fmt.Fprintln(out)
		}
	}
	printEdges("->", r.Outgoing)
	printEdges("<-", r.Incoming)
	return 0
}
