package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// BedrockAPI is the part of *bedrockruntime.Client the drivers use; tests
// inject a fake.
type BedrockAPI interface {
	InvokeModel(ctx context.Context, in *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
	Converse(ctx context.Context, in *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// bedrockConn builds the AWS client on first use from the default credential
// chain (environment, shared config/profile, SSO, instance role).
type bedrockConn struct {
	region, profile string
	once            sync.Once
	api             BedrockAPI
	err             error
}

func (b *bedrockConn) client(ctx context.Context) (BedrockAPI, error) {
	b.once.Do(func() {
		if b.api != nil {
			return
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, bedrockLoadOptions(b.region, b.profile)...)
		if err != nil {
			b.err = fmt.Errorf("%w: %v", &AuthError{}, err) // unreadable profile / SSO config: a credentials problem
			return
		}
		b.api = bedrockruntime.NewFromConfig(cfg)
	})
	return b.api, b.err
}

// bedrockLoadOptions are the AWS config options: region and profile when set,
// and the SDK's own retryer switched off so that only retryThrottled's
// 1s/4s/16s schedule applies (otherwise throttling would be retried twice).
func bedrockLoadOptions(region, profile string) []func(*awsconfig.LoadOptions) error {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }),
	}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	return opts
}

// Bedrock error codes by class.
var (
	bedrockAuthCodes = map[string]bool{
		"AccessDeniedException": true, "UnrecognizedClientException": true, "ExpiredTokenException": true,
		"InvalidSignatureException": true, "ExpiredToken": true, "InvalidClientTokenId": true, "UnauthorizedException": true,
	}
	bedrockThrottleCodes = map[string]bool{
		"ThrottlingException": true, "TooManyRequestsException": true, "Throttling": true,
		"ServiceQuotaExceededException": true, "RequestLimitExceeded": true,
	}
	bedrockDownCodes = map[string]bool{
		"ModelNotReadyException": true, "ServiceUnavailableException": true, "InternalServerException": true,
		"ModelTimeoutException": true, "InternalFailure": true,
	}
)

// classifyBedrock maps an AWS SDK error to the provider error classes and
// reports whether it was throttling (to be retried with backoff).
func classifyBedrock(ctx context.Context, err error) (throttled bool, out error) {
	if err == nil {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		code := ae.ErrorCode()
		switch {
		case bedrockAuthCodes[code]:
			return false, fmt.Errorf("%w: %s", &AuthError{}, code)
		case bedrockThrottleCodes[code]:
			return true, fmt.Errorf("%w: %s", ErrDown, code)
		case bedrockDownCodes[code]:
			return false, fmt.Errorf("%w: %s", ErrDown, code)
		case code == "ValidationException":
			if bedrockInputSize(ae.ErrorMessage()) {
				return false, &ItemError{"input too large"}
			}
			return false, fmt.Errorf("%w: ValidationException: %s", ErrBroken, ae.ErrorMessage())
		case code == "ResourceNotFoundException":
			return false, fmt.Errorf("%w: %s", ErrBroken, ae.ErrorMessage())
		case code == "ModelErrorException":
			return false, &ItemError{"model error"}
		}
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		switch c := re.HTTPStatusCode(); {
		case c == 401 || c == 403:
			return false, &AuthError{Code: c}
		case c == 429:
			return true, fmt.Errorf("%w: HTTP 429", ErrDown)
		case c >= 500 || c == 404 || c == 408:
			return false, fmt.Errorf("%w: HTTP %d", ErrDown, c)
		case c >= 400:
			return false, &ItemError{fmt.Sprintf("http %d", c)}
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "get credentials") || strings.Contains(msg, "failed to retrieve credentials") ||
		strings.Contains(msg, "no EC2 IMDS role") || strings.Contains(msg, "failed to refresh cached credentials") ||
		strings.Contains(msg, "SSO session") {
		return false, fmt.Errorf("%w: %v", &AuthError{}, err)
	}
	return false, fmt.Errorf("%w: %v", ErrDown, err)
}

func bedrockInputSize(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"input", "token", "too long", "length"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// call runs op with throttling backoff and classifies its error.
func bedrockCall(ctx context.Context, op func(ctx context.Context) error) error {
	return retryThrottled(ctx, func() (bool, time.Duration, error) {
		cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		err := op(cctx)
		if err == nil {
			return false, 0, nil
		}
		if ctx.Err() == nil && cctx.Err() != nil {
			return false, 0, &timeoutError{} // our own request timeout
		}
		th, cerr := classifyBedrock(ctx, err)
		return th, 0, cerr
	})
}

// timeoutError marks a request that exceeded the per-request timeout.
type timeoutError struct{}

func (*timeoutError) Error() string { return "request timed out" }
func (*timeoutError) Is(t error) bool {
	return t == ErrDown
}

// Embedding model families on Bedrock.
const (
	familyTitan  = "titan"
	familyCohere = "cohere"
)

func bedrockEmbedFamily(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "titan-embed-text-v2"):
		return familyTitan
	case strings.Contains(m, "cohere.embed-"):
		return familyCohere
	}
	return ""
}

// cohereBatch is Cohere's maximum number of texts per request.
const cohereBatch = 96

// BedrockEmbedder embeds with Titan Text Embeddings V2 or Cohere Embed v3 via
// InvokeModel. For Cohere, the "prefixes" are input_type values.
type BedrockEmbedder struct {
	Model                  string
	family                 string
	docPrefix, queryPrefix string
	dims                   dimTracker
	conn                   bedrockConn
}

func newBedrockEmbedder(c EmbedConfig) *BedrockEmbedder {
	e := &BedrockEmbedder{Model: c.Model, family: bedrockEmbedFamily(c.Model), docPrefix: *c.DocPrefix, queryPrefix: *c.QueryPrefix,
		conn: bedrockConn{region: c.Region, profile: c.Profile}}
	e.dims.n.Store(int64(c.Dims))
	return e
}

