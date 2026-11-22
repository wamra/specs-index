package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/wamra/specs-index/internal/api"
	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/workspace"
)

// RootFromEnv returns the project root the server works on: CLAUDE_PROJECT_DIR
// when set, otherwise the working directory, resolved through workspace.Find
// exactly as the CLI does. Tool arguments can never change it.
func RootFromEnv() (string, error) {
	dir := os.Getenv("CLAUDE_PROJECT_DIR")
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = cwd
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("project directory: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("project directory %s is not a directory", dir)
	}
	return workspace.Find(dir)
}

type tool struct {
	name        string
	description string
	readOnly    bool
	writes      bool // changes the local index (destructiveHint false)
	openWorld   bool // may contact the configured embedding provider
	props       map[string]any
	required    []string
	run         func(ctx context.Context, s *server, args json.RawMessage) (any, error)
}

const (
	defaultK = 10
	maxK     = 100
	maxDepth = 50
)

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var tools = []tool{
	{
		name: "add_source",
		description: "Register a file or directory of Markdown requirement/design documents or Gherkin .feature files as a source of the index. " +
			"The path must be inside the project root. Run index afterwards; sources are only read, never modified.",
		writes:   true,
		props:    map[string]any{"path": str("File or directory to register, relative to the project root (or absolute, but inside it), for example \"docs\" or \"features\".")},
		required: []string{"path"},
		run:      toolAddSource,
	},
	{
		name: "remove_source",
		description: "Unregister a source added with add_source and drop the indexed documents that no remaining source covers. " +
			"The files on disk are not touched.",
		writes:   true,
		props:    map[string]any{"path": str("The source path to unregister, as listed by status or add_source (relative to the project root).")},
		required: []string{"path"},
		run:      toolRemoveSource,
	},
	{
		name: "index",
		description: "Bring the index up to date with the registered sources and embed new text for semantic search when an embedder is configured. " +
			"Run it after add_source or remove_source, and whenever search or show results are marked stale; without an embedder the index is text-only and the result says so.",
		writes:    true,
		openWorld: true,
		props: map[string]any{"reembed": map[string]any{"type": "boolean",
			"description": "Discard every stored vector and embed all text again. Only needed after the embedding model changed; fails without touching vectors when no embedder answers. Default false."}},
		run: toolIndex,
	},
	{
		name: "search",
		description: "Find requirements, designs and scenarios by text, meaning, entity key or filters. Use this first to locate an entity (for example \"login lockout\", \"BRD-001\", or \"type:requirement status:draft -has:scenario\"); " +
			"use show to read a hit and trace to follow its links. Hits marked stale were indexed before their file changed: run index.",
		readOnly: true,
		props: map[string]any{
			"query": str("Search text, an entity key such as BRD-001, or text with filters type:, status:, priority:, tag:, path:, has:scenario, has:fds (each can be negated with a leading minus). Required."),
			"mode": map[string]any{"type": "string", "enum": []string{"auto", "exact", "fts", "vector", "hybrid"},
				"description": "auto (default) uses meaning plus keywords when an embedder is configured and keywords alone otherwise; exact matches keys; fts is keyword search; vector and hybrid need an embedder."},
			"k": map[string]any{"type": "integer", "minimum": 1, "maximum": maxK, "description": fmt.Sprintf("Maximum number of hits (default %d, at most %d).", defaultK, maxK)},
		},
		required: []string{"query"},
		run:      toolSearch,
	},
	{
		name: "show",
		description: "Read one entity by key (requirement ID, design ID, or scenario key): its source lines, metadata, and its direct incoming and outgoing links. " +
			"An unknown key returns near matches.",
		readOnly: true,
		props:    map[string]any{"key": str("Entity key, for example BRD-001, as returned by search.")},
		required: []string{"key"},
		run:      toolShow,
	},
	{
		name: "trace",
		description: "Follow explicit links from an entity to see what implements, verifies or depends on it: down from a requirement to designs and scenarios, up from a scenario to the requirements it verifies, or both. " +
			"Use it to judge impact or traceability; use coverage to find gaps across everything.",
		readOnly: true,
		props: map[string]any{
			"key":       str("Entity key to start from, for example BRD-001."),
			"direction": map[string]any{"type": "string", "enum": []string{"down", "up", "both"}, "description": "down (default) follows links away from the key, up follows them back toward its parents, both does both."},
			"depth":     map[string]any{"type": "integer", "minimum": 1, "maximum": maxDepth, "description": "How many links to follow (default 5)."},
			"kinds": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"parent_of", "implements", "verified_by", "depends_on", "conflicts_with", "references"}},
				"description": "Edge kinds to follow; exactly this set is followed. Default parent_of, implements and verified_by."},
			"include_inferred": map[string]any{"type": "boolean", "description": "Also follow inferred links suggested by similarity (explicit=false). They never count as coverage. Default false."},
		},
		required: []string{"key"},
		run:      toolTrace,
	},
	{
		name: "coverage",
		description: "List the gaps in the indexed graph: requirements with no design, designs with no verifying scenario, scenarios linked to nothing, dangling or duplicate IDs, and conflicts. " +
			"Only explicit links count; run index first so the result reflects the current files.",
		readOnly: true,
		run:      toolCoverage,
	},
	{
		name: "status",
		description: "Show the index state: the registered sources, document and entity counts, when it was last indexed, and the configured embedder. " +
			"Use it to check that sources are registered and the index exists before searching.",
		readOnly: true,
		run:      toolStatus,
	},
}

