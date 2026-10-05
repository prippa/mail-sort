package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestXOAuth2InitialMatchesMicrosoftExample(t *testing.T) {
	raw := xoauth2Initial("test@contoso.onmicrosoft.com", "EwBAAl3BAAUFFpUAo7J3Ve0bjLBWZWCclRC3EoAA")
	got := base64.StdEncoding.EncodeToString([]byte(raw))
	const want = "dXNlcj10ZXN0QGNvbnRvc28ub25taWNyb3NvZnQuY29tAWF1dGg9QmVhcmVyIEV3QkFBbDNCQUFVRkZwVUFvN0ozVmUwYmpMQldaV0NjbFJDM0VvQUEBAQ=="
	if got != want {
		t.Fatalf("initial %s", got)
	}
}

func TestConnectOAuthSendsInitialResponse(t *testing.T) {
	const token = "EwBAAl3BAAUFFpUAo7J3Ve0bjLBWZWCclRC3EoAA"
	got := make(chan string, 1)
	host, port, ca := scriptIMAP(t, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		payload, ok := readXOAuth(conn, token, false)
		got <- payload
		if ok {
			_, _ = io.Copy(io.Discard, conn)
		}
	})
	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: ImplicitTLS, CAFile: ca},
		Auth:     AuthOAuthMicrosoft,
		Email:    "test@contoso.onmicrosoft.com",
		Username: "test@contoso.onmicrosoft.com",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := NewClient().ConnectOAuth(ctx, account, &staticToken{token: token})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case payload := <-got:
		want := base64.StdEncoding.EncodeToString([]byte(xoauth2Initial(account.Email, token)))
		if payload != want {
			t.Fatalf("payload %s", payload)
		}
	case <-ctx.Done():
		t.Fatal("server saw no AUTHENTICATE")
	}
}

func TestConnectOAuthRetriesWithRefreshedToken(t *testing.T) {
	const first = "first-access-token"
	const second = "second-access-token"
	lines := make(chan string, 2)
	host, port, ca := scriptIMAP(t, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		payload, ok := readXOAuth(conn, first, len(lines) == 0)
		lines <- payload
		if ok {
			_, _ = io.Copy(io.Discard, conn)
		}
	})
	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: ImplicitTLS, CAFile: ca},
		Auth:     AuthOAuthGoogle,
		Email:    "ada@example.com",
	}
	src := &refreshToken{tokens: []string{first, second}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := NewClient().ConnectOAuth(ctx, account, src)
	if err != nil {
		t.Fatal(err)
	}
	session.Close()
	if src.refreshed != 1 {
		t.Fatalf("refreshed %d", src.refreshed)
	}
	var payloads []string
	for range 2 {
		select {
		case line := <-lines:
			payloads = append(payloads, line)
		case <-ctx.Done():
			t.Fatal("missing authenticate")
		}
	}
	if !strings.Contains(payloads[1], base64.StdEncoding.EncodeToString([]byte(xoauth2Initial(account.Email, second)))) {
		t.Fatalf("second %s", payloads[1])
	}
	if strings.Contains(payloads[1], base64.StdEncoding.EncodeToString([]byte(xoauth2Initial(account.Email, first)))) {
		t.Fatal("second authenticate reused the first token")
	}
}

func TestConnectOAuthScrubsToken(t *testing.T) {
	const token = "access-token-do-not-print"
	host, port, ca := scriptIMAP(t, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = readXOAuth(conn, token, true)
	})
	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: ImplicitTLS, CAFile: ca},
		Auth:     AuthOAuthGoogle,
		Email:    "ada@example.com",
	}
	src := &refreshToken{tokens: []string{token, token}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := NewClient().ConnectOAuth(ctx, account, src)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectOAuthDoesNotDial(t *testing.T) {
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
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	account := Account{
		Endpoint: Endpoint{Host: host, Port: port, Security: ImplicitTLS},
		Auth:     AuthOAuthGoogle,
		Email:    "ada@example.com",
	}
	_, err = NewClient().Connect(context.Background(), account, "secret-value")
	if err == nil || !strings.Contains(err.Error(), "OAuth") || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-accepted:
		t.Fatal("password connect dialed an oauth account")
	case <-time.After(50 * time.Millisecond):
	}
}

func scriptIMAP(t *testing.T, handle func(net.Conn)) (string, int, string) {
	t.Helper()
	cert, _, _ := newCert(t)
	ca := writeCert(t, cert.Certificate[0])
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	go func() {
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port, ca
}

func readXOAuth(conn net.Conn, token string, fail bool) (string, bool) {
	_, _ = fmt.Fprintf(conn, "* OK [CAPABILITY IMAP4rev1 AUTH=XOAUTH2 SASL-IR] ready\r\n")
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", false
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		tag := fields[0]
		switch strings.ToUpper(fields[1]) {
		case "CAPABILITY":
			_, _ = fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 AUTH=XOAUTH2 SASL-IR\r\n%s OK\r\n", tag)
		case "AUTHENTICATE":
			payload := ""
			if len(fields) >= 3 && strings.EqualFold(fields[2], "XOAUTH2") && len(fields) >= 4 {
				payload = fields[3]
			} else {
				_, _ = fmt.Fprintf(conn, "+\r\n")
				next, err := reader.ReadString('\n')
				if err != nil {
					return "", false
				}
				payload = strings.TrimSpace(next)
			}
			if fail {
				_, _ = fmt.Fprintf(conn, "%s NO %s\r\n", tag, token)
				return payload, true
			}
			_, _ = fmt.Fprintf(conn, "%s OK authenticated\r\n", tag)
			return payload, true
		default:
			_, _ = fmt.Fprintf(conn, "%s OK\r\n", tag)
		}
	}
}

type staticToken struct{ token string }

func (s *staticToken) AccessToken(context.Context) (string, error)  { return s.token, nil }
func (s *staticToken) ForceRefresh(context.Context) (string, error) { return s.token, nil }

type refreshToken struct {
	tokens    []string
	refreshed int
}

func (r *refreshToken) AccessToken(context.Context) (string, error) {
	return r.tokens[r.refreshed], nil
}

func (r *refreshToken) ForceRefresh(context.Context) (string, error) {
	r.refreshed++
	return r.tokens[r.refreshed], nil
}
