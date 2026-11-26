package cli

import (
	"time"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/transcript"
)

func (a *App) runSessions(cfg config.Config, source *domain.Provider) int {
	sessions := transcript.DiscoverAll(cfg.SessionsRoot, cfg.ClaudeProjectsRoot, source)
	if len(sessions) == 0 {
		writeln(a.stdout(), "セッションが見つかりません")
		return ExitOK
	}
	writeln(a.stdout(), "取得元\tID\t最終更新\t形式\t区分\tcwd")
	for _, session := range sessions {
		updated := time.Unix(0, int64(session.UpdatedAt*1e9)).Format("2006-01-02 15:04:05")
		origin := session.OriginLabel()
		if origin == "" {
			origin = "-"
		}
		cwd := "-"
		if session.Cwd != nil {
			cwd = *session.Cwd
		}
		writef(a.stdout(), "%s\t%s\t%s\t%s\t%s\t%s\n", session.Provider.Label(), session.ThreadID, updated, session.ModeLabel(), origin, cwd)
	}
	return ExitOK
}

func (a *App) runInspect(cfg config.Config, ref string, source *domain.Provider) int {
	sessions := transcript.DiscoverAll(cfg.SessionsRoot, cfg.ClaudeProjectsRoot, source)
	session, err := transcript.SelectSession(sessions, ref, source)
	if err != nil {
		writef(a.stderr(), "セッションエラー: %s\n", err)
		return ExitUsage
	}
	reader := transcript.ReaderForSession(session, cfg.SessionsRoot, cfg.ClaudeProjectsRoot)
	result, err := reader.ScanAll()
	if err != nil {
		writeln(a.stderr(), "セッションエラー: ログを読み取れません")
		return ExitFailure
	}
	writef(a.stdout(), "取得元: %s\n", session.Provider.Label())
	writef(a.stdout(), "セッション: %s\n", session.ThreadID)
	cwd := "-"
	if session.Cwd != nil {
		cwd = *session.Cwd
	}
	writef(a.stdout(), "cwd: %s\n", cwd)
	version := "不明"
	if session.CLIVersion != nil {
		version = *session.CLIVersion
	}
	writef(a.stdout(), "version: %s\n", version)
	mode := "未対応"
	if result.HistoryMode != nil {
		mode = *result.HistoryMode
	}
	writef(a.stdout(), "形式: %s\n", mode)
	if result.UnsupportedMode != nil {
		writef(a.stdout(), "未対応の history_mode: %s\n", *result.UnsupportedMode)
	}
	if result.ThreadChanged != nil {
		writeln(a.stdout(), "セッションID不一致を検出しました（本文は送信しません）")
	}
	stats := result.Stats
	writef(a.stdout(), "レコード数: %d\n", stats.Records)
	writef(a.stdout(), "イベント数: %d\n", stats.Events)
	writef(a.stdout(), "抽出数: %d\n", stats.Extracted)
	writef(a.stdout(), "無視数: %d\n", stats.Ignored)
	writef(a.stdout(), "不正行数: %d\n", stats.InvalidLines)
	writef(a.stdout(), "巨大行数: %d\n", stats.OversizedLines)
	if result.UnsupportedMode != nil {
		writeln(a.stdout(), "未対応形式のため本文の翻訳は行いません。")
	} else if stats.Extracted == 0 {
		writeln(a.stdout(), "対象イベント未検出（抽出0件）。ログ形式または内部イベントのみの可能性を確認してください。")
	}
	return ExitOK
}
