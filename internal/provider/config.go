package provider

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// Provider names.
const (
	Ollama = "ollama"
	OpenAI = "openai"
	// Anthropic summarizes through the Anthropic Messages API (no embeddings).
	Anthropic = "anthropic"
	// Bedrock embeds and summarizes through AWS Bedrock.
	Bedrock = "bedrock"
)

// Defaults (today's behavior).
const (
	DefaultOllamaURL      = "http://localhost:11434"
	DefaultOpenAIURL      = "https://api.openai.com"
	DefaultEmbedModel     = "embeddinggemma"
	DefaultSummarizeModel = "gemma4:e4b"
	DefaultAnthropicURL   = "https://api.anthropic.com"
	// DefaultAnthropicModel is the summarizer model of provider "anthropic".
	DefaultAnthropicModel = "claude-haiku-4-5"
	// DefaultAnthropicKeyEnv is the standard variable of Anthropic's SDKs.
	DefaultAnthropicKeyEnv = "ANTHROPIC_API_KEY"
	DefaultMaxTokens       = 120
	// DefaultMaxInputChars is the clip applied to every summarizer input.
	DefaultMaxInputChars = 8000
	// HostedDailyCap is the max_items_per_day default of hosted providers.
	HostedDailyCap     = 2000
	DefaultTemperature = 0.2
	// DefaultDims is the vector size of the default model; used when nothing
	// else determines it.
	DefaultDims = 768
	// FingerprintVersion is the chunking/prompt version in the fingerprint.
	FingerprintVersion = "v1"
)

// EmbedConfig is the "embed" section of the config file. Prefix pointers
// distinguish "unset" (use the known-model default) from an explicit "".
type EmbedConfig struct {
	Provider    string  `json:"provider,omitempty"`
	BaseURL     string  `json:"base_url,omitempty"`
	Model       string  `json:"model,omitempty"`
	Dims        int     `json:"dims,omitempty"`
	DocPrefix   *string `json:"doc_prefix,omitempty"`
	QueryPrefix *string `json:"query_prefix,omitempty"`
	APIKeyEnv   string  `json:"api_key_env,omitempty"`
	// Region and Profile select the AWS region and shared-config profile of
	// provider "bedrock" (credentials come from the default AWS chain).
	Region  string `json:"region,omitempty"`
	Profile string `json:"profile,omitempty"`
	// MaxItemsPerDay caps chunks embedded per UTC day: 0 = unset (2000 for a
	// hosted provider, unlimited for a local one), negative = unlimited.
	MaxItemsPerDay int `json:"max_items_per_day,omitempty"`
}

