package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yuggr/transpane/internal/cli"
	"github.com/yuggr/transpane/internal/config"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := func(ctx context.Context, cfg config.Config, initial *domain.SessionInfo, cwd string, source *domain.Provider) error {
		return tui.Run(ctx, cfg, initial, cwd, source)
	}
	os.Exit(cli.Main(ctx, start))
}
