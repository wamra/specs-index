package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/wamra/specs-index/internal/mcp"
)

// cmdMCP serves the tools over stdio for Claude Code. stdout carries protocol
// messages only; notices go to stderr. The root comes from CLAUDE_PROJECT_DIR,
// else the working directory, never from a tool argument.
func cmdMCP(args []string, out, errw io.Writer) int {
	if len(args) != 0 {
		return usageErr(errw, "mcp takes no arguments (the project root is CLAUDE_PROJECT_DIR, else the working directory)")
	}
	root, err := mcp.RootFromEnv()
	if err != nil {
		return fail(errw, err)
	}
	var vbuf bytes.Buffer
	cmdVersion(nil, &vbuf, errw)
	mcp.ServerVersion = strings.TrimSpace(strings.TrimPrefix(vbuf.String(), "specs-index"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(errw, "specs-index mcp: serving project %s on stdio\n", root)
	if err := mcp.Serve(ctx, os.Stdin, out, root); err != nil {
		return fail(errw, err)
	}
	return 0
}
