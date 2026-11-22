package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wamra/specs-index/internal/api"
	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/corpus"
	"github.com/wamra/specs-index/internal/embedtest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// setHome points HOME and USERPROFILE at a fresh temp dir and writes a config
// that turns embeddings off, so no test reaches a network or the real home.
func setHome(t *testing.T) string {
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
	writeConfig(t, home, `{"provider":{"embed":"none"}}`)
	return home
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "specs-index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
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

// newWorkspace copies the corpus into a temp root and points the index at a file
// outside it. It returns the root.
func newWorkspace(t *testing.T) string {
	t.Helper()
	setHome(t)
	tmp := t.TempDir()
	root := filepath.Join(tmp, "ws")
	copyTree(t, corpus.Dir(), root)
	root, _ = filepath.EvalSymlinks(root)
	t.Setenv("SPECS_INDEX_DB", filepath.Join(tmp, "db", "specs.sqlite"))
	return root
}

func cli(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String(), errw.String()
}

func mustOK(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errw := cli(t, args...)
	if code != 0 {
		t.Fatalf("specs-index %s: exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, out, errw)
	}
	return out
}

// indexed returns a workspace with docs and features added and indexed.
func indexed(t *testing.T) string {
	t.Helper()
	root := newWorkspace(t)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"), filepath.Join(root, "features"))
	mustOK(t, "index", "--root", root)
	return root
}

func contains(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("output lacks %q:\n%s", sub, s)
		}
	}
}

func TestAddIndexSearchTraceCoverageEndToEnd(t *testing.T) {
	root := newWorkspace(t)

	out := mustOK(t, "add", "--root", root, filepath.Join(root, "docs"), filepath.Join(root, "features"))
	contains(t, out, "docs", "features")
	contains(t, mustOK(t, "sources", "--root", root), "docs  dir", "features  dir")

	out = mustOK(t, "index", "--root", root)
	contains(t, out, "scanned 11", "text-only")

	out = mustOK(t, "search", "--root", root, "BRD-004")
	contains(t, out, "BRD-004  requirement", "docs/brd.md:")

	out = mustOK(t, "show", "--root", root, "BRD-004")
	contains(t, out, "BRD-004  requirement", "## BRD-004")

	out = mustOK(t, "trace", "--root", root, "BRD-001")
	contains(t, out, "BRD-001  requirement")
	if !strings.Contains(out, "\n  ") || !strings.Contains(out, "via implements reversed") {
		t.Errorf("trace is not an indented tree with via:\n%s", out)
	}

	out = mustOK(t, "coverage", "--root", root)
	contains(t, out, "BRD-015", "BRD-099", "AC-010")

	out = mustOK(t, "status", "--root", root)
	contains(t, out, "entities")

	out = mustOK(t, "doctor", "--root", root)
	contains(t, out, "integrity", "chunk_fts")

	// the root holds no .Wammy when SPECS_INDEX_DB points elsewhere, and the
	// sources were not modified
	if _, err := os.Stat(filepath.Join(root, ".Wammy")); err == nil {
		t.Error(".Wammy created although SPECS_INDEX_DB was set")
	}
}

func TestRelativePathsResolveAgainstWorkingDirectory(t *testing.T) {
	root := newWorkspace(t)
	t.Chdir(filepath.Join(root, "docs"))
	mustOK(t, "add", "--root", root, "brd.md")
	contains(t, mustOK(t, "sources", "--root", root), "docs/brd.md  file")
	mustOK(t, "remove", "--root", root, "brd.md")
	if out := mustOK(t, "sources", "--root", root); strings.Contains(out, "brd.md") {
		t.Errorf("source not removed:\n%s", out)
	}
}

func TestAddRefusesPathOutsideRoot(t *testing.T) {
	root := newWorkspace(t)
	outside := t.TempDir()
	code, _, errw := cli(t, "add", "--root", root, outside)
	if code != 1 || !strings.Contains(errw, "outside") {
		t.Fatalf("exit %d, stderr %q", code, errw)
	}
}

func TestIndexExcludesStateDirInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := newWorkspace(t)
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	b, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(b), ".Wammy/") {
		t.Fatalf("exclude file: %v %q", err, b)
	}
}

