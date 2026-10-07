package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/engine"
	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/oauth"
	"github.com/prippa/mail-sort/internal/secrets"
	"github.com/prippa/mail-sort/internal/store"
)

const watchUsageText = `usage: mailsorter watch --profile NAME [--folder INBOX] [--poll 5m] [--limit 200] [--unread] [--since YYYY-MM-DD] [--max-chars N] [--workers 80] [--max-moves 200] [--copy-only] [--include-flagged] [--include-drafts]
`

func runWatch(cfg config.Config, configPath string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name")
	folder := fs.String("folder", "INBOX", "source mailbox")
	poll := fs.Duration("poll", mail.DefaultPoll, "how often to check when IDLE is unavailable")
	limit := fs.Int("limit", engine.DefaultLimit, "newest messages to read")
	unread := fs.Bool("unread", false, "only unread messages")
	sinceText := fs.String("since", "", "only messages on or after YYYY-MM-DD")
	maxChars := fs.Int("max-chars", 0, "body cap in runes")
	workers := fs.Int("workers", engine.DefaultWorkers, "classifier workers")
	maxMoves := fs.Int("max-moves", engine.DefaultMaxMoves, "moves and copies before pausing")
	copyOnly := fs.Bool("copy-only", false, "copy instead of moving")
	includeFlagged := fs.Bool("include-flagged", false, "include flagged messages")
	includeDrafts := fs.Bool("include-drafts", false, "include drafts")
	fs.Usage = func() {
		if err := writeString(stderr, watchUsageText); err != nil {
			return
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *profile == "" || fs.NArg() != 0 || *limit < 1 || *limit > 10000 || *workers < 1 || *workers > engine.MaxWorkers || *maxMoves < 1 || *maxMoves > 100000 || *maxChars < 0 || *maxChars > 100000 || *poll < time.Second || *poll > 30*time.Minute {
		fs.Usage()
		return 2
	}
	var since time.Time
	if *sinceText != "" {
		parsed, err := time.Parse("2006-01-02", *sinceText)
		if err != nil {
			fs.Usage()
			return 2
		}
		since = parsed.UTC()
	}
	accountProfile, err := findProfile(cfg, *profile)
	if err != nil {
		return writeError(stderr, err)
	}
	lang := displayLang(cfg)
	db, err := openState()
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = db.Close() }()
	ok, err := db.Confirmed(context.Background(), *profile)
	if err != nil {
		return writeError(stderr, err)
	}
	if !ok {
		return writeNote(stderr, i18n.T(lang, "run.not_confirmed"))
	}
	openRun, _, err := db.LatestOpen(context.Background(), *profile)
	if err == nil && openRun.Status == store.StatusDry {
		return writeNote(stderr, i18n.T(lang, "watch.open_plan"))
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return writeError(stderr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger, closer, err := openLogger()
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = closer.Close() }()
	set, usingStarter, err := loadCategories(cfg, configPath)
	if err != nil {
		return writeError(stderr, err)
	}
	if usingStarter {
		if err := writeString(stderr, "mailsorter: using starter categories\n"); err != nil {
			return 1
		}
	}
	dbPath, err := stateDBPath()
	if err != nil {
		return writeError(stderr, err)
	}
	cache, err := classify.OpenCache(ctx, dbPath)
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = cache.Close() }()
	keys, err := newSecretLookup()
	if err != nil {
		return writeError(stderr, err)
	}
	providers, err := classify.Providers(cfg.Classifiers, keys.get, logger, nil)
	if keys.err != nil {
		err = keys.err
	}
	if err != nil {
		return writeError(stderr, err)
	}
	chars := accountProfile.MaxChars
	if *maxChars > 0 {
		chars = *maxChars
	}
	plan := engine.PlanOptions{
		Profile: *profile,
		Mailbox: *folder,
		Workers: *workers,
		Read: mail.ReadOptions{
			UnreadOnly:     *unread,
			Since:          since,
			Limit:          *limit,
			SkipFlagged:    !*includeFlagged,
			SkipDrafts:     !*includeDrafts,
			MaxChars:       chars,
			AllowProtected: flagVisited(fs, "folder"),
		},
	}
	apply := engine.ApplyOptions{Profile: *profile, CopyOnly: *copyOnly, MaxMoves: *maxMoves}
	clf := chain{set: set, providers: providers, cache: cache, privacy: cfg.Privacy}
	allowProtected := flagVisited(fs, "folder")

	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return writeStopped(stdout, lang)
		}
		err := watchSession(ctx, accountProfile, *folder, *poll, allowProtected, plan, apply, clf, db, stdout, lang, logger)
		if err == nil || errors.Is(err, context.Canceled) {
			return writeStopped(stdout, lang)
		}
		if !watchAgain(err) {
			logger.Error("watch stopped", slog.String("profile", *profile), slog.String("error", err.Error()))
			return writeEngineError(stderr, lang, err)
		}
		logger.Info("watch reconnect", slog.String("profile", *profile), slog.Int("attempt", attempt+1), slog.String("error", err.Error()))
		if err := wait(ctx, watchDelay(attempt)); err != nil {
			return writeStopped(stdout, lang)
		}
		attempt++
	}
}

