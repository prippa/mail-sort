// Package oauth signs a mailbox in with the user's own OAuth client.
// Refresh tokens are the caller's to store. Access tokens stay in memory.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/prippa/mail-sort/internal/buildinfo"
	"github.com/prippa/mail-sort/internal/config"
)

const (
	// GoogleScope is the Gmail IMAP scope documented by Google.
	GoogleScope = "https://mail.google.com/"
	// MicrosoftIMAPScope is the Outlook IMAP scope documented by Microsoft.
	MicrosoftIMAPScope = "https://outlook.office.com/IMAP.AccessAsUser.All"
	// MicrosoftGraphScope is the delegated Mail.ReadWrite permission.
	// Message move and copy list it as the least-privileged delegated permission.
	// It is a different resource from IMAP.AccessAsUser.All, so Graph filing
	// uses a second sign-in.
	// VERIFY: a live authorize request with this v2 scope string was not run.
	MicrosoftGraphScope = "https://graph.microsoft.com/Mail.ReadWrite"
	// OfflineAccess asks Microsoft for a refresh token.
	OfflineAccess = "offline_access"

	refreshEarly = 5 * time.Minute
)

var (
	// ErrNotSignedIn means no refresh token is stored for the account.
	ErrNotSignedIn = errors.New("oauth: this account is not signed in. Open the local page and sign in")
	// ErrNoClientID means neither the profile nor the build has a client id.
	ErrNoClientID = errors.New("oauth: client id is empty")
	// ErrReauth means the provider rejected the refresh token.
	ErrReauth = errors.New("oauth: the saved sign-in is no longer valid. Sign in again")
	// ErrGoogleDevice means a device-code request was made for Gmail.
	ErrGoogleDevice = errors.New("oauth: Google sign-in uses a browser on this computer. The device flow does not cover the Gmail scope")
)

// Account is the public OAuth client for one profile.
type Account struct {
	Auth     string
	ClientID string
	Tenant   string
	Email    string
	Device   bool
	// Graph asks for Mail.ReadWrite instead of the IMAP scope.
	Graph bool
	// Endpoint overrides the provider URLs. Tests set it. Production leaves it nil.
	Endpoint *oauth2.Endpoint
	// HTTP is the client used for the token endpoint. Tests set it.
	HTTP *http.Client
}

// FromProfile fills a client id from the profile or from the build.
func FromProfile(profile config.Profile) (Account, error) {
	account := Account{
		Auth:     profile.Auth,
		ClientID: strings.TrimSpace(profile.ClientID),
		Tenant:   strings.TrimSpace(profile.Tenant),
		Email:    strings.TrimSpace(profile.Email),
		Device:   profile.DeviceCode,
	}
	if account.ClientID == "" {
		switch profile.Auth {
		case "oauth_google":
			account.ClientID = strings.TrimSpace(buildinfo.DefaultGoogleClientID)
		case "oauth_microsoft":
			account.ClientID = strings.TrimSpace(buildinfo.DefaultMicrosoftClientID)
		default:
			return Account{}, fmt.Errorf("oauth: auth %q is not a sign-in", profile.Auth)
		}
	}
	if account.ClientID == "" {
		return Account{}, ErrNoClientID
	}
	if account.Email == "" {
		return Account{}, errors.New("oauth: profile is missing an email")
	}
	return account, nil
}

// Token is a sign-in result. Only the refresh token is stored.
type Token struct {
	RefreshToken string
	AccessToken  string
	Expiry       time.Time
}

// Flow is one in-progress sign-in.
type Flow struct {
	conf            *oauth2.Config
	verifier        string
	state           string
	authURL         string
	userCode        string
	verificationURI string
	device          *oauth2.DeviceAuthResponse
	http            *http.Client
	listener        net.Listener
	stop            context.CancelFunc
	done            chan struct{}
	token           Token
	err             error
	once            sync.Once
}

// AuthURL is the address to open in a browser. It is empty for device code.
func (f *Flow) AuthURL() string {
	if f == nil {
		return ""
	}
	return f.authURL
}

// UserCode is the short code the user types for a Microsoft device sign-in.
func (f *Flow) UserCode() string {
	if f == nil {
		return ""
	}
	return f.userCode
}

// VerificationURI is where the user enters UserCode.
func (f *Flow) VerificationURI() string {
	if f == nil {
		return ""
	}
	return f.verificationURI
}

