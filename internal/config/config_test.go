package config

import (
	"os"
	"path/filepath"
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
