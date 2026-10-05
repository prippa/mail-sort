package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prippa/mail-sort/internal/i18n"
	"github.com/prippa/mail-sort/internal/secrets"
)

func TestUISecurityAndPreview(t *testing.T) {
	srv := startUI(t)
	page := call(t, srv, http.MethodGet, "/", "", false)
	if page.StatusCode != http.StatusOK {
		t.Fatalf("page %d", page.StatusCode)
	}
	if csp := page.Header.Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("csp %s", csp)
	}
	body := readAll(t, page)
	if strings.Contains(body, "https://") || strings.Contains(body, "http://") || strings.Contains(body, "x-html") {
		t.Fatal("page references another origin or renders HTML")
	}

	missing := call(t, srv, http.MethodGet, "/api/bootstrap", "", false)
	if missing.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token %d %s", missing.StatusCode, readAll(t, missing))
	}
	_ = missing.Body.Close()
	req, err := http.NewRequest(http.MethodGet, srv.origin+"/api/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = srv.host
	req.Header.Set("Origin", srv.origin)
	req.Header.Set(tokenHeader, srv.token)
	res, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("header only %d", res.StatusCode)
	}

	badOrigin, err := http.NewRequest(http.MethodGet, srv.origin+"/api/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	badOrigin.Host = srv.host
	badOrigin.Header.Set("Origin", "http://evil.example")
	badOrigin.Header.Set(tokenHeader, srv.token)
	badOrigin.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	res, err = (&http.Client{Timeout: 3 * time.Second}).Do(badOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("origin %d", res.StatusCode)
	}

	for _, site := range []string{"", "same-origin", "none"} {
		same, err := http.NewRequest(http.MethodGet, srv.origin+"/api/bootstrap", nil)
		if err != nil {
			t.Fatal(err)
		}
		same.Host = srv.host
		if site != "" {
			same.Header.Set("Sec-Fetch-Site", site)
		}
		same.Header.Set(tokenHeader, srv.token)
		same.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
		res, err = (&http.Client{Timeout: 3 * time.Second}).Do(same)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("fetch site %q status %d", site, res.StatusCode)
		}
		_ = res.Body.Close()
	}
	cross, err := http.NewRequest(http.MethodGet, srv.origin+"/api/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	cross.Host = srv.host
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	cross.Header.Set(tokenHeader, srv.token)
	cross.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	res, err = (&http.Client{Timeout: 3 * time.Second}).Do(cross)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site %d", res.StatusCode)
	}
	_ = res.Body.Close()

	badHost, err := http.NewRequest(http.MethodGet, srv.origin+"/api/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	badHost.Host = "localhost" + srv.host[strings.LastIndex(srv.host, ":"):]
	badHost.Header.Set("Origin", srv.origin)
	badHost.Header.Set(tokenHeader, srv.token)
	badHost.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	res, err = (&http.Client{Timeout: 3 * time.Second}).Do(badHost)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("host %d %s", res.StatusCode, badHost.Host)
	}

	const secret = "hunter2-secret-value"
	denied := call(t, srv, http.MethodPost, "/api/profiles", `{"name":"A","password":"`+secret+`"}`, true)
	deniedBody := readAll(t, denied)
	if denied.StatusCode != http.StatusBadRequest || strings.Contains(deniedBody, secret) {
		t.Fatalf("status %d body %s", denied.StatusCode, deniedBody)
	}

	preview := call(t, srv, http.MethodPost, "/api/preview", `{"from":"ada@example.com","subject":"Invoice","body":"card 4111111111111111"}`, true)
	previewBody := readAll(t, preview)
	if preview.StatusCode != http.StatusOK || strings.Contains(previewBody, "4111111111111111") || !strings.Contains(previewBody, "[redacted]") {
		t.Fatalf("preview %d %s", preview.StatusCode, previewBody)
	}
}

