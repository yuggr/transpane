package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/yuggr/transpane/internal/cache"
	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/transcript"
	"github.com/yuggr/transpane/internal/translate"
	"golang.org/x/sys/unix"
)

var rootMessages = map[string]string{
	"missing":    "存在しません",
	"invalid":    "ディレクトリではありません",
	"unreadable": "読み取れません",
}

func defaultRootState(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing"
		}
		return "unreadable"
	}
	if !info.IsDir() {
		return "invalid"
	}
	if err := unix.Access(path, unix.R_OK|unix.X_OK); err != nil {
		return "unreadable"
	}
	return "ok"
}

func (a *App) rootState(path string) string {
	if a.RootState != nil {
		return a.RootState(path)
	}
	return defaultRootState(path)
}

func (a *App) printRoot(label, state string, required bool) {
	switch {
	case state == "ok":
		writef(a.stdout(), "  [OK] 読み取り可能 (%s)\n", label)
	case required:
		writef(a.stdout(), "  [NG] %s (%s)\n", rootMessages[state], label)
	default:
		writef(a.stdout(), "  [WARN] %s (%s: この取得元は利用できません)\n", rootMessages[state], label)
	}
}

func (a *App) doctorRoots(cfg config.Config, source *domain.Provider) (bool, string) {
	codexState := a.rootState(cfg.SessionsRoot)
	claudeState := a.rootState(cfg.ClaudeProjectsRoot)
	switch {
	case source == nil:
		a.printRoot("Codex", codexState, false)
		a.printRoot("Claude Code", claudeState, false)
		return codexState == "ok" || claudeState == "ok", claudeState
	case *source == domain.ProviderCodex:
		a.printRoot("Codex", codexState, true)
		a.printRoot("Claude Code", claudeState, false)
		return codexState == "ok", claudeState
	default:
		a.printRoot("Codex", codexState, false)
		a.printRoot("Claude Code", claudeState, true)
		return claudeState == "ok", claudeState
	}
}

func (a *App) doctorClaudeCounts(cfg config.Config, source *domain.Provider, claudeState string) {
	if claudeState != "ok" {
		return
	}
	if source != nil && *source != domain.ProviderClaude {
		return
	}
	confirmed, unconfirmed := transcript.ClaudeTranscriptCounts(cfg.ClaudeProjectsRoot)
	line := fmt.Sprintf("  Claude transcript: ID確認済み %d 件", confirmed)
	if unconfirmed > 0 {
		line += fmt.Sprintf(" / ID未確認 %d 件（候補外）", unconfirmed)
	}
	writeln(a.stdout(), line)
}

func (a *App) doctorCache(cfg config.Config) bool {
	instance := cache.Open(cfg.CachePath)
	defer func() { _ = instance.Close() }()
	status := instance.Status()
	writef(a.stdout(), "キャッシュ: %s\n", status.Path)
	if status.Available {
		writeln(a.stdout(), "  [OK] 利用可能")
		return true
	}
	writef(a.stdout(), "  [NG] %s\n", status.Message)
	return false
}

func (a *App) doctorTranslation(ctx context.Context, cfg config.Config) bool {
	writef(a.stdout(), "翻訳サーバー: %s / model=%s / form=%s\n", cfg.BaseURL, cfg.Model, cfg.Form)
	listModels := a.ListModels
	if listModels == nil {
		listModels = defaultListModels
	}
	models, err := listModels(ctx, cfg)
	if err != nil {
		kind := translate.ErrorKindConnect
		var failure *translate.Failure
		if errors.As(err, &failure) {
			kind = failure.Kind
		}
		writef(a.stdout(), "  [NG] %s\n", kind.Label())
		return false
	}
	writef(a.stdout(), "  [OK] 接続できました（モデル %d 件）\n", len(models))
	for _, model := range models {
		if model == cfg.Model {
			writef(a.stdout(), "  [OK] モデル %s が見つかりました\n", cfg.Model)
			return true
		}
	}
	writef(a.stdout(), "  [NG] モデル %s が /v1/models にありません\n", cfg.Model)
	for _, model := range models {
		writef(a.stdout(), "        - %s\n", model)
	}
	return false
}

func (a *App) runDoctor(ctx context.Context, cfg config.Config, source *domain.Provider) int {
	writef(a.stdout(), "設定ファイル: %s\n", configPathLabel(cfg.ConfigPath))
	writef(a.stdout(), "Codex home: %s\n", cfg.CodexHome)
	writef(a.stdout(), "Codex セッションディレクトリ: %s\n", cfg.SessionsRoot)
	writef(a.stdout(), "Claude home: %s\n", cfg.ClaudeHome)
	writef(a.stdout(), "Claude プロジェクトディレクトリ: %s\n", cfg.ClaudeProjectsRoot)

	rootsOK, claudeState := a.doctorRoots(cfg, source)
	a.doctorClaudeCounts(cfg, source, claudeState)
	cacheOK := a.doctorCache(cfg)
	translationOK := a.doctorTranslation(ctx, cfg)
	if rootsOK && cacheOK && translationOK {
		return ExitOK
	}
	return ExitFailure
}

func configPathLabel(path string) string {
	if path == "" {
		return "なし（既定値）"
	}
	return path
}