func watchSession(ctx context.Context, profile config.Profile, folder string, poll time.Duration, allowProtected bool, plan engine.PlanOptions, apply engine.ApplyOptions, clf chain, db *store.DB, stdout io.Writer, lang i18n.Lang, logger *slog.Logger) error {
	account, err := resolveAccount(profile)
	if err != nil {
		return err
	}
	if account.Discover {
		found, err := mail.DiscoverIMAP(ctx, account.Email, mail.Discover{})
		if err != nil {
			return err
		}
		account.Endpoint.Host = found.Host
		account.Endpoint.Port = found.Port
		account.Endpoint.Security = found.Security
	}
	logger.Info("connect", slog.String("profile", profile.Name), slog.String("host", account.Endpoint.Host))
	session, err := connectAccount(ctx, profile, account)
	if err != nil {
		return err
	}
	defer session.Close()
	caps, err := session.Capabilities(ctx)
	if err != nil {
		return err
	}
	mode := "poll"
	if caps.Idle {
		mode = "idle"
	}
	logger.Info("watch", slog.String("profile", profile.Name), slog.String("mailbox", folder), slog.String("mode", mode))
	if err := writeString(stdout, fmt.Sprintf("watching: %s %s %s\n", profile.Name, folder, mode)); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		report, changed, err := engine.Next(ctx, session, clf, db, plan, apply)
		if err != nil {
			return err
		}
		if changed {
			logRun(logger, "watch", report, time.Since(started))
			note := i18n.T(lang, "run.applied")
			if report.Aborted {
				note = i18n.T(lang, "run.stopped")
			} else if report.Pending > 0 {
				note = i18n.T(lang, "run.pending")
			} else if report.CopyOnlyRefused {
				note = i18n.T(lang, "run.copy_only")
			}
			if err := writeReport(stdout, report, "apply", lang, clf.set.Categories, time.Since(started), note); err != nil {
				return err
			}
		}
		if report.Pending > 0 {
			if err := wait(ctx, poll); err != nil {
				return err
			}
			continue
		}
		wake, err := session.Wait(ctx, folder, poll, allowProtected)
		if err != nil {
			return err
		}
		logger.Info("watch wake", slog.String("profile", profile.Name), slog.String("mailbox", folder), slog.String("wake", string(wake)))
	}
}

func watchAgain(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, engine.ErrNotConfirmed) || errors.Is(err, engine.ErrOpenPlan) || errors.Is(err, mail.ErrProtected) {
		return false
	}
	if errors.Is(err, oauth.ErrNotSignedIn) || errors.Is(err, oauth.ErrReauth) || errors.Is(err, oauth.ErrNoClientID) || errors.Is(err, secrets.ErrMasterPassword) {
		return false
	}
	if strings.HasPrefix(err.Error(), "config:") {
		return false
	}
	return true
}

func watchDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 9 {
		attempt = 9
	}
	base := min(time.Second<<attempt, 5*time.Minute)
	return base + time.Duration(rand.IntN(1000))*time.Millisecond
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeStopped(w io.Writer, lang i18n.Lang) int {
	if err := writeString(w, "mailsorter: "+i18n.T(lang, "watch.stopped")+"\n"); err != nil {
		return 1
	}
	return 0
}
