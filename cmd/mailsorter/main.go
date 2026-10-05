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
  ui          open the local web UI (default)
  run         dry-run, confirm, and file mail
  watch       watch a mailbox and file new mail (phase 6)
  undo        reverse a filed run
  test-conn   connect and show IMAP capabilities and folders
  dev-clean   print cleaned messages from one folder
  classify    classify one message from stdin
  categories  export the starter category list
  version     print the build version
  help        show this help

  test-conn --profile NAME
  dev-clean --profile NAME [--folder INBOX] [--limit 5] [--max-chars N]
  classify --stdin [--max-chars N]
  categories export
  run --profile NAME [--folder INBOX] [--limit 200]
  run --profile NAME --confirm
  run --profile NAME --override UID=category
  run --profile NAME --apply [--copy-only]
  undo --profile NAME [--run ID] [--uid UID]

The mailbox password is read from the environment variable named by password_env.
Google and Microsoft sign in from the local page. A refresh token stays in the keyring.
Jev reads TYPESAFE_API_KEY, then the keyring. Other classifiers read key_env, then the keyring.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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
			if _, _, err := loadConfig(*configPath); err != nil {
				return writeError(stderr, err)
			}
		}
		if err := writeString(stdout, buildinfo.Version+"\n"); err != nil {
			return 1
		}
		return 0
	case "test-conn":
		cfg, _, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runTestConn(cfg, fs.Args()[1:], stdout, stderr)
	case "dev-clean":
		cfg, _, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runDevClean(cfg, fs.Args()[1:], stdout, stderr)
	case "categories":
		return runCategories(fs.Args()[1:], stdout, stderr)
	case "classify":
		cfg, path, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runClassify(cfg, path, fs.Args()[1:], stdin, stdout, stderr)
	case "run":
		cfg, path, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runMail(cfg, path, fs.Args()[1:], stdout, stderr)
	case "undo":
		cfg, _, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runUndo(cfg, fs.Args()[1:], stdout, stderr)
	case "ui":
		cfg, path, err := loadConfig(*configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		return runUI(cfg, path, stdout, stderr)
	case "watch":
		if _, _, err := loadConfig(*configPath); err != nil {
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
		"watch": "6",
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

func loadConfig(explicit string) (config.Config, string, error) {
	path, present, err := resolveConfigPath(explicit)
	if err != nil {
		return config.Config{}, "", err
	}
	if !present {
		return config.Config{}, path, nil
	}
	cfg, err := config.Load(context.Background(), path)
	if err != nil {
		return config.Config{}, path, err
	}
	return cfg, path, nil
}

func resolveConfigPath(explicit string) (string, bool, error) {
	if explicit != "" {
		return explicit, true, nil
	}
	path, err := config.Path()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, false, nil
		}
		return "", false, fmt.Errorf("config: open %s: %w", path, err)
	}
	return path, true, nil
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