// toolList is the tools/list payload.
func toolList(annotations bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		props := t.props
		if props == nil {
			props = map[string]any{}
		}
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(t.required) > 0 {
			schema["required"] = t.required
		}
		entry := map[string]any{"name": t.name, "description": t.description, "inputSchema": schema}
		if annotations { // ToolAnnotations exist from 2025-03-26 on
			a := map[string]any{"readOnlyHint": t.readOnly, "openWorldHint": t.openWorld}
			if t.writes {
				a["destructiveHint"] = false // only the local index changes; sources are never modified
			}
			entry["annotations"] = a
		}
		out = append(out, entry)
	}
	return out
}

// callTool runs tools/call. An unknown tool or a malformed request is a
// JSON-RPC error; a failing tool or bad arguments is a normal result with
// isError set, as the specification asks.
func (s *server) callTool(ctx context.Context, params json.RawMessage) (res map[string]any, rerr *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs params with a tool name"}
	}
	var t *tool
	for i := range tools {
		if tools[i].name == p.Name {
			t = &tools[i]
		}
	}
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Unknown tool: " + p.Name}
	}
	if err := checkArgNames(t, p.Arguments); err != nil {
		return textResult(err.Error(), true), nil
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(s.log, "specs-index mcp: tool %s panicked: %v\n", t.name, r)
			res, rerr = textResult(fmt.Sprintf("internal error in %s: %v", t.name, r), true), nil
		}
	}()
	v, err := t.run(ctx, s, p.Arguments)
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%s was cancelled: %w", t.name, ctx.Err())
		}
		return textResult(err.Error(), true), nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // the same encoding as the CLI's --json
	if err := enc.Encode(v); err != nil {
		return textResult("encode result: "+err.Error(), true), nil
	}
	return textResult(strings.TrimRight(b.String(), "\n"), false), nil
}

func textResult(text string, isError bool) map[string]any {
	r := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isError {
		r["isError"] = true
	}
	return r
}

// checkArgNames requires the arguments to be an object whose keys are exactly
// (case-sensitively) the properties the tool's inputSchema declares.
func checkArgNames(t *tool, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return errors.New("invalid arguments: arguments must be a JSON object")
	}
	for k := range m {
		if _, ok := t.props[k]; !ok {
			return fmt.Errorf("invalid arguments: unknown argument %q", k)
		}
	}
	return nil
}

// intArg parses an optional integer argument. Integer-valued JSON numbers such
// as 5.0 are accepted (JSON Schema "integer"); fractions and non-numbers are
// refused naming the argument as the client knows it.
func intArg(name string, raw json.RawMessage, lo, hi int) (*int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f != math.Trunc(f) {
		return nil, fmt.Errorf("invalid arguments: %q must be an integer", name)
	}
	if f < float64(lo) || f > float64(hi) {
		return nil, fmt.Errorf("invalid arguments: %q must be between %d and %d", name, lo, hi)
	}
	n := int(f)
	return &n, nil
}

