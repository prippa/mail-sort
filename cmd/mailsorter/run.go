package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/engine"
	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/logging"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/store"
)

const runTimeout = 30 * time.Minute

const runUsageText = `usage: mailsorter run --profile NAME [--folder INBOX] [--limit 200] [--unread] [--since YYYY-MM-DD] [--max-chars N] [--workers 4] [--csv path]
       mailsorter run --profile NAME --confirm
       mailsorter run --profile NAME --override UID=category
       mailsorter run --profile NAME --apply [--copy-only] [--max-moves 200]
`

const undoUsageText = "usage: mailsorter undo --profile NAME [--run ID] [--uid UID]\n"

type overrideFlag []overrideSpec

type overrideSpec struct {
	uid      uint32
	category string
}

func (f *overrideFlag) String() string {
	parts := make([]string, len(*f))
	for i, item := range *f {
		parts[i] = strconv.FormatUint(uint64(item.uid), 10) + "=" + item.category
	}
	return strings.Join(parts, ",")
}

func (f *overrideFlag) Set(value string) error {
	uidText, category, ok := strings.Cut(value, "=")
	if !ok || category == "" {
		return errors.New("override must be UID=category")
	}
	n, err := strconv.ParseUint(uidText, 10, 32)
	if err != nil || n == 0 {
		return errors.New("override UID must be a positive integer")
	}
	*f = append(*f, overrideSpec{uid: uint32(n), category: category})
	return nil
}

type uidFlag []uint32

func (f *uidFlag) String() string {
	parts := make([]string, len(*f))
	for i, uid := range *f {
		parts[i] = strconv.FormatUint(uint64(uid), 10)
	}
	return strings.Join(parts, ",")
}

func (f *uidFlag) Set(value string) error {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 {
		return errors.New("uid must be a positive integer")
	}
	*f = append(*f, uint32(n))
	return nil
}

