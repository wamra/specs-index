package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/markdown"
)

// ConfigResult is the effective configuration. Types is the Markdown ID
// prefix mapping in effect (the defaults with the configured types merged
// over them). File is the config
// file path with the home directory shown as "~". APIKeySet says only whether
// the variable named by APIKeyEnv has a value; the value is never exposed.
type ConfigResult struct {
	File        string            `json:"file"`
	FileExists  bool              `json:"file_exists"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	BaseURL     string            `json:"base_url"`
	AllowCloud  bool              `json:"allow_cloud"`
	Dims        int               `json:"dims"`
	APIKeyEnv   string            `json:"api_key_env"`
	APIKeySet   bool              `json:"api_key_set"`
	Region      string            `json:"region"`
	Profile     string            `json:"profile"`
	Types       map[string]string `json:"types"`
	Environment []EnvVarInfo      `json:"environment"`
}

// EnvVarInfo is one environment variable the tool reads (from
// config.EnvVars). Value is the current value as shown to the user: the API
// key variable holds a variable's name, never a key; a base URL is shown with
// any password redacted (RedactURL); SPECS_INDEX_DB is shown through
// DisplayPath, like the db field of status.
type EnvVarInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Set         bool   `json:"set"`
	Value       string `json:"value"`
}

// EnvInfo lists the supported environment variables with their current
// values, displayed for root (see EnvVarInfo).
func EnvInfo(root string) []EnvVarInfo {
	vars := config.EnvVars()
	out := make([]EnvVarInfo, 0, len(vars))
	for _, v := range vars {
		val, set := os.LookupEnv(v.Name)
		if val != "" {
			switch {
			case v.Name == "SPECS_INDEX_DB":
				val = DisplayPath(root, val)
			case strings.HasSuffix(v.Name, "_BASE_URL"):
				val = RedactURL(val)
			}
		}
		out = append(out, EnvVarInfo{Name: v.Name, Description: v.Description, Set: set && val != "", Value: val})
	}
	return out
}

// RedactURL returns raw with any password in its user info replaced by "xxxxx"
// (url.URL.Redacted), "(unparseable URL)" when it cannot be parsed, and "" for
// "".
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	return u.Redacted()
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

// Config returns the effective configuration (defaults, then the config file,
// then the environment, as resolved by internal/config). root is used only to
// display SPECS_INDEX_DB.
func Config(cfg config.Config, root string) (ConfigResult, error) {
	p, err := config.Path()
	if err != nil {
		return ConfigResult{}, err
	}
	_, statErr := os.Stat(p)
	r := ConfigResult{
		File: tildePath(p), FileExists: statErr == nil,
		Provider: cfg.Provider.Embed, Model: cfg.Model, BaseURL: RedactURL(cfg.BaseURL), AllowCloud: cfg.AllowCloud,
		Dims: cfg.Dims, APIKeyEnv: cfg.APIKeyEnv, Region: cfg.Region, Profile: cfg.Profile,
		Types: markdown.MergeTypes(cfg.Types),
	}
	if cfg.APIKeyEnv != "" {
		r.APIKeySet = os.Getenv(cfg.APIKeyEnv) != ""
	}
	r.Environment = EnvInfo(root)
	return r, nil
}

// ConfigInitResult is the result of config init.
type ConfigInitResult struct {
	File    string `json:"file"`
	Written bool   `json:"written"`
}

// ErrConfigExists is returned by ConfigInit when the file already exists.
var ErrConfigExists = errors.New("config file already exists; not overwriting")

// ConfigInit writes a commented example config to the config file path, only
// if no file exists there. The "_comment" keys are ignored when the file is
// loaded.
func ConfigInit() (ConfigInitResult, error) {
	p, err := config.Path()
	if err != nil {
		return ConfigInitResult{}, err
	}
	r := ConfigInitResult{File: tildePath(p)}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return r, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return r, fmt.Errorf("%s: %w", r.File, ErrConfigExists)
	}
	if err != nil {
		return r, err
	}
	if _, err := f.WriteString(exampleConfig()); err != nil {
		f.Close()
		return r, err
	}
	r.Written = true
	return r, f.Close()
}

func exampleConfig() string {
	ex := []struct{ k, v any }{
		{"_comment", "specs-index embedding settings. Delete this file to use the defaults. Environment variables override these values."},
		{"provider", map[string]string{"embed": config.DefaultProvider}},
		{"_comment_provider", "embed: ollama (local, default), openai (any OpenAI-compatible endpoint), bedrock (cloud), or none for full text search only."},
		{"model", config.DefaultModel},
		{"_comment_model", "Embedding model name for the chosen provider."},
		{"base_url", ""},
		{"_comment_base_url", "Endpoint override. A host other than this machine counts as cloud."},
		{"allow_cloud", false},
		{"_comment_allow_cloud", "Must be true (or pass --allow-cloud) before document text is sent to a cloud provider."},
		{"types", map[string]string{}},
		{"_comment_types", "Markdown ID prefix to entity type (requirement, design or acceptance), merged over BRD=requirement, FDS=design, AC=acceptance; any other prefix is a requirement. Example: {\"SRS\": \"requirement\", \"SDD\": \"design\"}."},
	}
	var b strings.Builder
	b.WriteString("{\n")
	for i, kv := range ex {
		k, _ := json.Marshal(kv.k)
		v, _ := json.Marshal(kv.v)
		b.WriteString("  " + string(k) + ": " + string(v))
		if i < len(ex)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String()
}
