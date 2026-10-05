package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/engine"
	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/message"
	"github.com/prippa/mail-sort/internal/secrets"
	"github.com/prippa/mail-sort/internal/store"
)

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), cfg))
}

func (s *Server) view(ctx context.Context, cfg config.Config) map[string]any {
	lang := s.language(cfg)
	set, _ := s.categories(cfg)
	consentID, consentRequired := classify.RemoteConsentID(cfg.Classifiers, cfg.Privacy)
	granted := false
	if consentRequired {
		ok, err := s.db.HasConsent(ctx, consentID)
		if err == nil {
			granted = ok
		}
	}
	return map[string]any{
		"language":         string(lang),
		"language_setting": cfg.Language,
		"strings":          i18n.Catalog(lang),
		"config_path":      s.configPath,
		"state_dir":        s.stateDir,
		"categories_path":  s.categoriesPath(cfg),
		"privacy":          cfg.Privacy,
		"consent_required": consentRequired,
		"consent_granted":  granted,
		"profiles":         s.profileViews(ctx, cfg),
		"presets":          s.presetViews(),
		"categories":       categoryViews(set.Categories),
		"rules":            ruleViews(set.Rules),
		"classifiers":      s.classifierViews(cfg),
	}
}

func (s *Server) profileViews(ctx context.Context, cfg config.Config) []map[string]any {
	out := make([]map[string]any, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		confirmed, err := s.db.Confirmed(ctx, profile.Name)
		if err != nil {
			confirmed = false
		}
		out = append(out, map[string]any{
			"name":         profile.Name,
			"provider":     profile.Provider,
			"host":         profile.Host,
			"host_id":      profile.HostID,
			"port":         profile.Port,
			"security":     profile.Security,
			"username":     profile.Username,
			"email":        profile.Email,
			"password_env": profile.PasswordEnv,
			"password_set": lookupSet(s.lookup, profile.PasswordEnv),
			"auth":         profile.Auth,
			"client_id":    profile.ClientID,
			"tenant":       profile.Tenant,
			"device_code":  profile.DeviceCode,
			"signed_in":    s.signedIn(profile.Name),
			"discover":     profile.Discover,
			"max_chars":    profile.MaxChars,
			"confirmed":    confirmed,
		})
	}
	return out
}

func lookupSet(lookup func(string) (string, bool), name string) bool {
	if name == "" || lookup == nil {
		return false
	}
	value, ok := lookup(name)
	return ok && value != ""
}

func (s *Server) presetViews() []map[string]any {
	presets, err := mail.LoadPresets(filepath.Join(filepath.Dir(s.configPath), "presets.json"))
	if err != nil {
		presets = nil
	}
	out := make([]map[string]any, 0, len(presets)+1)
	for _, preset := range presets {
		hosts := make([]map[string]any, 0, len(preset.Hosts))
		for _, host := range preset.Hosts {
			hosts = append(hosts, map[string]any{
				"id":       host.ID,
				"host":     host.Host,
				"port":     host.Port,
				"security": host.Security,
				"note":     host.Note,
			})
		}
		out = append(out, map[string]any{
			"id":            preset.ID,
			"name":          preset.Name,
			"auth":          preset.Auth,
			"username_hint": preset.UsernameHint,
			"note":          preset.Note,
			"source":        preset.Source,
			"hosts":         hosts,
		})
	}
	out = append(out, map[string]any{
		"id":            "custom",
		"name":          "",
		"auth":          []string{"password"},
		"username_hint": "",
		"note":          "",
		"source":        "",
		"hosts":         []map[string]any{},
	})
	return out
}

func (s *Server) classifierViews(cfg config.Config) []map[string]any {
	out := make([]map[string]any, 0, len(cfg.Classifiers))
	for _, spec := range cfg.Classifiers {
		envName := spec.KeyEnv
		if envName == "" && spec.Provider == "jev" {
			envName = "TYPESAFE_API_KEY"
		}
		base := spec.BaseURL
		out = append(out, map[string]any{
			"provider":       spec.Provider,
			"model":          spec.Model,
			"base_url":       base,
			"key_env":        spec.KeyEnv,
			"key_set":        s.keySet(envName),
			"min_confidence": spec.MinConfidence,
			"min_margin":     spec.MinMargin,
			"rps":            spec.RPS,
			"burst":          spec.Burst,
			"price_input":    spec.PriceInput,
			"price_output":   spec.PriceOutput,
			"local":          classify.LoopbackBase(base),
		})
	}
	return out
}