// assertJSON checks that out is valid JSON with no null and no absolute path
// of the temp workspace, then compares it with the golden file.
func assertJSON(t *testing.T, root, name, out string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%s: invalid JSON: %v\n%s", name, err, out)
	}
	if strings.Contains(out, ": null") || strings.Contains(out, "[null") {
		t.Errorf("%s: JSON contains null:\n%s", name, out)
	}
	for _, bad := range []string{root, filepath.Dir(root), "\\\\"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: JSON contains %q", name, bad)
		}
	}
	golden := filepath.Join("testdata", "golden", name+".json")
	if *update {
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%s: %v (run with -update)", name, err)
	}
	if string(want) != out {
		t.Errorf("%s differs from %s:\n--- got\n%s\n--- want\n%s", name, golden, out, want)
	}
}

func TestJSONOutputsAreValidAndStable(t *testing.T) {
	root := indexed(t)
	cases := map[string][]string{
		"sources":          {"sources"},
		"search_id":        {"search", "BRD-004"},
		"search_fts":       {"search", "upload type:requirement status:approved"},
		"search_empty":     {"search", "zzzznomatch"},
		"show_requirement": {"show", "BRD-004"},
		"trace_brd004":     {"trace", "BRD-004", "--depth", "2"},
		"trace_up":         {"trace", "AC-001", "--direction", "up", "--depth", "2"},
		"coverage":         {"coverage"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			full := append(append([]string{}, args...), "--root", root, "--json")
			assertJSON(t, root, name, mustOK(t, full...))
		})
	}
	// index and doctor are not golden (counts depend on timing-free facts but
	// doctor details name no paths): just valid, non-null JSON.
	for _, args := range [][]string{{"index"}, {"doctor"}, {"status"}, {"config"}} {
		out := mustOK(t, append(append([]string{}, args...), "--root", root, "--json")...)
		var v any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Errorf("%v: invalid JSON: %v\n%s", args, err, out)
		}
		if strings.Contains(out, "null") {
			t.Errorf("%v: JSON contains null:\n%s", args, out)
		}
	}
}

