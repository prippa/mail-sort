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

const usageText = `MailSorter files mail into folders. This build is the phase 0 scaffold.

Usage:
  mailsorter [command] [--config path]

Global flags come before the command.

Commands:
  ui          open the local web UI (default; phase 4)
  run         classify and file mail (phase 3)
  watch       watch a mailbox and file new mail (phase 6)
  undo        reverse a run (phase 3)
  test-conn   connect and show IMAP capabilities (phase 1)
  classify    classify one message from stdin (phase 2)
  version     print the build version
  help        show this help

version is the only command this build runs. Passwords, API keys, and tokens
are rejected if a config file contains them.
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
			if err := loadConfig(*configPath); err != nil {
				return writeError(stderr, err)
			}
		}
		if err := writeString(stdout, buildinfo.Version+"\n"); err != nil {
			return 1
		}
		return 0
	case "ui", "run", "watch", "undo", "test-conn", "classify":
		if err := loadConfig(*configPath); err != nil {
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
		"test-conn": "1",
		"classify":  "2",
		"run":       "3",
		"undo":      "3",
		"ui":        "4",
		"watch":     "6",
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

func loadConfig(explicit string) error {
	path := explicit
	if path == "" {
		var err error
		path, err = config.Path()
		if err != nil {
			return err
		}
		_, err = os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("config: open %s: %w", path, err)
		}
	}
	_, err := config.Load(context.Background(), path)
	return err
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
