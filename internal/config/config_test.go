package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) (env map[string]string, root string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "config", "transpane")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"CODEX_HOME":        filepath.Join(root, "codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude"),
		"XDG_CONFIG_HOME":   filepath.Join(root, "config"),
		"XDG_CACHE_HOME":    filepath.Join(root, "cache"),
	}, root
}

func TestLoadReadsAllValues(t *testing.T) {
	env, root := writeConfig(t, "[translation]\nbase_url = \"http://192.168.1.9:1234/v1\"\nmodel = \"plamo-3-translate-2b\"\nform = \"plain\"\n")
	cfg, err := LoadFrom(env, root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://192.168.1.9:1234/v1" || cfg.Model != "plamo-3-translate-2b" || cfg.Form != "plain" {
		t.Fatalf("unexpected %+v", cfg)
	}
	if cfg.SessionsRoot != filepath.Join(root, "codex", "sessions") || cfg.ClaudeProjectsRoot != filepath.Join(root, "claude", "projects") {
		t.Fatalf("unexpected roots %+v", cfg)
	}
	if cfg.CachePath != filepath.Join(root, "cache", "transpane", "translations.sqlite3") {
		t.Fatalf("cache %s", cfg.CachePath)
	}
}

func TestEmptyClaudeDirFallsBackToHome(t *testing.T) {
	env, root := writeConfig(t, "[translation]\nmodel = \"m\"\n")
	delete(env, "CLAUDE_CONFIG_DIR")
	env["CLAUDE_CONFIG_DIR"] = ""
	cfg, err := LoadFrom(env, root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeHome != filepath.Join(root, ".claude") {
		t.Fatalf("claude home = %s", cfg.ClaudeHome)
	}
}

func TestMissingModel(t *testing.T) {
	env, root := writeConfig(t, "[translation]\nform = 'polite'\n")
	if _, err := LoadFrom(env, root); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnknownKeys(t *testing.T) {
	for _, body := range []string{
		"[translation]\nmodel = \"m\"\nworker_count = 4\n",
		"model = \"m\"\n",
	} {
		env, root := writeConfig(t, body)
		if _, err := LoadFrom(env, root); err == nil {
			t.Fatalf("expected unknown key error for %q", body)
		}
	}
}

func TestInvalidForm(t *testing.T) {
	env, root := writeConfig(t, "[translation]\nmodel = \"m\"\nform = \"casual\"\n")
	if _, err := LoadFrom(env, root); err == nil {
		t.Fatal("expected error")
	}
}

func TestBrokenTOML(t *testing.T) {
	env, root := writeConfig(t, "[translation\n")
	if _, err := LoadFrom(env, root); err == nil {
		t.Fatal("expected error")
	}
}

func TestMissingConfigUsesDefaults(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{
		"XDG_CONFIG_HOME":   filepath.Join(root, "config"),
		"XDG_CACHE_HOME":    filepath.Join(root, "cache"),
		"CODEX_HOME":        filepath.Join(root, "codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude"),
	}
	cfg, err := LoadFrom(env, root)
	if err == nil {
		t.Fatal("model is required even without a config file")
	}
	_ = cfg
}
