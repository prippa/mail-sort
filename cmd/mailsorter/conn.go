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
	"strings"
	"time"

	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/logging"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/message"
	"github.com/prippa/mail-sort/internal/oauth"
	"github.com/prippa/mail-sort/internal/secrets"
)

func runTestConn(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("test-conn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("profile", "", "profile name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || fs.NArg() != 0 {
		if err := writeString(stderr, "mailsorter: test-conn requires --profile NAME\n"); err != nil {
			return 1
		}
		return 2
	}
	return withSession(cfg, *name, stderr, 2*time.Minute, func(ctx context.Context, session *mail.Session, account mail.Account, logger *slog.Logger) int {
		caps, err := session.Capabilities(ctx)
		if err != nil {
			return loggedError(logger, stderr, err)
		}
		folders, err := session.ListFolders(ctx)
		if err != nil {
			return loggedError(logger, stderr, err)
		}
		logger.Info("test-conn",
			slog.String("host", account.Endpoint.Host),
			slog.Int("folders", len(folders)),
		)
		text := fmt.Sprintf("host: %s:%d\nsecurity: %s\nMOVE: %s\nUIDPLUS: %s\nIDLE: %s\nSPECIAL-USE: %s\nX-GM-EXT-1: %s\nfolders:\n",
			account.Endpoint.Host, account.Endpoint.Port, account.Endpoint.Security,
			yesNo(caps.Move), yesNo(caps.UIDPlus), yesNo(caps.Idle), yesNo(caps.SpecialUse), yesNo(caps.GmailExt))
		if err := writeString(stdout, text); err != nil {
			return 1
		}
		for _, folder := range folders {
			line := "  " + folder.Name
			if len(folder.SpecialUse) > 0 {
				line += " " + strings.Join(folder.SpecialUse, " ")
			}
			if folder.Delimiter != 0 {
				line += fmt.Sprintf(" delim=%q", string(folder.Delimiter))
			}
			if err := writeString(stdout, line+"\n"); err != nil {
				return 1
			}
		}
		return 0
	})
}

func runDevClean(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev-clean", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("profile", "", "profile name")
	folder := fs.String("folder", "INBOX", "folder to read")
	limit := fs.Int("limit", 5, "newest messages to print")
	maxChars := fs.Int("max-chars", 0, "body cap in runes; 0 uses the profile or 1500")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || fs.NArg() != 0 || *limit < 1 || *limit > 100 || *maxChars < 0 || *maxChars > 100000 {
		if err := writeString(stderr, "mailsorter: dev-clean requires --profile NAME [--folder INBOX] [--limit 5] [--max-chars N]\n"); err != nil {
			return 1
		}
		return 2
	}
	return withSession(cfg, *name, stderr, 2*time.Minute, func(ctx context.Context, session *mail.Session, _ mail.Account, logger *slog.Logger) int {
		profile, err := findProfile(cfg, *name)
		if err != nil {
			return writeError(stderr, err)
		}
		chars := profile.MaxChars
		if *maxChars > 0 {
			chars = *maxChars
		}
		msgs, err := session.FetchNewest(ctx, *folder, *limit, chars)
		if err != nil {
			return loggedError(logger, stderr, err)
		}
		logger.Info("dev-clean", slog.String("folder", *folder), slog.Int("messages", len(msgs)))
		if err := writeString(stdout, fmt.Sprintf("folder: %s\nmessages: %d\n", *folder, len(msgs))); err != nil {
			return 1
		}
		for _, msg := range msgs {
			if err := writeString(stdout, formatMessage(msg)); err != nil {
				return 1
			}
		}
		return 0
	})
}

func withSession(cfg config.Config, name string, stderr io.Writer, timeout time.Duration, run func(context.Context, *mail.Session, mail.Account, *slog.Logger) int) int {
	profile, err := findProfile(cfg, name)
	if err != nil {
		return writeError(stderr, err)
	}
	logger, closer, err := openLogger()
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = closer.Close() }()
	account, err := resolveAccount(profile)
	if err != nil {
		return loggedError(logger, stderr, err)
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if account.Discover {
		found, err := mail.DiscoverIMAP(ctx, account.Email, mail.Discover{})
		if err != nil {
			return loggedError(logger, stderr, err)
		}
		account.Endpoint.Host = found.Host
		account.Endpoint.Port = found.Port
		account.Endpoint.Security = found.Security
	}
	logger.Info("connect", slog.String("profile", profile.Name), slog.String("host", account.Endpoint.Host))
	session, err := connectAccount(ctx, profile, account)
	if err != nil {
		return loggedError(logger, stderr, err)
	}
	defer session.Close()
	return run(ctx, session, account, logger)
}

func resolveAccount(profile config.Profile) (mail.Account, error) {
	dir, err := config.Dir()
	if err != nil {
		return mail.Account{}, err
	}
	presets, err := mail.LoadPresets(filepath.Join(dir, "presets.json"))
	if err != nil {
		return mail.Account{}, err
	}
	return mail.Resolve(profile, presets)
}

func findProfile(cfg config.Config, name string) (config.Profile, error) {
	for _, profile := range cfg.Profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	return config.Profile{}, fmt.Errorf("config: no profile named %q", name)
}

func connectAccount(ctx context.Context, profile config.Profile, account mail.Account) (*mail.Session, error) {
	if account.Auth == mail.AuthPassword {
		password, err := passwordFromEnv(profile.PasswordEnv)
		if err != nil {
			return nil, err
		}
		return mail.NewClient().Connect(ctx, account, password)
	}
	store, err := openSecrets()
	if err != nil {
		return nil, err
	}
	refresh, err := store.Get(secrets.RefreshAccount(profile.Name))
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, oauth.ErrNotSignedIn
	}
	if err != nil {
		return nil, err
	}
	oAccount, err := oauth.FromProfile(profile)
	if err != nil {
		return nil, err
	}
	source, err := oauth.NewSource(oAccount, refresh, func(next string) error {
		return store.Set(secrets.RefreshAccount(profile.Name), next)
	})
	if err != nil {
		return nil, err
	}
	return mail.NewClient().ConnectOAuth(ctx, account, source)
}