func categoryViews(cats []classify.Category) []map[string]any {
	out := make([]map[string]any, 0, len(cats))
	for _, cat := range cats {
		out = append(out, map[string]any{
			"key":            cat.Key,
			"name":           cat.Name,
			"name_ru":        cat.NameRU,
			"description":    cat.Description,
			"examples":       cat.Examples,
			"folder":         cat.Folder,
			"action":         cat.Action,
			"min_confidence": cat.MinConfidence,
			"reserved":       cat.Key == classify.NeedsReview || cat.Key == classify.KeepInInbox,
		})
	}
	return out
}

func ruleViews(rules []classify.Rule) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]any{
			"name":         rule.Name,
			"from":         rule.From,
			"from_domain":  rule.FromDomain,
			"to":           rule.To,
			"subject":      rule.Subject,
			"header":       rule.Header,
			"header_value": rule.HeaderValue,
			"keywords":     rule.Keywords,
			"attachment":   rule.Attachment,
			"category":     rule.Category,
			"action":       rule.Action,
		})
	}
	return out
}

type profileBody struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Host        string `json:"host"`
	HostID      string `json:"host_id"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	PasswordEnv string `json:"password_env"`
	Email       string `json:"email"`
	Auth        string `json:"auth"`
	ClientID    string `json:"client_id"`
	Tenant      string `json:"tenant"`
	DeviceCode  bool   `json:"device_code"`
	Discover    bool   `json:"discover"`
	MaxChars    int    `json:"max_chars"`
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	var body profileBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	profile := config.Profile{
		Name:        strings.TrimSpace(body.Name),
		Provider:    strings.TrimSpace(body.Provider),
		Host:        strings.TrimSpace(body.Host),
		HostID:      strings.TrimSpace(body.HostID),
		Port:        body.Port,
		Security:    strings.TrimSpace(body.Security),
		Username:    strings.TrimSpace(body.Username),
		PasswordEnv: strings.TrimSpace(body.PasswordEnv),
		Email:       strings.TrimSpace(body.Email),
		Auth:        strings.TrimSpace(body.Auth),
		ClientID:    strings.TrimSpace(body.ClientID),
		Tenant:      strings.TrimSpace(body.Tenant),
		DeviceCode:  body.DeviceCode,
		Discover:    body.Discover,
		MaxChars:    body.MaxChars,
	}
	for _, existing := range cfg.Profiles {
		if existing.Name == profile.Name {
			if profile.CAFile == "" {
				profile.CAFile = existing.CAFile
			}
			if profile.CertSHA256 == "" {
				profile.CertSHA256 = existing.CertSHA256
			}
		}
	}
	presets, err := mail.LoadPresets(filepath.Join(filepath.Dir(s.configPath), "presets.json"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	account, err := mail.Resolve(profile, presets)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if profile.Auth == "" {
		profile.Auth = string(account.Auth)
	}
	if profile.Auth == "password" && profile.PasswordEnv == "" {
		s.fail(w, http.StatusBadRequest, "config: profile has no password_env")
		return
	}
	if err := s.storeProfile(r.Context(), cfg, profile); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.bootstrap(w, r)
}

func (s *Server) storeProfile(ctx context.Context, cfg config.Config, profile config.Profile) error {
	replaced := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == profile.Name {
			cfg.Profiles[i] = profile
			replaced = true
		}
	}
	if !replaced {
		cfg.Profiles = append(cfg.Profiles, profile)
	}
	return config.Save(ctx, s.configPath, cfg)
}

type nameBody struct {
	Name    string `json:"name"`
	Profile string `json:"profile"`
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	var body nameBody
	if !s.readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = strings.TrimSpace(body.Profile)
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if err := s.db.ForgetProfile(r.Context(), name); err != nil {
		s.fail(w, http.StatusConflict, s.publicError(cfg, err))
		return
	}
	if vault, err := s.secretStore(); err == nil {
		_ = vault.Delete(secrets.RefreshAccount(name))
	}
	next := cfg.Profiles[:0]
	for _, profile := range cfg.Profiles {
		if profile.Name != name {
			next = append(next, profile)
		}
	}
	cfg.Profiles = next
	if err := config.Save(r.Context(), s.configPath, cfg); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.bootstrap(w, r)
}

func (s *Server) testProfile(w http.ResponseWriter, r *http.Request) {
	var body nameBody
	if !s.readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = strings.TrimSpace(body.Profile)
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	profile, err := findProfile(cfg, name)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	session, account, err := s.connect(ctx, profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	defer session.Close()
	caps, err := session.Capabilities(ctx)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	folders, err := session.ListFolders(ctx)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.log.Info("ui test-conn", slog.String("profile", profile.Name), slog.String("host", account.Endpoint.Host), slog.Int("folders", len(folders)))
	listed := make([]map[string]any, 0, len(folders))
	for _, folder := range folders {
		listed = append(listed, map[string]any{
			"name":        folder.Name,
			"special_use": folder.SpecialUse,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":        account.Endpoint.Host,
		"port":        account.Endpoint.Port,
		"security":    account.Endpoint.Security,
		"move":        caps.Move,
		"uidplus":     caps.UIDPlus,
		"idle":        caps.Idle,
		"special_use": caps.SpecialUse,
		"gmail_ext":   caps.GmailExt,
		"folders":     listed,
	})
}

type categoryBody struct {
	Categories []categoryDTO `json:"categories"`
	Rules      []ruleDTO     `json:"rules"`
}

type categoryDTO struct {
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	NameRU        string   `json:"name_ru"`
	Description   string   `json:"description"`
	Examples      []string `json:"examples"`
	Folder        string   `json:"folder"`
	Action        string   `json:"action"`
	MinConfidence float64  `json:"min_confidence"`
}

type ruleDTO struct {
	Name        string   `json:"name"`
	From        string   `json:"from"`
	FromDomain  string   `json:"from_domain"`
	To          string   `json:"to"`
	Subject     string   `json:"subject"`
	Header      string   `json:"header"`
	HeaderValue string   `json:"header_value"`
	Keywords    []string `json:"keywords"`
	Attachment  string   `json:"attachment"`
	Category    string   `json:"category"`
	Action      string   `json:"action"`
}

func (s *Server) saveCategories(w http.ResponseWriter, r *http.Request) {
	var body categoryBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	set := classify.Set{Categories: categoriesFrom(body.Categories), Rules: rulesFrom(body.Rules)}
	if err := classify.SaveCategories(r.Context(), s.categoriesPath(cfg), set); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.bootstrap(w, r)
}

func (s *Server) starterCategories(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if err := classify.SaveCategories(r.Context(), s.categoriesPath(cfg), classify.Starter()); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.bootstrap(w, r)
}

func categoriesFrom(in []categoryDTO) []classify.Category {
	out := make([]classify.Category, 0, len(in))
	for _, item := range in {
		out = append(out, classify.Category{
			Key:           item.Key,
			Name:          item.Name,
			NameRU:        item.NameRU,
			Description:   item.Description,
			Examples:      item.Examples,
			Folder:        item.Folder,
			Action:        item.Action,
			MinConfidence: item.MinConfidence,
		})
	}
	return out
}

func rulesFrom(in []ruleDTO) []classify.Rule {
	out := make([]classify.Rule, 0, len(in))
	for _, item := range in {
		out = append(out, classify.Rule{
			Name:        item.Name,
			From:        item.From,
			FromDomain:  item.FromDomain,
			To:          item.To,
			Subject:     item.Subject,
			Header:      item.Header,
			HeaderValue: item.HeaderValue,
			Keywords:    item.Keywords,
			Attachment:  item.Attachment,
			Category:    item.Category,
			Action:      item.Action,
		})
	}
	return out
}

type classifierBody struct {
	Classifiers []classifierDTO `json:"classifiers"`
}

type classifierDTO struct {
	Provider      string  `json:"provider"`
	Model         string  `json:"model"`
	BaseURL       string  `json:"base_url"`
	KeyEnv        string  `json:"key_env"`
	MinConfidence float64 `json:"min_confidence"`
	MinMargin     float64 `json:"min_margin"`
	RPS           float64 `json:"rps"`
	Burst         int     `json:"burst"`
	PriceInput    float64 `json:"price_input"`
	PriceOutput   float64 `json:"price_output"`
}

func (s *Server) saveClassifiers(w http.ResponseWriter, r *http.Request) {
	var body classifierBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	next := make([]config.Classifier, 0, len(body.Classifiers))
	for _, item := range body.Classifiers {
		next = append(next, config.Classifier{
			Provider:      strings.TrimSpace(item.Provider),
			Model:         strings.TrimSpace(item.Model),
			BaseURL:       strings.TrimSpace(item.BaseURL),
			KeyEnv:        strings.TrimSpace(item.KeyEnv),
			MinConfidence: item.MinConfidence,
			MinMargin:     item.MinMargin,
			RPS:           item.RPS,
			Burst:         item.Burst,
			PriceInput:    item.PriceInput,
			PriceOutput:   item.PriceOutput,
		})
	}
	cfg.Classifiers = next
	if err := s.localOnlyOK(cfg); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if err := config.Save(r.Context(), s.configPath, cfg); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.bootstrap(w, r)
}

type settingsBody struct {
	Language string         `json:"language"`
	Privacy  config.Privacy `json:"privacy"`
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var body settingsBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	switch body.Language {
	case "", "auto", "en", "ru":
		if body.Language == "auto" {
			body.Language = ""
		}
	default:
		s.fail(w, http.StatusBadRequest, i18n.T(s.language(cfg), "error.bad_request"))
		return
	}
	cfg.Language = body.Language
	cfg.Privacy = body.Privacy
	if err := s.localOnlyOK(cfg); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if err := config.Save(r.Context(), s.configPath, cfg); err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.bootstrap(w, r)
}

func (s *Server) localOnlyOK(cfg config.Config) error {
	if !cfg.Privacy.LocalOnly {
		return nil
	}
	for _, spec := range cfg.Classifiers {
		base := spec.BaseURL
		if base == "" && spec.Provider == "jev" {
			base = "https://api.typesafe.ai"
		}
		if base == "" && spec.Provider == "anthropic" {
			base = "https://api.anthropic.com"
		}
		if base != "" && !classify.LoopbackBase(base) {
			return errors.New("classify: local-only allows a loopback classifier only")
		}
	}
	return nil
}

func (s *Server) grantConsent(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	id, required := classify.RemoteConsentID(cfg.Classifiers, cfg.Privacy)
	if required {
		if err := s.db.GrantConsent(r.Context(), id); err != nil {
			s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
			return
		}
	}
	s.bootstrap(w, r)
}

type sampleBody struct {
	Profile string `json:"profile"`
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var body sampleBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	features := s.features(cfg, body)
	writeJSON(w, http.StatusOK, map[string]any{
		"sent":   false,
		"fields": featureFields(features),
	})
}

func (s *Server) tryClassify(w http.ResponseWriter, r *http.Request) {
	var body sampleBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if !s.ensureConsent(w, r, cfg) {
		return
	}
	set, err := s.mustCategories(cfg)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	providers, err := classify.Providers(cfg.Classifiers, s.secretLookup, s.log, s.httpClient)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	input := s.sampleInput(cfg, body)
	ctx, cancel := context.WithTimeout(classify.WithPrivacy(r.Context(), cfg.Privacy), 2*time.Minute)
	defer cancel()
	decision, err := classify.Classify(ctx, input, set, providers, s.cache)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.log.Info("ui try", slog.String("category", decision.Category), slog.String("source", decision.Source))
	writeJSON(w, http.StatusOK, map[string]any{
		"sent":   decision.Source != "rule" && decision.Source != "gate" && decision.Source != "",
		"fields": featureFields(classify.OutgoingFeatures(ctx, input)),
		"result": map[string]any{
			"category":   decision.Category,
			"confidence": decision.Confidence,
			"source":     decision.Source,
			"provider":   decision.Provider,
			"model":      decision.Model,
			"action":     decision.Action,
			"folder":     decision.Folder,
			"reason":     decision.Reason,
		},
	})
}

func (s *Server) features(cfg config.Config, body sampleBody) classify.Features {
	ctx := classify.WithPrivacy(context.Background(), cfg.Privacy)
	return classify.OutgoingFeatures(ctx, s.sampleInput(cfg, body))
}

func (s *Server) sampleInput(cfg config.Config, body sampleBody) classify.Input {
	chars := 0
	if profile, err := findProfile(cfg, body.Profile); err == nil {
		chars = profile.MaxChars
	}
	return classify.Input{Message: message.Message{
		From:    addresses(body.From),
		To:      addresses(body.To),
		Subject: body.Subject,
		Body:    message.Clean(body.Body, chars),
	}}
}

func addresses(raw string) []message.Address {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if name, email, ok := strings.Cut(raw, "<"); ok {
		email = strings.TrimSpace(strings.TrimSuffix(email, ">"))
		return []message.Address{{Name: strings.TrimSpace(name), Email: email}}
	}
	if strings.Contains(raw, "@") {
		return []message.Address{{Email: raw}}
	}
	return []message.Address{{Name: raw}}
}

func featureFields(features classify.Features) []map[string]string {
	attachments := strings.Join(features.Attachments, ", ")
	bulk := "false"
	if features.IsBulk {
		bulk = "true"
	}
	return []map[string]string{
		{"key": "from", "value": features.From},
		{"key": "to", "value": features.To},
		{"key": "subject", "value": features.Subject},
		{"key": "date", "value": features.Date},
		{"key": "is_bulk", "value": bulk},
		{"key": "attachments", "value": attachments},
		{"key": "body", "value": features.Body},
	}
}

func (s *Server) ensureConsent(w http.ResponseWriter, r *http.Request, cfg config.Config) bool {
	id, required := classify.RemoteConsentID(cfg.Classifiers, cfg.Privacy)
	if !required {
		return true
	}
	ok, err := s.db.HasConsent(r.Context(), id)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return false
	}
	if ok {
		return true
	}
	lang := s.language(cfg)
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":   i18n.T(lang, "consent.required"),
		"consent": true,
	})
	return false
}

type planBody struct {
	Profile        string `json:"profile"`
	Folder         string `json:"folder"`
	Limit          int    `json:"limit"`
	Unread         bool   `json:"unread"`
	Since          string `json:"since"`
	IncludeFlagged bool   `json:"include_flagged"`
	IncludeDrafts  bool   `json:"include_drafts"`
	MaxChars       int    `json:"max_chars"`
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var body planBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if body.Limit < 1 {
		body.Limit = engine.DefaultLimit
	}
	if body.Limit > 10000 || body.MaxChars < 0 || body.MaxChars > 100000 {
		s.fail(w, http.StatusBadRequest, i18n.T(s.language(cfg), "error.bad_request"))
		return
	}
	since, err := parseSince(body.Since)
	if err != nil {
		s.fail(w, http.StatusBadRequest, i18n.T(s.language(cfg), "error.bad_request"))
		return
	}
	if !s.ensureConsent(w, r, cfg) {
		return
	}
	profile, err := findProfile(cfg, body.Profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	set, err := s.mustCategories(cfg)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	providers, err := classify.Providers(cfg.Classifiers, s.secretLookup, s.log, s.httpClient)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	folder := body.Folder
	if folder == "" {
		folder = "INBOX"
	}
	chars := profile.MaxChars
	if body.MaxChars > 0 {
		chars = body.MaxChars
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	session, _, err := s.connect(ctx, profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	defer session.Close()
	started := time.Now()
	report, err := engine.Plan(ctx, session, classifierChain{set: set, providers: providers, cache: s.cache, privacy: cfg.Privacy}, s.db, engine.PlanOptions{
		Profile: profile.Name,
		Mailbox: folder,
		Workers: engine.DefaultWorkers,
		Read: mail.ReadOptions{
			UnreadOnly:     body.Unread,
			Since:          since,
			Limit:          body.Limit,
			SkipFlagged:    !body.IncludeFlagged,
			SkipDrafts:     !body.IncludeDrafts,
			MaxChars:       chars,
			AllowProtected: !strings.EqualFold(folder, "INBOX"),
		},
	})
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.log.Info("ui plan", slog.String("profile", profile.Name), slog.Int("rows", len(report.Rows)), slog.Int("errors", report.Errors), slog.Duration("latency", time.Since(started)))
	writeJSON(w, http.StatusOK, s.present(cfg, report, "plan"))
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	var body nameBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	name := strings.TrimSpace(body.Profile)
	if name == "" {
		name = strings.TrimSpace(body.Name)
	}
	if err := engine.Confirm(r.Context(), s.db, name); err != nil {
		status := http.StatusConflict
		msg := s.engineMessage(cfg, err)
		s.fail(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"note_key": "run.confirmed"})
}

type overrideBody struct {
	Profile  string `json:"profile"`
	UID      uint32 `json:"uid"`
	Category string `json:"category"`
}

func (s *Server) override(w http.ResponseWriter, r *http.Request) {
	var body overrideBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	set, err := s.mustCategories(cfg)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if _, err := engine.Override(r.Context(), s.db, body.Profile, body.UID, set.Categories, body.Category); err != nil {
		s.fail(w, http.StatusBadRequest, s.engineMessage(cfg, err))
		return
	}
	s.writeOpen(w, r, cfg, body.Profile)
}

type applyBody struct {
	Profile  string `json:"profile"`
	CopyOnly bool   `json:"copy_only"`
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var body applyBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	ok, err := s.db.Confirmed(r.Context(), body.Profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if !ok {
		s.fail(w, http.StatusConflict, i18n.T(s.language(cfg), "run.not_confirmed"))
		return
	}
	profile, err := findProfile(cfg, body.Profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	session, _, err := s.connect(ctx, profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	defer session.Close()
	report, err := engine.Apply(ctx, session, s.db, engine.ApplyOptions{
		Profile:  profile.Name,
		CopyOnly: body.CopyOnly,
		MaxMoves: engine.DefaultMaxMoves,
	})
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.engineMessage(cfg, err))
		return
	}
	s.log.Info("ui apply", slog.String("profile", profile.Name), slog.Int("moves", report.Moves), slog.Int("copies", report.Copies), slog.Int("errors", report.Errors))
	writeJSON(w, http.StatusOK, s.present(cfg, report, "apply"))
}

type undoBody struct {
	Profile string   `json:"profile"`
	RunID   int64    `json:"run_id"`
	UIDs    []uint32 `json:"uids"`
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	var body undoBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	profile, err := findProfile(cfg, body.Profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	session, _, err := s.connect(ctx, profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	defer session.Close()
	report, err := engine.Undo(ctx, session, s.db, engine.UndoOptions{
		Profile: profile.Name,
		RunID:   body.RunID,
		UIDs:    body.UIDs,
	})
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.engineMessage(cfg, err))
		return
	}
	s.log.Info("ui undo", slog.String("profile", profile.Name), slog.Int("moves", report.Moves))
	writeJSON(w, http.StatusOK, s.present(cfg, report, "undo"))
}

func (s *Server) currentRun(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	s.writeOpen(w, r, cfg, r.URL.Query().Get("profile"))
}

func (s *Server) writeOpen(w http.ResponseWriter, r *http.Request, cfg config.Config, profile string) {
	run, rows, err := s.db.LatestOpen(r.Context(), profile)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"note_key": "run.empty", "rows": []any{}, "run": map[string]any{}})
			return
		}
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	writeJSON(w, http.StatusOK, s.present(cfg, engine.Report{Run: run, Rows: rows}, "plan"))
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	runs, err := s.db.RecentRuns(r.Context(), 30)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, map[string]any{
			"id":         run.ID,
			"profile":    run.Profile,
			"mailbox":    run.Mailbox,
			"status":     run.Status,
			"created_at": run.CreatedAt.Format(time.RFC3339),
			"moves":      run.Moves,
			"copies":     run.Copies,
			"errors":     run.Errors,
			"api_calls":  run.APICalls,
			"tokens_in":  run.TokensIn,
			"tokens_out": run.TokensOut,
			"cost_usd":   run.CostUSD,
			"has_cost":   run.HasCost,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (s *Server) present(cfg config.Config, report engine.Report, kind string) map[string]any {
	rows := make([]map[string]any, 0, len(report.Rows))
	for _, row := range report.Rows {
		rows = append(rows, map[string]any{
			"uid":        row.UID,
			"subject":    row.Subject,
			"from":       row.From,
			"category":   row.Category,
			"action":     row.Action,
			"folder":     row.Folder,
			"status":     row.Status,
			"confidence": row.Confidence,
			"source":     row.Source,
			"detail":     s.scrub(cfg, row.Detail),
		})
	}
	return map[string]any{
		"note_key": noteKey(report, kind),
		"run": map[string]any{
			"id":      report.Run.ID,
			"status":  report.Run.Status,
			"moves":   report.Run.Moves,
			"copies":  report.Run.Copies,
			"errors":  report.Run.Errors,
			"pending": report.Pending,
		},
		"rows": rows,
	}
}

func noteKey(report engine.Report, kind string) string {
	switch {
	case report.Aborted:
		return "run.stopped"
	case report.CopyOnlyRefused:
		return "run.copy_only"
	case kind == "undo":
		return "undo.done"
	case kind == "apply" && report.Pending > 0:
		return "run.pending"
	case kind == "apply":
		return "run.applied"
	default:
		return "run.dry"
	}
}

func (s *Server) engineMessage(cfg config.Config, err error) string {
	lang := s.language(cfg)
	switch {
	case errors.Is(err, engine.ErrNotConfirmed):
		return i18n.T(lang, "run.not_confirmed")
	case errors.Is(err, engine.ErrNoDryRun), errors.Is(err, store.ErrNotFound):
		return i18n.T(lang, "run.need_dry_run")
	case errors.Is(err, engine.ErrNothingToApply):
		return i18n.T(lang, "run.nothing_to_apply")
	case errors.Is(err, engine.ErrNothingToUndo):
		return i18n.T(lang, "undo.nothing")
	default:
		return s.publicError(cfg, err)
	}
}

func (s *Server) categories(cfg config.Config) (classify.Set, bool) {
	path := s.categoriesPath(cfg)
	if _, err := os.Stat(path); err != nil {
		return classify.Starter(), true
	}
	set, err := classify.LoadCategories(context.Background(), path)
	if err != nil {
		return classify.Starter(), true
	}
	return set, false
}

func (s *Server) mustCategories(cfg config.Config) (classify.Set, error) {
	path := s.categoriesPath(cfg)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return classify.Starter(), nil
		}
		return classify.Set{}, err
	}
	return classify.LoadCategories(context.Background(), path)
}

func (s *Server) categoriesPath(cfg config.Config) string {
	name := cfg.CategoriesFile
	if name == "" {
		name = "categories.yaml"
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(filepath.Dir(s.configPath), name)
	}
	return name
}

func findProfile(cfg config.Config, name string) (config.Profile, error) {
	name = strings.TrimSpace(name)
	for _, profile := range cfg.Profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	return config.Profile{}, fmt.Errorf("config: no profile named %q", name)
}

func parseSince(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func (s *Server) connect(ctx context.Context, profile config.Profile) (*mail.Session, mail.Account, error) {
	presets, err := mail.LoadPresets(filepath.Join(filepath.Dir(s.configPath), "presets.json"))
	if err != nil {
		return nil, mail.Account{}, err
	}
	account, err := mail.Resolve(profile, presets)
	if err != nil {
		return nil, mail.Account{}, err
	}
	if account.Discover {
		found, err := mail.DiscoverIMAP(ctx, account.Email, mail.Discover{})
		if err != nil {
			return nil, mail.Account{}, err
		}
		account.Endpoint.Host = found.Host
		account.Endpoint.Port = found.Port
		account.Endpoint.Security = found.Security
	}
	if account.Auth == mail.AuthPassword {
		password, err := s.password(profile.PasswordEnv)
		if err != nil {
			return nil, account, err
		}
		session, err := mail.NewClient().Connect(ctx, account, password)
		if err != nil {
			return nil, account, err
		}
		return session, account, nil
	}
	session, err := s.connectOAuth(ctx, profile, account)
	if err != nil {
		return nil, account, err
	}
	return session, account, nil
}

func (s *Server) password(name string) (string, error) {
	if name == "" {
		return "", errors.New("config: profile has no password_env")
	}
	value, ok := s.lookup(name)
	if !ok || value == "" {
		return "", fmt.Errorf("config: environment variable %s is unset or empty", name)
	}
	return value, nil
}

type classifierChain struct {
	set       classify.Set
	providers []classify.Provider
	cache     classify.Cache
	privacy   config.Privacy
}

func (c classifierChain) Classify(ctx context.Context, in classify.Input) (classify.Decision, error) {
	return classify.Classify(classify.WithPrivacy(ctx, c.privacy), in, c.set, c.providers, c.cache)
}
