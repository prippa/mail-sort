package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/prippa/mail-sort/internal/config"
)

func TestLoopbackSignIn(t *testing.T) {
	var posted url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		posted = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	account := Account{
		Auth:     "oauth_google",
		ClientID: "desktop-client",
		Email:    "ada@example.com",
		Endpoint: testEndpoint(srv.URL),
		HTTP:     srv.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	flow, err := Begin(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = flow.Wait(context.Background())
	})
	auth, err := url.Parse(flow.AuthURL())
	if err != nil {
		t.Fatal(err)
	}
	q := auth.Query()
	if q.Get("scope") != GoogleScope || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("auth query %s", auth.RawQuery)
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Fatalf("redirect %s", q.Get("redirect_uri"))
	}
	callback := q.Get("redirect_uri") + "/?code=auth-code&state=" + url.QueryEscape(q.Get("state"))
	res, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(body), "auth-code") || !strings.Contains(string(body), "close this window") {
		t.Fatalf("callback %d %s", res.StatusCode, body)
	}
	tok, err := flow.Wait(ctx)
	if err != nil || tok.RefreshToken != "refresh-1" || tok.AccessToken != "access-1" {
		t.Fatalf("token %+v err=%v", tok, err)
	}
	if posted.Get("grant_type") != "authorization_code" || posted.Get("code_verifier") == "" || posted.Get("client_secret") != "" {
		t.Fatalf("token form %v", posted)
	}
}

func TestGoogleDeviceDoesNotDial(t *testing.T) {
	account := Account{
		Auth:     "oauth_google",
		ClientID: "desktop-client",
		Email:    "ada@example.com",
		Device:   true,
		HTTP:     &http.Client{Transport: failTransport{t}},
	}
	_, err := Begin(context.Background(), account)
	if !errors.Is(err, ErrGoogleDevice) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceCode(t *testing.T) {
	var grant string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "device-1",
				"user_code":        "ABCD-EFGH",
				"verification_uri": srv.URL + "/verify",
				"expires_in":       60,
				"interval":         1,
			})
		case "/token":
			grant = r.PostForm.Get("grant_type")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-device",
				"refresh_token": "refresh-device",
				"expires_in":    3600,
				"token_type":    "Bearer",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	account := Account{
		Auth:     "oauth_microsoft",
		ClientID: "public-client",
		Tenant:   "common",
		Email:    "ada@example.com",
		Device:   true,
		Endpoint: testEndpoint(srv.URL),
		HTTP:     srv.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	flow, err := Begin(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if flow.UserCode() != "ABCD-EFGH" || flow.VerificationURI() == "" || flow.AuthURL() != "" {
		t.Fatalf("flow url=%q code=%q uri=%q", flow.AuthURL(), flow.UserCode(), flow.VerificationURI())
	}
	tok, err := flow.Wait(ctx)
	if err != nil || tok.RefreshToken != "refresh-device" {
		t.Fatalf("token %+v err=%v", tok, err)
	}
	if grant != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("grant %q", grant)
	}
}

