package main

import (
	"fmt"
	"io"
	"os"

	"github.com/wamra/specs-index/internal/api"
	"github.com/wamra/specs-index/internal/workspace"
)

// excludeState keeps .Wammy/ out of git; a failure is only a warning.
func excludeState(root string, errw io.Writer) {
	if err := workspace.EnsureExcluded(root); err != nil {
		fmt.Fprintf(errw, "specs-index: warning: could not update the git exclude file: %v\n", err)
	}
}

func printSources(out io.Writer, r api.SourcesResult, verb string) {
	for _, p := range r.Changed {
		fmt.Fprintf(out, "%s %s\n", verb, p)
	}
	if len(r.Sources) == 0 {
		fmt.Fprintln(out, "no sources registered")
		return
	}
	if verb != "" {
		fmt.Fprintln(out, "sources:")
	}
	for _, s := range r.Sources {
		fmt.Fprintf(out, "%s  %s\n", s.Path, s.Kind)
	}
}

func cmdSources(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("sources", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) != 0 {
		return usageErr(errw, "sources takes no arguments")
	}
	st, _, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	r, err := api.Sources(st)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	printSources(out, r, "")
	return 0
}

func cmdAdd(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("add", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return usageErr(errw, "add: give at least one PATH")
	}
	root, err := c.resolveRoot()
	if err != nil {
		return fail(errw, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(errw, err)
	}
	st, err := api.OpenForWrite(root)
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	excludeState(root, errw)
	r, err := api.Add(st, root, cwd, pos)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	printSources(out, r, "added")
	return 0
}

func cmdRemove(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("remove", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return usageErr(errw, "remove: give at least one PATH")
	}
	st, root, err := c.openExisting()
	if err != nil {
		return fail(errw, err)
	}
	defer st.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return fail(errw, err)
	}
	r, err := api.Remove(st, root, cwd, pos)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	printSources(out, r, "removed")
	return 0
}
