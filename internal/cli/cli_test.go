package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/translate"
)

const (
	userText   = "保存処理を調べて"
	secretBody = "SECRET BODY SHOULD NOT BE PRINTED"
)

func codexMeta(id string) map[string]any {
	return map[string]any{"timestamp": "2026-11-25T01:00:00Z", "type": "session_meta", "payload": map[string]any{
		"id": id, "cwd": "/tmp/project", "cli_version": "0.169.0", "history_mode": "paginated",
	}}
}

func codexRecords(id string) []map[string]any {
	return []map[string]any{
		codexMeta(id),
		{"timestamp": "2026-11-25T01:00:01Z", "type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "UserMessage", "id": "u1", "content": []any{map[string]any{"type": "text", "text": userText}}}}},
		{"timestamp": "2026-11-25T01:00:02Z", "type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "id": "a1", "phase": "commentary", "content": []any{map[string]any{"type": "Text", "text": secretBody}}}}},
		{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant"}},
	}
}

func writeSessions(t *testing.T, env map[string]string, id string) string {
	t.Helper()
	dir := filepath.Join(env["CODEX_HOME"], "sessions", "2026", "11", "25")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-"+id+".jsonl")
	file, err := os.Create(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, record := range codexRecords(id) {
		_ = encoder.Encode(record)
	}
	_ = file.Close()
	return path
}

func testEnv(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "transpane")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[translation]\nbase_url = \"http://127.0.0.1:1234/v1\"\nmodel = \"plamo-3-translate-2b\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"CODEX_HOME":        filepath.Join(root, "codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude"),
		"XDG_CONFIG_HOME":   filepath.Join(root, "config"),
		"XDG_CACHE_HOME":    filepath.Join(root, "cache"),
	}
}

type appResult struct {
	code int
	out  string
	err  string
}

func runApp(t *testing.T, env map[string]string, args []string, configure func(*App)) appResult {
	t.Helper()
	var out, errBuf bytes.Buffer
	app := &App{Out: &out, Err: &errBuf, Env: env, Home: filepath.Dir(env["XDG_CONFIG_HOME"])}
	if configure != nil {
		configure(app)
	}
	code := app.Run(context.Background(), args)
	return appResult{code: code, out: out.String(), err: errBuf.String()}
}

func TestSessionsListsMetadataWithoutBody(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	result := runApp(t, env, []string{"sessions"}, nil)
	if result.code != 0 || !strings.Contains(result.out, "t") || !strings.Contains(result.out, "/tmp/project") || !strings.Contains(result.out, "paginated") {
		t.Fatalf("%+v", result)
	}
	if strings.Contains(result.out, userText) || strings.Contains(result.out, secretBody) {
		t.Fatal("body printed")
	}
}

func TestInspectCountsWithoutBodyOrCache(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	result := runApp(t, env, []string{"inspect", "--session", "t"}, nil)
	if result.code != 0 || !strings.Contains(result.out, "形式: paginated") || !strings.Contains(result.out, "レコード数: 4") || !strings.Contains(result.out, "抽出数: 2") || !strings.Contains(result.out, "無視数: 1") {
		t.Fatalf("%+v", result)
	}
	if strings.Contains(result.out, userText) || strings.Contains(result.out, secretBody) {
		t.Fatal("body printed")
	}
	if _, err := os.Stat(filepath.Join(env["XDG_CACHE_HOME"], "transpane", "translations.sqlite3")); !os.IsNotExist(err) {
		t.Fatal("inspect created cache")
	}
}

func TestInspectUnknownSession(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	result := runApp(t, env, []string{"inspect", "--session", "missing"}, nil)
	if result.code != 2 || !strings.Contains(result.err, "見つかりません") {
		t.Fatalf("%+v", result)
	}
}

func TestDoctorSuccessAndFailures(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	fake := func(context.Context, config.Config) ([]string, error) {
		return []string{"plamo-3-translate-2b", "other"}, nil
	}
	result := runApp(t, env, []string{"doctor"}, func(app *App) { app.ListModels = fake })
	if result.code != 0 || !strings.Contains(result.out, "[OK] モデル plamo-3-translate-2b") {
		t.Fatalf("%+v", result)
	}
	missing := func(context.Context, config.Config) ([]string, error) { return []string{"other-model"}, nil }
	result = runApp(t, env, []string{"doctor"}, func(app *App) { app.ListModels = missing })
	if result.code != 1 || !strings.Contains(result.out, "other-model") {
		t.Fatalf("%+v", result)
	}
	connect := func(context.Context, config.Config) ([]string, error) {
		return nil, &translate.Failure{Kind: translate.ErrorKindConnect}
	}
	result = runApp(t, env, []string{"doctor"}, func(app *App) { app.ListModels = connect })
	if result.code != 1 || !strings.Contains(result.out, "接続失敗") || !strings.Contains(result.out, "キャッシュ:") {
		t.Fatalf("%+v", result)
	}
}

func TestConfigErrorsExitTwo(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env["XDG_CONFIG_HOME"], "transpane", "config.toml")
	_ = os.WriteFile(path, []byte("[translation]\nmodel = ''\n"), 0o600)
	result := runApp(t, env, []string{"sessions"}, nil)
	if result.code != 2 || !strings.Contains(result.err, "設定エラー") {
		t.Fatalf("%+v", result)
	}
}

func TestHelpVersionAndGrammar(t *testing.T) {
	env := testEnv(t)
	env["XDG_CONFIG_HOME"] = filepath.Join(t.TempDir(), "none")
	if result := runApp(t, env, []string{"--help"}, nil); result.code != 0 {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"--version"}, nil); result.code != 0 || !strings.Contains(result.out, "transpane") {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"--session", "t", "--cwd", "/tmp/project"}, nil); result.code != 2 {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"sessions", "--source", "claude"}, nil); result.code != 2 {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"inspect"}, nil); result.code != 2 {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"--source", "bad", "sessions"}, nil); result.code != 2 {
		t.Fatalf("%+v", result)
	}
}

