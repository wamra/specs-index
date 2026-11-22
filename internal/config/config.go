// Package config loads the embedding settings: defaults, then
// ~/.config/specs-index/config.json, then SPECS_INDEX_* environment variables.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// SPECS_INDEX_* environment variables over it: PROVIDER_EMBED, MODEL,
// BASE_URL, ALLOW_CLOUD, DIMS, API_KEY_ENV, REGION, PROFILE.
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
	e.Provider.Embed = os.Getenv("SPECS_INDEX_PROVIDER_EMBED")
	e.Model = os.Getenv("SPECS_INDEX_MODEL")
	e.BaseURL = os.Getenv("SPECS_INDEX_BASE_URL")
	e.APIKeyEnv = os.Getenv("SPECS_INDEX_API_KEY_ENV")
	e.Region = os.Getenv("SPECS_INDEX_REGION")
	e.Profile = os.Getenv("SPECS_INDEX_PROFILE")
	if v := os.Getenv("SPECS_INDEX_DIMS"); v != "" {
		if e.Dims, err = strconv.Atoi(v); err != nil || e.Dims < 0 {
			return c, fmt.Errorf("SPECS_INDEX_DIMS %q: want a positive integer", v)
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
}