func (e *BedrockEmbedder) DocPrefix() string   { return e.docPrefix }
func (e *BedrockEmbedder) QueryPrefix() string { return e.queryPrefix }
func (e *BedrockEmbedder) Dims() int           { return e.dims.get() }

// Fingerprint is "bedrock|<model>|<dims>|<input_type or prefix>|v1".
func (e *BedrockEmbedder) Fingerprint() string {
	return Fingerprint(Bedrock, e.Model, e.Dims(), e.docPrefix)
}

func (e *BedrockEmbedder) invoke(ctx context.Context, body map[string]any) ([]byte, error) {
	api, err := e.conn.client(ctx)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(body)
	var out []byte
	err = bedrockCall(ctx, func(cctx context.Context) error {
		r, err := api.InvokeModel(cctx, &bedrockruntime.InvokeModelInput{
			ModelId: aws.String(e.Model), ContentType: aws.String("application/json"), Accept: aws.String("application/json"), Body: b,
		})
		if err != nil {
			return err
		}
		out = r.Body
		return nil
	})
	return out, err
}

// Embed embeds texts (each capped to 2000 runes). Titan takes one text per
// request; Cohere takes up to 96, with input_type search_query when prefix is
// the query type (else search_document) and truncate END.
func (e *BedrockEmbedder) Embed(ctx context.Context, texts []string, prefix string) ([][]float32, error) {
	var vecs [][]float32
	switch e.family {
	case familyTitan:
		for _, t := range capAll(texts, prefix) {
			raw, err := e.invoke(ctx, map[string]any{"inputText": t, "dimensions": e.Dims(), "normalize": true})
			if err != nil {
				return nil, err
			}
			var out struct {
				Embedding []float32 `json:"embedding"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("%w: decode: %v", ErrMalformed, err)
			}
			vecs = append(vecs, out.Embedding)
		}
	case familyCohere:
		inputType := CohereDocType
		if prefix == CohereQueryType {
			inputType = CohereQueryType
		}
		in := capAll(texts, "")
		for i := 0; i < len(in); i += cohereBatch {
			chunk := in[i:min(i+cohereBatch, len(in))]
			raw, err := e.invoke(ctx, map[string]any{"texts": chunk, "input_type": inputType, "truncate": "END"})
			if err != nil {
				return nil, err
			}
			embs, err := cohereEmbeddings(raw)
			if err != nil {
				return nil, err
			}
			if len(embs) != len(chunk) {
				return nil, fmt.Errorf("%w: got %d embeddings for %d inputs", ErrMalformed, len(embs), len(chunk))
			}
			vecs = append(vecs, embs...)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported bedrock embedding model %q", ErrBroken, e.Model)
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs", ErrMalformed, len(vecs), len(texts))
	}
	if err := e.dims.check(vecs); err != nil {
		return nil, err
	}
	return vecs, nil
}

// cohereEmbeddings decodes both Cohere response forms: "embeddings": [[...]]
// and "embeddings": {"float": [[...]]}.
func cohereEmbeddings(raw []byte) ([][]float32, error) {
	var out struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrMalformed, err)
	}
	var flat [][]float32
	if json.Unmarshal(out.Embeddings, &flat) == nil && flat != nil {
		return flat, nil
	}
	var typed struct {
		Float [][]float32 `json:"float"`
	}
	if err := json.Unmarshal(out.Embeddings, &typed); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrMalformed, err)
	}
	return typed.Float, nil
}

// Health embeds one short text.
func (e *BedrockEmbedder) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := e.Embed(ctx, []string{"health probe"}, "")
	return err
}

// BedrockSummarizer summarizes through the Converse API with any
// anthropic.claude-* (or us.anthropic.claude-*) model id.
type BedrockSummarizer struct {
	Model       string
	Temperature float64
	conn        bedrockConn
}

func newBedrockSummarizer(c SummarizeConfig) *BedrockSummarizer {
	return &BedrockSummarizer{Model: c.Model, Temperature: *c.Temperature, conn: bedrockConn{region: c.Region, profile: c.Profile}}
}

func (s *BedrockSummarizer) Fingerprint() string { return Bedrock + ":" + s.Model }

// Summarize sends system and user to Converse. ValidationException about the
// input size and the request timeout are *ItemError; credentials problems are
// ErrAuth; throttling is retried with backoff and then ErrDown.
func (s *BedrockSummarizer) Summarize(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	api, err := s.conn.client(ctx)
	if err != nil {
		return "", err
	}
	in := &bedrockruntime.ConverseInput{
		ModelId: aws.String(s.Model),
		System:  []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: system}},
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: user}},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(maxTokens)), Temperature: aws.Float32(float32(s.Temperature))},
	}
	var out *bedrockruntime.ConverseOutput
	err = bedrockCall(ctx, func(cctx context.Context) (err error) {
		out, err = api.Converse(cctx, in)
		return err
	})
	if err != nil {
		var te *timeoutError
		if errors.As(err, &te) {
			return "", &ItemError{"timeout"}
		}
		return "", err
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", &ItemError{"bad response"}
	}
	var sb strings.Builder
	for _, b := range msg.Value.Content {
		if t, ok := b.(*types.ContentBlockMemberText); ok {
			sb.WriteString(t.Value)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// Health sends a one-token Converse request.
func (s *BedrockSummarizer) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := s.Summarize(ctx, "Reply with one word.", "ping", 1)
	var ie *ItemError
	if errors.As(err, &ie) && ie.Reason != "timeout" {
		return nil // the service answered: reachable, authorized, model exists
	}
	return err
}