func runMail(cfg config.Config, configPath string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name")
	folder := fs.String("folder", "INBOX", "source mailbox")
	limit := fs.Int("limit", engine.DefaultLimit, "newest messages to read")
	unread := fs.Bool("unread", false, "only unread messages")
	sinceText := fs.String("since", "", "only messages on or after YYYY-MM-DD")
	maxChars := fs.Int("max-chars", 0, "body cap in runes")
	workers := fs.Int("workers", engine.DefaultWorkers, "classifier workers")
	maxMoves := fs.Int("max-moves", engine.DefaultMaxMoves, "moves and copies per apply")
	confirm := fs.Bool("confirm", false, "store the confirmation for this profile")
	apply := fs.Bool("apply", false, "file the latest dry run")
	copyOnly := fs.Bool("copy-only", false, "copy instead of moving")
	csvPath := fs.String("csv", "", "write the dry run to a CSV file")
	includeFlagged := fs.Bool("include-flagged", false, "include flagged messages")
	includeDrafts := fs.Bool("include-drafts", false, "include drafts")
	var overrides overrideFlag
	fs.Var(&overrides, "override", "UID=category on the latest dry run")
	fs.Usage = func() {
		if err := writeString(stderr, runUsageText); err != nil {
			return
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *profile == "" || fs.NArg() != 0 || *limit < 1 || *limit > 10000 || *workers < 1 || *workers > 32 || *maxMoves < 1 || *maxMoves > 100000 || *maxChars < 0 || *maxChars > 100000 {
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
	planning := !*confirm && !*apply && len(overrides) == 0
	if !validRunFlags(fs, planning, *apply) {
		fs.Usage()
		return 2
	}
	if _, err := findProfile(cfg, *profile); err != nil {
		return writeError(stderr, err)
	}
	lang := displayLang(cfg)
	db, err := openState()
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = db.Close() }()

	if *apply && !*confirm {
		ok, err := db.Confirmed(context.Background(), *profile)
		if err != nil {
			return writeError(stderr, err)
		}
		if !ok {
			return writeNote(stderr, i18n.T(lang, "run.not_confirmed"))
		}
	}

	needCategories := planning || len(overrides) > 0
	var set classify.Set
	var usingStarter bool
	if needCategories {
		set, usingStarter, err = loadCategories(cfg, configPath)
		if err != nil {
			return writeError(stderr, err)
		}
		if usingStarter {
			if err := writeString(stderr, "mailsorter: using starter categories\n"); err != nil {
				return 1
			}
		}
	}

	logger, closer, err := openLogger()
	if err != nil {
		return writeError(stderr, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = closer.Close()
		}
	}()
	finishLog := func() int {
		if err := closer.Close(); err != nil {
			closed = true
			return writeError(stderr, err)
		}
		closed = true
		return 0
	}

	if len(overrides) > 0 {
		for _, item := range overrides {
			row, err := engine.Override(context.Background(), db, *profile, item.uid, set.Categories, item.category)
			if err != nil {
				logger.Error("override failed", slog.String("error", err.Error()))
				return writeEngineError(stderr, lang, err)
			}
			if err := writeRow(stdout, row, lang, set.Categories); err != nil {
				return 1
			}
		}
		if !*apply && !*confirm {
			return finishLog()
		}
	}
	if *confirm && !*apply {
		if err := engine.Confirm(context.Background(), db, *profile); err != nil {
			logger.Error("confirm failed", slog.String("error", err.Error()))
			return writeEngineError(stderr, lang, err)
		}
		logger.Info("confirmed", slog.String("profile", *profile))
		if err := writeString(stdout, "mailsorter: "+i18n.T(lang, "run.confirmed")+"\n"); err != nil {
			return 1
		}
		return finishLog()
	}
	if *apply {
		if *confirm {
			if err := engine.Confirm(context.Background(), db, *profile); err != nil {
				logger.Error("confirm failed", slog.String("error", err.Error()))
				return writeEngineError(stderr, lang, err)
			}
		}
		folderSet := flagVisited(fs, "folder")
		if code := finishLog(); code != 0 {
			return code
		}
		return withSession(cfg, *profile, stderr, runTimeout, func(ctx context.Context, session *mail.Session, _ mail.Account, sessionLogger *slog.Logger) int {
			if folderSet {
				openRun, _, err := db.LatestOpen(ctx, *profile)
				if err != nil {
					sessionLogger.Error("apply failed", slog.String("error", err.Error()))
					return writeEngineError(stderr, lang, err)
				}
				if openRun.Mailbox != *folder {
					return writeError(stderr, fmt.Errorf("engine: dry run mailbox is %s", openRun.Mailbox))
				}
			}
			started := time.Now()
			report, err := engine.Apply(ctx, session, db, engine.ApplyOptions{
				Profile:  *profile,
				CopyOnly: *copyOnly,
				MaxMoves: *maxMoves,
			})
			if err != nil {
				sessionLogger.Error("apply failed", slog.String("error", err.Error()))
				return writeEngineError(stderr, lang, err)
			}
			logRun(sessionLogger, "apply", report, time.Since(started))
			note := i18n.T(lang, "run.applied")
			if report.Pending > 0 {
				note = i18n.T(lang, "run.pending")
			}
			if report.CopyOnlyRefused {
				note = i18n.T(lang, "run.copy_only")
			}
			if err := writeReport(stdout, report, "apply", lang, set.Categories, time.Since(started), note); err != nil {
				return 1
			}
			return 0
		})
	}

	accountProfile, err := findProfile(cfg, *profile)
	if err != nil {
		return writeError(stderr, err)
	}
	chars := accountProfile.MaxChars
	if *maxChars > 0 {
		chars = *maxChars
	}
	if code := finishLog(); code != 0 {
		return code
	}
	return withSession(cfg, *profile, stderr, runTimeout, func(ctx context.Context, session *mail.Session, _ mail.Account, sessionLogger *slog.Logger) int {
		dbPath, err := stateDBPath()
		if err != nil {
			sessionLogger.Error("run failed", slog.String("error", err.Error()))
			return writeError(stderr, err)
		}
		cache, err := classify.OpenCache(ctx, dbPath)
		if err != nil {
			sessionLogger.Error("run failed", slog.String("error", err.Error()))
			return writeError(stderr, err)
		}
		defer func() { _ = cache.Close() }()
		keys, err := newSecretLookup()
		if err != nil {
			sessionLogger.Error("run failed", slog.String("error", err.Error()))
			return writeError(stderr, err)
		}
		providers, err := classify.Providers(cfg.Classifiers, keys.get, sessionLogger, nil)
		if keys.err != nil {
			err = keys.err
		}
		if err != nil {
			sessionLogger.Error("run failed", slog.String("error", err.Error()))
			return writeError(stderr, err)
		}
		started := time.Now()
		report, err := engine.Plan(ctx, session, chain{set: set, providers: providers, cache: cache, privacy: cfg.Privacy}, db, engine.PlanOptions{
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
		})
		if err != nil {
			sessionLogger.Error("run failed", slog.String("error", err.Error()))
			return writeEngineError(stderr, lang, err)
		}
		logRun(sessionLogger, "dry", report, time.Since(started))
		if *csvPath != "" {
			if err := writeCSV(*csvPath, report.Rows); err != nil {
				sessionLogger.Error("csv failed", slog.String("error", err.Error()))
				return writeError(stderr, err)
			}
		}
		note := i18n.T(lang, "run.dry")
		if report.Aborted {
			note = i18n.T(lang, "run.stopped")
		}
		if err := writeReport(stdout, report, "dry", lang, set.Categories, time.Since(started), note); err != nil {
			return 1
		}
		return 0
	})
}

func runUndo(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name")
	runID := fs.Int64("run", 0, "run id")
	var uids uidFlag
	fs.Var(&uids, "uid", "source UID to undo")
	fs.Usage = func() {
		if err := writeString(stderr, undoUsageText); err != nil {
			return
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *profile == "" || fs.NArg() != 0 || *runID < 0 {
		fs.Usage()
		return 2
	}
	if _, err := findProfile(cfg, *profile); err != nil {
		return writeError(stderr, err)
	}
	lang := displayLang(cfg)
	db, err := openState()
	if err != nil {
		return writeError(stderr, err)
	}
	defer func() { _ = db.Close() }()
	if *runID == 0 {
		if _, _, err := db.LatestFiled(context.Background(), *profile); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return writeNote(stderr, i18n.T(lang, "undo.nothing"))
			}
			return writeError(stderr, err)
		}
	}
	return withSession(cfg, *profile, stderr, runTimeout, func(ctx context.Context, session *mail.Session, _ mail.Account, sessionLogger *slog.Logger) int {
		started := time.Now()
		report, err := engine.Undo(ctx, session, db, engine.UndoOptions{
			Profile: *profile,
			RunID:   *runID,
			UIDs:    uids,
		})
		if err != nil {
			sessionLogger.Error("undo failed", slog.String("error", err.Error()))
			return writeEngineError(stderr, lang, err)
		}
		logRun(sessionLogger, "undo", report, time.Since(started))
		if err := writeReport(stdout, report, "undo", lang, nil, time.Since(started), i18n.T(lang, "undo.done")); err != nil {
			return 1
		}
		return 0
	})
}

type chain struct {
	set       classify.Set
	providers []classify.Provider
	cache     classify.Cache
	privacy   config.Privacy
}

func (c chain) Classify(ctx context.Context, in classify.Input) (classify.Decision, error) {
	return classify.Classify(classify.WithPrivacy(ctx, c.privacy), in, c.set, c.providers, c.cache)
}

func openState() (*store.DB, error) {
	path, err := stateDBPath()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(context.Background(), path)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func stateDBPath() (string, error) {
	stateDir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	if err := logging.EnsureDir(stateDir); err != nil {
		return "", err
	}
	return stateDir + "/mailsorter.db", nil
}

func validRunFlags(fs *flag.FlagSet, planning, apply bool) bool {
	ok := true
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "unread", "since", "csv", "include-flagged", "include-drafts", "limit", "workers", "max-chars", "folder":
			if !planning {
				ok = false
			}
		case "copy-only", "max-moves":
			if !apply {
				ok = false
			}
		}
	})
	return ok
}