// Begin starts a loopback sign-in, or a Microsoft device-code sign-in.
// The returned flow is ready to show. Wait blocks until the provider answers.
func Begin(ctx context.Context, account Account) (*Flow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if account.Device && account.Auth == "oauth_google" {
		return nil, ErrGoogleDevice
	}
	conf, err := configFor(account, "")
	if err != nil {
		return nil, err
	}
	flow := &Flow{
		conf: conf,
		http: account.HTTP,
		done: make(chan struct{}),
	}
	if account.Device {
		return flow.beginDevice(ctx)
	}
	return flow.beginLoopback(ctx, account)
}

func (f *Flow) beginDevice(ctx context.Context) (*Flow, error) {
	ctx = f.withClient(ctx)
	pending, err := f.conf.DeviceAuth(ctx)
	if err != nil {
		return nil, safeTokenError(err, "")
	}
	f.device = pending
	f.userCode = pending.UserCode
	f.verificationURI = pending.VerificationURI
	go func() {
		tok, err := f.conf.DeviceAccessToken(f.withClient(ctx), pending)
		f.finish(tok, err)
	}()
	return f, nil
}

func (f *Flow) beginLoopback(ctx context.Context, account Account) (*Flow, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("oauth: listen: %w", err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcp.Port == 0 {
		_ = ln.Close()
		return nil, errors.New("oauth: listener is not loopback")
	}
	port := strconv.Itoa(tcp.Port)
	// VERIFY: a Google Desktop client may reject an unregistered random port.
	redirect := "http://127.0.0.1:" + port
	if account.Auth == "oauth_microsoft" {
		// VERIFY: Entra may require the registered http://localhost to match this port.
		redirect = "http://localhost:" + port
	}
	conf, err := configFor(account, redirect)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	f.conf = conf
	f.listener = ln
	state, err := randomHex(32)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	f.state = state
	f.verifier = oauth2.GenerateVerifier()
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(f.verifier)}
	if account.Auth == "oauth_google" {
		opts = append(opts, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	}
	if account.Email != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", account.Email))
	}
	f.authURL = conf.AuthCodeURL(state, opts...)
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.callback)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	watch, stop := context.WithCancel(ctx)
	f.stop = stop
	go func() { _ = server.Serve(ln) }()
	go func() {
		<-watch.Done()
		f.fail(watch.Err())
		_ = server.Close()
	}()
	return f, nil
}

func (f *Flow) callback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	if query.Get("code") == "" && query.Get("error") == "" && query.Get("state") == "" {
		http.NotFound(w, r)
		return
	}
	if query.Get("error") != "" {
		f.fail(errors.New("oauth: the provider did not complete sign-in"))
		writeDone(w)
		return
	}
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(f.state)) != 1 {
		f.fail(errors.New("oauth: sign-in response did not match this request"))
		http.Error(w, "state", http.StatusBadRequest)
		return
	}
	code := query.Get("code")
	if code == "" || len(code) > 2048 {
		f.fail(errors.New("oauth: sign-in response had no authorization code"))
		http.Error(w, "code", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(f.withClient(context.Background()), 30*time.Second)
	defer cancel()
	tok, err := f.conf.Exchange(ctx, code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		http.Error(w, "token", http.StatusBadGateway)
		f.finish(tok, err)
		return
	}
	writeDone(w)
	f.finish(tok, nil)
}

func writeDone(w http.ResponseWriter) {
	const page = "<!DOCTYPE html><meta charset=\"utf-8\"><title>MailSorter</title><p>You can close this window and return to MailSorter.</p><p>Это окно можно закрыть и вернуться в MailSorter.</p>"
	_, _ = io.WriteString(w, page)
}

func (f *Flow) finish(tok *oauth2.Token, err error) {
	f.once.Do(func() {
		defer close(f.done)
		if err != nil {
			f.err = safeTokenError(err, "")
			return
		}
		if tok == nil || tok.RefreshToken == "" {
			f.err = errors.New("oauth: the server did not return a refresh token. Sign in again")
			return
		}
		if tok.AccessToken == "" {
			f.err = errors.New("oauth: the server did not return an access token")
			return
		}
		f.token = Token{RefreshToken: tok.RefreshToken, AccessToken: tok.AccessToken, Expiry: tok.Expiry}
	})
}

func (f *Flow) fail(err error) {
	f.finish(nil, err)
}

// Wait returns the tokens, or ctx.Err if the caller gives up.
func (f *Flow) Wait(ctx context.Context) (Token, error) {
	if f == nil {
		return Token{}, errors.New("oauth: sign-in was not started")
	}
	select {
	case <-ctx.Done():
		f.fail(ctx.Err())
		<-f.done
		f.shutdown()
		return Token{}, ctx.Err()
	case <-f.done:
		f.shutdown()
		if f.err != nil {
			return Token{}, f.err
		}
		return f.token, nil
	}
}

func (f *Flow) shutdown() {
	if f.stop != nil {
		f.stop()
	}
}

// Source turns a stored refresh token into access tokens.
type Source struct {
	mu       sync.Mutex
	conf     *oauth2.Config
	http     *http.Client
	refresh  string
	access   string
	expiry   time.Time
	onRotate func(string) error
}

// NewSource remembers refresh and refreshes the access token about five
// minutes before it expires. onRotate receives a replacement refresh token.
func NewSource(account Account, refresh string, onRotate func(string) error) (*Source, error) {
	if strings.TrimSpace(refresh) == "" {
		return nil, ErrNotSignedIn
	}
	conf, err := configFor(account, "")
	if err != nil {
		return nil, err
	}
	return &Source{conf: conf, http: account.HTTP, refresh: refresh, onRotate: onRotate}, nil
}

// AccessToken returns a cached access token, refreshing early when needed.
func (s *Source) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access != "" && time.Until(s.expiry) > refreshEarly {
		return s.access, nil
	}
	return s.refreshLocked(ctx)
}

