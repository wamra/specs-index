package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

type awsLoadOptions = awsconfig.LoadOptions

func init() {
	// No test waits for the production backoff (1s/4s/16s).
	retryDelays = []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
}

func anthropicSum(t *testing.T, url string) Summarizer {
	t.Helper()
	s, err := NewSummarizer(SummarizeConfig{Provider: Anthropic, BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAnthropicRequestShapeAndResponse(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	var hdr http.Header
	var body map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr, path = r.Header.Clone(), r.URL.Path
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":"msg_1","type":"message","content":[{"type":"text","text":"  the summary \n"}]}`)
	}))
	defer srv.Close()
	got, err := anthropicSum(t, srv.URL).Summarize(context.Background(), "be brief", "the input", 77)
	if err != nil || got != "the summary" {
		t.Fatalf("got %q err %v", got, err)
	}
	if path != "/v1/messages" || hdr.Get("x-api-key") != "sk-test" || hdr.Get("anthropic-version") != "2023-06-01" || hdr.Get("Authorization") != "" {
		t.Fatalf("path %q headers %v", path, hdr)
	}
	if body["model"] != "claude-haiku-4-5" || body["max_tokens"] != float64(77) || body["temperature"] != 0.2 || body["system"] != "be brief" {
		t.Fatalf("body %v", body)
	}
	msgs := body["messages"].([]any)
	m := msgs[0].(map[string]any)
	if len(msgs) != 1 || m["role"] != "user" || m["content"] != "the input" {
		t.Fatalf("messages %v", msgs)
	}
	if s := anthropicSum(t, srv.URL); s.Fingerprint() != "anthropic:claude-haiku-4-5" {
		t.Fatal(s.Fingerprint())
	}
}

func TestAnthropicBaseURLWithV1Suffix(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"content":[{"type":"text","text":"x"}]}`)
	}))
	defer srv.Close()
	if _, err := anthropicSum(t, srv.URL+"/v1/").Summarize(context.Background(), "s", "u", 1); err != nil || path != "/v1/messages" {
		t.Fatalf("%q %v", path, err)
	}
}

func TestAnthropicRetriesOn429HonoringRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var stamps []time.Time
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		if calls.Add(1) <= 2 {
			w.Header().Set("retry-after", "0.05")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error"}}`)
			return
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	got, err := anthropicSum(t, srv.URL).Summarize(context.Background(), "s", "u", 1)
	if err != nil || got != "ok" || calls.Load() != 3 {
		t.Fatalf("got %q err %v calls %d", got, err, calls.Load())
	}
	for i := 1; i < len(stamps); i++ {
		if d := stamps[i].Sub(stamps[i-1]); d < 45*time.Millisecond {
			t.Fatalf("retry %d came after %v, retry-after was 50ms", i, d)
		}
	}
}

func TestAnthropic429ExhaustedIsDownAfterFourRequests(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
	}))
	defer srv.Close()
	_, err := anthropicSum(t, srv.URL).Summarize(context.Background(), "s", "u", 1)
	if !errors.Is(err, ErrDown) || calls.Load() != 4 {
		t.Fatalf("err %v calls %d", err, calls.Load())
	}
}