func TestSessionsStripsControlCharacters(t *testing.T) {
	env := testEnv(t)
	dir := filepath.Join(env["CODEX_HOME"], "sessions", "2026", "11", "25")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := codexMeta("t")
	meta["payload"].(map[string]any)["cwd"] = "/tmp/\x1b]52;c;ZXZpbA==\x07x"
	file, err := os.Create(filepath.Join(dir, "rollout-t.jsonl")) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(file).Encode(meta)
	_ = file.Close()
	result := runApp(t, env, []string{"sessions"}, nil)
	if result.code != 0 || strings.Contains(result.out, "\x1b]52") {
		t.Fatalf("%+v", result)
	}
}

func TestLongOptionPrefixesMatchArgparse(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	if result := runApp(t, env, []string{"--sou", "codex", "sessions"}, nil); result.code != 0 {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"--s", "codex", "sessions"}, nil); result.code != 2 || !strings.Contains(result.err, "ambiguous") {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"doctor", "--h"}, nil); result.code != 0 {
		t.Fatalf("%+v", result)
	}
}

func TestSessionsEmpty(t *testing.T) {
	env := testEnv(t)
	result := runApp(t, env, []string{"sessions"}, nil)
	if result.code != 0 || !strings.Contains(result.out, "見つかりません") {
		t.Fatalf("%+v", result)
	}
}

func TestPrefixedIDsAndSourceConflicts(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	if result := runApp(t, env, []string{"inspect", "--session", "codex:t"}, nil); result.code != 0 {
		t.Fatalf("%+v", result)
	}
	if result := runApp(t, env, []string{"--source", "claude", "inspect", "--session", "codex:t"}, nil); result.code != 2 || !strings.Contains(result.err, "矛盾") {
		t.Fatalf("%+v", result)
	}
}

func TestDoctorSourceRootRulesAndCounts(t *testing.T) {
	env := testEnv(t)
	okModels := func(context.Context, config.Config) ([]string, error) { return []string{"plamo-3-translate-2b"}, nil }
	result := runApp(t, env, []string{"--source", "claude", "doctor"}, func(app *App) { app.ListModels = okModels })
	if result.code != 1 || !strings.Contains(result.out, "[NG]") {
		t.Fatalf("%+v", result)
	}
	writeSessions(t, env, "t")
	result = runApp(t, env, []string{"--source", "codex", "doctor"}, func(app *App) { app.ListModels = okModels })
	if result.code != 0 || !strings.Contains(result.out, "[OK] 読み取り可能") {
		t.Fatalf("%+v", result)
	}
}

func TestDoctorReadsEachRootOnce(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	counts := map[string]int{}
	original := defaultRootState
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: env, Home: filepath.Dir(env["XDG_CONFIG_HOME"])}
	app.ListModels = func(context.Context, config.Config) ([]string, error) { return []string{"plamo-3-translate-2b"}, nil }
	app.RootState = func(path string) string { counts[path]++; return original(path) }
	if code := app.Run(context.Background(), []string{"doctor"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if counts[env["CODEX_HOME"]+"/sessions"] != 1 || counts[env["CLAUDE_CONFIG_DIR"]+"/projects"] != 1 {
		t.Fatalf("%+v", counts)
	}
}

func TestVersionBeforeConfig(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "missing")}
	result := runApp(t, env, []string{"--version"}, nil)
	if result.code != 0 || strings.Contains(result.err, "設定エラー") {
		t.Fatalf("%+v", result)
	}
}

func TestSourceMustPrecedeSubcommand(t *testing.T) {
	env := testEnv(t)
	if result := runApp(t, env, []string{"doctor", "--source", "claude"}, nil); result.code != 2 {
		t.Fatalf("%+v", result)
	}
}

func TestTUIHookGetsSelection(t *testing.T) {
	env := testEnv(t)
	writeSessions(t, env, "t")
	var got *domain.SessionInfo
	var gotCwd string
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Env: env, Home: filepath.Dir(env["XDG_CONFIG_HOME"])}
	app.StartTUI = func(_ context.Context, _ config.Config, initial *domain.SessionInfo, cwd string, _ *domain.Provider) error {
		got = initial
		gotCwd = cwd
		return nil
	}
	code := app.Run(context.Background(), []string{"--cwd", "/tmp/project"})
	if code != 0 || got != nil || gotCwd != "/tmp/project" {
		t.Fatalf("code=%d session=%+v cwd=%s", code, got, gotCwd)
	}
}
