package mail

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/prippa/mail-sort/internal/config"
	"golang.org/x/text/encoding/charmap"
)

func TestPresetsMatchDocumentedHosts(t *testing.T) {
	t.Parallel()
	presets, err := LoadPresets("")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Preset{}
	for _, preset := range presets {
		if _, ok := byID[preset.ID]; ok {
			t.Fatalf("duplicate preset %s", preset.ID)
		}
		byID[preset.ID] = preset
		if !preset.Verified || preset.Source == "" {
			t.Fatalf("%s is not marked verified", preset.ID)
		}
	}
	for _, id := range []string{"mailru", "mail.ru", "proton"} {
		if _, ok := byID[id]; ok {
			t.Fatalf("unverified preset %s was shipped", id)
		}
	}
	if host := byID["gmail"].Hosts[0].Host; host != "imap.gmail.com" {
		t.Fatalf("gmail host %s", host)
	}
	if host := byID["gmx"].Hosts[0].Host; host != "imap.gmx.com" {
		t.Fatalf("gmx host %s", host)
	}
	if len(byID["microsoft"].Auth) != 1 || byID["microsoft"].Auth[0] != "oauth_microsoft" {
		t.Fatalf("microsoft auth %v", byID["microsoft"].Auth)
	}
	if len(byID["zoho"].Hosts) != 2 || len(byID["workmail"].Hosts) != 3 {
		t.Fatal("zoho or workmail host list changed")
	}
}

