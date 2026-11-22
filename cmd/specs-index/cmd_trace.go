package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/wamra/specs-index/internal/api"
)

func cmdTrace(args []string, out, errw io.Writer) int {
	var c common
	var o api.TraceOptions
	var kinds string
	fs := newFlags("trace", errw, &c)
	fs.StringVar(&o.Direction, "direction", "down", "down, up or both")
	fs.IntVar(&o.Depth, "depth", 5, "how many links to follow")
	fs.StringVar(&kinds, "kinds", "", "comma-separated edge kinds to follow (default parent_of,implements,verified_by)")
	fs.BoolVar(&o.IncludeInferred, "include-inferred", false, "also follow inferred (explicit=0) links")
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return usageErr(errw, "trace: give exactly one KEY")
	}
	for _, k := range strings.Split(kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			o.Kinds = append(o.Kinds, k)
		}
	}
	st, _, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	n, err := api.Trace(context.Background(), st, pos[0], o)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, n)
	}
	printTrace(out, n, 0)
	return 0
}

func printTrace(out io.Writer, n api.TraceNode, depth int) {
	fmt.Fprint(out, strings.Repeat("  ", depth))
	if n.Missing {
		fmt.Fprintf(out, "%s  (undefined)", n.Key)
	} else {
		fmt.Fprintf(out, "%s  %s  %s", n.Key, n.Type, n.Title)
	}
	if n.Via != "" {
		fmt.Fprintf(out, "  [via %s]", n.Via)
	}
	if !n.Explicit {
		fmt.Fprint(out, "  [inferred]")
	}
	if n.Repeat {
		fmt.Fprint(out, "  [repeat]")
	}
	if n.Path != "" {
		fmt.Fprintf(out, "  %s:%d", n.Path, n.Line)
	}
	fmt.Fprintln(out)
	for _, c := range n.Children {
		printTrace(out, c, depth+1)
	}
}