// SummarizeConfig is the "summarize" section of the config file.
type SummarizeConfig struct {
	Provider       string   `json:"provider,omitempty"`
	BaseURL        string   `json:"base_url,omitempty"`
	Model          string   `json:"model,omitempty"`
	APIKeyEnv      string   `json:"api_key_env,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
	MaxItemsPerRun int      `json:"max_items_per_run,omitempty"`
	Region         string   `json:"region,omitempty"`
	Profile        string   `json:"profile,omitempty"`
	// MaxItemsPerDay caps summaries per UTC day (same rules as embed).
	MaxItemsPerDay int `json:"max_items_per_day,omitempty"`
	// MaxInputChars clips each summarizer input (default 8000: first 3/4 and
	// last 1/4 are kept).
	MaxInputChars int `json:"max_input_chars,omitempty"`
	// Kinds restricts what is summarized: tool_output, file_change, asset
	// (default all).
	Kinds []string `json:"kinds,omitempty"`
}

// SummaryKindNames are the accepted values of summarize.kinds.
var SummaryKindNames = []string{"tool_output", "file_change", "asset"}

// Hosted reports whether a provider sends data off the machine: anthropic and
// bedrock always, openai unless base_url points at this machine or a private
// network (LM Studio, vLLM). Ollama is local.
func Hosted(providerName, baseURL string) bool {
	switch providerName {
	case Anthropic, Bedrock:
		return true
	case OpenAI:
		return !localHost(baseURL)
	}
	return false
}

func localHost(base string) bool {
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return h != "" && !strings.Contains(h, ".") // dotless hostname: a LAN or container name
}

// DailyCap resolves a max_items_per_day setting: the configured positive
// value, else 2000 for a hosted provider, else 0 (unlimited). A negative
// setting is an explicit "unlimited". source says where the value came from.
func DailyCap(set int, hosted bool) (limit int, source string) {
	switch {
	case set > 0:
		return set, "configured"
	case set < 0:
		return 0, "unlimited (configured)"
	case hosted:
		return HostedDailyCap, "hosted default"
	}
	return 0, "unlimited"
}

// DailyCap is the effective embed.max_items_per_day (0 = unlimited).
func (c EmbedConfig) DailyCap() (int, string) {
	return DailyCap(c.MaxItemsPerDay, Hosted(c.Provider, c.BaseURL))
}

// DailyCap is the effective summarize.max_items_per_day (0 = unlimited).
func (c SummarizeConfig) DailyCap() (int, string) {
	return DailyCap(c.MaxItemsPerDay, Hosted(c.Provider, c.BaseURL))
}

// Str returns a pointer to s (for the prefix fields).
func Str(s string) *string { return &s }

func defaultBase(p string) string {
	switch p {
	case OpenAI:
		return DefaultOpenAIURL
	case Anthropic:
		return DefaultAnthropicURL
	case Bedrock:
		return "" // the AWS SDK resolves the regional endpoint
	}
	return DefaultOllamaURL
}

// Resolved fills every unset field: provider ollama, the provider's default
// base URL, the default model (ollama only), and the known-model dims and
// prefixes. Dims stays 0 when it must be auto-detected.
func (c EmbedConfig) Resolved() EmbedConfig {
	if c.Provider == "" {
		c.Provider = Ollama
	}
	if c.BaseURL == "" {
		c.BaseURL = defaultBase(c.Provider)
	}
	c.BaseURL = trimBase(c.BaseURL)
	if c.Model == "" && c.Provider == Ollama {
		c.Model = DefaultEmbedModel
	}
	kd, _ := KnownEmbedModel(c.Model)
	if c.Dims == 0 {
		c.Dims = kd.Dims
	}
	if c.DocPrefix == nil {
		c.DocPrefix = Str(kd.DocPrefix)
	}
	if c.QueryPrefix == nil {
		c.QueryPrefix = Str(kd.QueryPrefix)
	}
	return c
}

// Validate reports an unusable resolved config.
func (c EmbedConfig) Validate() error {
	switch c.Provider {
	case Ollama, OpenAI, Bedrock:
	case Anthropic:
		return fmt.Errorf("embed.provider %q: Anthropic has no embeddings endpoint; use %q with base_url \"https://api.voyageai.com\" (Voyage is OpenAI-compatible for /v1/embeddings, e.g. model voyage-3), %q, or a local model (%q, or %q such as LM Studio)", c.Provider, OpenAI, Bedrock, Ollama, OpenAI)
	default:
		return fmt.Errorf("embed.provider %q: want %q, %q or %q", c.Provider, Ollama, OpenAI, Bedrock)
	}
	if c.Model == "" {
		return fmt.Errorf("embed.model is required for provider %q", c.Provider)
	}
	if c.Dims < 0 {
		return fmt.Errorf("embed.dims %d: must be positive", c.Dims)
	}
	if c.Provider == Bedrock {
		switch bedrockEmbedFamily(c.Model) {
		case familyTitan:
			if c.Dims != 256 && c.Dims != 512 && c.Dims != 1024 {
				return fmt.Errorf("embed.dims %d: Titan Text Embeddings V2 supports 256, 512 or 1024", c.Dims)
			}
		case familyCohere:
			if c.Dims != 1024 {
				return fmt.Errorf("embed.dims %d: %s returns 1024-dimensional vectors", c.Dims, c.Model)
			}
		default:
			return fmt.Errorf("embed.model %q: bedrock embeddings support amazon.titan-embed-text-v2:0, cohere.embed-english-v3 and cohere.embed-multilingual-v3", c.Model)
		}
	}
	return nil
}

// Resolved fills unset fields with the defaults.
func (c SummarizeConfig) Resolved() SummarizeConfig {
	if c.Provider == "" {
		c.Provider = Ollama
	}
	if c.BaseURL == "" {
		c.BaseURL = defaultBase(c.Provider)
	}
	c.BaseURL = trimBase(c.BaseURL)
	switch {
	case c.Model == "" && c.Provider == Ollama:
		c.Model = DefaultSummarizeModel
	case c.Model == "" && c.Provider == Anthropic:
		c.Model = DefaultAnthropicModel
	}
	if c.APIKeyEnv == "" && c.Provider == Anthropic {
		c.APIKeyEnv = DefaultAnthropicKeyEnv
	}
	if c.MaxInputChars == 0 {
		c.MaxInputChars = DefaultMaxInputChars
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	if c.Temperature == nil {
		t := DefaultTemperature
		c.Temperature = &t
	}
	if c.Enabled == nil {
		t := true
		c.Enabled = &t
	}
	return c
}

// Validate reports an unusable resolved config.
func (c SummarizeConfig) Validate() error {
	switch c.Provider {
	case Ollama, OpenAI, Anthropic, Bedrock:
	default:
		return fmt.Errorf("summarize.provider %q: want %q, %q, %q or %q", c.Provider, Ollama, OpenAI, Anthropic, Bedrock)
	}
	if c.Model == "" {
		return fmt.Errorf("summarize.model is required for provider %q", c.Provider)
	}
	if c.MaxInputChars < 0 {
		return fmt.Errorf("summarize.max_input_chars %d: must be positive", c.MaxInputChars)
	}
	for _, k := range c.Kinds {
		if !contains(SummaryKindNames, k) {
			return fmt.Errorf("summarize.kinds: unknown kind %q (want %s)", k, strings.Join(SummaryKindNames, ", "))
		}
	}
	return nil
}

// NewEmbedder builds the embedder for c (resolved first).
func NewEmbedder(c EmbedConfig) (Embedder, error) {
	c = c.Resolved()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Provider {
	case OpenAI:
		return newOpenAIEmbedder(c), nil
	case Bedrock:
		return newBedrockEmbedder(c), nil
	}
	return newOllamaEmbedder(c), nil
}

// NewSummarizer builds the summarizer for c (resolved first).
func NewSummarizer(c SummarizeConfig) (Summarizer, error) {
	c = c.Resolved()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Provider {
	case OpenAI:
		return newOpenAISummarizer(c), nil
	case Anthropic:
		return newAnthropicSummarizer(c), nil
	case Bedrock:
		return newBedrockSummarizer(c), nil
	}
	return newOllamaSummarizer(c), nil
}

// KeyState reports "set" or "unset" for the env var named name ("" for no
// name). The value is never returned.
func KeyState(name string) string {
	switch {
	case name == "":
		return "(none configured)"
	case os.Getenv(name) != "":
		return name + ": set"
	}
	return name + ": unset"
}

// Fingerprint builds "<provider>|<model>|<dims>|<doc_prefix>|v1".
func Fingerprint(provider, model string, dims int, docPrefix string) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s", provider, model, dims, docPrefix, FingerprintVersion)
}

// LegacyFingerprint is the string stored by archives created before
// providers existed; it is equivalent to NormalizedLegacyFingerprint.
const (
	LegacyFingerprint           = "embeddinggemma|title: none | text: |v1"
	NormalizedLegacyFingerprint = "ollama|embeddinggemma|768|title: none | text: |v1"
)

// NormalizeFingerprint maps the legacy fingerprint to its current form.
func NormalizeFingerprint(fp string) string {
	if fp == LegacyFingerprint {
		return NormalizedLegacyFingerprint
	}
	return fp
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
