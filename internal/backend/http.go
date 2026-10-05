package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const bodyLimit = 1 << 20

var defaultHTTP = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type statusError struct {
	name   string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: request failed with HTTP %d", e.name, e.status)
}

func call(ctx context.Context, client *http.Client, name, token, method, rawURL string, payload any) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: request: %w", name, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: request: %w", name, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = defaultHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, scrub(fmt.Errorf("%s: request: %w", name, err), token)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, resp.StatusCode, scrub(fmt.Errorf("%s: response: %w", name, err), token)
	}
	if len(raw) > bodyLimit {
		return nil, resp.StatusCode, fmt.Errorf("%s: response is too large", name)
	}
	return raw, resp.StatusCode, nil
}

func scrub(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New("backend: request failed")
}

func tokenOf(ctx context.Context, access Access) (string, error) {
	if access == nil {
		return "", errors.New("backend: not signed in")
	}
	token, err := access(ctx)
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("backend: not signed in")
	}
	return token, nil
}

func cleanMessageID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 500 || strings.ContainsAny(id, "\" \t\r\n") {
		return "", errors.New("backend: message id is not usable")
	}
	for _, r := range id {
		if r < 32 || r == 127 {
			return "", errors.New("backend: message id is not usable")
		}
	}
	return id, nil
}

func cleanFolder(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || strings.ContainsAny(name, "/\\") {
		return "", errors.New("backend: that folder is left in place")
	}
	if refusedFolder(name) {
		return "", errors.New("backend: refusing to file into that folder")
	}
	return name, nil
}

func refusedFolder(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "trash", "junk", "spam", "drafts", "draft", "sent", "sent items", "sentitems",
		"deleted items", "deleteditems", "junkemail", "junk email", "bin",
		"recoverableitemsdeletions":
		return true
	default:
		return false
	}
}

func isInbox(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "INBOX")
}