func flagVisited(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

func logRun(logger *slog.Logger, mode string, report engine.Report, elapsed time.Duration) {
	if logger == nil {
		return
	}
	logger.Info("run",
		slog.String("mode", mode),
		slog.String("profile", report.Run.Profile),
		slog.String("mailbox", report.Run.Mailbox),
		slog.Int64("run", report.Run.ID),
		slog.Int("messages", len(report.Rows)),
		slog.Int("moves", report.Moves),
		slog.Int("copies", report.Copies),
		slog.Int("errors", report.Errors),
		slog.Int("pending", report.Pending),
		slog.Bool("aborted", report.Aborted),
		slog.Int("api_calls", report.Run.APICalls),
		slog.Int("tokens_in", report.Run.TokensIn),
		slog.Int("tokens_out", report.Run.TokensOut),
		slog.Float64("cost_usd", report.Run.CostUSD),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
}

func writeReport(w io.Writer, report engine.Report, mode string, lang i18n.Lang, cats []classify.Category, elapsed time.Duration, note string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "run: %d\n", report.Run.ID)
	fmt.Fprintf(&b, "profile: %s\n", report.Run.Profile)
	fmt.Fprintf(&b, "mailbox: %s\n", report.Run.Mailbox)
	fmt.Fprintf(&b, "uidvalidity: %d\n", report.Run.UIDValidity)
	fmt.Fprintf(&b, "mode: %s\n", mode)
	fmt.Fprintf(&b, "messages: %d\n", len(report.Rows))
	if mode == "apply" {
		fmt.Fprintf(&b, "moves: %d\n", report.Moves)
		fmt.Fprintf(&b, "copies: %d\n", report.Copies)
		fmt.Fprintf(&b, "pending: %d\n", report.Pending)
	}
	for _, row := range report.Rows {
		b.WriteString("---\n")
		if err := formatRow(&b, row, lang, cats); err != nil {
			return err
		}
	}
	b.WriteString("summary:\n")
	counts := map[string]int{}
	var keys []string
	for _, row := range report.Rows {
		if row.Category == "" {
			continue
		}
		if _, ok := counts[row.Category]; !ok {
			keys = append(keys, row.Category)
		}
		counts[row.Category]++
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "%s: %d\n", key, counts[key])
	}
	fmt.Fprintf(&b, "api_calls: %d\n", report.Run.APICalls)
	fmt.Fprintf(&b, "tokens_in: %d\n", report.Run.TokensIn)
	fmt.Fprintf(&b, "tokens_out: %d\n", report.Run.TokensOut)
	if report.Run.HasCost {
		fmt.Fprintf(&b, "cost_usd: %.6f\n", report.Run.CostUSD)
	}
	fmt.Fprintf(&b, "elapsed_ms: %d\n", elapsed.Milliseconds())
	fmt.Fprintf(&b, "note: %s\n", note)
	return writeString(w, b.String())
}

