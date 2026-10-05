package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	maxConnections = 2
	dialAttempts   = 3
	commandTimeout = 30 * time.Second
)

// Client limits how many IMAP connections a process opens.
type Client struct {
	gate chan struct{}
}

// NewClient returns a client that keeps at most two connections open.
func NewClient() *Client {
	return &Client{gate: make(chan struct{}, maxConnections)}
}

// Session is one logged-in IMAP connection. Close it when finished.
// Close drops the TCP connection. It does not send IMAP CLOSE, which would
// expunge, and it does not send EXPUNGE.
type Session struct {
	client   *imapclient.Client
	release  func()
	password string
	hint     string
	once     sync.Once
}

// Connect dials, upgrades TLS, and logs in with a password.
// OAuth returns a PhaseError and does not open a connection.
func (c *Client) Connect(ctx context.Context, account Account, password string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if account.Auth != AuthPassword {
		return nil, phaseError("5", "oauth is not implemented yet (phase 5)")
	}
	if account.Username == "" {
		return nil, errors.New("imap: username is empty")
	}
	if account.Endpoint.Host == "" || account.Endpoint.Port == 0 {
		return nil, errors.New("imap: host is empty")
	}
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	release := func() { <-c.gate }
	raw, err := dialRetry(ctx, account.Endpoint)
	if err != nil {
		release()
		return nil, scrub(fmt.Errorf("imap: dial %s: %w", account.Endpoint.addr(), err), password)
	}
	session := &Session{
		client:   raw,
		release:  release,
		password: password,
		hint:     account.AuthFailureHint,
	}
	err = session.login(ctx, account.Username, password)
	if err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) login(ctx context.Context, username, password string) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.client.Login(username, password).Wait()
	}()
	select {
	case <-ctx.Done():
		s.closeConn()
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		if err == nil {
			return nil
		}
		s.closeConn()
		// The server text is dropped. A NO response can echo the password.
		if s.hint != "" {
			return fmt.Errorf("imap: authentication failed. %s", s.hint)
		}
		return errors.New("imap: authentication failed")
	}
}

// Close releases the connection slot. A second call does nothing.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.closeConn()
}

func (s *Session) closeConn() {
	s.once.Do(func() {
		if s.client != nil {
			_ = s.client.Close()
		}
		if s.release != nil {
			s.release()
			s.release = nil
		}
		s.password = ""
	})
}

func (s *Session) do(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- fn()
	}()
	select {
	case <-ctx.Done():
		s.closeConn()
		<-errCh
		return fmt.Errorf("imap: %w", ctx.Err())
	case err := <-errCh:
		if err != nil {
			return scrub(err, s.password)
		}
		return nil
	}
}

func dialRetry(ctx context.Context, ep Endpoint) (*imapclient.Client, error) {
	var err error
	for attempt := 0; attempt < dialAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var client *imapclient.Client
		client, err = dial(ctx, ep)
		if err == nil {
			return client, nil
		}
		if !retryable(err) || attempt == dialAttempts-1 {
			return nil, err
		}
		if err = sleep(ctx, backoff(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, err
}

func dial(ctx context.Context, ep Endpoint) (*imapclient.Client, error) {
	cfg, err := tlsConfig(ep)
	if err != nil {
		return nil, err
	}
	switch ep.Security {
	case ImplicitTLS:
		conn, err := (&tls.Dialer{
			NetDialer: &net.Dialer{Timeout: commandTimeout},
			Config:    cfg,
		}).DialContext(ctx, "tcp", ep.addr())
		if err != nil {
			return nil, err
		}
		return imapclient.New(conn, nil), nil
	case StartTLS:
		conn, err := (&net.Dialer{Timeout: commandTimeout}).DialContext(ctx, "tcp", ep.addr())
		if err != nil {
			return nil, err
		}
		client, err := startTLS(ctx, conn, cfg)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return client, nil
	default:
		return nil, errors.New("imap: security must be implicit_tls or starttls")
	}
}

func startTLS(ctx context.Context, conn net.Conn, cfg *tls.Config) (*imapclient.Client, error) {
	type result struct {
		client *imapclient.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		client, err := imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: cfg})
		ch <- result{client, err}
	}()
	select {
	case <-ctx.Done():
		_ = conn.Close()
		<-ch
		return nil, ctx.Err()
	case got := <-ch:
		return got.client, got.err
	}
}

func retryable(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func backoff(attempt int) time.Duration {
	base := 200 * time.Millisecond * time.Duration(1<<attempt)
	return base + time.Duration(rand.IntN(100))*time.Millisecond
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func scrub(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New("imap: operation failed")
}