// decodeArgs strictly decodes the tool arguments: an absent or null value is
// an empty object, an unknown field is an error (so a request for something
// like allow_cloud is refused, not ignored).
func decodeArgs(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if dec.More() {
		return errors.New("invalid arguments: trailing data")
	}
	return nil
}

func needText(name, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("invalid arguments: %q is required and must not be empty", name)
	}
	return v, nil
}

func (s *server) loadConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// withRead opens the index for one call and closes it afterwards, so the CLI
// can use the same index while the server runs.
func (s *server) withRead(fn func(st *store.Store) (any, error)) (any, error) {
	st, err := api.OpenExisting(s.root)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return fn(st)
}

func (s *server) withWrite(fn func(st *store.Store) (any, error)) (any, error) {
	st, err := api.OpenForWrite(s.root)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if err := workspace.EnsureExcluded(s.root); err != nil {
		fmt.Fprintf(s.log, "specs-index mcp: warning: could not update the git exclude file: %v\n", err)
	}
	return fn(st)
}

type pathArgs struct {
	Path string `json:"path"`
}

func toolAddSource(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a pathArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	p, err := needText("path", a.Path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Refuse before anything is created on disk; api.Add checks again.
	if _, err := workspace.Rel(s.root, p); err != nil {
		return nil, err
	}
	return s.withWrite(func(st *store.Store) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return api.Add(st, s.root, s.root, []string{p})
	})
}

func toolRemoveSource(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a pathArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	p, err := needText("path", a.Path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.withRead(func(st *store.Store) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return api.Remove(st, s.root, s.root, []string{p})
	})
}

func toolIndex(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a struct {
		Reembed bool `json:"reembed"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	// AllowCloud stays false: cloud embedding happens only when the user's
	// own config file already allows it.
	return s.withWrite(func(st *store.Store) (any, error) {
		return api.Index(ctx, st, s.root, cfg, api.IndexOptions{Reembed: a.Reembed})
	})
}

func toolSearch(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a struct {
		Query string          `json:"query"`
		Mode  string          `json:"mode"`
		K     json.RawMessage `json:"k"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	q, err := needText("query", a.Query)
	if err != nil {
		return nil, err
	}
	opts := api.SearchOptions{Mode: a.Mode, K: defaultK}
	if err := api.ValidateSearchOptions(opts); err != nil {
		return nil, err
	}
	k, err := intArg("k", a.K, 1, maxK)
	if err != nil {
		return nil, err
	}
	if k != nil {
		opts.K = *k
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	return s.withRead(func(st *store.Store) (any, error) {
		return api.Search(ctx, st, s.root, q, cfg, opts)
	})
}

func toolShow(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a struct {
		Key string `json:"key"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	key, err := needText("key", a.Key)
	if err != nil {
		return nil, err
	}
	return s.withRead(func(st *store.Store) (any, error) { return api.Show(ctx, st, s.root, key) })
}

func toolTrace(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a struct {
		Key             string          `json:"key"`
		Direction       string          `json:"direction"`
		Depth           json.RawMessage `json:"depth"`
		Kinds           []string        `json:"kinds"`
		IncludeInferred bool            `json:"include_inferred"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	key, err := needText("key", a.Key)
	if err != nil {
		return nil, err
	}
	opts := api.TraceOptions{Direction: a.Direction, Depth: 5 /* the CLI default */, Kinds: a.Kinds, IncludeInferred: a.IncludeInferred}
	if err := api.ValidateTraceOptions(opts); err != nil {
		return nil, err
	}
	depth, err := intArg("depth", a.Depth, 1, maxDepth)
	if err != nil {
		return nil, err
	}
	if depth != nil {
		opts.Depth = *depth
	}
	return s.withRead(func(st *store.Store) (any, error) {
		return api.Trace(ctx, st, key, opts)
	})
}

func toolCoverage(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a struct{}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	return s.withRead(func(st *store.Store) (any, error) { return api.Coverage(ctx, st) })
}

func toolStatus(ctx context.Context, s *server, raw json.RawMessage) (any, error) {
	var a struct{}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	return s.withRead(func(st *store.Store) (any, error) { return api.Status(ctx, st, s.root, cfg) })
}
