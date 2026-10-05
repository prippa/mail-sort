package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/prippa/mail-sort/internal/buildinfo"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/logging"
)

const usageText = `MailSorter files mail into folders.

Usage:
  mailsorter [--config path] <command> [flags]

Global flags come before the command.

Commands:
  ui          open the local web UI (default; phase 4)
  run         classify and file mail (phase 3)
  watch       watch a mailbox and file new mail (phase 6)
  undo        reverse a run (phase 3)
  test-conn   connect and show IMAP capabilities and folders
  dev-clean   print cleaned messages from one folder
  classify    classify one message from stdin (phase 2)
  version     print the build version
  help        show this help

  test-conn --profile NAME
  dev-clean --profile NAME [--folder INBOX] [--limit 5] [--max-chars N]

The password is read from the environment variable named by password_env.
OAuth is not implemented yet.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mailsorter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Usage = func() {
		if err := writeString(stderr, usageText); err != nil {
			return
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cmd := "ui"
	if rest := fs.Args(); len(rest) > 0 {
		cmd = rest[0]
	}
	switch cmd {
	case "help":
		if err := writeString(stdout, usageText); err != nil {
			return 1
		}
		return 0
	case "version":
		if *configPath != "" {
			if _, err := loadConfig(*configPath); err != nil {
				return writeError(stderr, err)
			}
		}
		if err := writeString(stdout, buildinfo.Version+"\n"); err != nil {
			return 1
		}
		return 0
	case "test-conn":
		cfg, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runTestConn(cfg, fs.Args()[1:], stdout, stderr)
	case "dev-clean":
		cfg, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runDevClean(cfg, fs.Args()[1:], stdout, stderr)
	case "ui", "run", "watch", "undo", "classify":
		if _, err := loadConfig(*configPath); err != nil {
			return writeError(stderr, err)
		}
		return notImplemented(cmd, stderr)
	default:
		if err := writeString(stderr, "mailsorter: unknown command "+quote(cmd)+"\n"+usageText); err != nil {
			return 1
		}
		return 2
	}
}

func notImplemented(cmd string, stderr io.Writer) int {
	phase, ok := map[string]string{
		"classify": "2",
		"run":      "3",
		"undo":     "3",
		"ui":       "4",
		"watch":    "6",
	}[cmd]
	if !ok {
		phase = "?"
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return writeError(stderr, err)
	}
	if err := logging.EnsureDir(stateDir); err != nil {
		return writeError(stderr, err)
	}
	logger, closer, err := logging.Open(filepath.Join(stateDir, "mailsorter.log"), logging.Options{})
	if err != nil {
		return writeError(stderr, err)
	}
	logger.Info("command not implemented", slog.String("command", cmd), slog.String("phase", phase))
	if err := closer.Close(); err != nil {
		return writeError(stderr, err)
	}
	message := fmt.Sprintf("mailsorter: %s is not implemented yet (phase %s)\n", cmd, phase)
	if err := writeString(stderr, message); err != nil {
		return 1
	}
	return 2
}

func loadConfig(explicit string) (config.Config, error) {
	path := explicit
	if path == "" {
		var err error
		path, err = config.Path()
		if err != nil {
			return config.Config{}, err
		}
		_, err = os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return config.Config{}, nil
		}
		if err != nil {
			return config.Config{}, fmt.Errorf("config: open %s: %w", path, err)
		}
	}
	return config.Load(context.Background(), path)
}

func writeError(stderr io.Writer, err error) int {
	if writeErr := writeString(stderr, "mailsorter: "+err.Error()+"\n"); writeErr != nil {
		return 1
	}
	return 1
}

func writeString(w io.Writer, text string) error {
	_, err := io.WriteString(w, text)
	return err
}

func quote(s string) string {
	return fmt.Sprintf("%q", s)
}