func TestStatusReportsModelAndCounts(t *testing.T) {
	root := indexed(t)
	var st struct {
		Sources        int            `json:"sources"`
		Documents      int            `json:"documents"`
		Entities       int            `json:"entities"`
		EntitiesByType map[string]int `json:"entities_by_type"`
		Chunks         int            `json:"chunks"`
		Embedder       struct {
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			APIKeySet bool   `json:"api_key_set"`
		} `json:"embedder"`
	}
	out := mustOK(t, "status", "--root", root, "--json")
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st.Sources != 2 || st.Documents != 11 || st.Entities == 0 || st.Chunks != st.Entities || st.EntitiesByType["requirement"] != 25 {
		t.Errorf("unexpected counts: %+v", st)
	}
	if st.Embedder.Provider != "none" {
		t.Errorf("embedder provider = %q", st.Embedder.Provider)
	}

	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"embeddinggemma","base_url":"http://127.0.0.1:1"}`)
	text := mustOK(t, "status", "--root", root)
	contains(t, text, "ollama", "embeddinggemma", "25 requirement")
}

func TestStatusNeverPrintsAPIKeyValue(t *testing.T) {
	root := indexed(t)
	home, _ := os.UserHomeDir()
	t.Setenv("SPECS_TEST_KEY_VAR", "sk-super-secret-value")
	writeConfig(t, home, `{"provider":{"embed":"openai"},"model":"m","base_url":"http://127.0.0.1:1","api_key_env":"SPECS_TEST_KEY_VAR"}`)
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"config"}, {"config", "--json"}, {"doctor"}} {
		_, out, errw := cli(t, append(args, "--root", root)...)
		if strings.Contains(out+errw, "sk-super-secret-value") {
			t.Errorf("%v leaked the key value", args)
		}
	}
	contains(t, mustOK(t, "config", "--root", root), "api key", "set")
}

func TestUsageErrorsExit2(t *testing.T) {
	root := newWorkspace(t)
	for _, args := range [][]string{
		{},
		{"nonsense"},
		{"search"},
		{"show"},
		{"trace"},
		{"add"},
		{"remove"},
		{"search", "x", "--bogus"},
		{"index", "--root", root, "extra"},
		{"config", "bogus"},
		{"coverage", "--root", root, "extra"},
		{"trace", "BRD-001", "--root", root, "--depth", "abc"},
		{"search", "x", "--root", root, "--k", "abc"},
		{"search", "x", "--root", root, "--mode", "bogus"},
		{"trace", "BRD-001", "--root", root, "--direction", "sideways"},
		{"trace", "BRD-001", "--root", root, "--kinds", "implements,bogus"},
		{"index", "--root", root, "--no-embed", "--reembed"},
		{"index", "--root", root, "--no-embed", "--infer"},
	} {
		if code, _, errw := cli(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, errw)
		}
	}
	indexedRoot := indexed(t)
	for _, args := range [][]string{
		{"search", "x", "--mode", "bogus"}, {"trace", "BRD-001", "--direction", "sideways"},
		{"trace", "BRD-001", "--kinds", "bogus"}, {"index", "--no-embed", "--reembed"},
	} {
		if code, _, _ := cli(t, append(args, "--root", indexedRoot)...); code != 2 {
			t.Errorf("%v on an index: exit %d, want 2", args, code)
		}
	}
	if code, _, errw := cli(t, "trace", "BRD-00", "--root", indexedRoot); code != 1 || !strings.Contains(errw, "BRD-001") {
		t.Errorf("unknown key: exit %d, stderr %q (want near matches)", code, errw)
	}
}

func TestIndexWithoutSourcesExplains(t *testing.T) {
	root := newWorkspace(t)
	code, _, errw := cli(t, "index", "--root", root)
	if code != 1 || !strings.Contains(errw, "add") || !strings.Contains(errw, "no sources") {
		t.Fatalf("exit %d, stderr %q", code, errw)
	}
	if _, err := os.Stat(os.Getenv("SPECS_INDEX_DB")); err == nil {
		t.Error("index without sources created the database")
	}
	if _, err := os.Stat(filepath.Join(root, ".Wammy")); err == nil {
		t.Error("index without sources created .Wammy")
	}
}

func TestSearchWithoutIndexExplains(t *testing.T) {
	root := newWorkspace(t)
	code, _, errw := cli(t, "search", "--root", root, "BRD-004")
	if code != 1 || !strings.Contains(errw, "index") {
		t.Fatalf("exit %d, stderr %q", code, errw)
	}
}

func TestVersionPrintsLdflagsValue(t *testing.T) {
	old := version
	version = "1.2.3"
	defer func() { version = old }()
	contains(t, mustOK(t, "version"), "1.2.3")
}

func TestConfigInitWritesOnceAndLoads(t *testing.T) {
	home := setHome(t)
	p := filepath.Join(home, ".config", "specs-index", "config.json")
	os.Remove(p)
	out := mustOK(t, "config", "init")
	contains(t, out, "~/.config/specs-index/config.json")
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "_comment") {
		t.Fatalf("config not written: %v %s", err, b)
	}
	// the example loads and is valid
	mustOK(t, "config")
	// a second init refuses and leaves the file alone
	if err := os.WriteFile(p, []byte(`{"provider":{"embed":"none"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errw := cli(t, "config", "init")
	if code != 1 || !strings.Contains(errw, "not overwriting") {
		t.Fatalf("exit %d stderr %q", code, errw)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"provider":{"embed":"none"}}` {
		t.Errorf("existing config was modified: %s", b)
	}
}

func TestCloudEmbedderWithoutAllowIsTextOnly(t *testing.T) {
	root := newWorkspace(t)
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"openai"},"model":"m","base_url":"https://embeddings.invalid"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	out := mustOK(t, "index", "--root", root)
	contains(t, out, "text-only", "allow-cloud")
	// search still works as full text, and doctor skips the endpoint
	contains(t, mustOK(t, "search", "--root", root, "BRD-004"), "BRD-004")
	contains(t, mustOK(t, "doctor", "--root", root), "skipped (cloud not allowed)")
	code, _, errw := cli(t, "search", "--root", root, "--mode", "vector", "upload")
	if code != 1 || !strings.Contains(errw, "allow") {
		t.Errorf("vector mode: exit %d stderr %q", code, errw)
	}
}

func TestUnreachableLocalEmbedderIsTextOnlyWarning(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(8, "fake")
	srv.Down.Store(true)
	defer srv.Close()
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":8,"base_url":"`+srv.URL+`"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	out := mustOK(t, "index", "--root", root)
	contains(t, out, "text-only", "unreachable")
	contains(t, mustOK(t, "search", "--root", root, "BRD-004"), "BRD-004")
	out = mustOK(t, "doctor", "--root", root)
	contains(t, out, "warn", "embedder")
}

func TestEmbeddingEndToEndWithFakeServer(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(32, "fake")
	defer srv.Close()
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":32,"base_url":"`+srv.URL+`"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"), filepath.Join(root, "features"))
	out := mustOK(t, "index", "--root", root, "--infer")
	contains(t, out, "embedded")
	contains(t, mustOK(t, "search", "--root", root, "--mode", "hybrid", "upload"), "requirement")
	contains(t, mustOK(t, "doctor", "--root", root), "chunk_vec")
	if code, _, errw := cli(t, "doctor", "--root", root); code != 0 {
		t.Errorf("doctor failed: %s", errw)
	}
	mustOK(t, "index", "--root", root, "--reembed")

	// a different model: auto degrades with a notice, vector/hybrid refuse
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"other","dims":32,"base_url":"`+srv.URL+`"}`)
	code, out, errw := cli(t, "search", "--root", root, "upload")
	if code != 0 || !strings.Contains(out, "requirement") || !strings.Contains(errw, "index --reembed") {
		t.Errorf("auto mismatch: exit %d stdout %q stderr %q", code, out, errw)
	}
	code, out, _ = cli(t, "search", "--root", root, "--json", "upload")
	var jr struct{ Notice string }
	if err := json.Unmarshal([]byte(out), &jr); code != 0 || err != nil || !strings.Contains(jr.Notice, "index --reembed") {
		t.Errorf("json notice: exit %d err %v %s", code, err, out)
	}
	for _, mode := range []string{"vector", "hybrid"} {
		code, out, errw = cli(t, "search", "--root", root, "--mode", mode, "BRD-004")
		if code != 1 || out != "" || !strings.Contains(errw, "index --reembed") {
			t.Errorf("%s BRD-004 mismatch: exit %d stdout %q stderr %q", mode, code, out, errw)
		}
		code, _, errw = cli(t, "search", "--root", root, "--mode", mode, "upload")
		if code != 1 || !strings.Contains(errw, "index --reembed") || !strings.Contains(errw, "--mode fts") {
			t.Errorf("%s mismatch: exit %d stderr %q", mode, code, errw)
		}
	}
	contains(t, mustOK(t, "search", "--root", root, "--mode", "fts", "upload"), "requirement")
}