func TestAnthropicErrorMapping(t *testing.T) {
	for _, c := range []struct {
		status  int
		want    error
		retries bool
	}{
		{401, ErrAuth, false}, {403, ErrAuth, false}, {400, ErrItem, false}, {413, ErrItem, false},
		{500, ErrDown, false}, {529, ErrDown, false}, {503, ErrDown, false}, {429, ErrDown, true},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(c.status)
			fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"x"}}`)
		}))
		_, err := anthropicSum(t, srv.URL).Summarize(context.Background(), "s", "u", 1)
		srv.Close()
		if !errors.Is(err, c.want) {
			t.Errorf("HTTP %d: %v, want %v", c.status, err, c.want)
		}
		if c.want == ErrAuth && (AuthCode(err) != c.status || !IsSystemic(err)) {
			t.Errorf("HTTP %d: auth code %d systemic %v", c.status, AuthCode(err), IsSystemic(err))
		}
		if !c.retries && calls.Load() != 1 {
			t.Errorf("HTTP %d was retried (%d calls)", c.status, calls.Load())
		}
	}
}

func TestAnthropicBadResponseIsItemError(t *testing.T) {
	srv := server(t, 200, `{"content":[]}`, nil)
	if _, err := anthropicSum(t, srv.URL).Summarize(context.Background(), "s", "u", 1); !errors.Is(err, ErrItem) {
		t.Fatal(err)
	}
}

func TestAnthropicHealth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "k")
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()
	if err := anthropicSum(t, srv.URL).Health(context.Background()); err != nil || hdr.Get("x-api-key") != "k" {
		t.Fatalf("%v %v", err, hdr)
	}
	bad := server(t, 401, `{}`, nil)
	if err := anthropicSum(t, bad.URL).Health(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatal(err)
	}
}

func TestOpenAIRetries429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		fmt.Fprintf(w, `{"data":[{"index":0,"embedding":%s}]}`, vecJSON(3))
	}))
	defer srv.Close()
	e := openaiEmb(t, srv.URL, EmbedConfig{Model: "m", Dims: 3})
	if _, err := e.Embed(context.Background(), []string{"a"}, ""); err != nil || calls.Load() != 2 {
		t.Fatalf("%v calls %d", err, calls.Load())
	}
}

func TestOllama429IsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(429) }))
	defer srv.Close()
	if _, err := ollamaEmb(t, srv.URL, "embeddinggemma").Embed(context.Background(), []string{"a"}, ""); !errors.Is(err, ErrDown) || calls.Load() != 1 {
		t.Fatalf("%v calls %d", err, calls.Load())
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for h, want := range map[string]time.Duration{
		"": 0, "3": 3 * time.Second, "0.5": 500 * time.Millisecond, "garbage": 0, "-4": 0,
		now.Add(10 * time.Second).Format(http.TimeFormat): 10 * time.Second,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	} {
		if got := parseRetryAfter(h, now); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Hour}
	defer func() { retryDelays = old }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := retryThrottled(ctx, func() (bool, time.Duration, error) { return true, 0, ErrDown })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// ---- config ----

func TestEmbedAnthropicIsConfigErrorWithHint(t *testing.T) {
	_, err := NewEmbedder(EmbedConfig{Provider: Anthropic, Model: "x"})
	if err == nil || !strings.Contains(err.Error(), "no embeddings endpoint") ||
		!strings.Contains(err.Error(), "https://api.voyageai.com") || !strings.Contains(err.Error(), `"openai"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicSummarizeDefaults(t *testing.T) {
	r := SummarizeConfig{Provider: Anthropic}.Resolved()
	if r.BaseURL != "https://api.anthropic.com" || r.Model != "claude-haiku-4-5" || r.APIKeyEnv != "ANTHROPIC_API_KEY" || r.MaxInputChars != 8000 {
		t.Fatalf("%+v", r)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SummarizeConfig{Provider: Bedrock}.Resolved()).Validate(); err == nil {
		t.Fatal("bedrock without a model must be rejected")
	}
	bad := SummarizeConfig{Provider: Ollama, Kinds: []string{"tool_output", "bogus"}}.Resolved()
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("kinds: %v", err)
	}
}

func TestVoyageKnownModels(t *testing.T) {
	for m, dims := range map[string]int{"voyage-3": 1024, "voyage-3-lite": 512, "VOYAGE-3": 1024} {
		d, ok := KnownEmbedModel(m)
		if !ok || d.Dims != dims || d.DocPrefix != "" || d.QueryPrefix != "" {
			t.Errorf("%s: %+v %v", m, d, ok)
		}
	}
	e := openaiEmb(t, "https://api.voyageai.com", EmbedConfig{Model: "voyage-3-lite"})
	if e.Dims() != 512 || e.Fingerprint() != "openai|voyage-3-lite|512||v1" {
		t.Fatalf("%d %s", e.Dims(), e.Fingerprint())
	}
}