func TestPresetOverlayReplacesByID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "presets.json")
	body := `[{
		"id": "gmail",
		"name": "Gmail",
		"source": "https://example.test/gmail",
		"verified": true,
		"default_host": "default",
		"hosts": [{"id": "default", "host": "imap.example.test", "port": 993, "security": "implicit_tls"}],
		"auth": ["password"],
		"username_hint": "full address",
		"note": "overlay",
		"auth_failure_hint": "overlay hint"
	}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	presets, err := LoadPresets(path)
	if err != nil {
		t.Fatal(err)
	}
	preset, ok := findPreset(presets, "gmail")
	if !ok || preset.Hosts[0].Host != "imap.example.test" || preset.Note != "overlay" {
		t.Fatalf("overlay = %+v", preset)
	}
}

func TestResolveAuthAndHosts(t *testing.T) {
	t.Parallel()
	presets, err := LoadPresets("")
	if err != nil {
		t.Fatal(err)
	}
	gmail, err := Resolve(profile("Gmail", "gmail", "MAIL_SORTER_PASSWORD_GMAIL"), presets)
	if err != nil {
		t.Fatal(err)
	}
	if gmail.Endpoint.Host != "imap.gmail.com" || gmail.Auth != AuthPassword {
		t.Fatalf("gmail = %+v", gmail)
	}
	gmailOAuth, err := Resolve(profileAuth("Gmail", "gmail", "oauth_google"), presets)
	if err != nil || gmailOAuth.Auth != AuthOAuthGoogle || gmailOAuth.Endpoint.Host != "imap.gmail.com" {
		t.Fatalf("oauth = %+v err=%v", gmailOAuth, err)
	}
	microsoft, err := Resolve(profile("Work", "microsoft", "MAIL_SORTER_PASSWORD_WORK"), presets)
	if err != nil || microsoft.Auth != AuthOAuthMicrosoft || microsoft.Endpoint.Host != "outlook.office365.com" {
		t.Fatalf("microsoft = %+v err=%v", microsoft, err)
	}
	_, err = Resolve(profileAuth("Work", "microsoft", "password"), presets)
	if err == nil || !strings.Contains(err.Error(), "OAuth2") {
		t.Fatalf("microsoft password err = %v", err)
	}
	_, err = Resolve(profile("Zoho", "zoho", "MAIL_SORTER_PASSWORD_ZOHO"), presets)
	if err == nil || !strings.Contains(err.Error(), "host_id") {
		t.Fatalf("zoho err = %v", err)
	}
	zoho := profile("Zoho", "zoho", "MAIL_SORTER_PASSWORD_ZOHO")
	zoho.HostID = "personal"
	got, err := Resolve(zoho, presets)
	if err != nil || got.Endpoint.Host != "imap.zoho.com" {
		t.Fatalf("zoho = %+v err=%v", got, err)
	}
	work := profile("AWS", "workmail", "MAIL_SORTER_PASSWORD_AWS")
	work.HostID = "eu-west-1"
	got, err = Resolve(work, presets)
	if err != nil || got.Endpoint.Host != "imap.mail.eu-west-1.awsapps.com" {
		t.Fatalf("workmail = %+v err=%v", got, err)
	}
	yandex, err := Resolve(profile("Ya", "yandex", "MAIL_SORTER_PASSWORD_YA"), presets)
	if err != nil || yandex.Endpoint.Host != "imap.yandex.com" {
		t.Fatalf("yandex = %+v err=%v", yandex, err)
	}
	custom := profile("Mine", "custom", "MAIL_SORTER_PASSWORD_MINE")
	custom.Discover = true
	custom.Email = "ada@example.com"
	got, err = Resolve(custom, presets)
	if err != nil || !got.Discover {
		t.Fatalf("custom = %+v err=%v", got, err)
	}
}

func TestTLSConfigKeepsVerification(t *testing.T) {
	t.Parallel()
	cfg, err := tlsConfig(Endpoint{Host: "imap.example.com", Port: 993, Security: ImplicitTLS})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InsecureSkipVerify || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("tls config disables verification or allows old TLS")
	}
	pinned, err := tlsConfig(Endpoint{Host: "127.0.0.1", Port: 993, Security: ImplicitTLS, CertSHA256: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.InsecureSkipVerify || pinned.VerifyPeerCertificate == nil {
		t.Fatal("fingerprint pin must check the leaf")
	}
}

func TestConnectionGate(t *testing.T) {
	t.Parallel()
	client := NewClient()
	if err := client.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.acquire(ctx); err == nil {
		t.Fatal("third connection was accepted")
	}
}

func TestImplicitTLSFetchLeavesSeenUnset(t *testing.T) {
	cert, pool, pin := newCert(t)
	const user = "ada"
	const password = "s3cret-do-not-echo"
	addr := startIMAP(t, cert, implicitListener, fullCaps(), password)
	host, port := splitAddr(t, addr)
	caPath := writeCert(t, cert.Certificate[0])

	raw := dialRaw(t, addr, pool, user, password)
	older := appendRaw(t, raw, "INBOX", textMessage("older@example.com", "Older", "first"))
	newer := appendRaw(t, raw, "INBOX", multipartMessage())
	encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte("Привет"))
	if err != nil {
		t.Fatal(err)
	}
	charsetMsg := appendRaw(t, raw, "INBOX", eightBitMessage(encoded))
	if err := raw.Logout().Wait(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: ImplicitTLS, CAFile: caPath},
		Auth:     AuthPassword,
		Username: user,
	}
	ctx := context.Background()
	client := NewClient()
	session, err := client.Connect(ctx, account, password)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	caps, err := session.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Move || !caps.UIDPlus || !caps.Idle || !caps.SpecialUse || caps.GmailExt {
		t.Fatalf("caps = %+v", caps)
	}
	folders, err := session.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFolder(folders, "INBOX") {
		t.Fatalf("folders = %+v", folders)
	}
	msgs, err := session.FetchNewest(ctx, "INBOX", 2, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].UID != uint32(charsetMsg) || msgs[1].UID != uint32(newer) {
		t.Fatalf("messages = %+v, want uids %d then %d", msgs, charsetMsg, newer)
	}
	if !strings.Contains(msgs[0].Body, "Привет") {
		t.Fatalf("charset body = %q", msgs[0].Body)
	}
	body := msgs[1].Body
	if !strings.Contains(body, "Invoice") || !strings.Contains(body, "shop.example") {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(body, "old secret") || strings.Contains(body, "https://") || strings.Contains(body, "HTML only") {
		t.Fatalf("body kept quoted or html text: %q", body)
	}
	if len(msgs[1].Attachments) != 1 || msgs[1].Attachments[0].Name != "invoice.pdf" {
		t.Fatalf("attachments = %+v", msgs[1].Attachments)
	}

	check := dialRaw(t, addr, pool, user, password)
	if _, err := check.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []imap.UID{newer, charsetMsg} {
		if seen(t, check, uid) {
			t.Fatalf("uid %d was marked seen after BODY.PEEK", uid)
		}
	}
	if _, err := check.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := check.Fetch(imap.UIDSetNum(older), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect(); err != nil {
		t.Fatal(err)
	}
	if !seen(t, check, older) {
		t.Fatal("non-PEEK fetch did not set \\Seen; the PEEK assertion is inconclusive")
	}

	bad := account
	bad.Endpoint.CertSHA256 = pin
	bad.Endpoint.CAFile = ""
	if _, err := NewClient().Connect(ctx, bad, password); err != nil {
		t.Fatal(err)
	}
	bad.Endpoint.CertSHA256 = strings.Repeat("0", 64)
	if _, err := NewClient().Connect(ctx, bad, password); err == nil || strings.Contains(err.Error(), pin) {
		t.Fatalf("bad pin err = %v", err)
	}
	naked := account
	naked.Endpoint.CAFile = ""
	if _, err := NewClient().Connect(ctx, naked, password); err == nil {
		t.Fatal("self-signed certificate was accepted without a CA or pin")
	}
}

func TestAuthFailureHidesPassword(t *testing.T) {
	cert, _, _ := newCert(t)
	const sent = "wrong-s3cret-do-not-echo"
	addr := startIMAP(t, cert, implicitListener, fullCaps(), "server-password")
	host, port := splitAddr(t, addr)
	caPath := writeCert(t, cert.Certificate[0])
	account := Account{
		Endpoint:        Endpoint{Host: host, Port: port, Security: ImplicitTLS, CAFile: caPath},
		Auth:            AuthPassword,
		Username:        "ada",
		AuthFailureHint: "Gmail app passwords need 2-Step Verification",
	}
	_, err := NewClient().Connect(context.Background(), account, sent)
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if strings.Contains(err.Error(), sent) || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "2-Step Verification") {
		t.Fatalf("err = %v", err)
	}
}

func TestSTARTTLSAndUTF7(t *testing.T) {
	cert, pool, _ := newCert(t)
	const user = "ada"
	const password = "starttls-password"
	addr := startIMAP(t, cert, plainListener, imap.CapSet{imap.CapIMAP4rev1: {}}, password)
	host, port := splitAddr(t, addr)
	caPath := writeCert(t, cert.Certificate[0])
	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: StartTLS, CAFile: caPath},
		Auth:     AuthPassword,
		Username: user,
	}
	session, err := NewClient().Connect(context.Background(), account, password)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, name := range []string{"Входящие", "A&B"} {
		if err := session.CreateMailbox(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	folders, err := session.ListFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasFolder(folders, "Входящие") || !hasFolder(folders, "A&B") {
		t.Fatalf("folders = %+v", folders)
	}
	_ = pool
}

func profile(name, provider, env string) config.Profile {
	return config.Profile{
		Name:        name,
		Provider:    provider,
		Username:    "ada@example.com",
		PasswordEnv: env,
		Email:       "ada@example.com",
	}
}

func profileAuth(name, provider, auth string) config.Profile {
	item := profile(name, provider, "")
	item.Auth = auth
	return item
}

type listenerMode int

const (
	implicitListener listenerMode = iota
	plainListener
)

func fullCaps() imap.CapSet {
	return imap.CapSet{
		imap.CapIMAP4rev1:  {},
		imap.CapIMAP4rev2:  {},
		imap.CapMove:       {},
		imap.CapUIDPlus:    {},
		imap.CapSpecialUse: {},
	}
}

func startIMAP(t *testing.T, cert tls.Certificate, mode listenerMode, caps imap.CapSet, password string) string {
	t.Helper()
	return listenIMAP(t, imapmemserver.New(), imapmemserver.NewUser("ada", password), cert, mode, caps)
}

func listenIMAP(t *testing.T, mem *imapmemserver.Server, user *imapmemserver.User, cert tls.Certificate, mode listenerMode, caps imap.CapSet) string {
	t.Helper()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		TLSConfig: serverTLS,
		// The test server allows LOGIN before STARTTLS. Production dials never do that.
		InsecureAuth: true,
		Caps:         caps,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serve := ln
	if mode == implicitListener {
		serve = tls.NewListener(ln, serverTLS)
	}
	go func() {
		_ = srv.Serve(serve)
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func newCert(t *testing.T) (tls.Certificate, *x509.CertPool, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool, hex.EncodeToString(sum[:])
}

func writeCert(t *testing.T, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, block, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func dialRaw(t *testing.T, addr string, pool *x509.CertPool, user, password string) *imapclient.Client {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: host})
	if err != nil {
		t.Fatal(err)
	}
	client := imapclient.New(conn, nil)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Login(user, password).Wait(); err != nil {
		t.Fatal(err)
	}
	return client
}

func appendRaw(t *testing.T, client *imapclient.Client, mailbox, raw string) imap.UID {
	t.Helper()
	cmd := client.Append(mailbox, int64(len(raw)), nil)
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if data.UID == 0 {
		t.Fatal("append did not return a UID")
	}
	return data.UID
}

func seen(t *testing.T, client *imapclient.Client, uid imap.UID) bool {
	t.Helper()
	bufs, err := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(bufs) != 1 {
		t.Fatalf("flags fetch = %d messages", len(bufs))
	}
	for _, flag := range bufs[0].Flags {
		if flag == imap.FlagSeen {
			return true
		}
	}
	return false
}

func hasFolder(folders []Folder, name string) bool {
	for _, folder := range folders {
		if folder.Name == name {
			return true
		}
	}
	return false
}

func textMessage(id, subject, body string) string {
	return strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: Bob <bob@example.com>",
		"Subject: " + subject,
		"Date: Mon, 05 Oct 2026 10:00:00 +0000",
		"Message-ID: <" + id + ">",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")
}

func eightBitMessage(body []byte) string {
	head := strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: Bob <bob@example.com>",
		"Subject: Charset",
		"Date: Mon, 05 Oct 2026 11:00:00 +0000",
		"Message-ID: <charset@example.com>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=windows-1251",
		"Content-Transfer-Encoding: 8bit",
		"",
		"",
	}, "\r\n")
	return head + string(body)
}

func multipartMessage() string {
	return strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: Bob <bob@example.com>",
		"Subject: Invoice",
		"Date: Mon, 05 Oct 2026 10:00:00 +0000",
		"Message-ID: <invoice@example.com>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=outer",
		"",
		"--outer",
		"Content-Type: multipart/alternative; boundary=inner",
		"",
		"--inner",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Invoice https://shop.example/pay",
		"",
		"On Monday Ada wrote:",
		"> old secret",
		"",
		"--inner",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><p>HTML only secret</p></body></html>",
		"--inner--",
		"--outer",
		"Content-Type: application/pdf; name=\"invoice.pdf\"",
		"Content-Disposition: attachment; filename=\"invoice.pdf\"",
		"Content-Transfer-Encoding: base64",
		"",
		"JVBERg==",
		"--outer--",
		"",
	}, "\r\n")
}
