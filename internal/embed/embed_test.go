package embed

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/corpus"
	"github.com/wamra/specs-index/internal/coverage"
	"github.com/wamra/specs-index/internal/embedtest"
	"github.com/wamra/specs-index/internal/indexer"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/vector"
)

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// workspace copies the corpus to a temp root, indexes it and returns both.
func workspace(t *testing.T) (string, *store.Store) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	copyDir(t, corpus.Dir(), root)
	dbDir, _ := filepath.EvalSymlinks(t.TempDir())
	st, err := store.Open(filepath.Join(dbDir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, d := range []string{"docs", "features"} {
		if err := indexer.AddSource(st, root, d); err != nil {
			t.Fatal(err)
		}
	}
	index(t, st, root)
	return root, st
}

func index(t *testing.T, st *store.Store, root string) {
	t.Helper()
	rep, err := indexer.Run(context.Background(), st, root, nil)
	if err != nil || rep.Failed != 0 {
		t.Fatalf("index: %v %+v", err, rep)
	}
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func edit(t *testing.T, path, old, repl string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, repl, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// localEmbedder returns an embedder for the fake server (a loopback URL, so
// it is local) with the default model embeddinggemma (768 dimensions).
func localEmbedder(t *testing.T, srv *embedtest.Server, model string) Embedder {
	t.Helper()
	e, err := New(config.Config{
		Provider: config.Providers{Embed: "ollama"}, Model: model, BaseURL: srv.URL, Dims: 768,
	}, false)
	if err != nil || e == nil {
		t.Fatalf("New: %v %v", e, err)
	}
	return e
}

func TestPendingEmbedsOnlyChanged(t *testing.T) {
	ctx := context.Background()
	root, st := workspace(t)
	srv := embedtest.New(768, "embeddinggemma")
	defer srv.Close()
	e := localEmbedder(t, srv, "embeddinggemma")
	vs := vector.NewVec1(st.DB())

	chunks := count(t, st, `SELECT count(*) FROM chunks`)
	n, err := Pending(ctx, st, e, vs)
	if err != nil || n != chunks {
		t.Fatalf("first Pending = %d, %v; want %d", n, err, chunks)
	}
	if got := srv.Texts.Load(); int(got) != chunks {
		t.Fatalf("server saw %d texts, want %d", got, chunks)
	}
	if got := count(t, st, `SELECT count(*) FROM chunk_vec`); got != chunks {
		t.Fatalf("chunk_vec has %d rows, want %d", got, chunks)
	}
	if fp, _ := st.Meta("embed_fingerprint"); fp != e.Fingerprint() || fp == "" {
		t.Fatalf("meta fingerprint = %q, want %q", fp, e.Fingerprint())
	}

	before := srv.Texts.Load()
	if n, err := Pending(ctx, st, e, vs); err != nil || n != 0 {
		t.Fatalf("second Pending = %d, %v; want 0", n, err)
	}
	if srv.Texts.Load() != before {
		t.Fatalf("second Pending sent %d texts, want 0", srv.Texts.Load()-before)
	}

	edit(t, filepath.Join(root, "docs", "brd.md"),
		"Users can upload a document into a folder from the browser.",
		"Users can upload a document into a folder from the browser or by email.")
	index(t, st, root)
	n, err = Pending(ctx, st, e, vs)
	if err != nil || n != 1 {
		t.Fatalf("Pending after one edit = %d, %v; want 1", n, err)
	}
	if got := srv.Texts.Load() - before; got != 1 {
		t.Fatalf("server saw %d texts after one edit, want 1", got)
	}
}

func TestDeletedEntityVectorRemoved(t *testing.T) {
	ctx := context.Background()
	root, st := workspace(t)
	srv := embedtest.New(768, "embeddinggemma")
	defer srv.Close()
	e := localEmbedder(t, srv, "embeddinggemma")
	vs := vector.NewVec1(st.DB())
	if _, err := Pending(ctx, st, e, vs); err != nil {
		t.Fatal(err)
	}
	var chunkID int64
	if err := st.DB().QueryRow(`SELECT c.id FROM chunks c JOIN entities x ON x.id = c.entity_id WHERE x.key = 'BRD-025'`).Scan(&chunkID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := vs.Get(ctx, chunkID); !ok {
		t.Fatal("BRD-025 has no vector before the deletion")
	}

	// remove the BRD-025 section (the last one in brd.md)
	p := filepath.Join(root, "docs", "brd.md")
	b, _ := os.ReadFile(p)
	i := strings.Index(string(b), "## BRD-025")
	if i < 0 {
		t.Fatal("BRD-025 heading not found")
	}
	if err := os.WriteFile(p, b[:i], 0o644); err != nil {
		t.Fatal(err)
	}
	index(t, st, root)
	if count(t, st, `SELECT count(*) FROM entities WHERE key = 'BRD-025'`) != 0 {
		t.Fatal("BRD-025 still indexed")
	}
	if _, ok, err := vs.Get(ctx, chunkID); err != nil || ok {
		t.Fatalf("vector of deleted entity remains: ok=%v err=%v", ok, err)
	}
	chunks := count(t, st, `SELECT count(*) FROM chunks`)
	if got := count(t, st, `SELECT count(*) FROM chunk_vec`); got != chunks {
		t.Fatalf("chunk_vec has %d rows for %d chunks", got, chunks)
	}

	// a whole file going away takes its vectors with it
	if err := os.Remove(filepath.Join(root, "docs", "acceptance.md")); err != nil {
		t.Fatal(err)
	}
	index(t, st, root)
	orphans := `SELECT count(*) FROM chunk_vec WHERE rowid NOT IN (SELECT id FROM chunks)`
	if got := count(t, st, orphans); got != 0 {
		t.Fatalf("after file removal %d vectors have no chunk", got)
	}
	// (AC-010 is also defined in fds.md, which takes the key over: that new
	// chunk has no vector yet, so there can be fewer vectors than chunks.)
	if got, chunks := count(t, st, `SELECT count(*) FROM chunk_vec`), count(t, st, `SELECT count(*) FROM chunks`); got > chunks {
		t.Fatalf("%d vectors for %d chunks", got, chunks)
	}
	// a changed entity loses its stale vector; Pending brings the new one
	vecs := count(t, st, `SELECT count(*) FROM chunk_vec`)
	edit(t, p, "Users can upload a document into a folder from the browser.", "Users can upload a document from the browser.")
	index(t, st, root)
	if got := count(t, st, `SELECT count(*) FROM chunk_vec`); got != vecs-1 {
		t.Fatalf("after a text change chunk_vec has %d rows, want %d", got, vecs-1)
	}
	if _, err := Pending(ctx, st, e, vs); err != nil {
		t.Fatal(err)
	}
	if got, chunks := count(t, st, `SELECT count(*) FROM chunk_vec`), count(t, st, `SELECT count(*) FROM chunks`); got != chunks {
		t.Fatalf("after Pending chunk_vec has %d rows for %d chunks", got, chunks)
	}
}

func TestMetadataRefreshedOnStatusChange(t *testing.T) {
	ctx := context.Background()
	root, st := workspace(t)
	srv := embedtest.New(768, "embeddinggemma")
	defer srv.Close()
	e := localEmbedder(t, srv, "embeddinggemma")
	vs := vector.NewVec1(st.DB())
	if _, err := Pending(ctx, st, e, vs); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.DB().QueryRow(`SELECT c.id FROM chunks c JOIN entities x ON x.id = c.entity_id WHERE x.key = 'BRD-004'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if en, _, _ := vs.Get(ctx, id); en.Status != "draft" || en.Type != "requirement" {
		t.Fatalf("BRD-004 metadata = %+v, want requirement/draft", en)
	}

	edit(t, filepath.Join(root, "docs", "brd.md"), "Status: draft    Priority: P2\nParent: BRD-003", "Status: approved    Priority: P2\nParent: BRD-003")
	index(t, st, root)
	before := srv.Texts.Load()
	n, err := Pending(ctx, st, e, vs)
	if err != nil || n != 0 {
		t.Fatalf("Pending after a status-only change = %d, %v; want 0 re-embeds", n, err)
	}
	if srv.Texts.Load() != before {
		t.Fatal("a status-only change re-embedded text")
	}
	en, ok, _ := vs.Get(ctx, id)
	if !ok || en.Status != "approved" {
		t.Fatalf("vector metadata not refreshed: %+v ok=%v", en, ok)
	}
	q, _, _ := vs.Get(ctx, id)
	ms, err := vs.KNN(ctx, q.Vec, 5, vector.Filter{Status: "approved", Type: "requirement"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ms {
		found = found || m.ID == id
	}
	if !found {
		t.Fatalf("KNN with status:approved does not return the refreshed chunk: %v", ms)
	}
}

func TestPendingRefusesFingerprintMismatch(t *testing.T) {
	ctx := context.Background()
	_, st := workspace(t)
	srv := embedtest.New(768, "embeddinggemma", "other-model")
	defer srv.Close()
	a := localEmbedder(t, srv, "embeddinggemma")
	vs := vector.NewVec1(st.DB())
	if _, err := Pending(ctx, st, a, vs); err != nil {
		t.Fatal(err)
	}
	b := localEmbedder(t, srv, "other-model")
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("different models share a fingerprint")
	}
	before := srv.Texts.Load()
	_, err := Pending(ctx, st, b, vs)
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Pending with another model = %v, want ErrFingerprintMismatch", err)
	}
	if !strings.Contains(err.Error(), "index --reembed") {
		t.Errorf("error does not name `index --reembed`: %v", err)
	}
	if srv.Texts.Load() != before {
		t.Error("a refused Pending still sent texts")
	}
	if err := CheckFingerprint(ctx, st, b); !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("CheckFingerprint = %v", err)
	}
	if err := CheckFingerprint(ctx, st, a); err != nil {
		t.Errorf("CheckFingerprint with the pinned embedder = %v", err)
	}

	// --reembed: clear and start over with the new model
	if err := Reembed(ctx, st, vs); err != nil {
		t.Fatal(err)
	}
	chunks := count(t, st, `SELECT count(*) FROM chunks`)
	if n, err := Pending(ctx, st, b, vs); err != nil || n != chunks {
		t.Fatalf("Pending after Reembed = %d, %v; want %d", n, err, chunks)
	}
	if fp, _ := st.Meta("embed_fingerprint"); fp != b.Fingerprint() {
		t.Errorf("fingerprint after reembed = %q", fp)
	}
}

func TestDetectsDimensionsOfUnknownModel(t *testing.T) {
	ctx := context.Background()
	_, st := workspace(t)
	srv := embedtest.New(32, "mystery")
	defer srv.Close()
	e, err := New(config.Config{Provider: config.Providers{Embed: "ollama"}, Model: "mystery", BaseURL: srv.URL}, false)
	if err != nil {
		t.Fatal(err)
	}
	vs := vector.NewVec1(st.DB())
	if _, err := Pending(ctx, st, e, vs); err != nil {
		t.Fatal(err)
	}
	fp, _ := st.Meta("embed_fingerprint")
	if !strings.Contains(fp, "|32|") {
		t.Fatalf("fingerprint %q lacks the detected dimension", fp)
	}
	if err := CheckFingerprint(ctx, st, e); err != nil {
		t.Fatal(err)
	}
}

// redirect sends every request to the fake server, whatever host it names, so
// a "remote" host can be tested without a network.
func redirect(srv *embedtest.Server) func(*http.Client) {
	addr := srv.Listener.Addr().String()
	return func(c *http.Client) {
		c.Transport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
	}
}

func TestCloudRefusedWithoutAllow(t *testing.T) {
	srv := embedtest.New(768, "embeddinggemma")
	defer srv.Close()
	clientHook = redirect(srv)
	defer func() { clientHook = nil }()
	cases := []config.Config{
		{Provider: config.Providers{Embed: "bedrock"}, Model: "amazon.titan-embed-text-v2:0", Region: "us-east-1"},
		{Provider: config.Providers{Embed: "openai"}, Model: "text-embedding-3-small"},
		{Provider: config.Providers{Embed: "openai"}, Model: "m", BaseURL: "https://embeddings.example.test"},
		{Provider: config.Providers{Embed: "openai"}, Model: "m", BaseURL: "http://192.168.1.20:1234"},
		{Provider: config.Providers{Embed: "ollama"}, Model: "embeddinggemma", BaseURL: "http://ollama.example.test:11434"},
		{Provider: config.Providers{Embed: "ollama"}, Model: "embeddinggemma", BaseURL: "http://localhost.example.test:11434"},
	}
	for _, c := range cases {
		e, err := New(c, false)
		if !errors.Is(err, ErrCloudNotAllowed) || e != nil {
			t.Errorf("New(%s %s) = %v, %v; want ErrCloudNotAllowed", c.Provider.Embed, c.BaseURL, e, err)
		}
	}
	if n := srv.Hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the server although cloud was refused", n)
	}
}

func TestRefusalDoesNotLeakCredentials(t *testing.T) {
	_, err := New(config.Config{Provider: config.Providers{Embed: "openai"}, Model: "m", BaseURL: "https://user:s3cr3t@embeddings.example.test/v1"}, false)
	if !errors.Is(err, ErrCloudNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("refusal leaks the URL password: %v", err)
	}
	if !strings.Contains(err.Error(), "embeddings.example.test") {
		t.Errorf("refusal does not name the host: %v", err)
	}
}

// TestRedirectCannotLeaveLoopback: a loopback endpoint that answers 307 to a
// remote host must not get the document text re-POSTed there unless cloud is
// allowed.
func TestRedirectCannotLeaveLoopback(t *testing.T) {
	remote := embedtest.New(768, "embeddinggemma")
	defer remote.Close()
	local := http.NewServeMux()
	local.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://remote.example.test"+r.URL.Path, http.StatusTemporaryRedirect)
	})
	front := httptest.NewServer(local)
	defer front.Close()
	frontAddr, remoteAddr := front.Listener.Addr().String(), remote.Listener.Addr().String()
	clientHook = func(c *http.Client) {
		c.Transport = &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target := frontAddr
			if strings.HasPrefix(addr, "remote.example.test") {
				target = remoteAddr
			}
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}}
	}
	defer func() { clientHook = nil }()
	cfg := config.Config{Provider: config.Providers{Embed: "ollama"}, Model: "embeddinggemma", BaseURL: front.URL, Dims: 768}

	e, err := New(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), []string{"secret text"}); err == nil {
		t.Fatal("a redirect to a remote host was followed")
	}
	if n := remote.Hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the redirect target", n)
	}

	// control: with cloud allowed the default policy follows the redirect
	e, err = New(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), []string{"secret text"}); err != nil {
		t.Fatal(err)
	}
	if remote.Texts.Load() != 1 {
		t.Fatalf("control: remote saw %d texts, want 1", remote.Texts.Load())
	}
}

func TestCloudAllowed(t *testing.T) {
	srv := embedtest.New(1536, "m")
	defer srv.Close()
	clientHook = redirect(srv)
	defer func() { clientHook = nil }()
	for name, c := range map[string]struct {
		cfg   config.Config
		allow bool
	}{
		"flag":   {config.Config{Provider: config.Providers{Embed: "openai"}, Model: "text-embedding-3-small", BaseURL: "http://embeddings.example.test"}, true},
		"config": {config.Config{Provider: config.Providers{Embed: "openai"}, Model: "text-embedding-3-small", BaseURL: "http://embeddings.example.test", AllowCloud: true}, false},
	} {
		before := srv.Texts.Load()
		e, err := New(c.cfg, c.allow)
		if err != nil || e == nil {
			t.Fatalf("%s: New = %v, %v", name, e, err)
		}
		if !e.IsCloud() {
			t.Errorf("%s: IsCloud = false for a remote host", name)
		}
		vs, err := e.Embed(context.Background(), []string{"hello"})
		if err != nil || len(vs) != 1 || len(vs[0]) != 1536 {
			t.Fatalf("%s: Embed = %v, %v", name, len(vs), err)
		}
		if srv.Texts.Load()-before != 1 {
			t.Errorf("%s: server saw %d texts, want 1", name, srv.Texts.Load()-before)
		}
	}
}

func TestLocalHostsAreNotCloud(t *testing.T) {
	for _, base := range []string{
		"", "http://localhost:11434", "http://LOCALHOST:1234/v1", "http://127.0.0.1:1234", "http://127.5.6.7",
		"http://[::1]:11434", "localhost:11434",
	} {
		for _, p := range []string{"ollama", "openai"} {
			e, err := New(config.Config{Provider: config.Providers{Embed: p}, Model: "m", BaseURL: base, Dims: 8}, false)
			if p == "openai" && base == "" {
				if !errors.Is(err, ErrCloudNotAllowed) { // the default OpenAI URL is remote
					t.Errorf("openai with the default URL = %v, want ErrCloudNotAllowed", err)
				}
				continue
			}
			if err != nil || e == nil || e.IsCloud() {
				t.Errorf("%s %q: New = %v, %v (cloud=%v)", p, base, e, err, e != nil && e.IsCloud())
			}
		}
	}
}

func TestNoneProviderGivesNilEmbedder(t *testing.T) {
	for _, p := range []string{"none", ""} {
		e, err := New(config.Config{Provider: config.Providers{Embed: p}}, false)
		if e != nil || err != nil {
			t.Errorf("provider %q: New = %v, %v; want nil, nil", p, e, err)
		}
	}
}

// writeExtra adds a requirement, a design and a scenario about the same topic.
func writeExtra(t *testing.T, root string) {
	t.Helper()
	w := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("docs/extra_brd.md", "---\ntype: brd\nversion: 0.1\n---\n# Extra requirements\n\n## BRD-100 Quantum widget calibration\nStatus: draft    Priority: P2\n\nCalibrate the quantum widget regularly.\n")
	w("docs/extra_fds.md", "---\ntype: fds\nversion: 0.1\n---\n# Extra design\n\n## FDS-100 Quantum widget calibration\nCalibrate the quantum widget regularly.\n")
	w("features/extra.feature", "Feature: Quantum widget calibration\n\n  Scenario: Quantum widget calibration\n    When calibrate the quantum widget regularly\n")
}

func TestInferStoresSuggestionsOnly(t *testing.T) {
	ctx := context.Background()
	root, st := workspace(t)
	writeExtra(t, root)
	index(t, st, root)
	srv := embedtest.New(768, "embeddinggemma")
	defer srv.Close()
	e := localEmbedder(t, srv, "embeddinggemma")
	vs := vector.NewVec1(st.DB())
	if _, err := Pending(ctx, st, e, vs); err != nil {
		t.Fatal(err)
	}
	explicit := count(t, st, `SELECT count(*) FROM edges WHERE explicit = 1`)
	before, err := coverage.Run(ctx, st, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}

	n, err := Infer(ctx, st, vs)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("Infer found no suggestions for the BRD-100 / FDS-100 / scenario trio")
	}
	if got := count(t, st, `SELECT count(*) FROM edges WHERE explicit = 0`); got != n {
		t.Fatalf("%d inferred rows, Infer reported %d", got, n)
	}
	if got := count(t, st, `SELECT count(*) FROM edges WHERE explicit = 1`); got != explicit {
		t.Fatalf("explicit edges changed: %d -> %d", explicit, got)
	}
	if got := count(t, st, `SELECT count(*) FROM edges WHERE explicit = 0 AND (origin <> 'inferred' OR confidence IS NULL OR confidence <= 0.75 OR confidence > 1)`); got != 0 {
		t.Fatalf("%d inferred edges with a wrong origin or confidence", got)
	}
	var c float64
	if err := st.DB().QueryRow(`SELECT confidence FROM edges WHERE explicit = 0 AND kind = 'implements' AND src_key = 'FDS-100' AND dst_key = 'BRD-100'`).Scan(&c); err != nil {
		t.Fatalf("no inferred FDS-100 implements BRD-100 edge: %v", err)
	}
	if got := count(t, st, `SELECT count(*) FROM edges WHERE explicit = 0 AND kind = 'verified_by' AND src_key = 'FDS-100' AND dst_key LIKE 'features/extra.feature#%'`); got != 1 {
		t.Fatalf("no inferred FDS-100 verified_by scenario edge (%d)", got)
	}
	// never suggest for something that already has an explicit edge
	if got := count(t, st, `SELECT count(*) FROM edges i WHERE i.explicit = 0 AND i.kind = 'implements' AND EXISTS
		(SELECT 1 FROM edges x WHERE x.explicit = 1 AND x.kind = 'implements' AND x.dst_key = i.dst_key)`); got != 0 {
		t.Fatalf("%d suggestions for requirements that already have an explicit implements edge", got)
	}

	after, err := coverage.Run(ctx, st, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	before.Candidates, after.Candidates = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("coverage changed after Infer:\nbefore %+v\nafter  %+v", before, after)
	}
	listed := false
	for _, it := range after.RequirementsWithoutDesign {
		listed = listed || it.Key == "BRD-100"
	}
	if !listed {
		t.Fatal("BRD-100 left RequirementsWithoutDesign because of an inferred edge")
	}

	// A second run clears and recomputes: same count, no duplicates.
	n2, err := Infer(ctx, st, vs)
	if err != nil || n2 != n {
		t.Fatalf("second Infer = %d, %v; want %d", n2, err, n)
	}
	if got := count(t, st, `SELECT count(*) FROM edges WHERE explicit = 0`); got != n {
		t.Fatalf("%d inferred rows after the second run, want %d", got, n)
	}
}

func TestRefusalNamesWhereTheFlagExists(t *testing.T) {
	_, err := New(config.Config{Provider: config.Providers{Embed: "openai"}, Model: "m", BaseURL: "https://embeddings.example.test"}, false)
	if err == nil || !strings.Contains(err.Error(), "--allow-cloud to index or doctor") {
		t.Fatalf("refusal = %v; want it to say --allow-cloud applies to index or doctor", err)
	}
}