func TestHostedAndDailyCap(t *testing.T) {
	for _, c := range []struct {
		p, base string
		hosted  bool
	}{
		{Ollama, "http://localhost:11434", false}, {Ollama, "https://example.com", false},
		{OpenAI, "http://localhost:1234", false}, {OpenAI, "http://127.0.0.1:1234/v1", false}, {OpenAI, "http://192.168.1.5:8000", false},
		{OpenAI, "http://workstation.local:1234", false}, {OpenAI, "https://api.openai.com", true}, {OpenAI, "https://api.voyageai.com", true},
		{Anthropic, "https://api.anthropic.com", true}, {Bedrock, "", true},
	} {
		if Hosted(c.p, c.base) != c.hosted {
			t.Errorf("Hosted(%s, %s) = %v", c.p, c.base, !c.hosted)
		}
	}
	for _, c := range []struct {
		set    int
		hosted bool
		want   int
	}{{0, false, 0}, {0, true, 2000}, {50, true, 50}, {50, false, 50}, {-1, true, 0}} {
		if got, _ := DailyCap(c.set, c.hosted); got != c.want {
			t.Errorf("DailyCap(%d,%v) = %d, want %d", c.set, c.hosted, got, c.want)
		}
	}
	if n, src := (SummarizeConfig{Provider: Anthropic}.Resolved()).DailyCap(); n != 2000 || src != "hosted default" {
		t.Fatalf("%d %s", n, src)
	}
	if n, _ := (SummarizeConfig{}.Resolved()).DailyCap(); n != 0 {
		t.Fatal("local default must be unlimited")
	}
	if n, _ := (EmbedConfig{Provider: OpenAI, BaseURL: "https://api.voyageai.com", Model: "voyage-3"}.Resolved()).DailyCap(); n != 2000 {
		t.Fatal(n)
	}
}

func TestBedrockEmbedConfigValidation(t *testing.T) {
	for _, c := range []struct {
		cfg EmbedConfig
		ok  bool
	}{
		{EmbedConfig{Provider: Bedrock, Model: "amazon.titan-embed-text-v2:0"}, true},
		{EmbedConfig{Provider: Bedrock, Model: "amazon.titan-embed-text-v2:0", Dims: 256}, true},
		{EmbedConfig{Provider: Bedrock, Model: "amazon.titan-embed-text-v2:0", Dims: 300}, false},
		{EmbedConfig{Provider: Bedrock, Model: "cohere.embed-english-v3"}, true},
		{EmbedConfig{Provider: Bedrock, Model: "cohere.embed-multilingual-v3", Dims: 512}, false},
		{EmbedConfig{Provider: Bedrock, Model: "amazon.titan-embed-text-v1"}, false},
		{EmbedConfig{Provider: Bedrock}, false},
	} {
		err := c.cfg.Resolved().Validate()
		if (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.cfg, err)
		}
	}
}

// ---- bedrock ----

type fakeAPI struct {
	mu        sync.Mutex
	invokes   []*bedrockruntime.InvokeModelInput
	converses []*bedrockruntime.ConverseInput
	invoke    func(call int, in *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error)
	converse  func(call int, in *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error)
}

func (f *fakeAPI) InvokeModel(ctx context.Context, in *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	f.mu.Lock()
	f.invokes = append(f.invokes, in)
	n := len(f.invokes)
	f.mu.Unlock()
	return f.invoke(n, in)
}

func (f *fakeAPI) Converse(ctx context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.mu.Lock()
	f.converses = append(f.converses, in)
	n := len(f.converses)
	f.mu.Unlock()
	return f.converse(n, in)
}

type apiErr struct{ code, msg string }