func passwordFromEnv(name string) (string, error) {
	if name == "" {
		return "", errors.New("config: profile has no password_env")
	}
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("config: environment variable %s is unset or empty", name)
	}
	return value, nil
}

func openLogger() (*slog.Logger, io.Closer, error) {
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, nil, err
	}
	if err := logging.EnsureDir(stateDir); err != nil {
		return nil, nil, err
	}
	return logging.Open(filepath.Join(stateDir, "mailsorter.log"), logging.Options{})
}

func loggedError(logger *slog.Logger, stderr io.Writer, err error) int {
	logger.Error("command failed", slog.String("error", err.Error()))
	code := writeError(stderr, err)
	var phase *mail.PhaseError
	if errors.As(err, &phase) {
		return 2
	}
	if code == 0 {
		return 1
	}
	return code
}

func formatMessage(msg message.Message) string {
	date := "-"
	if !msg.Date.IsZero() {
		date = msg.Date.UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "from: %s\n", formatAddresses(msg.From))
	fmt.Fprintf(&b, "to: %s\n", formatAddresses(msg.To))
	fmt.Fprintf(&b, "date: %s\n", date)
	fmt.Fprintf(&b, "subject: %s\n", msg.Subject)
	fmt.Fprintf(&b, "is_bulk: %t\n", msg.IsBulk)
	if len(msg.Attachments) == 0 {
		b.WriteString("attachments:\n")
	} else {
		b.WriteString("attachments:")
		for _, item := range msg.Attachments {
			fmt.Fprintf(&b, " %s (%s, %d bytes)", item.Name, item.MediaType, item.Size)
		}
		b.WriteByte('\n')
	}
	b.WriteString("body:\n")
	b.WriteString(msg.Body)
	if !strings.HasSuffix(msg.Body, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func formatAddresses(list []message.Address) string {
	if len(list) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(list))
	for _, addr := range list {
		if addr.Name != "" {
			parts = append(parts, addr.Name+" <"+addr.Email+">")
			continue
		}
		parts = append(parts, addr.Email)
	}
	return strings.Join(parts, ", ")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