// ForceRefresh discards the cached access token and asks the provider again.
func (s *Source) ForceRefresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = ""
	s.expiry = time.Time{}
	return s.refreshLocked(ctx)
}

func (s *Source) refreshLocked(ctx context.Context) (string, error) {
	ctx = s.withClient(ctx)
	old := s.refresh
	tok, err := s.conf.TokenSource(ctx, &oauth2.Token{
		RefreshToken: old,
		Expiry:       time.Now().Add(-time.Hour),
	}).Token()
	if err != nil {
		return "", safeTokenError(err, old)
	}
	if tok.AccessToken == "" {
		return "", errors.New("oauth: the server did not return an access token")
	}
	s.access = tok.AccessToken
	s.expiry = tok.Expiry
	if s.expiry.IsZero() {
		s.expiry = time.Now().Add(time.Hour)
	}
	if tok.RefreshToken != "" && tok.RefreshToken != old {
		s.refresh = tok.RefreshToken
		if s.onRotate != nil {
			if err := s.onRotate(tok.RefreshToken); err != nil {
				return "", err
			}
		}
	}
	return s.access, nil
}

func (s *Source) withClient(ctx context.Context) context.Context {
	if s.http == nil {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, s.http)
}

func (f *Flow) withClient(ctx context.Context) context.Context {
	if f.http == nil {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, f.http)
}

func configFor(account Account, redirect string) (*oauth2.Config, error) {
	if account.ClientID == "" {
		return nil, ErrNoClientID
	}
	endpoint, scopes, err := providerEndpoint(account)
	if err != nil {
		return nil, err
	}
	if account.Endpoint != nil {
		endpoint = *account.Endpoint
	}
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	return &oauth2.Config{
		ClientID:    account.ClientID,
		Endpoint:    endpoint,
		RedirectURL: redirect,
		Scopes:      scopes,
	}, nil
}

func providerEndpoint(account Account) (oauth2.Endpoint, []string, error) {
	switch account.Auth {
	case "oauth_google":
		// Google documents these for a desktop app. The Gmail scope is not on the device flow.
		return oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		}, []string{GoogleScope}, nil
	case "oauth_microsoft":
		tenant := account.Tenant
		if tenant == "" {
			tenant = "common"
		}
		base := "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/"
		scopes := []string{MicrosoftIMAPScope, OfflineAccess}
		if account.Graph {
			scopes = []string{MicrosoftGraphScope, OfflineAccess}
		}
		return oauth2.Endpoint{
			AuthURL:       base + "authorize",
			TokenURL:      base + "token",
			DeviceAuthURL: base + "devicecode",
		}, scopes, nil
	default:
		return oauth2.Endpoint{}, nil, fmt.Errorf("oauth: auth %q is not a sign-in", account.Auth)
	}
}

func safeTokenError(err error, secret string) error {
	if err == nil {
		return nil
	}
	var retrieved *oauth2.RetrieveError
	if errors.As(err, &retrieved) && retrieved.ErrorCode == "invalid_grant" {
		return ErrReauth
	}
	text := err.Error()
	if secret != "" && strings.Contains(text, secret) {
		return errors.New("oauth: the token request failed")
	}
	if strings.Contains(text, "invalid_grant") {
		return ErrReauth
	}
	return errors.New("oauth: the token request failed")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oauth: random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