func (e *apiErr) Error() string                 { return e.code + ": " + e.msg }
func (e *apiErr) ErrorCode() string             { return e.code }
func (e *apiErr) ErrorMessage() string          { return e.msg }
func (e *apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func bedrockEmb(t *testing.T, api BedrockAPI, c EmbedConfig) *BedrockEmbedder {
	t.Helper()
	c.Provider = Bedrock
	e, err := NewEmbedder(c)
	if err != nil {
		t.Fatal(err)
	}
	be := e.(*BedrockEmbedder)
	be.conn.api = api
	return be
}

func bedrockSum(t *testing.T, api BedrockAPI, model string) *BedrockSummarizer {
	t.Helper()
	s, err := NewSummarizer(SummarizeConfig{Provider: Bedrock, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	bs := s.(*BedrockSummarizer)
	bs.conn.api = api
	return bs
}

func TestBedrockTitanRequestAndFingerprint(t *testing.T) {
	api := &fakeAPI{invoke: func(n int, in *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		return &bedrockruntime.InvokeModelOutput{Body: []byte(fmt.Sprintf(`{"embedding":%s,"inputTextTokenCount":3}`, vecJSON(256)))}, nil
	}}
	e := bedrockEmb(t, api, EmbedConfig{Model: "amazon.titan-embed-text-v2:0", Dims: 256})
	vecs, err := e.Embed(context.Background(), []string{"first", "second"}, DocPrefixOf(e))
	if err != nil || len(vecs) != 2 || len(vecs[0]) != 256 {
		t.Fatalf("%v %d", err, len(vecs))
	}
	if len(api.invokes) != 2 {
		t.Fatalf("titan takes one text per request, got %d requests", len(api.invokes))
	}
	in := api.invokes[1]
	var body map[string]any
	json.Unmarshal(in.Body, &body)
	if *in.ModelId != "amazon.titan-embed-text-v2:0" || *in.ContentType != "application/json" ||
		body["inputText"] != "second" || body["dimensions"] != float64(256) || body["normalize"] != true || len(body) != 3 {
		t.Fatalf("model %s body %v", *in.ModelId, body)
	}
	if got, want := e.Fingerprint(), "bedrock|amazon.titan-embed-text-v2:0|256||v1"; got != want {
		t.Fatalf("fingerprint %q, want %q", got, want)
	}
	// default size is Titan's 1024
	if d := bedrockEmb(t, api, EmbedConfig{Model: "amazon.titan-embed-text-v2:0"}).Dims(); d != 1024 {
		t.Fatalf("default dims %d", d)
	}
}

func TestBedrockTitanWrongDimsIsBroken(t *testing.T) {
	api := &fakeAPI{invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":` + vecJSON(10) + `}`)}, nil
	}}
	e := bedrockEmb(t, api, EmbedConfig{Model: "amazon.titan-embed-text-v2:0", Dims: 256})
	if _, err := e.Embed(context.Background(), []string{"a"}, ""); !errors.Is(err, ErrBroken) {
		t.Fatal(err)
	}
}

func TestBedrockCohereBatchAndInputType(t *testing.T) {
	api := &fakeAPI{invoke: func(n int, in *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		var body struct {
			Texts []string `json:"texts"`
		}
		json.Unmarshal(in.Body, &body)
		vs := make([]string, len(body.Texts))
		for i := range vs {
			vs[i] = vecJSON(1024)
		}
		return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embeddings":[` + strings.Join(vs, ",") + `],"id":"x"}`)}, nil
	}}
	e := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"})
	texts := make([]string, 100)
	for i := range texts {
		texts[i] = fmt.Sprint("t", i)
	}
	vecs, err := e.Embed(context.Background(), texts, DocPrefixOf(e))
	if err != nil || len(vecs) != 100 {
		t.Fatalf("%v %d", err, len(vecs))
	}
	if len(api.invokes) != 2 {
		t.Fatalf("100 texts must go out as 96+4, got %d requests", len(api.invokes))
	}
	var b1, b2 map[string]any
	json.Unmarshal(api.invokes[0].Body, &b1)
	json.Unmarshal(api.invokes[1].Body, &b2)
	if len(b1["texts"].([]any)) != 96 || len(b2["texts"].([]any)) != 4 ||
		b1["input_type"] != "search_document" || b1["truncate"] != "END" || len(b1) != 3 {
		t.Fatalf("body %v", b1)
	}
	if _, err := Query(context.Background(), e, "find me"); err != nil {
		t.Fatal(err)
	}
	var bq map[string]any
	json.Unmarshal(api.invokes[2].Body, &bq)
	if bq["input_type"] != "search_query" || bq["texts"].([]any)[0] != "find me" {
		t.Fatalf("query body %v", bq)
	}
	if got, want := e.Fingerprint(), "bedrock|cohere.embed-english-v3|1024|search_document|v1"; got != want {
		t.Fatalf("fingerprint %q, want %q", got, want)
	}
}

func TestBedrockCohereCountMismatchIsMalformed(t *testing.T) {
	api := &fakeAPI{invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embeddings":[]}`)}, nil
	}}
	e := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-multilingual-v3"})
	if _, err := e.Embed(context.Background(), []string{"a"}, ""); !errors.Is(err, ErrMalformed) {
		t.Fatal(err)
	}
}

func TestBedrockErrorMapping(t *testing.T) {
	for _, c := range []struct {
		code, msg string
		want      error
		retried   bool
	}{
		{"AccessDeniedException", "no", ErrAuth, false},
		{"UnrecognizedClientException", "bad token", ErrAuth, false},
		{"ExpiredTokenException", "expired", ErrAuth, false},
		{"ThrottlingException", "slow down", ErrDown, true},
		{"ValidationException", "Input is too long for requested model", ErrItem, false},
		{"ModelNotReadyException", "", ErrDown, false},
		{"ServiceUnavailableException", "", ErrDown, false},
		{"InternalServerException", "", ErrDown, false},
		{"ValidationException", "The provided model identifier is invalid", ErrBroken, false},
	} {
		api := &fakeAPI{invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
			return nil, fmt.Errorf("operation error Bedrock Runtime: InvokeModel: %w", &apiErr{c.code, c.msg})
		}}
		e := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"})
		_, err := e.Embed(context.Background(), []string{"x"}, "")
		if !errors.Is(err, c.want) {
			t.Errorf("%s/%q: %v, want %v", c.code, c.msg, err, c.want)
		}
		if c.want == ErrAuth && !IsSystemic(err) {
			t.Errorf("%s: auth must be systemic", c.code)
		}
		if c.retried != (len(api.invokes) == 4) || (!c.retried && len(api.invokes) != 1) {
			t.Errorf("%s: %d requests", c.code, len(api.invokes))
		}
	}
}