func TestSourceRefresh(t *testing.T) {
	var calls int
	var rotated string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		refresh := "refresh-rotated"
		if calls == 1 {
			refresh = "refresh-old"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-" + r.PostForm.Get("refresh_token"),
			"refresh_token": refresh,
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	account := Account{
		Auth:     "oauth_google",
		ClientID: "desktop-client",
		Email:    "ada@example.com",
		Endpoint: testEndpoint(srv.URL),
		HTTP:     srv.Client(),
	}
	src, err := NewSource(account, "refresh-old", func(next string) error {
		rotated = next
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := src.AccessToken(ctx)
	if err != nil || first != "access-refresh-old" || calls != 1 {
		t.Fatalf("first %q calls=%d err=%v", first, calls, err)
	}
	second, err := src.AccessToken(ctx)
	if err != nil || second != first || calls != 1 {
		t.Fatalf("cached %q calls=%d err=%v", second, calls, err)
	}
	forced, err := src.ForceRefresh(ctx)
	if err != nil || forced == "" || calls != 2 || rotated != "refresh-rotated" {
		t.Fatalf("forced %q calls=%d rotated=%q err=%v", forced, calls, rotated, err)
	}
}

func TestSourceRefreshesInsideFiveMinutes(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"refresh_token": "refresh-old",
			"expires_in":    60,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	account := Account{
		Auth:     "oauth_microsoft",
		ClientID: "public-client",
		Email:    "ada@example.com",
		Endpoint: testEndpoint(srv.URL),
		HTTP:     srv.Client(),
	}
	src, err := NewSource(account, "refresh-old", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := src.AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestInvalidGrantDoesNotEchoToken(t *testing.T) {
	const secret = "refresh-secret-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": secret,
		})
	}))
	t.Cleanup(srv.Close)
	account := Account{
		Auth:     "oauth_google",
		ClientID: "desktop-client",
		Email:    "ada@example.com",
		Endpoint: testEndpoint(srv.URL),
		HTTP:     srv.Client(),
	}
	src, err := NewSource(account, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.AccessToken(context.Background())
	if !errors.Is(err, ErrReauth) || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}

func TestGraphScopeStaysOffTheIMAPSignIn(t *testing.T) {
	_, scopes, err := providerEndpoint(Account{Auth: "oauth_microsoft"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0] != MicrosoftIMAPScope || scopes[1] != OfflineAccess {
		t.Fatalf("imap scopes %v", scopes)
	}
	_, graphScopes, err := providerEndpoint(Account{Auth: "oauth_microsoft", Graph: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(graphScopes) != 2 || graphScopes[0] != MicrosoftGraphScope || graphScopes[1] != OfflineAccess {
		t.Fatalf("graph scopes %v", graphScopes)
	}
	for _, scope := range graphScopes {
		if strings.Contains(scope, "IMAP") {
			t.Fatalf("graph scopes %v", graphScopes)
		}
	}
}

func TestLoopbackSendsDesktopClientSecret(t *testing.T) {
	const secret = "desktop-secret-do-not-print"
	var posted url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		posted = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	account := Account{
		Auth:         "oauth_google",
		ClientID:     "desktop-client",
		ClientSecret: secret,
		Email:        "ada@example.com",
		Endpoint:     testEndpoint(srv.URL),
		HTTP:         srv.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	flow, err := Begin(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := url.Parse(flow.AuthURL())
	if err != nil {
		t.Fatal(err)
	}
	q := auth.Query()
	res, err := http.Get(q.Get("redirect_uri") + "/?code=auth-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(body), secret) {
		t.Fatal("callback page contains the client secret")
	}
	tok, err := flow.Wait(ctx)
	if err != nil || tok.RefreshToken != "refresh-1" {
		t.Fatalf("wait err=%v", err)
	}
	if posted.Get("client_secret") != secret || posted.Get("code_verifier") == "" {
		t.Fatal("token form omitted the desktop client secret or the verifier")
	}
}

func TestFromProfileReadsGoogleClientSecret(t *testing.T) {
	const secret = "desktop-secret-do-not-print"
	t.Setenv(EnvGoogleClientSecret, secret)
	google, err := FromProfile(config.Profile{Auth: "oauth_google", ClientID: "desktop-client", Email: "ada@example.com"})
	if err != nil || google.ClientSecret != secret {
		t.Fatal("google client secret was not read from the environment")
	}
	microsoft, err := FromProfile(config.Profile{Auth: "oauth_microsoft", ClientID: "public-client", Email: "ada@example.com"})
	if err != nil || microsoft.ClientSecret != "" {
		t.Fatal("microsoft sign-in picked up the google client secret")
	}
}

func TestStoredClientSecretDoesNotOverrideEnv(t *testing.T) {
	const secret = "desktop-secret-do-not-print"
	t.Setenv(EnvGoogleClientSecret, secret)
	account, err := FromProfile(config.Profile{Auth: "oauth_google", ClientID: "desktop-client", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got := account.UseStoredClientSecret("other-value"); got.ClientSecret != secret {
		t.Fatal("stored secret replaced the environment")
	}
	empty := Account{Auth: "oauth_google"}.UseStoredClientSecret("  stored  ")
	if empty.ClientSecret != "stored" {
		t.Fatal("stored secret was not applied")
	}
	microsoft := Account{Auth: "oauth_microsoft"}.UseStoredClientSecret("stored")
	if microsoft.ClientSecret != "" {
		t.Fatal("microsoft account accepted the google secret")
	}
}

func TestMissingGoogleClientSecret(t *testing.T) {
	err := safeTokenError(&oauth2.RetrieveError{
		ErrorCode:        "invalid_request",
		ErrorDescription: "client_secret is missing.",
	})
	if !errors.Is(err, ErrGoogleClientSecret) {
		t.Fatal(err)
	}
	const secret = "desktop-secret-do-not-print"
	echoed := safeTokenError(&oauth2.RetrieveError{
		ErrorCode:        "invalid_request",
		ErrorDescription: "client_secret is missing. " + secret,
	}, secret)
	if errors.Is(echoed, ErrGoogleClientSecret) || strings.Contains(echoed.Error(), secret) {
		t.Fatalf("echoed %v", echoed)
	}
}

func TestMissingClientAndRefreshDoNotDial(t *testing.T) {
	account := Account{Auth: "oauth_google", Email: "ada@example.com", HTTP: &http.Client{Transport: failTransport{t}}}
	_, err := Begin(context.Background(), account)
	if !errors.Is(err, ErrNoClientID) {
		t.Fatalf("begin %v", err)
	}
	_, err = FromProfile(config.Profile{Auth: "oauth_google", Email: "ada@example.com"})
	if !errors.Is(err, ErrNoClientID) {
		t.Fatalf("profile %v", err)
	}
	_, err = NewSource(Account{Auth: "oauth_google", ClientID: "desktop-client", Email: "ada@example.com", HTTP: account.HTTP}, "  ", nil)
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("source %v", err)
	}
}

type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("oauth dialed")
	return nil, errors.New("dialed")
}

func testEndpoint(base string) *oauth2.Endpoint {
	return &oauth2.Endpoint{
		AuthURL:       base + "/auth",
		TokenURL:      base + "/token",
		DeviceAuthURL: base + "/device",
	}
}
