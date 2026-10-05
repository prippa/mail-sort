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
	updates  chan uint32
	once     sync.Once
}

// Connect dials, upgrades TLS, and logs in with a password.
// An OAuth account is refused here, before a connection is opened.
func (c *Client) Connect(ctx context.Context, account Account, password string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if account.Auth != AuthPassword {
		return nil, errors.New("imap: this account uses OAuth")
	}
	if account.Username == "" {
		return nil, errors.New("imap: username is empty")
	}
	session, err := c.dial(ctx, account, password)
	if err != nil {
		return nil, err
	}
	if err := session.login(ctx, account.Username, password); err != nil {
		return nil, err
	}
	return session, nil
}

// ConnectOAuth dials and authenticates with XOAUTH2. On an authentication
// failure it refreshes the access token once and tries one new connection.
func (c *Client) ConnectOAuth(ctx context.Context, account Account, src TokenSource) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if account.Auth != AuthOAuthGoogle && account.Auth != AuthOAuthMicrosoft {
		return nil, errors.New("imap: this account uses a password")
	}
	if src == nil {
		return nil, errors.New("oauth: this account is not signed in. Open the local page and sign in")
	}
	user := strings.TrimSpace(account.Email)
	if user == "" {
		user = strings.TrimSpace(account.Username)
	}
	if user == "" {
		return nil, errors.New("imap: profile is missing an email")
	}
	token, err := src.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	session, err := c.loginOAuth(ctx, account, user, token)
	if err == nil || ctx.Err() != nil || !authFailed(err) {
		return session, err
	}
	token, err = src.ForceRefresh(ctx)
	if err != nil {
		return nil, err
	}
	return c.loginOAuth(ctx, account, user, token)
}

func (c *Client) dial(ctx context.Context, account Account, secret string) (*Session, error) {
	if account.Endpoint.Host == "" || account.Endpoint.Port == 0 {
		return nil, errors.New("imap: host is empty")
	}
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	release := func() { <-c.gate }
	updates := make(chan uint32, 1)
	raw, err := dialRetry(ctx, account.Endpoint, updates)
	if err != nil {
		release()
		return nil, scrub(fmt.Errorf("imap: dial %s: %w", account.Endpoint.addr(), err), secret)
	}
	return &Session{
		client:   raw,
		release:  release,
		password: secret,
		hint:     account.AuthFailureHint,
		updates:  updates,
	}, nil
}

func (c *Client) loginOAuth(ctx context.Context, account Account, user, token string) (*Session, error) {
	session, err := c.dial(ctx, account, token)
	if err != nil {
		return nil, err
	}
	if err := session.xoauth2(ctx, user, token); err != nil {
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
	return s.waitAuth(ctx, func() error {
		return s.client.Login(username, password).Wait()
	})
}

func (s *Session) xoauth2(ctx context.Context, user, token string) error {
	return s.waitAuth(ctx, func() error {
		return s.client.Authenticate(&xoauth2Client{user: user, token: token})
	})
}

func (s *Session) waitAuth(ctx context.Context, auth func() error) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- auth()
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
		// The server text is dropped. A NO response can echo the secret.
		if s.hint != "" {
			return fmt.Errorf("imap: authentication failed. %s", s.hint)
		}
		return errors.New("imap: authentication failed")
	}
}

func authFailed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "authentication failed")
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

func dialRetry(ctx context.Context, ep Endpoint, updates chan uint32) (*imapclient.Client, error) {
	var err error
	for attempt := 0; attempt < dialAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var client *imapclient.Client
		client, err = dial(ctx, ep, updates)
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

func dial(ctx context.Context, ep Endpoint, updates chan uint32) (*imapclient.Client, error) {
	cfg, err := tlsConfig(ep)
	if err != nil {
		return nil, err
	}
	opts := &imapclient.Options{
		TLSConfig:             cfg,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{Mailbox: countHandler(updates)},
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
		return imapclient.New(conn, opts), nil
	case StartTLS:
		conn, err := (&net.Dialer{Timeout: commandTimeout}).DialContext(ctx, "tcp", ep.addr())
		if err != nil {
			return nil, err
		}
		client, err := startTLS(ctx, conn, opts)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return client, nil
	default:
		return nil, errors.New("imap: security must be implicit_tls or starttls")
	}
}

func countHandler(updates chan uint32) func(*imapclient.UnilateralDataMailbox) {
	return func(data *imapclient.UnilateralDataMailbox) {
		if data == nil || data.NumMessages == nil {
			return
		}
		pushCount(updates, *data.NumMessages)
	}
}

func pushCount(updates chan uint32, n uint32) {
	if updates == nil {
		return
	}
	select {
	case <-updates:
	default:
	}
	select {
	case updates <- n:
	default:
	}
}

func startTLS(ctx context.Context, conn net.Conn, opts *imapclient.Options) (*imapclient.Client, error) {
	type result struct {
		client *imapclient.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		client, err := imapclient.NewStartTLS(conn, opts)
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