func TestBedrockThrottlingRecovers(t *testing.T) {
	api := &fakeAPI{invoke: func(n int, in *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		if n < 3 {
			return nil, &apiErr{"ThrottlingException", "Too many requests"}
		}
		return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embeddings":[` + vecJSON(1024) + `]}`)}, nil
	}}
	e := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"})
	if _, err := e.Embed(context.Background(), []string{"x"}, ""); err != nil || len(api.invokes) != 3 {
		t.Fatalf("%v %d", err, len(api.invokes))
	}
}

func TestBedrockCredentialChainFailureIsAuth(t *testing.T) {
	api := &fakeAPI{converse: func(int, *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
		return nil, errors.New("operation error Bedrock Runtime: Converse, get identity: get credentials: failed to refresh cached credentials, no EC2 IMDS role found")
	}}
	_, err := bedrockSum(t, api, "anthropic.claude-haiku-4-5").Summarize(context.Background(), "s", "u", 5)
	if !errors.Is(err, ErrAuth) || AuthCode(err) != 0 {
		t.Fatal(err)
	}
}

func TestBedrockConverseRequestAndResponse(t *testing.T) {
	api := &fakeAPI{converse: func(n int, in *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
		return &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role: types.ConversationRoleAssistant, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: " a summary "}},
		}}}, nil
	}}
	s := bedrockSum(t, api, "us.anthropic.claude-haiku-4-5-v1:0")
	got, err := s.Summarize(context.Background(), "be brief", "the input", 90)
	if err != nil || got != "a summary" {
		t.Fatalf("%q %v", got, err)
	}
	in := api.converses[0]
	sys := in.System[0].(*types.SystemContentBlockMemberText).Value
	user := in.Messages[0].Content[0].(*types.ContentBlockMemberText).Value
	if *in.ModelId != "us.anthropic.claude-haiku-4-5-v1:0" || sys != "be brief" || user != "the input" || in.Messages[0].Role != types.ConversationRoleUser ||
		*in.InferenceConfig.MaxTokens != 90 || *in.InferenceConfig.Temperature != float32(0.2) || len(in.Messages) != 1 {
		t.Fatalf("%+v", in)
	}
	if s.Fingerprint() != "bedrock:us.anthropic.claude-haiku-4-5-v1:0" {
		t.Fatal(s.Fingerprint())
	}
}

