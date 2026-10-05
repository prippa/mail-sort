package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRedactsSecretsAndBody(t *testing.T) {
	t.Parallel()
	const secret = "s3cret-value"
	var buf bytes.Buffer
	logger := slog.New(newRedactingHandler(slog.NewJSONHandler(&buf, nil), Options{}))
	logger.Info("classify",
		slog.String("password", secret),
		slog.String("host", "imap.example"),
		slog.String("subject", "Invoice"),
		slog.Group("mail",
			slog.String("api_key", secret),
			slog.String("from", "a@b.c"),
		),
	)
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatal("log contains the secret value")
	}
	if strings.Contains(out, "Invoice") {
		t.Fatal("subject was logged while subject logging is off")
	}
	if !strings.Contains(out, "imap.example") || !strings.Contains(out, "a@b.c") {
		t.Fatalf("log dropped ordinary fields: %s", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["password"] != redacted {
		t.Fatalf("password = %#v", decoded["password"])
	}
}

func TestSubjectOptIn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(newRedactingHandler(slog.NewJSONHandler(&buf, nil), Options{LogSubjects: true}))
	logger.Info("classify", slog.String("subject", "Invoice"), slog.String("body", "secret-body"))
	if !strings.Contains(buf.String(), "Invoice") {
		t.Fatalf("subject missing: %s", buf.String())
	}
	if strings.Contains(buf.String(), "secret-body") {
		t.Fatal("body was logged")
	}
}

func TestRotator(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mailsorter.log")
	rot, err := openRotator(path, 40, 3)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("0123456789abcdef\n")
	for range 6 {
		if _, err := rot.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := rot.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", name, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("oldest file retained: %v", err)
	}
}

func TestRotatorConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mailsorter.log")
	rot, err := openRotator(path, 80, 4)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if _, err := rot.Write([]byte("0123456789\n")); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if err := rot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mailsorter.log")
	logger, closer, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("start", slog.String("token", "raw-token"))
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "raw-token") {
		t.Fatal("file log contains the token")
	}
	if !strings.Contains(string(data), redacted) {
		t.Fatalf("file log = %s", data)
	}
}
