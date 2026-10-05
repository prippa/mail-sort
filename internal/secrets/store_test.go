package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvMasterPassword, "correct horse battery")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(RefreshAccount("Work")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file err = %v", err)
	}
	const secret = "refresh-token-value"
	if err := store.Set(RefreshAccount("Work"), secret); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "secrets.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "secrets.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "correct horse") {
		t.Fatal("secret file contains plaintext")
	}
	got, err := store.Get(RefreshAccount("Work"))
	if err != nil || got != secret {
		t.Fatalf("got %q err=%v", got, err)
	}
	t.Setenv(EnvMasterPassword, "wrong-password")
	_, err = store.Get(RefreshAccount("Work"))
	if !errors.Is(err, ErrMasterPassword) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "wrong-password") {
		t.Fatalf("err = %v", err)
	}
	if err := store.Set(APIKeyAccount("TYPESAFE_API_KEY"), strings.Repeat("k", MaxSecretBytes+1)); err == nil || strings.Contains(err.Error(), "k") {
		t.Fatalf("long secret err = %v", err)
	}
}

func TestMissingFileDoesNotNeedPassword(t *testing.T) {
	t.Setenv(EnvMasterPassword, "")
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Get(RefreshAccount("Work"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyringStore(t *testing.T) {
	keyring.MockInit()
	store := keyringStore{}
	const secret = "keyring-refresh"
	if err := store.Set(RefreshAccount("Work"), secret); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(RefreshAccount("Work"))
	if err != nil || got != secret {
		t.Fatalf("got %q err=%v", got, err)
	}
	if err := store.Delete(RefreshAccount("Work")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(RefreshAccount("Work")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	keyring.MockInitWithError(errors.New("backend down " + secret))
	_, err = store.Get(RefreshAccount("Work"))
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "keyring read failed") {
		t.Fatalf("err = %v", err)
	}
}
