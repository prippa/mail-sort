package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const fileName = "config.yaml"

// Dir is the directory that holds config.yaml.
// Linux uses $XDG_CONFIG_HOME/mailsorter (or ~/.config/mailsorter).
// Windows uses %AppData%\MailSorter.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(base, appDir(runtime.GOOS)), nil
}

// Path is the default config.yaml location.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// StateDir is where logs and the SQLite database live.
// Linux uses $XDG_STATE_HOME/mailsorter (or ~/.local/state/mailsorter).
// Windows uses %LocalAppData%\MailSorter.
// A relative XDG_STATE_HOME is ignored so a crafted environment cannot redirect
// state into the working directory.
func StateDir() (string, error) {
	if runtime.GOOS == "windows" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("state dir: %w", err)
		}
		return filepath.Join(base, appDir("windows")), nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, appDir("linux")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("state dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", appDir("linux")), nil
}

func appDir(goos string) string {
	if goos == "windows" {
		return "MailSorter"
	}
	return "mailsorter"
}
