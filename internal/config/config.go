// Package config loads the settings: defaults, then
// ~/.config/specs-index/config.json, then SPECS_INDEX_* environment variables.
// The ID prefix types are read from the file only.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Provider names accepted for Provider.Embed.
const (
	None    = "none"
	Ollama  = "ollama"
	OpenAI  = "openai"
	Bedrock = "bedrock"
)

// Entity types a Types entry may name.
const (
	TypeRequirement = "requirement"
	TypeDesign      = "design"
	TypeAcceptance  = "acceptance"
)

var prefixRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)

// Defaults.
const (
	DefaultProvider = Ollama
	DefaultModel    = "embeddinggemma"
)

// Providers holds the provider choice per capability.
type Providers struct {
	Embed string `json:"embed,omitempty"`
}

// Config is the embedding configuration. AllowCloud must be true (or the
// --allow-cloud flag given) before any non-local provider is contacted.
type Config struct {
	Provider   Providers `json:"provider"`
	Model      string    `json:"model,omitempty"`
	BaseURL    string    `json:"base_url,omitempty"`
	AllowCloud bool      `json:"allow_cloud,omitempty"`
	// Optional provider settings: Dims overrides the model's vector size,
	// APIKeyEnv names the variable holding an OpenAI-compatible API key (the
	// key itself is never stored), Region and Profile select the AWS region
	// and shared-config profile for Bedrock.
	Dims      int    `json:"dims,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Region    string `json:"region,omitempty"`
	Profile   string `json:"profile,omitempty"`
	// Types maps a Markdown ID prefix (BRD in BRD-004) to an entity type:
	// requirement, design or acceptance. It is merged over the defaults
	// BRD=requirement, FDS=design, AC=acceptance; a prefix with no mapping
	// is a requirement.
	Types map[string]string `json:"types,omitempty"`
}

// EnvVar describes one environment variable the tools read.
type EnvVar struct{ Name, Description string }

// EnvVars lists the supported environment variables, for help output.
func EnvVars() []EnvVar {
	return []EnvVar{
		{"SPECS_INDEX_EMBED_PROVIDER", "embedding provider: ollama, openai, bedrock or none (default ollama)"},
		{"SPECS_INDEX_EMBED_MODEL", "embedding model name (default embeddinggemma for ollama)"},
		{"SPECS_INDEX_EMBED_BASE_URL", "base URL of the Ollama or OpenAI-compatible server"},
		{"SPECS_INDEX_EMBED_API_KEY_ENV", "name of the variable holding the OpenAI-compatible API key"},
		{"SPECS_INDEX_EMBED_REGION", "AWS region for the bedrock provider"},
		{"SPECS_INDEX_EMBED_PROFILE", "AWS shared-config profile for the bedrock provider"},
		{"SPECS_INDEX_EMBED_DIMS", "vector size override, a positive integer"},
		{"SPECS_INDEX_ALLOW_CLOUD", "true allows non-local embedding providers (same as --allow-cloud)"},
		{"SPECS_INDEX_DB", "path of the index database"},
	}
}

// Path returns the config file path, ~/.config/specs-index/config.json.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "specs-index", "config.json"), nil
}

// Load reads the config file (a missing file is fine) and applies the
// environment variables listed by EnvVars over it.
func Load() (Config, error) {
	c := Config{Provider: Providers{Embed: DefaultProvider}}
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return c, fmt.Errorf("read %s: %w", p, err)
	default:
		var f Config
		if err := json.Unmarshal(b, &f); err != nil {
			return c, fmt.Errorf("parse %s: %w", p, err)
		}
		merge(&c, f)
	}
	var e Config
	e.Provider.Embed = os.Getenv("SPECS_INDEX_EMBED_PROVIDER")
	e.Model = os.Getenv("SPECS_INDEX_EMBED_MODEL")
	e.BaseURL = os.Getenv("SPECS_INDEX_EMBED_BASE_URL")
	e.APIKeyEnv = os.Getenv("SPECS_INDEX_EMBED_API_KEY_ENV")
	e.Region = os.Getenv("SPECS_INDEX_EMBED_REGION")
	e.Profile = os.Getenv("SPECS_INDEX_EMBED_PROFILE")
	if v := os.Getenv("SPECS_INDEX_EMBED_DIMS"); v != "" {
		if e.Dims, err = strconv.Atoi(v); err != nil || e.Dims <= 0 {
			return c, fmt.Errorf("SPECS_INDEX_EMBED_DIMS %q: want a positive integer", v)
		}
	}
	merge(&c, e)
	if v := os.Getenv("SPECS_INDEX_ALLOW_CLOUD"); v != "" {
		if c.AllowCloud, err = strconv.ParseBool(v); err != nil {
			return c, fmt.Errorf("SPECS_INDEX_ALLOW_CLOUD %q: want true or false", v)
		}
	}
	c.Provider.Embed = strings.ToLower(strings.TrimSpace(c.Provider.Embed))
	switch c.Provider.Embed {
	case None, Ollama, OpenAI, Bedrock:
	default:
		return c, fmt.Errorf("provider.embed %q: want %s, %s, %s or %s", c.Provider.Embed, Ollama, OpenAI, Bedrock, None)
	}
	if c.Model == "" && c.Provider.Embed == Ollama {
		c.Model = DefaultModel // only Ollama has a default model
	}
	for k, v := range c.Types {
		if !prefixRe.MatchString(k) {
			return c, fmt.Errorf("types: prefix %q: want upper-case letters and digits starting with a letter, as in BRD", k)
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case TypeRequirement, TypeDesign, TypeAcceptance:
		default:
			return c, fmt.Errorf("types: %s %q: want %s, %s or %s", k, c.Types[k], TypeRequirement, TypeDesign, TypeAcceptance)
		}
		c.Types[k] = v
	}
	return c, nil
}

// merge copies o's non-empty fields over c. AllowCloud comes from the file
// only (the environment is applied by Load, so "false" can override true).
func merge(c *Config, o Config) {
	if o.Provider.Embed != "" {
		c.Provider.Embed = o.Provider.Embed
	}
	if o.Model != "" {
		c.Model = o.Model
	}
	if o.BaseURL != "" {
		c.BaseURL = o.BaseURL
	}
	if o.AllowCloud {
		c.AllowCloud = true
	}
	if o.Dims != 0 {
		c.Dims = o.Dims
	}
	if o.APIKeyEnv != "" {
		c.APIKeyEnv = o.APIKeyEnv
	}
	if o.Region != "" {
		c.Region = o.Region
	}
	if o.Profile != "" {
		c.Profile = o.Profile
	}
	for k, v := range o.Types {
		if c.Types == nil {
			c.Types = map[string]string{}
		}
		c.Types[k] = v
	}
}
