package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prippa/mail-sort/internal/classify"
	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/oauth"
	"github.com/prippa/mail-sort/internal/secrets"
	"github.com/prippa/mail-sort/internal/store"
)

//go:embed assets/index.html assets/app.css assets/app.js assets/alpine.min.js
var assetFS embed.FS

const (
	cookieName  = "mailsorter_session"
	tokenHeader = "X-MailSorter-Token"
	csp         = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"
)

// Options configures one local UI process.
type Options struct {
	ConfigPath  string
	Logger      *slog.Logger
	Lookup      func(string) (string, bool)
	HTTPClient  *http.Client
	OpenBrowser func(string) error
}

// Server is the local UI. URL includes the per-launch token in the fragment
// so the token is not sent as a query string.
type Server struct {
	URL         string
	configPath  string
	stateDir    string
	host        string
	origin      string
	token       string
	db          *store.DB
	cache       *classify.SQLite
	log         *slog.Logger
	lookup      func(string) (string, bool)
	httpClient  *http.Client
	openBrowser func(string) error
	http        *http.Server
	mu          sync.Mutex
	once        sync.Once
	vaultOnce   sync.Once
	vault       secrets.Store
	vaultErr    error
	signIns     map[string]*signIn
}

// Start listens on 127.0.0.1 and a random port.
func Start(ctx context.Context, opt Options) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opt.ConfigPath == "" {
		return nil, errors.New("ui: config path is empty")
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opt.Lookup == nil {
		opt.Lookup = os.LookupEnv
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("ui: state dir: %w", err)
	}
	dbPath := filepath.Join(stateDir, "mailsorter.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	cache, err := classify.OpenCache(ctx, dbPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = cache.Close()
		_ = db.Close()
		return nil, fmt.Errorf("ui: listen: %w", err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		_ = cache.Close()
		_ = db.Close()
		return nil, errors.New("ui: listener is not loopback")
	}
	token, err := newToken()
	if err != nil {
		_ = ln.Close()
		_ = cache.Close()
		_ = db.Close()
		return nil, err
	}
	host := net.JoinHostPort(tcp.IP.String(), strconv.Itoa(tcp.Port))
	srv := &Server{
		URL:         "http://" + host + "/#" + token,
		configPath:  opt.ConfigPath,
		stateDir:    stateDir,
		host:        host,
		origin:      "http://" + host,
		token:       token,
		db:          db,
		cache:       cache,
		log:         opt.Logger,
		lookup:      opt.Lookup,
		httpClient:  opt.HTTPClient,
		openBrowser: opt.OpenBrowser,
		signIns:     map[string]*signIn{},
	}
	srv.http = &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	go func() {
		err := srv.http.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			srv.log.Error("ui stopped", slog.String("error", err.Error()))
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	srv.log.Info("ui listening", slog.String("host", "127.0.0.1"), slog.Int("port", tcp.Port))
	return srv, nil
}

// Shutdown stops the listener and closes the state database.
func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		for _, item := range s.signIns {
			if item.cancel != nil {
				item.cancel()
			}
		}
		s.mu.Unlock()
		err = s.http.Shutdown(ctx)
		if s.cache != nil {
			_ = s.cache.Close()
		}
		if s.db != nil {
			_ = s.db.Close()
		}
	})
	return err
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ui: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.Host != s.host {
		http.Error(w, i18n.T(i18n.EN, "error.host"), http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.api(w, r)
		return
	}
	s.static(w, r)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, i18n.T(i18n.EN, "error.bad_request"), http.StatusMethodNotAllowed)
		return
	}
	name := ""
	contentType := ""
	switch r.URL.Path {
	case "/", "/index.html":
		name = "assets/index.html"
		contentType = "text/html; charset=utf-8"
	case "/assets/app.css":
		name = "assets/app.css"
		contentType = "text/css; charset=utf-8"
	case "/assets/app.js":
		name = "assets/app.js"
		contentType = "text/javascript; charset=utf-8"
	case "/assets/alpine.min.js":
		name = "assets/alpine.min.js"
		contentType = "text/javascript; charset=utf-8"
	default:
		http.Error(w, i18n.T(i18n.EN, "error.not_found"), http.StatusNotFound)
		return
	}
	data, err := assetFS.ReadFile(name)
	if err != nil {
		http.Error(w, i18n.T(i18n.EN, "error.not_found"), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *Server) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == s.origin {
		return true
	}
	if origin != "" {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get(tokenHeader)
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" || header == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(header), []byte(s.token)) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.token)) == 1
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(r) {
		s.log.Info("ui origin rejected", slog.String("origin", r.Header.Get("Origin")), slog.String("fetch_site", r.Header.Get("Sec-Fetch-Site")))
		s.fail(w, http.StatusForbidden, i18n.T(i18n.EN, "error.origin"))
		return
	}
	if !s.authorized(r) {
		s.fail(w, http.StatusUnauthorized, i18n.T(i18n.EN, "error.token"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/bootstrap":
		s.bootstrap(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/activity":
		s.activity(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/evaluate":
		s.evaluate(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/evaluate/label":
		s.labelExample(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/run":
		s.currentRun(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/oauth/start":
		s.startOAuth(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/oauth/client-secret":
		s.saveGoogleClientSecret(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/oauth/status":
		s.oauthStatus(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/classifiers/key":
		s.saveClassifierKey(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/profiles":
		s.saveProfile(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/profiles/test":
		s.testProfile(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/profiles/delete":
		s.deleteProfile(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/categories":
		s.saveCategories(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/categories/starter":
		s.starterCategories(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/classifiers":
		s.saveClassifiers(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/settings":
		s.saveSettings(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/consent":
		s.grantConsent(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/preview":
		s.preview(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/try":
		s.tryClassify(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/plan":
		s.plan(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/confirm":
		s.confirm(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/override":
		s.override(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/apply":
		s.apply(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/undo":
		s.undo(w, r)
	default:
		s.fail(w, http.StatusNotFound, i18n.T(i18n.EN, "error.not_found"))
	}
}

func (s *Server) fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		s.fail(w, http.StatusBadRequest, i18n.T(i18n.EN, "error.bad_request"))
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	if key, bad := secretJSONKey(raw); bad {
		s.fail(w, http.StatusBadRequest, fmt.Sprintf("field %q is not allowed", key))
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		s.fail(w, http.StatusBadRequest, i18n.T(i18n.EN, "error.bad_request"))
		return false
	}
	return true
}

func secretJSONKey(raw []byte) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return walkSecret(value)
}

func walkSecret(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if secrets.IsSecretConfigKey(key) {
				return key, true
			}
			if found, bad := walkSecret(child); bad {
				return found, bad
			}
		}
	case []any:
		for _, child := range typed {
			if found, bad := walkSecret(child); bad {
				return found, bad
			}
		}
	}
	return "", false
}

func (s *Server) loadConfig(ctx context.Context) (config.Config, error) {
	if _, err := os.Stat(s.configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.Config{}, nil
		}
		return config.Config{}, err
	}
	return config.Load(ctx, s.configPath)
}

func (s *Server) language(cfg config.Config) i18n.Lang {
	switch cfg.Language {
	case "en":
		return i18n.EN
	case "ru":
		return i18n.RU
	default:
		return i18n.FromEnv(os.Getenv)
	}
}

func (s *Server) scrub(cfg config.Config, msg string) string {
	for _, secret := range s.secretValues(cfg) {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	return msg
}

func (s *Server) secretValues(cfg config.Config) []string {
	var out []string
	add := func(name string) {
		if name == "" {
			return
		}
		value, ok := s.lookup(name)
		if ok && len(value) >= 4 {
			out = append(out, value)
		}
	}
	for _, profile := range cfg.Profiles {
		add(profile.PasswordEnv)
		if profile.Auth != "oauth_google" {
			continue
		}
		vault, err := s.secretStore()
		if err != nil {
			continue
		}
		value, err := vault.Get(secrets.GoogleClientAccount(profile.Name))
		if err == nil && len(strings.TrimSpace(value)) >= 4 {
			out = append(out, strings.TrimSpace(value))
		}
	}
	add(oauth.EnvGoogleClientSecret)
	for _, spec := range cfg.Classifiers {
		name := spec.KeyEnv
		if name == "" && spec.Provider == "jev" {
			name = secrets.EnvTypeSafeAPIKey
		}
		add(name)
	}
	return out
}

func (s *Server) publicError(cfg config.Config, err error) string {
	if err == nil {
		return ""
	}
	return s.scrub(cfg, err.Error())
}
