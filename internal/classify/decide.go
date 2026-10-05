package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/message"
)

// Input is one message to classify. Message.Body is already cleaned.
// Headers keep their MIME names and are used only by rules.
type Input struct {
	Message message.Message
	Headers map[string][]string
}

// Features are the fields sent to a provider. They are redacted first.
type Features struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Subject     string   `json:"subject"`
	Date        string   `json:"date"`
	IsBulk      bool     `json:"is_bulk"`
	Attachments []string `json:"attachments"`
	Body        string   `json:"body"`
}

// Result is one provider answer, before the confidence gate.
type Result struct {
	Category     string
	Confidence   float64
	Margin       float64
	HasMargin    bool
	Model        string
	Reason       string
	InputTokens  int
	OutputTokens int
}

// Decision is the pipeline result. Source is rule, cache, a provider name, or gate.
type Decision struct {
	Category     string  `json:"category"`
	Confidence   float64 `json:"confidence"`
	Margin       float64 `json:"margin,omitempty"`
	HasMargin    bool    `json:"has_margin,omitempty"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	Source       string  `json:"source"`
	Action       string  `json:"action,omitempty"`
	Folder       string  `json:"folder,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	PriceInput   float64 `json:"-"`
	PriceOutput  float64 `json:"-"`
}

// Provider classifies one redacted feature set.
type Provider interface {
	Name() string
	Model() string
	MinConfidence() float64
	MinMargin() float64
	PriceInput() float64
	PriceOutput() float64
	Classify(ctx context.Context, features Features, cats []Category) (Result, error)
}

// ChoiceConfidence is the TypeSafe Choice formula
// (pMax - 1/n) / (1 - 1/n), clamped to 0..1.
// n is the number of options. The docs page derives the same value as
// (n*pMax - 1) / (n - 1).
func ChoiceConfidence(pMax float64, n int) float64 {
	if n < 2 {
		if pMax >= 1 {
			return 1
		}
		return 0
	}
	value := (pMax - 1/float64(n)) / (1 - 1/float64(n))
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// Classify runs rules, then the cache, then the provider chain.
// A missing or rejected API key stops the chain so the message is not sent
// to the next provider. When every answer is below its threshold, the
// decision is needs_review. When every provider fails, the error is returned
// and no category is invented.
func Classify(ctx context.Context, in Input, set Set, providers []Provider, cache Cache) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	cats := withReserved(set.Categories)
	rules, err := prepareRules(set.Rules, cats)
	if err != nil {
		return Decision{}, err
	}
	if decision, ok := matchRules(rules, cats, in); ok {
		return decision, nil
	}
	priv := PrivacyFrom(ctx)
	if priv.RulesOnly {
		return decorate(Decision{Category: NeedsReview, Source: "gate"}, cats), nil
	}
	if len(providers) == 0 {
		return Decision{}, errors.New("classify: no classifier is enabled")
	}
	features := redactFeatures(shapeFeatures(featuresFrom(in), priv), priv)
	var lastErr error
	var last Decision
	sawDecision := false
	for _, provider := range providers {
		key, err := cacheKey(features, cats, provider.Name(), provider.Model())
		if err != nil {
			return Decision{}, err
		}
		if cache != nil {
			cached, ok, err := cache.Get(ctx, key)
			if err != nil {
				return Decision{}, err
			}
			if ok {
				cached.Source = "cache"
				cached.PriceInput = provider.PriceInput()
				cached.PriceOutput = provider.PriceOutput()
				cached = decorate(cached, cats)
				if accept(cached, cats, provider.MinConfidence(), provider.MinMargin()) {
					return cached, nil
				}
				last = cached
				sawDecision = true
				continue
			}
		}
		result, err := provider.Classify(ctx, features, cats)
		if err != nil {
			if errors.Is(err, ErrNoAPIKey) || errors.Is(err, ErrUnauthorized) {
				return Decision{}, err
			}
			lastErr = err
			continue
		}
		decision := decisionFromResult(result, provider)
		decision = decorate(decision, cats)
		if cache != nil {
			if err := cache.Put(ctx, key, decision); err != nil {
				return Decision{}, err
			}
		}
		decision.Source = provider.Name()
		if accept(decision, cats, provider.MinConfidence(), provider.MinMargin()) {
			return decision, nil
		}
		last = decision
		sawDecision = true
	}
	if sawDecision {
		return decorate(Decision{
			Category:    NeedsReview,
			Confidence:  last.Confidence,
			Provider:    last.Provider,
			Model:       last.Model,
			Source:      "gate",
			PriceInput:  last.PriceInput,
			PriceOutput: last.PriceOutput,
		}, cats), nil
	}
	if lastErr != nil {
		return Decision{}, lastErr
	}
	return Decision{}, errors.New("classify: no classifier is enabled")
}

