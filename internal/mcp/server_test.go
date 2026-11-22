package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wamra/specs-index/internal/api"
	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/corpus"
)

type testRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *testRPCError   `json:"error"`
}

// setHome points HOME at a temp dir with a config that turns embeddings off,
// so no test reaches a network or the real home.
func setHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, kv := range os.Environ() { // a developer shell must not change results
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SPECS_INDEX_") {
			t.Setenv(k, "") // registers the restore
			os.Unsetenv(k)
		}
	}
	t.Setenv("SPECS_INDEX_EMBED_PROVIDER", "none")
	dir := filepath.Join(home, ".config", "specs-index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"provider":{"embed":"none"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// newRoot copies the corpus into a temp project root and points the index at a
// file outside it.
func newRoot(t *testing.T) string {
	t.Helper()
	setHome(t)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "ws")
	copyTree(t, corpus.Dir(), root)
	root, _ = filepath.EvalSymlinks(root)
	t.Setenv("SPECS_INDEX_DB", filepath.Join(tmp, "db", "specs.sqlite"))
	return root
}

// serve runs the server over lines (one JSON-RPC message each) and returns
// every message it wrote. It fails the test when stdout carries anything that
// is not a single-line JSON-RPC message.
func serve(t *testing.T, root string, lines ...string) []rpcMsg {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := Serve(context.Background(), in, &out, root); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var msgs []rpcMsg
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var m rpcMsg
		if err := json.Unmarshal([]byte(l), &m); err != nil || m.JSONRPC != "2.0" {
			t.Fatalf("stdout line is not a JSON-RPC message: %q (%v)", l, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func byID(t *testing.T, msgs []rpcMsg, id string) rpcMsg {
	t.Helper()
	for _, m := range msgs {
		if string(m.ID) == id {
			return m
		}
	}
	t.Fatalf("no response with id %s in %d messages", id, len(msgs))
	return rpcMsg{}
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func call(id int, name string, args string) string {
	if args == "" {
		args = "{}"
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": json.RawMessage(args)}})
	return string(b)
}

func result(t *testing.T, m rpcMsg) toolResult {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("JSON-RPC error: %+v", *m.Error)
	}
	var r toolResult
	if err := json.Unmarshal(m.Result, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("want exactly one text content item, got %+v", r.Content)
	}
	return r
}

const initLine = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

func TestInitializeAndListTools(t *testing.T) {
	root := newRoot(t)
	msgs := serve(t, root, initLine,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if len(msgs) != 3 {
		t.Fatalf("want 3 responses (the notification gets none), got %d", len(msgs))
	}
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct{ Name string }
	}
	if err := json.Unmarshal(byID(t, msgs, "1").Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" {
		t.Errorf("a supported client version must be echoed, got %q", init.ProtocolVersion)
	}
	if _, ok := init.Capabilities["tools"]; !ok || init.ServerInfo.Name != "specs-index" {
		t.Errorf("bad initialize result: %+v", init)
	}
	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Type       string                    `json:"type"`
				Properties map[string]map[string]any `json:"properties"`
				Required   []string                  `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(byID(t, msgs, "2").Result, &list); err != nil {
		t.Fatal(err)
	}
	want := []string{"add_source", "remove_source", "index", "search", "show", "trace", "coverage", "status"}
	if len(list.Tools) != len(want) {
		t.Fatalf("want %d tools, got %d", len(want), len(list.Tools))
	}
	for i, tl := range list.Tools {
		if tl.Name != want[i] {
			t.Errorf("tool %d: want %s, got %s", i, want[i], tl.Name)
		}
		if tl.InputSchema.Type != "object" {
			t.Errorf("%s: inputSchema type %q", tl.Name, tl.InputSchema.Type)
		}
		if len(tl.Description) < 40 {
			t.Errorf("%s: description too thin: %q", tl.Name, tl.Description)
		}
		for pn, p := range tl.InputSchema.Properties {
			if d, _ := p["description"].(string); d == "" {
				t.Errorf("%s.%s has no description", tl.Name, pn)
			}
		}
		for _, r := range tl.InputSchema.Required {
			if _, ok := tl.InputSchema.Properties[r]; !ok {
				t.Errorf("%s: required %q is not a property", tl.Name, r)
			}
		}
		if tl.Name == "index" {
			if _, ok := tl.InputSchema.Properties["allow_cloud"]; ok {
				t.Error("index must not offer allow_cloud")
			}
		}
	}
	if string(byID(t, msgs, "3").Result) != "{}" {
		t.Errorf("ping result: %s", byID(t, msgs, "3").Result)
	}
}

func TestVersionNegotiationFallsBackToServerVersion(t *testing.T) {
	root := newRoot(t)
	msgs := serve(t, root, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	var init struct{ ProtocolVersion string }
	if err := json.Unmarshal(msgs[0].Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-11-25" {
		t.Errorf("unsupported version must be answered with the server's latest handshake version, got %q", init.ProtocolVersion)
	}
}

// indexed adds the corpus sources and indexes them through the tools.
func indexed(t *testing.T, root string) {
	t.Helper()
	msgs := serve(t, root,
		call(1, "add_source", `{"path":"docs"}`),
		call(2, "add_source", `{"path":"features"}`),
		call(3, "index", `{}`))
	for _, id := range []string{"1", "2", "3"} {
		if r := result(t, byID(t, msgs, id)); r.IsError {
			t.Fatalf("setup call %s failed: %s", id, r.Content[0].Text)
		}
	}
}

// cliJSON renders v the way the CLI's --json prints it.
func cliJSON(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestCallSearchMatchesCLIJSON(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	const query = "upload type:requirement status:approved"
	st, err := api.OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	want, err := api.Search(context.Background(), st, root, query, cfg, api.SearchOptions{K: 10})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Hits) == 0 {
		t.Fatal("corpus search returned no hits; the comparison would be vacuous")
	}
	msgs := serve(t, root, call(1, "search", `{"query":"`+query+`"}`))
	r := result(t, byID(t, msgs, "1"))
	if r.IsError {
		t.Fatalf("search failed: %s", r.Content[0].Text)
	}
	if got := r.Content[0].Text + "\n"; got != cliJSON(t, want) {
		t.Errorf("tool text differs from CLI --json\ngot:\n%s\nwant:\n%s", got, cliJSON(t, want))
	}
}

func TestToolsMirrorAPI(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	st, err := api.OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	ctx := context.Background()
	cov, _ := api.Coverage(ctx, st)
	stat, _ := api.Status(ctx, st, root, cfg)
	srcs, _ := api.Sources(st)
	st.Close()
	msgs := serve(t, root, call(1, "coverage", ""), call(2, "status", ""))
	if got := result(t, byID(t, msgs, "1")).Content[0].Text + "\n"; got != cliJSON(t, cov) {
		t.Errorf("coverage differs:\n%s", got)
	}
	if got := result(t, byID(t, msgs, "2")).Content[0].Text + "\n"; got != cliJSON(t, stat) {
		t.Errorf("status differs:\n%s", got)
	}
	if len(srcs.Sources) != 2 {
		t.Errorf("sources: %+v", srcs)
	}
}

func TestShowTraceAndRemove(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	const key = "BRD-002" // a requirement of the corpus (see the search test)
	msgs := serve(t, root,
		call(1, "show", `{"key":"`+key+`"}`),
		call(2, "trace", `{"key":"`+key+`","direction":"both","depth":2,"kinds":["implements"],"include_inferred":true}`),
		call(3, "show", `{"key":"NOPE-999"}`),
		call(4, "remove_source", `{"path":"features"}`))
	for _, id := range []string{"1", "2", "4"} {
		if r := result(t, byID(t, msgs, id)); r.IsError {
			t.Errorf("call %s failed: %s", id, r.Content[0].Text)
		}
	}
	if got := result(t, byID(t, msgs, "1")).Content[0].Text; !strings.Contains(got, `"key": "`+key+`"`) {
		t.Errorf("show did not return %s: %s", key, got)
	}
	if got := result(t, byID(t, msgs, "2")).Content[0].Text; !strings.Contains(got, `"key": "`+key+`"`) {
		t.Errorf("trace root is not %s: %s", key, got)
	}
	if r := result(t, byID(t, msgs, "3")); !r.IsError {
		t.Error("an unknown key must be a tool error")
	}
	if got := result(t, byID(t, msgs, "4")).Content[0].Text; strings.Contains(got, `"features"`) && !strings.Contains(got, `"changed"`) {
		t.Errorf("remove_source result: %s", got)
	}
}

func TestUnknownToolIsJSONRPCError(t *testing.T) {
	msgs := serve(t, newRoot(t), call(1, "no_such_tool", ""))
	m := byID(t, msgs, "1")
	if m.Error == nil || m.Error.Code != -32602 || !strings.Contains(m.Error.Message, "no_such_tool") {
		t.Errorf("want -32602 naming the tool, got %+v", m.Error)
	}
}

func TestUnknownMethodAndNotifications(t *testing.T) {
	msgs := serve(t, newRoot(t),
		`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","method":"notifications/whatever"}`,
		`{"jsonrpc":"2.0","method":"unknown/notification"}`)
	if len(msgs) != 1 {
		t.Fatalf("want exactly one response, got %d", len(msgs))
	}
	if m := byID(t, msgs, "7"); m.Error == nil || m.Error.Code != -32601 {
		t.Errorf("want -32601, got %+v", m.Error)
	}
}

func TestBadArgumentsAreToolErrors(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	cases := map[string]string{
		"missing query":     call(1, "search", `{}`),
		"empty query":       call(2, "search", `{"query":"  "}`),
		"wrong type":        call(3, "search", `{"query":5}`),
		"bad mode":          call(4, "search", `{"query":"x","mode":"fuzzy"}`),
		"negative k":        call(5, "search", `{"query":"x","k":-1}`),
		"unknown argument":  call(6, "search", `{"query":"x","allow_cloud":true}`),
		"index allow_cloud": call(7, "index", `{"allow_cloud":true}`),
		"bad direction":     call(8, "trace", `{"key":"BRD-001","direction":"sideways"}`),
		"missing key":       call(9, "show", `{}`),
		"arguments not obj": `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"status","arguments":[1]}}`,
		"missing path":      call(11, "add_source", `{}`),
		"negative depth":    call(12, "trace", `{"key":"BRD-001","depth":-2}`),
		"no params at all":  `{"jsonrpc":"2.0","id":13,"method":"tools/call"}`,
	}
	var lines []string
	for _, l := range cases {
		lines = append(lines, l)
	}
	msgs := serve(t, root, lines...)
	if len(msgs) != len(cases) {
		t.Fatalf("want %d responses, got %d", len(cases), len(msgs))
	}
	for name, l := range cases {
		var h struct{ ID json.RawMessage }
		json.Unmarshal([]byte(l), &h)
		m := byID(t, msgs, string(h.ID))
		if name == "no params at all" {
			if m.Error == nil || m.Error.Code != -32602 {
				t.Errorf("%s: want -32602, got %+v", name, m)
			}
			continue
		}
		if r := result(t, m); !r.IsError || r.Content[0].Text == "" {
			t.Errorf("%s: want isError with a message, got %+v", name, r)
		}
	}
}

func TestNoIndexIsToolError(t *testing.T) {
	msgs := serve(t, newRoot(t), call(1, "status", ""), call(2, "index", ""))
	for _, id := range []string{"1", "2"} {
		r := result(t, byID(t, msgs, id))
		if !r.IsError || !strings.Contains(r.Content[0].Text, "specs-index") && !strings.Contains(r.Content[0].Text, "source") {
			t.Errorf("call %s: want a helpful isError, got %+v", id, r)
		}
	}
}

func TestAddSourceOutsideRootRefused(t *testing.T) {
	root := newRoot(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	linked := os.Symlink(outside, link) == nil
	args := []string{outside, filepath.Dir(root), "../.."}
	if linked {
		args = append(args, "escape")
	}
	var lines []string
	for i, a := range args {
		b, _ := json.Marshal(map[string]string{"path": a})
		lines = append(lines, call(i+1, "add_source", string(b)))
	}
	msgs := serve(t, root, lines...)
	for i, a := range args {
		r := result(t, byID(t, msgs, string(rune('0'+i+1))))
		if !r.IsError || !strings.Contains(r.Content[0].Text, "outside") {
			t.Errorf("add_source %q: want an 'outside' refusal, got %+v", a, r)
		}
	}
	// A refusal must leave no trace: no index file was even created.
	if _, err := os.Stat(os.Getenv("SPECS_INDEX_DB")); err == nil {
		t.Error("a refused add_source created the index file")
	}
	if _, err := api.OpenExisting(root); !errors.Is(err, api.ErrNoIndex) {
		t.Errorf("want ErrNoIndex after only refused paths, got %v", err)
	}
}

func TestMalformedLineDoesNotKillServer(t *testing.T) {
	msgs := serve(t, newRoot(t),
		`{this is not json`,
		`[1,2]`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if msgs[0].Error == nil || msgs[0].Error.Code != -32700 || string(msgs[0].ID) != "null" {
		t.Errorf("malformed JSON: want -32700 with id null, got %+v id=%s", msgs[0].Error, msgs[0].ID)
	}
	if msgs[1].Error == nil || msgs[1].Error.Code != -32600 {
		t.Errorf("batch/array: want -32600, got %+v", msgs[1].Error)
	}
	if m := byID(t, msgs, "5"); m.Error != nil {
		t.Errorf("ping after garbage failed: %+v", m.Error)
	}
}

func TestVeryLongLineIsAccepted(t *testing.T) {
	pad := strings.Repeat("x", 300_000) // well over bufio.Scanner's 64 KiB default
	msgs := serve(t, newRoot(t),
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"`+pad+`"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if len(msgs) != 2 || msgs[0].Error != nil || msgs[1].Error != nil {
		t.Fatalf("long line broke the reader: %+v", msgs)
	}
}

func TestModernStatelessRevision(t *testing.T) {
	root := newRoot(t)
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	msgs := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+meta+`}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"server/discover"}`)
	var disc struct {
		ResultType        string   `json:"resultType"`
		SupportedVersions []string `json:"supportedVersions"`
		Capabilities      map[string]any
	}
	if err := json.Unmarshal(byID(t, msgs, "1").Result, &disc); err != nil {
		t.Fatal(err)
	}
	if disc.ResultType != "complete" || len(disc.SupportedVersions) == 0 || disc.SupportedVersions[0] != "2026-07-28" {
		t.Errorf("discover: %+v", disc)
	}
	var list map[string]json.RawMessage
	if err := json.Unmarshal(byID(t, msgs, "2").Result, &list); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tools", "resultType", "ttlMs", "cacheScope"} {
		if _, ok := list[k]; !ok {
			t.Errorf("modern tools/list result lacks %q", k)
		}
	}
	if m := byID(t, msgs, "3"); m.Error == nil || m.Error.Code != -32022 || !strings.Contains(string(m.Error.Data), "supported") {
		t.Errorf("unsupported version: want -32022 with data.supported, got %+v", m.Error)
	}
	if m := byID(t, msgs, "4"); m.Error != nil {
		t.Errorf("discover without _meta must still answer: %+v", m.Error)
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, pr, &bytes.Buffer{}, newRoot(t)) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("cancel is a clean stop, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after cancel while stdin was idle")
	}
}

func TestRootFromEnv(t *testing.T) {
	repo := t.TempDir()
	repo, _ = filepath.EvalSymlinks(repo)
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", sub)
	got, err := RootFromEnv()
	if err != nil || got != repo {
		t.Errorf("CLAUDE_PROJECT_DIR=%s: want %s, got %s (%v)", sub, repo, got, err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", filepath.Join(repo, "missing"))
	if _, err := RootFromEnv(); err == nil {
		t.Error("a missing CLAUDE_PROJECT_DIR must be an error")
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Chdir(sub)
	if got, err := RootFromEnv(); err != nil || got != repo {
		t.Errorf("cwd fallback: want %s, got %s (%v)", repo, got, err)
	}
}

func ioPipeForTest() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

func TestIDEchoAndNullID(t *testing.T) {
	msgs := serve(t, newRoot(t),
		`{"jsonrpc":"2.0","id":"abc-1","method":"ping"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		call(0, "status", ""))
	for _, id := range []string{`"abc-1"`, `1.5`, `9007199254740993`, `0`} {
		byID(t, msgs, id)
	}
	var nulls int
	for _, m := range msgs {
		if string(m.ID) == "null" {
			nulls++
			if m.Error == nil || m.Error.Code != -32600 {
				t.Errorf("id null must be -32600, got %+v", m.Error)
			}
		}
	}
	if nulls != 1 || len(msgs) != 5 {
		t.Errorf("want 5 messages with exactly one null-id error, got %d / %d", len(msgs), nulls)
	}
}

func TestModernToolsCallCarriesResultType(t *testing.T) {
	root := newRoot(t)
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	msgs := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{},`+meta+`}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope",`+meta+`}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`)
	var r map[string]json.RawMessage
	if err := json.Unmarshal(byID(t, msgs, "1").Result, &r); err != nil {
		t.Fatal(err)
	}
	if string(r["resultType"]) != `"complete"` || r["content"] == nil || r["_meta"] == nil {
		t.Errorf("modern tools/call result: %s", byID(t, msgs, "1").Result)
	}
	if m := byID(t, msgs, "2"); m.Error == nil || m.Error.Code != -32602 {
		t.Errorf("modern unknown tool: %+v", m.Error)
	}
	if m := byID(t, msgs, "3"); m.Error == nil || m.Error.Code != -32022 {
		t.Errorf("modern tools/call with an unsupported version: %+v", m.Error)
	}
}

func TestIntegerArguments(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	msgs := serve(t, root,
		call(1, "search", `{"query":"upload","k":2.0}`),
		call(2, "search", `{"query":"upload","k":2.5}`),
		call(3, "search", `{"query":"upload","k":"2"}`),
		call(4, "trace", `{"key":"BRD-002","depth":3.0}`),
		call(5, "trace", `{"key":"BRD-002","depth":1.5}`),
		call(6, "search", `{"query":"upload","k":0}`),
		call(7, "search", `{"Query":"upload"}`),
		call(8, "search", `{"query":"upload","k":null}`))
	for _, id := range []string{"1", "4", "8"} {
		if r := result(t, byID(t, msgs, id)); r.IsError {
			t.Errorf("call %s should succeed: %s", id, r.Content[0].Text)
		}
	}
	want := map[string]string{"2": `"k"`, "3": `"k"`, "5": `"depth"`, "6": `"k"`, "7": `"Query"`}
	for id, name := range want {
		r := result(t, byID(t, msgs, id))
		if !r.IsError || !strings.Contains(r.Content[0].Text, name) || strings.Contains(r.Content[0].Text, "json:") {
			t.Errorf("call %s: want an isError naming %s without Go internals, got %q", id, name, r.Content[0].Text)
		}
	}
	var res api.SearchResult
	if err := json.Unmarshal([]byte(result(t, byID(t, msgs, "1")).Content[0].Text), &res); err != nil || len(res.Hits) > 2 {
		t.Errorf("k=2.0 not honoured: %d hits (%v)", len(res.Hits), err)
	}
}

func TestInvalidArgumentsFromAPIAreToolErrors(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	msgs := serve(t, root, call(1, "trace", `{"key":"BRD-002","kinds":["bogus"]}`))
	r := result(t, byID(t, msgs, "1"))
	if !r.IsError || !strings.Contains(r.Content[0].Text, "bogus") {
		t.Errorf("want an isError naming the bad kind, got %+v", r)
	}
}

func TestAnnotationsFollowTheNegotiatedRevision(t *testing.T) {
	root := newRoot(t)
	hints := func(msgs []rpcMsg, id string) map[string]map[string]any {
		var l struct {
			Tools []struct {
				Name        string         `json:"name"`
				Annotations map[string]any `json:"annotations"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(byID(t, msgs, id).Result, &l); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, tl := range l.Tools {
			out[tl.Name] = tl.Annotations
		}
		return out
	}
	m := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	h := hints(m, "2")
	if h["search"]["readOnlyHint"] != true || h["search"]["openWorldHint"] != false {
		t.Errorf("search annotations: %v", h["search"])
	}
	for _, n := range []string{"add_source", "remove_source", "index"} {
		if h[n]["readOnlyHint"] != false || h[n]["destructiveHint"] != false {
			t.Errorf("%s annotations: %v", n, h[n])
		}
	}
	if _, ok := h["search"]["destructiveHint"]; ok {
		t.Error("destructiveHint is meaningless on a read-only tool")
	}
	old := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	for n, a := range hints(old, "2") {
		if a != nil {
			t.Errorf("2024-11-05 has no tool annotations, %s sent %v", n, a)
		}
	}
}

func TestStatusRedactsURLCredentialsAndDBPath(t *testing.T) {
	root := newRoot(t)
	indexed(t, root)
	t.Setenv("SPECS_INDEX_EMBED_BASE_URL", "http://user:s3cr3tpw@127.0.0.1:1")
	text := result(t, byID(t, serve(t, root, call(1, "status", "")), "1")).Content[0].Text
	if strings.Contains(text, "s3cr3tpw") {
		t.Errorf("status handed the URL password to the model:\n%s", text)
	}
	if db := os.Getenv("SPECS_INDEX_DB"); strings.Contains(text, db) {
		t.Errorf("status exposed the absolute SPECS_INDEX_DB value:\n%s", text)
	}
}
