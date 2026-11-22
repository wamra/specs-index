package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/wamra/specs-index/internal/api"
)

func cmdStatus(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("status", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 0 {
		return usageErr(errw, "status takes no arguments")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(errw, err)
	}
	st, root, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	r, err := api.Status(context.Background(), st, root, cfg)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	fmt.Fprintf(out, "index:      %s\n", r.DB)
	fmt.Fprintf(out, "sources:    %d\n", r.Sources)
	for _, src := range r.SourcePaths {
		fmt.Fprintf(out, "  %s  %s\n", src.Path, src.Kind)
	}
	fmt.Fprintf(out, "documents:  %d (%d failed)  %s\n", r.Documents, r.FailedDocs, byType(r.DocumentsByType))
	fmt.Fprintf(out, "entities:   %d  %s\n", r.Entities, byType(r.EntitiesByType))
	fmt.Fprintf(out, "edges:      %d explicit, %d inferred\n", r.EdgesExplicit, r.EdgesInferred)
	fmt.Fprintf(out, "chunks:     %d (%d embedded)\n", r.Chunks, r.EmbeddedChunks)
	if r.IndexedAt != "" {
		fmt.Fprintf(out, "indexed at: %s\n", r.IndexedAt)
	}
	e := r.Embedder
	fmt.Fprintf(out, "embedder:   %s", e.Provider)
	if e.Model != "" {
		fmt.Fprintf(out, " %s", e.Model)
	}
	if e.Cloud {
		fmt.Fprintf(out, " (cloud, allowed: %t)", e.CloudAllow)
	}
	fmt.Fprintln(out)
	if e.Error != "" {
		fmt.Fprintf(out, "  problem:  %s\n", e.Error)
	}
	if e.Fingerprint != "" {
		fmt.Fprintf(out, "  index embedded with: %s (%d dimensions)\n", e.Fingerprint, e.Dims)
	}
	fmt.Fprintln(out, "environment variables:")
	for _, v := range r.Environment {
		cur := "not set"
		if v.Set {
			cur = "set to " + v.Value
		}
		fmt.Fprintf(out, "  %s (%s)  %s\n", v.Name, cur, v.Description)
	}
	return 0
}

// byType renders "3 design, 25 requirement" in key order.
func byType(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
	}
	return strings.Join(parts, ", ")
}

func cmdDoctor(args []string, out, errw io.Writer) int {
	var c common
	var allowCloud bool
	fs := newFlags("doctor", errw, &c)
	fs.BoolVar(&allowCloud, "allow-cloud", false, "probe a cloud embedder too (default: never contact one)")
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 0 {
		return usageErr(errw, "doctor takes no arguments")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(errw, err)
	}
	root, err := c.resolveRoot()
	if err != nil {
		return fail(errw, err)
	}
	r := api.Doctor(context.Background(), root, cfg, allowCloud, 10*time.Second)
	if c.json {
		printJSON(out, r)
	} else {
		for _, ck := range r.Checks {
			fmt.Fprintf(out, "%-5s %-10s %s\n", ck.Status, ck.Name, ck.Detail)
		}
	}
	if !r.OK {
		return 1
	}
	return 0
}