func TestReembedRefusesWhenEmbedderDown(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(32, "fake")
	defer srv.Close()
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":32,"base_url":"`+srv.URL+`"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	mustOK(t, "index", "--root", root)
	vecRows := func() int {
		var st struct {
			EmbeddedChunks int `json:"embedded_chunks"`
		}
		json.Unmarshal([]byte(mustOK(t, "status", "--root", root, "--json")), &st)
		return st.EmbeddedChunks
	}
	before := vecRows()
	if before == 0 {
		t.Fatal("nothing embedded")
	}
	srv.Down.Store(true)
	code, _, errw := cli(t, "index", "--root", root, "--reembed")
	if code != 1 || !strings.Contains(errw, "reembed") {
		t.Errorf("exit %d stderr %q", code, errw)
	}
	if after := vecRows(); after != before {
		t.Errorf("vectors changed: %d -> %d", before, after)
	}
	if code, _, errw := cli(t, "doctor", "--root", root); code != 0 {
		t.Errorf("doctor after refused reembed: %s", errw)
	}
	// provider none and --reembed is also refused
	writeConfig(t, home, `{"provider":{"embed":"none"}}`)
	if code, _, _ := cli(t, "index", "--root", root, "--reembed"); code != 1 {
		t.Errorf("none + --reembed: exit %d", code)
	}
}