func TestApplyWithoutConfirmDoesNotDial(t *testing.T) {
	srv := startUI(t)
	t.Setenv("MAIL_SORTER_PASSWORD_WORK", "mailbox-secret-value")
	saved := call(t, srv, http.MethodPost, "/api/profiles", `{
		"name":"Work","provider":"fastmail","host":"127.0.0.1","port":1,
		"username":"ada@example.com","email":"ada@example.com","password_env":"MAIL_SORTER_PASSWORD_WORK"
	}`, true)
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("save %d %s", saved.StatusCode, readAll(t, saved))
	}
	_ = saved.Body.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, srv.origin+"/api/apply", strings.NewReader(`{"profile":"Work"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = srv.host
	req.Header.Set("Origin", srv.origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, srv.token)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body := readAll(t, res)
	if res.StatusCode != http.StatusConflict || strings.Contains(body, "mailbox-secret-value") || !strings.Contains(body, "not confirmed") {
		t.Fatalf("apply %d %s", res.StatusCode, body)
	}
}

func TestRemoteTryNeedsConsent(t *testing.T) {
	dialed := false
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, Options{
		ConfigPath: filepath.Join(root, "config.yaml"),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			dialed = true
			t.Error("classifier was dialed")
			return nil, io.ErrClosedPipe
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	saved := call(t, srv, http.MethodPost, "/api/classifiers", `{"classifiers":[{"provider":"jev","base_url":"https://api.typesafe.ai"}]}`, true)
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("save %d %s", saved.StatusCode, readAll(t, saved))
	}
	_ = saved.Body.Close()
	tried := call(t, srv, http.MethodPost, "/api/try", `{"subject":"Hello","body":"Please file this"}`, true)
	body := readAll(t, tried)
	if tried.StatusCode != http.StatusConflict || !strings.Contains(body, `"consent":true`) || dialed {
		t.Fatalf("try %d dialed %v %s", tried.StatusCode, dialed, body)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	srv := startUI(t)
	saved := call(t, srv, http.MethodPost, "/api/settings", `{"language":"ru","privacy":{"redact_email":true,"local_only":true}}`, true)
	body := readAll(t, saved)
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("settings %d %s", saved.StatusCode, body)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	stringsMap, _ := view["strings"].(map[string]any)
	if stringsMap["nav.accounts"] != "Учётные записи" {
		t.Fatalf("language %+v", stringsMap["nav.accounts"])
	}
	raw, err := os.ReadFile(srv.configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "redact_email: true") || !strings.Contains(text, "language: ru") {
		t.Fatalf("config %s", text)
	}
	info, err := os.Stat(srv.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func TestOAuthStartAndKeyStayLocal(t *testing.T) {
	srv := startUI(t)
	t.Setenv(secrets.EnvMasterPassword, "test-master")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	const key = "classifier-secret-value"
	started := call(t, srv, http.MethodPost, "/api/oauth/start", `{
		"name":"Gmail","provider":"gmail","host":"`+host+`","port":`+port+`,
		"username":"ada@example.com","email":"ada@example.com","auth":"oauth_google"
	}`, true)
	startedBody := readAll(t, started)
	if started.StatusCode != http.StatusBadRequest || !strings.Contains(startedBody, "client id is empty") || strings.Contains(startedBody, key) {
		t.Fatalf("start %d %s", started.StatusCode, startedBody)
	}
	if raw, err := os.ReadFile(srv.configPath); err == nil && strings.Contains(string(raw), "Gmail") {
		t.Fatalf("empty client id was saved: %s", raw)
	}
	select {
	case <-accepted:
		t.Fatal("sign-in dialed the mailbox")
	case <-time.After(50 * time.Millisecond):
	}
	listed := call(t, srv, http.MethodPost, "/api/classifiers", `{"classifiers":[{"provider":"jev","model":"jev-1.13.0"}]}`, true)
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("classifier %d %s", listed.StatusCode, readAll(t, listed))
	}
	_ = listed.Body.Close()
	saved := call(t, srv, http.MethodPost, "/api/classifiers/key", `{"provider":"jev","value":"`+key+`"}`, true)
	savedBody := readAll(t, saved)
	if saved.StatusCode != http.StatusOK || strings.Contains(savedBody, key) {
		t.Fatalf("key %d %s", saved.StatusCode, savedBody)
	}
	raw, err := os.ReadFile(srv.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("config contains the key")
	}
	boot := call(t, srv, http.MethodGet, "/api/bootstrap", "", true)
	bootBody := readAll(t, boot)
	if !strings.Contains(bootBody, `"key_set":true`) || strings.Contains(bootBody, key) {
		t.Fatalf("bootstrap %s", bootBody)
	}
}

func TestAssetsAndStrings(t *testing.T) {
	t.Parallel()
	alpine, err := assetFS.ReadFile("assets/alpine.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(alpine)
	const alpineCSP3174 = "0d18d7f8d7910e2e0212f0f056b12f50bebc3abb7d88d2f7c7cb4c336fe4519a"
	if hex.EncodeToString(sum[:]) != alpineCSP3174 {
		t.Fatalf("alpine hash %s", hex.EncodeToString(sum[:]))
	}
	for _, name := range []string{"assets/index.html", "assets/app.js", "assets/app.css"} {
		data, err := assetFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "http://") || strings.Contains(text, "https://") || strings.Contains(text, "innerHTML") || strings.Contains(text, "x-html") {
			t.Fatalf("%s is not local", name)
		}
	}
	js, err := assetFS.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := assetFS.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	both := string(js) + string(html)
	for {
		start := strings.Index(both, "text('")
		if start < 0 {
			break
		}
		both = both[start+6:]
		end := strings.IndexByte(both, '\'')
		if end < 0 {
			t.Fatal("unclosed text key")
		}
		key := both[:end]
		both = both[end:]
		if i18n.T(i18n.EN, key) == key {
			t.Fatalf("missing string %s", key)
		}
	}
}

func startUI(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, Options{ConfigPath: filepath.Join(root, "config.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

func call(t *testing.T, srv *Server, method, path, body string, token bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.origin+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = srv.host
	req.Header.Set("Origin", srv.origin)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token {
		req.Header.Set(tokenHeader, srv.token)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
