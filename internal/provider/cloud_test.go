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
	"github.com/aws/smithy-go"
)

type awsLoadOptions = awsconfig.LoadOptions

func init() {
	// No test waits for the production backoff (1s/4s/16s).
	retryDelays = []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
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
	mu      sync.Mutex
	invokes []*bedrockruntime.InvokeModelInput
	invoke  func(call int, in *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error)
}

func (f *fakeAPI) InvokeModel(ctx context.Context, in *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	f.mu.Lock()
	f.invokes = append(f.invokes, in)
	n := len(f.invokes)
	f.mu.Unlock()
	return f.invoke(n, in)
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
	api := &fakeAPI{invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		return nil, errors.New("operation error Bedrock Runtime: InvokeModel, get identity: get credentials: failed to refresh cached credentials, no EC2 IMDS role found")
	}}
	_, err := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"}).Embed(context.Background(), []string{"x"}, "")
	if !errors.Is(err, ErrAuth) || AuthCode(err) != 0 {
		t.Fatal(err)
	}
}

func TestBedrockHealth(t *testing.T) {
	api := &fakeAPI{
		invoke: func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
			return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embeddings":[` + vecJSON(1024) + `]}`)}, nil
		},
	}
	if err := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"}).Health(context.Background()); err != nil || len(api.invokes) != 1 {
		t.Fatalf("%v %d", err, len(api.invokes))
	}
	api.invoke = func(int, *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		return nil, &apiErr{"AccessDeniedException", "x"}
	}
	if err := bedrockEmb(t, api, EmbedConfig{Model: "cohere.embed-english-v3"}).Health(context.Background()); !errors.Is(err, ErrAuth) {
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
	_, err = openaiEmb(t, srv.URL, EmbedConfig{Model: "m"}).Embed(ctx2, []string{"a"}, "")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDown) {
		t.Fatalf("embed err %v", err)
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