func decisionFromResult(result Result, provider Provider) Decision {
	return Decision{
		Category:     result.Category,
		Confidence:   result.Confidence,
		Margin:       result.Margin,
		HasMargin:    result.HasMargin,
		Provider:     provider.Name(),
		Model:        result.Model,
		Reason:       result.Reason,
		Source:       provider.Name(),
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		PriceInput:   provider.PriceInput(),
		PriceOutput:  provider.PriceOutput(),
	}
}

func decorate(decision Decision, cats []Category) Decision {
	if decision.Action == NeverTouch || decision.Category == NeverTouch {
		decision.Category = NeverTouch
		decision.Action = NeverTouch
		decision.Folder = ""
		return decision
	}
	cat, ok := findCategory(cats, decision.Category)
	if !ok {
		cat, _ = findCategory(cats, NeedsReview)
		decision.Category = NeedsReview
	}
	decision.Action = cat.Action
	decision.Folder = cat.Folder
	return decision
}

func accept(decision Decision, cats []Category, minConfidence, minMargin float64) bool {
	if decision.Category == "" || decision.Category == NeverTouch {
		return false
	}
	need := minConfidence
	if cat, ok := findCategory(cats, decision.Category); ok && cat.MinConfidence > need {
		need = cat.MinConfidence
	}
	if decision.Confidence < need {
		return false
	}
	if minMargin > 0 && (!decision.HasMargin || decision.Margin < minMargin) {
		return false
	}
	return true
}

func featuresFrom(in Input) Features {
	names := make([]string, 0, len(in.Message.Attachments))
	for _, attachment := range in.Message.Attachments {
		if attachment.Name != "" {
			names = append(names, attachment.Name)
		}
	}
	sort.Strings(names)
	return Features{
		From:        joinAddresses(in.Message.From),
		To:          joinAddresses(in.Message.To),
		Subject:     strings.TrimSpace(in.Message.Subject),
		Date:        formatDate(in.Message.Date),
		IsBulk:      in.Message.IsBulk,
		Attachments: names,
		Body:        in.Message.Body,
	}
}

func redactFeatures(features Features, priv config.Privacy) Features {
	features.From = redact(features.From, priv)
	features.To = redact(features.To, priv)
	features.Subject = redact(features.Subject, priv)
	features.Body = redact(features.Body, priv)
	for i := range features.Attachments {
		features.Attachments[i] = redact(features.Attachments[i], priv)
	}
	if features.Attachments == nil {
		features.Attachments = []string{}
	}
	return features
}

func joinAddresses(list []message.Address) string {
	parts := make([]string, 0, len(list))
	for _, addr := range list {
		email := strings.ToLower(strings.TrimSpace(addr.Email))
		name := strings.TrimSpace(addr.Name)
		switch {
		case name != "" && email != "":
			parts = append(parts, name+" <"+email+">")
		case email != "":
			parts = append(parts, email)
		case name != "":
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

type criterion struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

func cacheKey(features Features, cats []Category, provider, model string) (string, error) {
	view := struct {
		Features   Features    `json:"features"`
		Categories []criterion `json:"categories"`
		Provider   string      `json:"provider"`
		Model      string      `json:"model"`
	}{
		Features:   features,
		Categories: make([]criterion, len(cats)),
		Provider:   provider,
		Model:      model,
	}
	for i, cat := range cats {
		view.Categories[i] = criterion{Key: cat.Key, Text: cat.Criteria()}
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("classify: cache key: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// CostUSD estimates dollars from user-entered prices per million tokens.
func CostUSD(inputTokens, outputTokens int, priceInput, priceOutput float64) (float64, bool) {
	if priceInput == 0 && priceOutput == 0 {
		return 0, false
	}
	cost := float64(inputTokens)/1e6*priceInput + float64(outputTokens)/1e6*priceOutput
	return cost, true
}
