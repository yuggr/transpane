// Package cli implements the command grammar, diagnostics and TUI entry.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/transcript"
	"github.com/yuggr/transpane/internal/translate"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Version and Commit are set at build time.
var (
	Version = "0.1.0"
	Commit  = "unknown"
)

// TUIStarter launches the viewer.
type TUIStarter func(context.Context, config.Config, *domain.SessionInfo, string, *domain.Provider) error

// ListModelsFunc is the doctor translation probe.
type ListModelsFunc func(context.Context, config.Config) ([]string, error)

// App holds injectable process state for tests.
type App struct {
	Out        io.Writer
	Err        io.Writer
	Env        map[string]string
	Home       string
	StartTUI   TUIStarter
	ListModels ListModelsFunc
	RootState  func(string) string
}

// Run executes one command and returns the documented exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	parsed, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.stderr(), "usage: transpane [-h] [--version] [--source {all,codex,claude}] [--cwd PATH | --session ID] ...\ntranspane: error: %s\n", err)
		return ExitUsage
	}
	if parsed.help {
		fmt.Fprint(a.stdout(), helpText)
		return ExitOK
	}
	if parsed.version {
		fmt.Fprintf(a.stdout(), "transpane %s (%s, %s)\n", Version, Commit, runtime.Version())
		return ExitOK
	}

	cfg, err := config.LoadFrom(a.environment(), a.home())
	if err != nil {
		fmt.Fprintf(a.stderr(), "設定エラー: %s\n", err)
		return ExitUsage
	}
	source := sourceFor(parsed.source)
	switch parsed.command {
	case "doctor":
		return a.runDoctor(ctx, cfg, source)
	case "sessions":
		return a.runSessions(cfg, source)
	case "inspect":
		return a.runInspect(cfg, parsed.inspectSession, source)
	default:
		return a.runTUI(ctx, cfg, parsed.session, parsed.cwd, source)
	}
}

func sourceFor(value string) *domain.Provider {
	switch value {
	case "codex":
		provider := domain.ProviderCodex
		return &provider
	case "claude":
		provider := domain.ProviderClaude
		return &provider
	default:
		return nil
	}
}

func (a *App) stdout() io.Writer {
	if a.Out != nil {
		return a.Out
	}
	return os.Stdout
}

func (a *App) stderr() io.Writer {
	if a.Err != nil {
		return a.Err
	}
	return os.Stderr
}

func (a *App) environment() map[string]string {
	if a.Env != nil {
		return a.Env
	}
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := cutEnv(item)
		if ok {
			env[key] = value
		}
	}
	return env
}

func cutEnv(item string) (string, string, bool) {
	for index := 0; index < len(item); index++ {
		if item[index] == '=' {
			return item[:index], item[index+1:], true
		}
	}
	return item, "", false
}

func (a *App) home() string {
	if a.Home != "" {
		return a.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

func (a *App) runTUI(ctx context.Context, cfg config.Config, sessionID, cwd string, source *domain.Provider) int {
	var initial *domain.SessionInfo
	if sessionID != "" {
		sessions := transcript.DiscoverAll(cfg.SessionsRoot, cfg.ClaudeProjectsRoot, source)
		selected, err := transcript.SelectSession(sessions, sessionID, source)
		if err != nil {
			fmt.Fprintf(a.stderr(), "セッションエラー: %s\n", err)
			return ExitUsage
		}
		initial = &selected
	}
	if a.StartTUI == nil {
		return ExitOK
	}
	if err := a.StartTUI(ctx, cfg, initial, cwd, source); err != nil {
		fmt.Fprintf(a.stderr(), "画面エラー: %s\n", err)
		return ExitFailure
	}
	return ExitOK
}

func defaultListModels(ctx context.Context, cfg config.Config) ([]string, error) {
	client := translate.NewClient(cfg.BaseURL, cfg.Model, cfg.Form)
	defer client.Close()
	return client.ListModels(ctx)
}

// Main is the process entry point. The TUI is wired by cmd/transpane.
func Main(ctx context.Context, start TUIStarter) int {
	app := &App{StartTUI: start}
	return app.Run(ctx, os.Args[1:])
}