func writeRow(w io.Writer, row store.Row, lang i18n.Lang, cats []classify.Category) error {
	var b strings.Builder
	if err := formatRow(&b, row, lang, cats); err != nil {
		return err
	}
	return writeString(w, b.String())
}

func formatRow(b *strings.Builder, row store.Row, lang i18n.Lang, cats []classify.Category) error {
	fmt.Fprintf(b, "uid: %d\n", row.UID)
	fmt.Fprintf(b, "from: %s\n", row.From)
	fmt.Fprintf(b, "subject: %s\n", row.Subject)
	fmt.Fprintf(b, "category: %s\n", row.Category)
	if row.Category != "" {
		fmt.Fprintf(b, "name: %s\n", displayName(cats, row.Category, lang))
	}
	fmt.Fprintf(b, "confidence: %.3f\n", row.Confidence)
	fmt.Fprintf(b, "provider: %s\n", row.Provider)
	if row.Model != "" {
		fmt.Fprintf(b, "model: %s\n", row.Model)
	}
	if row.Source != "" {
		fmt.Fprintf(b, "source: %s\n", row.Source)
	}
	fmt.Fprintf(b, "action: %s\n", row.Action)
	if row.Folder != "" {
		fmt.Fprintf(b, "folder: %s\n", row.Folder)
	}
	fmt.Fprintf(b, "status: %s\n", row.Status)
	if row.DestUID != 0 {
		fmt.Fprintf(b, "dest_uid: %d\n", row.DestUID)
	}
	detail := row.Detail
	if row.Status == store.RowUndone && row.Filed == store.FiledCopy {
		detail = i18n.T(lang, "undo.left_copy")
	}
	if detail != "" {
		fmt.Fprintf(b, "detail: %s\n", detail)
	}
	return nil
}

func writeCSV(path string, rows []store.Row) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("run: csv: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("run: csv: %w", err)
	}
	writer := csv.NewWriter(file)
	if err := writer.Write([]string{"uid", "message_id", "from", "subject", "category", "confidence", "provider", "model", "action", "folder", "source", "status"}); err != nil {
		return fmt.Errorf("run: csv: %w", err)
	}
	for _, row := range rows {
		record := []string{
			strconv.FormatUint(uint64(row.UID), 10),
			row.MessageID,
			row.From,
			row.Subject,
			row.Category,
			strconv.FormatFloat(row.Confidence, 'f', 3, 64),
			row.Provider,
			row.Model,
			row.Action,
			row.Folder,
			row.Source,
			row.Status,
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("run: csv: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("run: csv: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("run: csv: %w", err)
	}
	closed = true
	return nil
}

func writeEngineError(stderr io.Writer, lang i18n.Lang, err error) int {
	switch {
	case errors.Is(err, engine.ErrNotConfirmed):
		return writeNote(stderr, i18n.T(lang, "run.not_confirmed"))
	case errors.Is(err, engine.ErrNoDryRun):
		return writeNote(stderr, i18n.T(lang, "run.need_dry_run"))
	case errors.Is(err, engine.ErrNothingToApply):
		return writeNote(stderr, i18n.T(lang, "run.nothing_to_apply"))
	case errors.Is(err, engine.ErrNothingToUndo):
		return writeNote(stderr, i18n.T(lang, "undo.nothing"))
	case errors.Is(err, engine.ErrOpenPlan):
		return writeNote(stderr, i18n.T(lang, "watch.open_plan"))
	case errors.Is(err, store.ErrApplyInProgress):
		return writeError(stderr, err)
	default:
		return writeError(stderr, err)
	}
}

func writeNote(w io.Writer, note string) int {
	if err := writeString(w, "mailsorter: "+note+"\n"); err != nil {
		return 1
	}
	return 1
}
