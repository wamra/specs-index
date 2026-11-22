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
	for _, k := range []string{"PROVIDER_EMBED", "MODEL", "BASE_URL", "ALLOW_CLOUD", "API_KEY_ENV", "REGION", "PROFILE", "DIMS"} {
		t.Setenv("SPECS_INDEX_"+k, "")
		os.Unsetenv("SPECS_INDEX_" + k)
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
	t.Setenv("SPECS_INDEX_PROVIDER_EMBED", "none")
	t.Setenv("SPECS_INDEX_MODEL", "m2")
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
