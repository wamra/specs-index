package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seen struct {
	Path, Method, Auth string
	Body               map[string]any
}

// server records the last request and answers with status/body.
func server(t *testing.T, status int, body string, last *seen) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if last != nil {
			last.Path, last.Method, last.Auth = r.URL.Path, r.Method, r.Header.Get("Authorization")
			last.Body = nil
			json.NewDecoder(r.Body).Decode(&last.Body)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func vecJSON(n int) string {
	v := make([]string, n)
	for i := range v {
		v[i] = "0.5"
	}
	return "[" + strings.Join(v, ",") + "]"
}

func ollamaEmb(t *testing.T, url, model string) Embedder {
	t.Helper()
	e, err := NewEmbedder(EmbedConfig{Provider: Ollama, BaseURL: url, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func openaiEmb(t *testing.T, url string, c EmbedConfig) Embedder {
	t.Helper()
	c.Provider, c.BaseURL = OpenAI, url
	e, err := NewEmbedder(c)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestOllamaEmbedRequestShape(t *testing.T) {
	var got seen
	s := server(t, 200, `{"embeddings":[`+vecJSON(768)+`,`+vecJSON(768)+`]}`, &got)
	e := ollamaEmb(t, s.URL, "embeddinggemma")
	long := strings.Repeat("x", 3000)
	v, err := e.Embed(context.Background(), []string{"a", long}, DocPrefixOf(e))
	if err != nil || len(v) != 2 {
		t.Fatalf("%v %d", err, len(v))
	}
	if got.Path != "/api/embed" || got.Method != "POST" || got.Auth != "" {
		t.Fatalf("%+v", got)
	}
	in := got.Body["input"].([]any)
	if got.Body["model"] != "embeddinggemma" || got.Body["truncate"] != true || got.Body["keep_alive"] != "15m" ||
		in[0] != "title: none | text: a" || in[1] != GemmaDocPrefix+strings.Repeat("x", 2000) {
		t.Fatalf("%+v", got.Body)
	}
	if e.Dims() != 768 || e.Fingerprint() != "ollama|embeddinggemma|768|title: none | text: |v1" {
		t.Fatalf("%d %s", e.Dims(), e.Fingerprint())
	}
	s1 := server(t, 200, `{"embeddings":[`+vecJSON(768)+`]}`, &got)
	e = ollamaEmb(t, s1.URL, "embeddinggemma")
	if _, err := Query(context.Background(), e, "q"); err != nil || got.Body["input"].([]any)[0] != "task: search result | query: q" {
		t.Fatalf("query %v %v", err, got.Body)
	}
}

func TestOpenAIEmbedRequestShape(t *testing.T) {
	var got seen
	// data arrives out of order; index restores the input order
	s := server(t, 200, `{"data":[{"index":1,"embedding":[3,4]},{"index":0,"embedding":[1,2]}]}`, &got)
	e := openaiEmb(t, s.URL+"/v1/", EmbedConfig{Model: "text-embedding-3-small", Dims: 2})
	v, err := e.Embed(context.Background(), []string{"a", "b"}, "p: ")
	if err != nil || v[0][0] != 1 || v[1][0] != 3 {
		t.Fatalf("%v %v", err, v)
	}
	in := got.Body["input"].([]any)
	if got.Path != "/v1/embeddings" || got.Body["model"] != "text-embedding-3-small" || len(in) != 2 || in[0] != "p: a" {
		t.Fatalf("%+v", got)
	}
	if _, has := got.Body["truncate"]; has {
		t.Fatal("openai request must not carry ollama fields")
	}
}

func TestDimsAutoDetectedOnFirstEmbed(t *testing.T) {
	s := server(t, 200, `{"data":[{"index":0,"embedding":[1,2,3,4,5]}]}`, nil)
	e := openaiEmb(t, s.URL, EmbedConfig{Model: "some-unknown-model"})
	if e.Dims() != 0 {
		t.Fatal("dims known too early")
	}
	if _, err := e.Embed(context.Background(), []string{"a"}, ""); err != nil || e.Dims() != 5 {
		t.Fatalf("%v dims=%d", err, e.Dims())
	}
	if e.Fingerprint() != "openai|some-unknown-model|5||v1" {
		t.Fatal(e.Fingerprint())
	}
	// a later answer of another size is a systemic break, not a bisectable item
	s2 := server(t, 200, `{"data":[{"index":0,"embedding":[1,2]}]}`, nil)
	e.(*OpenAIEmbedder).BaseURL = s2.URL
	_, err := e.Embed(context.Background(), []string{"a"}, "")
	if !errors.Is(err, ErrBroken) || errors.Is(err, ErrMalformed) || !IsSystemic(err) {
		t.Fatalf("%v", err)
	}
}

func TestWrongDimsAgainstConfiguredIsBroken(t *testing.T) {
	s := server(t, 200, `{"embeddings":[[1,2,3]]}`, nil)
	_, err := ollamaEmb(t, s.URL, "embeddinggemma").Embed(context.Background(), []string{"a"}, "")
	if !errors.Is(err, ErrBroken) || !IsSystemic(err) {
		t.Fatalf("%v", err)
	}
}

func TestBearerOnlyWhenKeyEnvSetAndNonEmpty(t *testing.T) {
	var got seen
	s := server(t, 200, `{"data":[{"index":0,"embedding":[1]}]}`, &got)
	emb := func(env string) Embedder { return openaiEmb(t, s.URL, EmbedConfig{Model: "m", APIKeyEnv: env}) }
	ctx := context.Background()

	t.Setenv("SA_TEST_KEY", "sekret")
	if _, err := emb("SA_TEST_KEY").Embed(ctx, []string{"a"}, ""); err != nil || got.Auth != "Bearer sekret" {
		t.Fatalf("%v auth=%q", err, got.Auth)
	}
	t.Setenv("SA_TEST_EMPTY", "")
	for _, env := range []string{"", "SA_TEST_EMPTY", "SA_TEST_UNSET_VAR"} {
		got.Auth = "stale"
		if _, err := emb(env).Embed(ctx, []string{"a"}, ""); err != nil || got.Auth != "" {
			t.Fatalf("env %q: %v auth=%q", env, err, got.Auth)
		}
	}
	// summarizer too
	var gs seen
	ss := server(t, 200, `{"choices":[{"message":{"content":"x"}}]}`, &gs)
	sum, _ := NewSummarizer(SummarizeConfig{Provider: OpenAI, BaseURL: ss.URL, Model: "m", APIKeyEnv: "SA_TEST_KEY"})
	if _, err := sum.Summarize(ctx, "s", "u", 10); err != nil || gs.Auth != "Bearer sekret" {
		t.Fatalf("%v %q", err, gs.Auth)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := map[int]error{
		500: ErrDown, 502: ErrDown, 503: ErrDown, 404: ErrDown, 408: ErrDown, 429: ErrDown, 401: ErrAuth, 403: ErrAuth,
		400: ErrItem, 413: ErrItem, 422: ErrItem,
	}
	ctx := context.Background()
	for code, want := range cases {
		s := server(t, code, `{"error":"x"}`, nil)
		sums := map[string]func() Summarizer{
			Ollama: func() Summarizer { x, _ := NewSummarizer(SummarizeConfig{Provider: Ollama, BaseURL: s.URL}); return x },
			OpenAI: func() Summarizer {
				x, _ := NewSummarizer(SummarizeConfig{Provider: OpenAI, BaseURL: s.URL, Model: "m"})
				return x
			},
		}
		embs := map[string]Embedder{
			Ollama: ollamaEmb(t, s.URL, "embeddinggemma"),
			OpenAI: openaiEmb(t, s.URL, EmbedConfig{Model: "m"}),
		}
		for name, e := range embs {
			if _, err := e.Embed(ctx, []string{"a"}, ""); !errors.Is(err, want) {
				t.Errorf("%s embed %d: %v, want %v", name, code, err, want)
			}
		}
		for name, mk := range sums {
			_, err := mk().Summarize(ctx, "s", "u", 10)
			if !errors.Is(err, want) {
				t.Errorf("%s summarize %d: %v, want %v", name, code, err, want)
			}
			if want == ErrAuth && (AuthCode(err) != code || !IsSystemic(err) || errors.Is(err, ErrDown) || errors.Is(err, ErrItem)) {
				t.Errorf("%s summarize %d: auth code %d systemic=%v", name, code, AuthCode(err), IsSystemic(err))
			}
			var ie *ItemError
			if want == ErrItem && (!errors.As(err, &ie) || ie.Reason != fmt.Sprintf("http %d", code)) {
				t.Errorf("%s summarize %d: reason %v", name, code, err)
			}
		}
	}
}

func TestConnectionRefusedIsDown(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	ctx := context.Background()
	if _, err := ollamaEmb(t, url, "embeddinggemma").Embed(ctx, []string{"a"}, ""); !errors.Is(err, ErrDown) {
		t.Fatal(err)
	}
	sum, _ := NewSummarizer(SummarizeConfig{Provider: OpenAI, BaseURL: url, Model: "m"})
	if _, err := sum.Summarize(ctx, "s", "u", 1); !errors.Is(err, ErrDown) {
		t.Fatal(err)
	}
	if err := ollamaEmb(t, url, "embeddinggemma").Health(ctx); !errors.Is(err, ErrDown) {
		t.Fatal(err)
	}
}

func TestMalformedResponsesAreBrokenButBisectable(t *testing.T) {
	ctx := context.Background()
	for name, e := range map[string]Embedder{
		"ollama-json":  ollamaEmb(t, server(t, 200, `not json`, nil).URL, "embeddinggemma"),
		"ollama-count": ollamaEmb(t, server(t, 200, `{"embeddings":[]}`, nil).URL, "embeddinggemma"),
		"openai-json":  openaiEmb(t, server(t, 200, `{`, nil).URL, EmbedConfig{Model: "m"}),
		"openai-count": openaiEmb(t, server(t, 200, `{"data":[]}`, nil).URL, EmbedConfig{Model: "m"}),
		"openai-empty": openaiEmb(t, server(t, 200, `{"data":[{"embedding":[]}]}`, nil).URL, EmbedConfig{Model: "m"}),
	} {
		_, err := e.Embed(ctx, []string{"a"}, "")
		if !errors.Is(err, ErrBroken) || !errors.Is(err, ErrMalformed) || IsSystemic(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOllamaSummarizeRequestShape(t *testing.T) {
	var got seen
	s := server(t, 200, `{"message":{"content":"  hello \n"}}`, &got)
	sum, _ := NewSummarizer(SummarizeConfig{Provider: Ollama, BaseURL: s.URL})
	out, err := sum.Summarize(context.Background(), "SYS", "USER", 77)
	if err != nil || out != "hello" {
		t.Fatalf("%q %v", out, err)
	}
	opts := got.Body["options"].(map[string]any)
	msgs := got.Body["messages"].([]any)
	if got.Path != "/api/chat" || got.Body["model"] != "gemma4:e4b" || got.Body["stream"] != false || got.Body["think"] != false ||
		got.Body["keep_alive"] != "15m" || opts["num_predict"] != float64(77) || opts["temperature"] != 0.2 ||
		msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "USER" {
		t.Fatalf("%+v", got.Body)
	}
	if sum.Fingerprint() != "ollama:gemma4:e4b" {
		t.Fatal(sum.Fingerprint())
	}
}

func TestOpenAISummarizeRequestShape(t *testing.T) {
	var got seen
	s := server(t, 200, `{"choices":[{"message":{"content":"done"}}]}`, &got)
	temp := 0.5
	sum, _ := NewSummarizer(SummarizeConfig{Provider: OpenAI, BaseURL: s.URL + "/v1", Model: "qwen", Temperature: &temp})
	out, err := sum.Summarize(context.Background(), "SYS", "USER", 33)
	if err != nil || out != "done" {
		t.Fatalf("%q %v", out, err)
	}
	msgs := got.Body["messages"].([]any)
	if got.Path != "/v1/chat/completions" || got.Body["model"] != "qwen" || got.Body["max_tokens"] != float64(33) || got.Body["temperature"] != 0.5 ||
		len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "SYS" || msgs[1].(map[string]any)["role"] != "user" {
		t.Fatalf("%+v", got.Body)
	}
	if sum.Fingerprint() != "openai:qwen" {
		t.Fatal(sum.Fingerprint())
	}
	// no choices: item failure
	s2 := server(t, 200, `{"choices":[]}`, nil)
	bad, _ := NewSummarizer(SummarizeConfig{Provider: OpenAI, BaseURL: s2.URL, Model: "m"})
	if _, err := bad.Summarize(context.Background(), "s", "u", 1); !errors.Is(err, ErrItem) {
		t.Fatal(err)
	}
}

func TestHealth(t *testing.T) {
	ctx := context.Background()
	tags := server(t, 200, `{"models":[{"name":"embeddinggemma:latest","model":"embeddinggemma:latest"}]}`, nil)
	if err := ollamaEmb(t, tags.URL, "embeddinggemma").Health(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ollamaEmb(t, tags.URL, "other").Health(ctx); err == nil || errors.Is(err, ErrDown) {
		t.Fatalf("unlisted model: %v", err)
	}
	var got seen
	models := server(t, 200, `{"data":[{"id":"nomic-embed"}]}`, &got)
	t.Setenv("SA_TEST_KEY", "k")
	e := openaiEmb(t, models.URL, EmbedConfig{Model: "nomic-embed", APIKeyEnv: "SA_TEST_KEY"})
	if err := e.Health(ctx); err != nil || got.Path != "/v1/models" || got.Auth != "Bearer k" {
		t.Fatalf("%v %+v", err, got)
	}
	if err := openaiEmb(t, models.URL, EmbedConfig{Model: "absent"}).Health(ctx); err == nil {
		t.Fatal("absent model reported healthy")
	}
}

func TestKnownModelDefaults(t *testing.T) {
	for _, c := range []struct {
		model      string
		doc, query string
		dims       int
	}{
		{"embeddinggemma", "title: none | text: ", "task: search result | query: ", 768},
		{"embeddinggemma:latest", "title: none | text: ", "task: search result | query: ", 768},
		{"nomic-embed-text", "search_document: ", "search_query: ", 768},
		{"nomic-embed-text:v1.5", "search_document: ", "search_query: ", 768},
		{"all-minilm", "", "", 384},
		{"all-minilm:l6-v2", "", "", 384},
		{"bge-m3", "", "Represent this sentence for searching relevant passages: ", 0},
		{"BAAI/bge-small-en-v1.5", "", "Represent this sentence for searching relevant passages: ", 0},
		{"text-embedding-3-small", "", "", 1536},
	} {
		d, ok := KnownEmbedModel(c.model)
		if !ok || d.DocPrefix != c.doc || d.QueryPrefix != c.query || d.Dims != c.dims {
			t.Errorf("%s: %+v ok=%v", c.model, d, ok)
		}
	}
	if _, ok := KnownEmbedModel("mystery-model"); ok {
		t.Fatal("unknown model matched")
	}
}

func TestResolvedDefaultsAndOverrides(t *testing.T) {
	r := EmbedConfig{}.Resolved()
	if r.Provider != Ollama || r.BaseURL != DefaultOllamaURL || r.Model != "embeddinggemma" || r.Dims != 768 ||
		*r.DocPrefix != GemmaDocPrefix || *r.QueryPrefix != GemmaQueryPrefix {
		t.Fatalf("%+v", r)
	}
	r = EmbedConfig{Provider: OpenAI, Model: "text-embedding-3-small"}.Resolved()
	if r.BaseURL != DefaultOpenAIURL || r.Dims != 1536 || *r.DocPrefix != "" {
		t.Fatalf("%+v", r)
	}
	// explicit config wins over the table, including an explicit empty prefix
	r = EmbedConfig{Model: "embeddinggemma", Dims: 256, DocPrefix: Str(""), QueryPrefix: Str("q: ")}.Resolved()
	if r.Dims != 256 || *r.DocPrefix != "" || *r.QueryPrefix != "q: " {
		t.Fatalf("%+v", r)
	}
	if err := (EmbedConfig{Provider: OpenAI}.Resolved()).Validate(); err == nil {
		t.Fatal("openai without model must be invalid")
	}
	if err := (EmbedConfig{Provider: "bedrock"}.Resolved()).Validate(); err == nil {
		t.Fatal("unknown provider must be invalid")
	}
	s := SummarizeConfig{}.Resolved()
	if s.Model != "gemma4:e4b" || s.MaxTokens != 120 || *s.Temperature != 0.2 || !*s.Enabled || s.MaxItemsPerRun != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestFingerprintEquivalence(t *testing.T) {
	if NormalizeFingerprint(LegacyFingerprint) != "ollama|embeddinggemma|768|title: none | text: |v1" {
		t.Fatal(NormalizeFingerprint(LegacyFingerprint))
	}
	if NormalizeFingerprint("x") != "x" {
		t.Fatal("non-legacy fingerprints pass through")
	}
	if Fingerprint("openai", "m", 3, "d: ") != "openai|m|3|d: |v1" {
		t.Fatal(Fingerprint("openai", "m", 3, "d: "))
	}
}

func TestOpenAIHealthRestrictedKeyFallsBackToProbe(t *testing.T) {
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
	}))
	defer s.Close()
	if err := openaiEmb(t, s.URL, EmbedConfig{Model: "m"}).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[1] != "/v1/embeddings" {
		t.Fatalf("%v", paths)
	}
	// 401 stays a failure
	s401 := server(t, 401, ``, nil)
	if err := openaiEmb(t, s401.URL, EmbedConfig{Model: "m"}).Health(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatal(err)
	}
}