func TestSearchByWholeEntityKey(t *testing.T) {
	root := indexed(t)
	for _, key := range []string{
		"features/bulk_upload.feature#Resume an interrupted upload",
		"features/bulk_upload.feature",
		"BRD-004",
	} {
		var r struct {
			Hits []struct{ Key, Source string }
		}
		if err := json.Unmarshal([]byte(mustOK(t, "search", "--root", root, "--json", key)), &r); err != nil {
			t.Fatal(err)
		}
		if len(r.Hits) == 0 || r.Hits[0].Key != key || r.Hits[0].Source != "exact" {
			t.Errorf("%q: hits %+v", key, r.Hits)
		}
	}
}

func TestStatusAndConfigListEnvironmentVariables(t *testing.T) {
	root := indexed(t)
	t.Setenv("SPECS_INDEX_ALLOW_CLOUD", "false")
	for _, args := range [][]string{{"config"}, {"status"}} {
		out := mustOK(t, append(args, "--root", root)...)
		contains(t, out, "SPECS_INDEX_EMBED_PROVIDER", "SPECS_INDEX_ALLOW_CLOUD", "SPECS_INDEX_DB")
	}
	var r struct {
		Environment []struct {
			Name, Description, Value string
			Set                      bool
		}
	}
	if err := json.Unmarshal([]byte(mustOK(t, "config", "--root", root, "--json")), &r); err != nil || len(r.Environment) < 8 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestProviderNoneMakesNoRequests(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(8, "fake")
	defer srv.Close()
	// every provider base URL points at the server; provider none must never use it
	for _, v := range config.EnvVars() {
		if strings.HasSuffix(v.Name, "BASE_URL") {
			t.Setenv(v.Name, srv.URL)
		}
	}
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"), filepath.Join(root, "features"))
	for _, args := range [][]string{
		{"index"}, {"index", "--infer"}, {"search", "upload"}, {"search", "--mode", "fts", "upload"},
		{"show", "BRD-004"}, {"trace", "BRD-001"}, {"coverage"}, {"status"}, {"doctor"}, {"config"},
	} {
		cli(t, append(args, "--root", root)...)
	}
	if n := srv.Hits.Load(); n != 0 {
		t.Errorf("provider none made %d request(s)", n)
	}
}

func jsonKeys(t *testing.T, args ...string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustOK(t, args...)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func requireKeys(t *testing.T, what string, m map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s JSON lacks key %q", what, k)
		}
	}
}

func TestStatusAndConfigJSONKeys(t *testing.T) {
	root := indexed(t)
	requireKeys(t, "status", jsonKeys(t, "status", "--root", root, "--json"),
		"db", "sources", "documents", "failed_documents", "documents_by_type", "entities", "entities_by_type",
		"edges_explicit", "edges_inferred", "chunks", "embedded_chunks", "indexed_at", "embedder", "environment")
	requireKeys(t, "config", jsonKeys(t, "config", "--root", root, "--json"),
		"file", "file_exists", "provider", "model", "base_url", "allow_cloud", "dims", "api_key_env", "api_key_set",
		"region", "profile", "types", "environment")
	var emb map[string]json.RawMessage
	json.Unmarshal(jsonKeys(t, "status", "--root", root, "--json")["embedder"], &emb)
	requireKeys(t, "status.embedder", emb, "provider", "model", "base_url", "cloud", "cloud_allowed", "api_key_env",
		"api_key_set", "error", "index_fingerprint", "index_dims")
	requireKeys(t, "index", jsonKeys(t, "index", "--root", root, "--json"),
		"scanned", "parsed", "unchanged", "removed", "failed", "entities", "edges", "failures", "embedding")
	requireKeys(t, "doctor", jsonKeys(t, "doctor", "--root", root, "--json"), "ok", "checks")
}

func TestAutoSearchWithVectorsAndEmbedderDownDegrades(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(32, "fake")
	defer srv.Close()
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":32,"base_url":"`+srv.URL+`"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	mustOK(t, "index", "--root", root)
	srv.Down.Store(true)
	code, out, errw := cli(t, "search", "--root", root, "upload")
	if code != 0 || !strings.Contains(out, "requirement") || strings.TrimSpace(errw) == "" {
		t.Errorf("auto, embedder down: exit %d stdout %q stderr %q", code, out, errw)
	}
	code, _, errw = cli(t, "search", "--root", root, "--mode", "vector", "upload")
	if code != 1 || errw == "" {
		t.Errorf("vector, embedder down: exit %d stderr %q", code, errw)
	}
}

