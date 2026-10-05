package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/ui"
)

var (
	newUIContext  = defaultUIContext
	launchBrowser = true
)

func defaultUIContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runUI(cfg config.Config, configPath string, stdout, stderr io.Writer) int {
	ctx, stop := newUIContext()
	defer stop()
	logger, closer, err := openLogger()
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = closer.Close() }()
	srv, err := ui.Start(ctx, ui.Options{ConfigPath: configPath, Logger: logger})
	if err != nil {
		logger.Error("ui failed", slog.String("error", err.Error()))
		return writeError(stderr, err)
	}
	lang := displayLang(cfg)
	if err := writeString(stdout, i18n.T(lang, "ui.open")+"\n"+srv.URL+"\n"+i18n.T(lang, "ui.stay")+"\n"); err != nil {
		_ = srv.Shutdown(context.Background())
		return 1
	}
	if launchBrowser {
		openBrowser(srv.URL)
	}
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	return 0
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
