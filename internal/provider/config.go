package provider

import (
	"fmt"
	"os"
)

// Provider names.
const (
	Ollama = "ollama"
	OpenAI = "openai"
	// Anthropic has no embeddings endpoint; the name is kept only so that
	// Validate can point at an alternative.
	Anthropic = "anthropic"
	// Bedrock embeds through AWS Bedrock.
	Bedrock = "bedrock"
)

// Defaults (today's behavior).
const (
	DefaultOllamaURL  = "http://localhost:11434"
	DefaultOpenAIURL  = "https://api.openai.com"
	DefaultEmbedModel = "embeddinggemma"
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
}

// Str returns a pointer to s (for the prefix fields).
func Str(s string) *string { return &s }

func defaultBase(p string) string {
	switch p {
	case OpenAI:
		return DefaultOpenAIURL
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