func TestStalledEmbedderIsBounded(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(32, "fake")
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":32,"base_url":"`+srv.URL+`"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	mustOK(t, "index", "--root", root)
	srv.Close()

	release := make(chan struct{})
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer stall.Close()
	defer close(release) // runs first: lets the handlers return so Close can finish
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":32,"base_url":"`+stall.URL+`"}`)
	old := api.QueryEmbedTimeout
	api.QueryEmbedTimeout = 300 * time.Millisecond
	defer func() { api.QueryEmbedTimeout = old }()

	start := time.Now()
	code, out, errw := cli(t, "search", "--root", root, "upload")
	if code != 0 || !strings.Contains(out, "requirement") || !strings.Contains(errw, "unresponsive") {
		t.Errorf("auto: exit %d stdout %q stderr %q", code, out, errw)
	}
	code, _, errw = cli(t, "search", "--root", root, "--mode", "vector", "upload")
	if code != 1 || !strings.Contains(errw, "unresponsive") {
		t.Errorf("vector: exit %d stderr %q", code, errw)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("searches took %s", d)
	}
}

func TestEmbedderNotRunningAtAllIsTextOnly(t *testing.T) {
	for _, cfg := range []string{
		`{"provider":{"embed":"ollama"},"model":"embeddinggemma","base_url":"http://127.0.0.1:1"}`,
		`{"provider":{"embed":"ollama"},"model":"embeddinggemma","dims":768,"base_url":"http://127.0.0.1:1"}`,
	} {
		root := newWorkspace(t)
		home, _ := os.UserHomeDir()
		writeConfig(t, home, cfg)
		mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
		out := mustOK(t, "index", "--root", root)
		contains(t, out, "text-only", "unreachable")
		contains(t, mustOK(t, "search", "--root", root, "BRD-004"), "BRD-004")
		contains(t, mustOK(t, "search", "--root", root, "resume"), "BRD-004")
		if code, _, errw := cli(t, "doctor", "--root", root); code != 0 {
			t.Errorf("doctor exit %d: %s", code, errw)
		}
	}
}

func TestShowScenarioAndStaleness(t *testing.T) {
	root := indexed(t)
	key := "features/bulk_upload.feature#Resume an interrupted upload"
	type showOut struct {
		Type  string `json:"type"`
		Stale bool   `json:"stale"`
		Steps []struct {
			Keyword string `json:"keyword"`
			Text    string `json:"text"`
			Line    int    `json:"line"`
		} `json:"steps"`
		Source string `json:"source"`
	}
	show := func() showOut {
		var s showOut
		if err := json.Unmarshal([]byte(mustOK(t, "show", "--root", root, "--json", key)), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := show()
	if s.Type != "scenario" || len(s.Steps) == 0 || s.Stale || !strings.Contains(s.Source, "Scenario:") {
		t.Fatalf("scenario show: %+v", s)
	}
	contains(t, mustOK(t, "show", "--root", root, key), "Given", "Scenario:")

	f := filepath.Join(root, "features", "bulk_upload.feature")
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString("\n# edited after indexing\n")
	fh.Close()

	if !show().Stale {
		t.Error("show: stale is false after the file changed")
	}
	var sr struct {
		Hits []struct {
			Key   string
			Stale bool
		}
	}
	if err := json.Unmarshal([]byte(mustOK(t, "search", "--root", root, "--json", "interrupted", "type:scenario")), &sr); err != nil {
		t.Fatal(err)
	}
	if len(sr.Hits) == 0 || !sr.Hits[0].Stale {
		t.Errorf("search: scenario hit not stale: %+v", sr)
	}
	contains(t, mustOK(t, "search", "--root", root, "interrupted", "type:scenario"), "stale")
}

func TestStatusAndConfigRedactURLCredentialsAndDBPath(t *testing.T) {
	root := indexed(t)
	dbAbs := os.Getenv("SPECS_INDEX_DB")
	t.Setenv("SPECS_INDEX_EMBED_BASE_URL", "http://user:s3cr3tpw@127.0.0.1:1")
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"config"}, {"config", "--json"}} {
		code, out, errw := cli(t, append(args, "--root", root)...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errw)
		}
		if strings.Contains(out+errw, "s3cr3tpw") {
			t.Errorf("%v printed the URL password:\n%s", args, out)
		}
		if strings.Contains(out+errw, dbAbs) {
			t.Errorf("%v printed the absolute SPECS_INDEX_DB value:\n%s", args, out)
		}
		contains(t, out, "127.0.0.1:1")
	}
}

