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

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/logging"
)

func runCategories(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("categories", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		if err := writeString(stderr, "usage: mailsorter categories export\n"); err != nil {
			return
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || fs.Arg(0) != "export" {
		fs.Usage()
		return 2
	}
	data, err := classify.ExportStarter()
	if err != nil {
		return writeError(stderr, err)
	}
	if _, err := stdout.Write(data); err != nil {
		return 1
	}
	return 0
}

func runClassify(cfg config.Config, configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("classify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useStdin := fs.Bool("stdin", false, "read one message from stdin")
	maxChars := fs.Int("max-chars", 0, "body cap in runes")
	fs.Usage = func() {
		if err := writeString(stderr, "usage: mailsorter classify --stdin [--max-chars N]\n"); err != nil {
			return
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !*useStdin || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if *maxChars < 0 || *maxChars > 100000 {
		fs.Usage()
		return 2
	}

	set, usingStarter, err := loadCategories(cfg, configPath)
	if err != nil {
		return writeError(stderr, err)
	}
	input, err := classify.ParseStdin(stdin, *maxChars)
	if err != nil {
		if errors.Is(err, classify.ErrEmptyStdin) || errors.Is(err, classify.ErrBadStdin) {
			if writeErr := writeString(stderr, "mailsorter: "+err.Error()+"\n"); writeErr != nil {
				return 1
			}
			return 2
		}
		return writeError(stderr, err)
	}
	if usingStarter {
		if err := writeString(stderr, "mailsorter: using starter categories\n"); err != nil {
			return 1
		}
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
	closed := false
	defer func() {
		if !closed {
			_ = closer.Close()
		}
	}()
	providers, err := classify.Providers(cfg.Classifiers, os.LookupEnv, logger, nil)
	if err != nil {
		logger.Error("classify failed", slog.String("error", err.Error()))
		return writeError(stderr, err)
	}
	cache, err := classify.OpenCache(context.Background(), filepath.Join(stateDir, "mailsorter.db"))
	if err != nil {
		logger.Error("classify failed", slog.String("error", err.Error()))
		return writeError(stderr, err)
	}
	defer func() { _ = cache.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	decision, err := classify.Classify(classify.WithPrivacy(ctx, cfg.Privacy), input, set, providers, cache)
	if err != nil {
		logger.Error("classify failed", slog.String("error", err.Error()))
		return writeError(stderr, err)
	}
	logger.Info("classified",
		slog.String("category", decision.Category),
		slog.String("source", decision.Source),
		slog.String("provider", decision.Provider),
		slog.String("model", decision.Model),
		slog.Float64("confidence", decision.Confidence),
	)
	if err := writeDecision(stdout, decision, displayLang(cfg), set.Categories); err != nil {
		return 1
	}
	if err := closer.Close(); err != nil {
		closed = true
		return writeError(stderr, err)
	}
	closed = true
	return 0
}

func loadCategories(cfg config.Config, configPath string) (classify.Set, bool, error) {
	path, ok := categoriesPath(configPath, cfg.CategoriesFile)
	if !ok {
		return classify.Starter(), true, nil
	}
	set, err := classify.LoadCategories(context.Background(), path)
	if err != nil {
		return classify.Set{}, false, err
	}
	return set, false, nil
}

func categoriesPath(configPath, configured string) (string, bool) {
	if configured != "" {
		if !filepath.IsAbs(configured) && configPath != "" {
			configured = filepath.Join(filepath.Dir(configPath), configured)
		}
		return configured, true
	}
	if configPath == "" {
		return "", false
	}
	candidate := filepath.Join(filepath.Dir(configPath), "categories.yaml")
	if _, err := os.Stat(candidate); err != nil {
		return "", false
	}
	return candidate, true
}

func displayLang(cfg config.Config) i18n.Lang {
	switch cfg.Language {
	case "en":
		return i18n.EN
	case "ru":
		return i18n.RU
	default:
		return i18n.FromEnv(os.Getenv)
	}
}

func writeDecision(w io.Writer, decision classify.Decision, lang i18n.Lang, cats []classify.Category) error {
	var b strings.Builder
	fmt.Fprintf(&b, "category: %s\n", decision.Category)
	fmt.Fprintf(&b, "name: %s\n", displayName(cats, decision.Category, lang))
	fmt.Fprintf(&b, "confidence: %.3f\n", decision.Confidence)
	if decision.HasMargin {
		fmt.Fprintf(&b, "margin: %.3f\n", decision.Margin)
	}
	fmt.Fprintf(&b, "provider: %s\n", decision.Provider)
	fmt.Fprintf(&b, "model: %s\n", decision.Model)
	fmt.Fprintf(&b, "reason: %s\n", decision.Reason)
	fmt.Fprintf(&b, "source: %s\n", decision.Source)
	fmt.Fprintf(&b, "action: %s\n", decision.Action)
	fmt.Fprintf(&b, "folder: %s\n", decision.Folder)
	if decision.InputTokens > 0 || decision.OutputTokens > 0 {
		fmt.Fprintf(&b, "tokens_in: %d\n", decision.InputTokens)
		fmt.Fprintf(&b, "tokens_out: %d\n", decision.OutputTokens)
	}
	if cost, ok := classify.CostUSD(decision.InputTokens, decision.OutputTokens, decision.PriceInput, decision.PriceOutput); ok {
		fmt.Fprintf(&b, "cost_usd: %.6f\n", cost)
	}
	return writeString(w, b.String())
}

func displayName(cats []classify.Category, key string, lang i18n.Lang) string {
	if key == classify.NeverTouch {
		return i18n.T(lang, "category.never_touch")
	}
	for _, cat := range cats {
		if cat.Key != key {
			continue
		}
		if lang == i18n.RU && cat.NameRU != "" {
			return cat.NameRU
		}
		if cat.Name != "" {
			return cat.Name
		}
		break
	}
	if name := i18n.T(lang, "category."+key); name != "category."+key {
		return name
	}
	return key
}