func TestBedrockSummarizeErrors(t *testing.T) {
	for code, want := range map[string]error{"ValidationException": ErrItem, "AccessDeniedException": ErrAuth, "ThrottlingException": ErrDown, "ServiceUnavailableException": ErrDown} {
		msg := ""
		if code == "ValidationException" {
			msg = "Too many input tokens: the prompt is too long"
		}
		api := &fakeAPI{converse: func(int, *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			return nil, &apiErr{code, msg}
		}}
		if _, err := bedrockSum(t, api, "anthropic.claude-haiku-4-5").Summarize(context.Background(), "s", "u", 5); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", code, err, want)
		}
	}
}

func TestBedrockHealth(t *testing.T) {
	api := &fakeAPI{
		invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
			return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embeddings":[` + vecJSON(1024) + `]}`)}, nil
		},
		converse: func(int, *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			return &bedrockruntime.ConverseOutput{Output: &types.ConverseOutputMemberMessage{Value: types.Message{Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "pong"}}}}}, nil
		},
	}
	if err := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"}).Health(context.Background()); err != nil || len(api.invokes) != 1 {
		t.Fatalf("%v %d", err, len(api.invokes))
	}
	if err := bedrockSum(t, api, "anthropic.claude-haiku-4-5").Health(context.Background()); err != nil || *api.converses[0].InferenceConfig.MaxTokens != 1 {
		t.Fatalf("%v", err)
	}
	api.converse = func(int, *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
		return nil, &apiErr{"AccessDeniedException", "x"}
	}
	if err := bedrockSum(t, api, "anthropic.claude-haiku-4-5").Health(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatal(err)
	}
}

func TestRequestRetryCancelDuringBackoffReturnsContextError(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Hour}
	defer func() { retryDelays = old }()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(429) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	resp, err := requestRetry(ctx, http.DefaultClient, http.MethodGet, srv.URL, nil, nil)
	if resp != nil || !errors.Is(err, context.Canceled) || errors.Is(err, ErrDown) {
		t.Fatalf("resp %v err %v", resp, err)
	}
	// and through a driver: cancelled, not "provider down"
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel2() }()
	_, err = anthropicSum(t, srv.URL).Summarize(ctx2, "s", "u", 1)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDown) {
		t.Fatalf("summarize err %v", err)
	}
}

func TestBedrockSDKRetryerIsOff(t *testing.T) {
	var lo awsLoadOptions
	for _, o := range bedrockLoadOptions("us-east-1", "work") {
		if err := o(&lo); err != nil {
			t.Fatal(err)
		}
	}
	if lo.Retryer == nil || lo.Retryer().MaxAttempts() != 1 || lo.Region != "us-east-1" || lo.SharedConfigProfile != "work" {
		t.Fatalf("%+v", lo)
	}
}

func TestCohereBothResponseForms(t *testing.T) {
	for name, body := range map[string]string{
		"array": `{"embeddings":[` + vecJSON(1024) + `]}`,
		"typed": `{"embeddings":{"float":[` + vecJSON(1024) + `]},"response_type":"embeddings_by_type"}`,
	} {
		body := body
		api := &fakeAPI{invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
			return &bedrockruntime.InvokeModelOutput{Body: []byte(body)}, nil
		}}
		vecs, err := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"}).Embed(context.Background(), []string{"x"}, "")
		if err != nil || len(vecs) != 1 || len(vecs[0]) != 1024 {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBedrockValidationClassification(t *testing.T) {
	for msg, want := range map[string]error{
		"Input is too long for requested model":    ErrItem,
		"Too many input tokens":                    ErrItem,
		"The input length exceeds the limit":       ErrItem,
		"The provided model identifier is invalid": ErrBroken,
		"dimensions must be one of 256, 512, 1024": ErrBroken,
	} {
		_, err := classifyBedrock(context.Background(), &apiErr{"ValidationException", msg})
		if !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", msg, err, want)
		}
	}
}

func TestDotlessHostnameIsLocal(t *testing.T) {
	for base, want := range map[string]bool{"http://desktop:1234": true, "http://lmstudio": true, "https://api.openai.com": false} {
		if Hosted(OpenAI, base) == want {
			t.Errorf("Hosted(openai, %s) = %v", base, !want)
		}
	}
}