func TestIndexAfterModelChangeReportsMismatchNotFailure(t *testing.T) {
	root := newWorkspace(t)
	srv := embedtest.New(32, "fake", "other")
	defer srv.Close()
	home, _ := os.UserHomeDir()
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"fake","dims":32,"base_url":"`+srv.URL+`"}`)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"), filepath.Join(root, "features"))
	mustOK(t, "index", "--root", root)
	writeConfig(t, home, `{"provider":{"embed":"ollama"},"model":"other","dims":32,"base_url":"`+srv.URL+`"}`)
	// a text change must still reach the index
	p := filepath.Join(root, "docs", "brd.md")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, []byte("\nmismatchmarkerword\n")...), 0o644)
	code, out, errw := cli(t, "index", "--root", root)
	if code != 0 || !strings.Contains(out, "1 parsed") || !strings.Contains(out, "index --reembed") {
		t.Errorf("index after model change: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errw)
	}
	var r api.IndexResult
	code, out, _ = cli(t, "index", "--root", root, "--json")
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil || r.Embedding.State != api.EmbedMismatch || !strings.Contains(r.Embedding.Note, "index --reembed") || r.Entities == 0 {
		t.Errorf("json: exit %d err %v %+v", code, err, r)
	}
	contains(t, mustOK(t, "search", "--root", root, "--mode", "fts", "mismatchmarkerword"), "BRD-")
	// explicit vector search still refuses
	if code, _, errw := cli(t, "search", "--root", root, "--mode", "vector", "upload"); code != 1 || !strings.Contains(errw, "index --reembed") {
		t.Errorf("vector after mismatch: exit %d %s", code, errw)
	}
}

func TestConfiguredTypesApplyToIndex(t *testing.T) {
	root := newWorkspace(t)
	home, _ := os.UserHomeDir()
	os.WriteFile(filepath.Join(root, "docs", "zz.md"), []byte("## ZZ-001 Custom prefix\n\n## QQ-001 Unmapped prefix\n"), 0o644)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	mustOK(t, "index", "--root", root)
	contains(t, mustOK(t, "show", "--root", root, "ZZ-001"), "requirement")
	writeConfig(t, home, `{"provider":{"embed":"none"},"types":{"ZZ":"design"}}`)
	out := mustOK(t, "index", "--root", root)
	contains(t, out, "4 parsed") // every Markdown file again: the mapping changed
	contains(t, mustOK(t, "show", "--root", root, "ZZ-001"), "design")
	contains(t, mustOK(t, "show", "--root", root, "QQ-001"), "requirement")
	contains(t, mustOK(t, "config", "--root", root), "ZZ=design", "BRD=requirement")
	var cr api.ConfigResult
	json.Unmarshal([]byte(mustOK(t, "config", "--root", root, "--json")), &cr)
	if cr.Types["ZZ"] != "design" || cr.Types["FDS"] != "design" {
		t.Errorf("config --json types = %+v", cr.Types)
	}
	writeConfig(t, home, `{"provider":{"embed":"none"},"types":{"ZZ":"bogus"}}`)
	if code, _, errw := cli(t, "index", "--root", root); code != 1 || !strings.Contains(errw, "types") {
		t.Errorf("bad type: exit %d %s", code, errw)
	}
}

func TestCoverageNoCandidatesAndProseIDs(t *testing.T) {
	root := newWorkspace(t)
	os.WriteFile(filepath.Join(root, "docs", "prose.md"), []byte("## BRD-900 Encoding\n\nText is UTF-8 and hashed with SHA-256; see FDS-998.\n"), 0o644)
	mustOK(t, "add", "--root", root, filepath.Join(root, "docs"))
	mustOK(t, "index", "--root", root)
	out := mustOK(t, "coverage", "--root", root)
	if strings.Contains(out, "UTF-8") || strings.Contains(out, "SHA-256") {
		t.Errorf("prose IDs reported as dangling:\n%s", out)
	}
	contains(t, out, "FDS-998")
	contains(t, mustOK(t, "coverage", "--root", root, "--no-candidates"), "note: candidate conflicts not computed")
	var r api.CoverageResult
	json.Unmarshal([]byte(mustOK(t, "coverage", "--root", root, "--no-candidates", "--json")), &r)
	if !strings.Contains(r.CandidatesNote, "skipped") {
		t.Errorf("json candidates_note = %q", r.CandidatesNote)
	}
}

func TestStatusListsSources(t *testing.T) {
	root := indexed(t)
	out := mustOK(t, "status", "--root", root)
	contains(t, out, "  docs  dir", "  features  dir")
	var r api.StatusResult
	json.Unmarshal([]byte(mustOK(t, "status", "--root", root, "--json")), &r)
	if r.Sources != 2 || len(r.SourcePaths) != 2 || r.SourcePaths[0].Path != "docs" {
		t.Errorf("status --json sources: %d %+v", r.Sources, r.SourcePaths)
	}
}

func TestKAndDepthBelowOneAreUsageErrors(t *testing.T) {
	root := indexed(t)
	for _, args := range [][]string{{"search", "--k", "0", "upload"}, {"search", "--k", "-3", "upload"}, {"trace", "--depth", "0", "BRD-001"}, {"trace", "--depth", "-1", "BRD-001"}} {
		if code, _, errw := cli(t, append(args, "--root", root)...); code != 2 {
			t.Errorf("%v: exit %d %s", args, code, errw)
		}
	}
}

func TestExcludeAppliesToExactHits(t *testing.T) {
	root := indexed(t)
	out := mustOK(t, "search", "--root", root, "--", `BRD-004 -"resumed"`)
	if strings.Contains(out, "BRD-004") {
		t.Errorf("excluded phrase still returns BRD-004:\n%s", out)
	}
	contains(t, mustOK(t, "search", "--root", root, "--", `BRD-004 -"nosuchphrase"`), "BRD-004")
}

func TestLowerCaseIDFindsEntity(t *testing.T) {
	root := indexed(t)
	contains(t, mustOK(t, "search", "--root", root, "brd-004"), "BRD-004  requirement")
}

func TestTracePrintsLinkLine(t *testing.T) {
	root := indexed(t)
	// BRD-003 is reached from BRD-002 through the Parent: line of BRD-003
	out := mustOK(t, "trace", "--root", root, "BRD-002", "--depth", "1", "--kinds", "parent_of")
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "BRD-003") {
			line = l
		}
	}
	if !strings.Contains(line, "docs/brd.md:20") {
		t.Errorf("BRD-003 line does not show the Parent: line docs/brd.md:20: %q\n%s", line, out)
	}
	var n api.TraceNode
	json.Unmarshal([]byte(mustOK(t, "trace", "--root", root, "BRD-002", "--depth", "1", "--kinds", "parent_of", "--json")), &n)
	for _, c := range n.Children {
		if c.Key == "BRD-003" && (c.LinkPath != "docs/brd.md" || c.LinkLine != 20 || c.Line != 18) {
			t.Errorf("BRD-003 node: %+v", c)
		}
	}
}

func TestRefusedAddCreatesNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	setHome(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Skip("git init failed")
	}
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	code, _, errw := cli(t, "add", "--root", root, filepath.Join(root, "docs"), t.TempDir())
	if code != 1 || !strings.Contains(errw, "outside") {
		t.Fatalf("exit %d %s", code, errw)
	}
	if _, err := os.Stat(filepath.Join(root, ".Wammy")); !os.IsNotExist(err) {
		t.Errorf(".Wammy created by a refused add (%v)", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude")); strings.Contains(string(b), ".Wammy") {
		t.Errorf("exclude written by a refused add")
	}
	if code, _, errw := cli(t, "add", "--root", root, filepath.Join(root, "missing")); code != 1 {
		t.Errorf("missing path: exit %d %s", code, errw)
	}
	if _, err := os.Stat(filepath.Join(root, ".Wammy")); !os.IsNotExist(err) {
		t.Errorf(".Wammy created by an add of a missing path")
	}
}
