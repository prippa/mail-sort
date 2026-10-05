package ui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/mail"
	"github.com/prippa/mail-sort/internal/oauth"
	"github.com/prippa/mail-sort/internal/secrets"
)

type signIn struct {
	status          string
	message         string
	authURL         string
	userCode        string
	verificationURI string
	cancel          context.CancelFunc
}

func (s *Server) secretStore() (secrets.Store, error) {
	s.vaultOnce.Do(func() {
		s.vault, s.vaultErr = secrets.Open(s.stateDir)
	})
	return s.vault, s.vaultErr
}

func (s *Server) signedIn(name string) bool {
	vault, err := s.secretStore()
	if err != nil {
		return false
	}
	_, err = vault.Get(secrets.RefreshAccount(name))
	return err == nil
}

func (s *Server) keySet(envName string) bool {
	if lookupSet(s.lookup, envName) {
		return true
	}
	vault, err := s.secretStore()
	if err != nil || envName == "" {
		return false
	}
	_, err = vault.Get(secrets.APIKeyAccount(envName))
	return err == nil
}

func (s *Server) secretLookup(name string) (string, bool) {
	if lookupSet(s.lookup, name) {
		value, _ := s.lookup(name)
		return strings.TrimSpace(value), strings.TrimSpace(value) != ""
	}
	vault, err := s.secretStore()
	if err != nil || name == "" {
		return "", false
	}
	value, err := vault.Get(secrets.APIKeyAccount(name))
	if err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func (s *Server) connectOAuth(ctx context.Context, profile config.Profile, account mail.Account) (*mail.Session, error) {
	vault, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	refresh, err := vault.Get(secrets.RefreshAccount(profile.Name))
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
		return vault.Set(secrets.RefreshAccount(profile.Name), next)
	})
	if err != nil {
		return nil, err
	}
	return mail.NewClient().ConnectOAuth(ctx, account, source)
}

func (s *Server) startOAuth(w http.ResponseWriter, r *http.Request) {
	var body profileBody
	if !s.readJSON(w, r, &body) {
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	profile, err := s.oauthProfile(r.Context(), cfg, body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	oAccount, err := oauth.FromProfile(profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	var flow *oauth.Flow
	s.withoutLock(func() {
		flow, err = oauth.Begin(ctx, oAccount)
	})
	if err != nil {
		cancel()
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if previous := s.signIns[profile.Name]; previous != nil && previous.cancel != nil {
		previous.cancel()
	}
	attempt := &signIn{
		status:          "pending",
		authURL:         flow.AuthURL(),
		userCode:        flow.UserCode(),
		verificationURI: flow.VerificationURI(),
		cancel:          cancel,
	}
	s.signIns[profile.Name] = attempt
	s.log.Info("oauth sign-in", slog.String("profile", profile.Name), slog.String("auth", profile.Auth))
	if attempt.authURL != "" && s.openBrowser != nil {
		_ = s.openBrowser(attempt.authURL)
	}
	if attempt.verificationURI != "" && attempt.authURL == "" && s.openBrowser != nil {
		_ = s.openBrowser(attempt.verificationURI)
	}
	go s.finishOAuth(ctx, profile.Name, flow)
	writeJSON(w, http.StatusOK, signInView(attempt))
}

func (s *Server) oauthProfile(ctx context.Context, cfg config.Config, body profileBody) (config.Profile, error) {
	name := strings.TrimSpace(body.Name)
	if strings.TrimSpace(body.Provider) == "" {
		return findProfile(cfg, name)
	}
	profile := config.Profile{
		Name:        name,
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
			profile.CAFile = existing.CAFile
			profile.CertSHA256 = existing.CertSHA256
		}
	}
	if profile.Auth == "" || profile.Auth == "password" {
		if profile.Provider == "microsoft" {
			profile.Auth = "oauth_microsoft"
		} else {
			profile.Auth = "oauth_google"
		}
	}
	if _, err := oauth.FromProfile(profile); err != nil {
		return config.Profile{}, err
	}
	presets, err := mail.LoadPresets(filepath.Join(filepath.Dir(s.configPath), "presets.json"))
	if err != nil {
		return config.Profile{}, err
	}
	if _, err := mail.Resolve(profile, presets); err != nil {
		return config.Profile{}, err
	}
	if err := s.storeProfile(ctx, cfg, profile); err != nil {
		return config.Profile{}, err
	}
	return profile, nil
}

func (s *Server) finishOAuth(ctx context.Context, name string, flow *oauth.Flow) {
	tok, err := flow.Wait(ctx)
	status := "done"
	message := ""
	if err != nil {
		status = "error"
		message = err.Error()
	} else if vault, vaultErr := s.secretStore(); vaultErr != nil {
		status = "error"
		message = vaultErr.Error()
	} else if err := vault.Set(secrets.RefreshAccount(name), tok.RefreshToken); err != nil {
		status = "error"
		message = strings.ReplaceAll(err.Error(), tok.RefreshToken, "[redacted]")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.signIns[name]
	if attempt == nil {
		return
	}
	attempt.status = status
	attempt.message = message
	if status == "done" {
		s.log.Info("oauth sign-in stored", slog.String("profile", name))
		return
	}
	s.log.Info("oauth sign-in failed", slog.String("profile", name), slog.String("error", message))
}

func (s *Server) withoutLock(fn func()) {
	s.mu.Unlock()
	defer s.mu.Lock()
	fn()
}

func (s *Server) oauthStatus(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("profile"))
	attempt := s.signIns[name]
	if attempt == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, signInView(attempt))
}

func signInView(attempt *signIn) map[string]any {
	return map[string]any{
		"status":           attempt.status,
		"error":            attempt.message,
		"url":              attempt.authURL,
		"user_code":        attempt.userCode,
		"verification_uri": attempt.verificationURI,
	}
}

type keyBody struct {
	KeyEnv   string `json:"key_env"`
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

func (s *Server) saveClassifierKey(w http.ResponseWriter, r *http.Request) {
	var body keyBody
	if !s.readJSON(w, r, &body) {
		return
	}
	envName := strings.TrimSpace(body.KeyEnv)
	if envName == "" && strings.TrimSpace(body.Provider) == "jev" {
		envName = secrets.EnvTypeSafeAPIKey
	}
	if envName == "" {
		s.fail(w, http.StatusBadRequest, "config: classifier is missing key_env")
		return
	}
	vault, err := s.secretStore()
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.withoutLock(func() {
		err = vault.Set(secrets.APIKeyAccount(envName), body.Value)
	})
	if err != nil {
		msg := err.Error()
		if body.Value != "" && len(body.Value) >= 4 {
			msg = strings.ReplaceAll(msg, body.Value, "[redacted]")
		}
		s.fail(w, http.StatusBadRequest, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key_set": true})
}
