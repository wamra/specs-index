package main

import (
	"context"
	"fmt"
	"io"

	"github.com/wamra/specs-index/internal/api"
)

func cmdCoverage(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("coverage", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 0 {
		return usageErr(errw, "coverage takes no arguments")
	}
	st, _, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	r, err := api.Coverage(context.Background(), st)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	items := func(title string, list []api.CoverageItem) {
		fmt.Fprintf(out, "%s (%d)\n", title, len(list))
		for _, i := range list {
			fmt.Fprintf(out, "  %s  %s  %s:%d\n", i.Key, i.Title, i.Path, i.Line)
		}
	}
	items("Requirements without an implementing design", r.RequirementsWithoutDesign)
	items("Designs without a verifying scenario or criterion", r.DesignsWithoutVerification)
	items("Scenarios verifying nothing", r.UnlinkedScenarios)
	fmt.Fprintf(out, "Dangling IDs (%d)\n", len(r.Dangling))
	for _, d := range r.Dangling {
		what := "undefined " + d.Side + " of " + d.Kind
		if d.Mention {
			what += ", mention"
		}
		fmt.Fprintf(out, "  %s  %s, referenced by %s  %s:%d\n", d.ID, what, d.Referrer, d.Path, d.Line)
	}
	fmt.Fprintf(out, "Duplicate IDs (%d)\n", len(r.Duplicates))
	for _, d := range r.Duplicates {
		fmt.Fprintf(out, "  %s  %s:%d  %s\n", d.Key, d.Path, d.Line, d.Detail)
	}
	fmt.Fprintf(out, "Conflicts (%d)\n", len(r.Conflicts))
	for _, p := range r.Conflicts {
		fmt.Fprintf(out, "  %s <-> %s  %s:%d\n", p.A, p.B, p.Path, p.Line)
	}
	fmt.Fprintf(out, "Candidate conflicts, from vector similarity (%d)\n", len(r.Candidates))
	for _, p := range r.Candidates {
		fmt.Fprintf(out, "  %s ~ %s  %s:%d\n", p.A, p.B, p.Path, p.Line)
	}
	return 0
}
