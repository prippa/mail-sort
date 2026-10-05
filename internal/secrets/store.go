package secrets

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// MaxSecretBytes is the Windows Credential Manager limit documented by
	// go-keyring. The file backend uses the same cap so a secret saved on one
	// computer still fits the keyring on another.
	MaxSecretBytes = 2560
	fileMagic      = "MSSE"
	fileVersion    = 1
)

var (
	// ErrNotFound means the account has no stored secret.
	ErrNotFound = errors.New("secrets: not found")
	// ErrMasterPassword means the encrypted file cannot be opened.
	ErrMasterPassword = errors.New("secrets: set MAIL_SORTER_MASTER_PASSWORD to unlock the secret file")
)

// RefreshAccount is the keyring account for one profile's refresh token.
func RefreshAccount(profile string) string {
	return "refresh:" + profile
}

// GraphAccount is the keyring account for a Microsoft Graph refresh token.
// It is separate from the IMAP refresh token.
func GraphAccount(profile string) string {
	return "graph:" + profile
}

// APIKeyAccount is the keyring account for a classifier environment variable.
func APIKeyAccount(envName string) string {
	return "apikey:" + envName
}

// Store keeps long-lived secrets. It does not keep access tokens.
type Store interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

type cost struct {
	time    uint32
	memory  uint32
	threads uint8
}

// Open returns the system keyring when Secret Service or Credential Manager
// answers, and an encrypted file in dir otherwise. Tests always get the file
// so a developer keyring is not read or written.
func Open(dir string) (Store, error) {
	if dir == "" {
		return nil, errors.New("secrets: directory is empty")
	}
	if testing.Testing() || !keyringReady() {
		return &fileStore{path: filepath.Join(dir, "secrets.bin"), cost: defaultCost()}, nil
	}
	return keyringStore{}, nil
}

func keyringReady() bool {
	_, err := keyring.Get(ServiceName, "probe")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

func defaultCost() cost {
	if testing.Testing() {
		return cost{time: 1, memory: 8, threads: 1}
	}
	// OWASP's lower-memory Argon2id choice: one pass and about 19 MiB.
	return cost{time: 1, memory: 19 * 1024, threads: 1}
}

type keyringStore struct{}

func (keyringStore) Get(account string) (string, error) {
	if err := checkAccount(account); err != nil {
		return "", err
	}
	value, err := keyring.Get(ServiceName, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", errors.New("secrets: keyring read failed")
	}
	return value, nil
}

func (keyringStore) Set(account, secret string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	if err := checkSecret(secret); err != nil {
		return err
	}
	if err := keyring.Set(ServiceName, account, secret); err != nil {
		if errors.Is(err, keyring.ErrSetDataTooBig) {
			return fmt.Errorf("secrets: secret is longer than %d bytes", MaxSecretBytes)
		}
		return errors.New("secrets: keyring write failed")
	}
	return nil
}

func (keyringStore) Delete(account string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	err := keyring.Delete(ServiceName, account)
	if errors.Is(err, keyring.ErrNotFound) || err == nil {
		return nil
	}
	return errors.New("secrets: keyring delete failed")
}

type fileStore struct {
	path string
	cost cost
	mu   sync.Mutex
}

func (s *fileStore) Get(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkAccount(account); err != nil {
		return "", err
	}
	items, err := s.read()
	if err != nil {
		return "", err
	}
	value, ok := items[account]
	if !ok || value == "" {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *fileStore) Set(account, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkAccount(account); err != nil {
		return err
	}
	if err := checkSecret(secret); err != nil {
		return err
	}
	items, err := s.read()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if items == nil {
		items = map[string]string{}
	}
	items[account] = secret
	return s.write(items)
}

func (s *fileStore) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkAccount(account); err != nil {
		return err
	}
	items, err := s.read()
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	delete(items, account)
	if len(items) == 0 {
		err := os.Remove(s.path)
		if errors.Is(err, os.ErrNotExist) || err == nil {
			return nil
		}
		return fmt.Errorf("secrets: remove file: %w", err)
	}
	return s.write(items)
}

func (s *fileStore) read() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: read file: %w", err)
	}
	password, err := masterPassword()
	if err != nil {
		return nil, err
	}
	plain, err := decrypt(data, password)
	if err != nil {
		return nil, err
	}
	var items map[string]string
	if err := json.Unmarshal(plain, &items); err != nil {
		return nil, errors.New("secrets: stored data could not be read")
	}
	return items, nil
}

func (s *fileStore) write(items map[string]string) error {
	password, err := masterPassword()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(items)
	if err != nil {
		return errors.New("secrets: stored data could not be written")
	}
	data, err := encrypt(plain, password, s.cost)
	if err != nil {
		return err
	}
	return writePrivate(s.path, data)
}

func masterPassword() (string, error) {
	value, ok := os.LookupEnv(EnvMasterPassword)
	if !ok || value == "" {
		return "", ErrMasterPassword
	}
	return value, nil
}

func checkAccount(account string) error {
	if account == "" || len(account) > 200 || strings.ContainsAny(account, "\r\n") {
		return errors.New("secrets: account name is invalid")
	}
	return nil
}

func checkSecret(secret string) error {
	if secret == "" {
		return errors.New("secrets: secret is empty")
	}
	if len(secret) > MaxSecretBytes {
		return fmt.Errorf("secrets: secret is longer than %d bytes", MaxSecretBytes)
	}
	return nil
}

func encrypt(plain []byte, password string, c cost) ([]byte, error) {
	header := make([]byte, 4+1+4+4+1+16+24)
	copy(header[:4], fileMagic)
	header[4] = fileVersion
	binary.BigEndian.PutUint32(header[5:9], c.time)
	binary.BigEndian.PutUint32(header[9:13], c.memory)
	header[13] = c.threads
	if _, err := rand.Read(header[14:30]); err != nil {
		return nil, fmt.Errorf("secrets: salt: %w", err)
	}
	if _, err := rand.Read(header[30:54]); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	aead, err := newAEAD(password, header[14:30], c)
	if err != nil {
		return nil, err
	}
	sealed := aead.Seal(nil, header[30:54], plain, header)
	return append(header, sealed...), nil
}

func decrypt(data []byte, password string) ([]byte, error) {
	const headerLen = 4 + 1 + 4 + 4 + 1 + 16 + 24
	if len(data) < headerLen+16 {
		return nil, ErrMasterPassword
	}
	if string(data[:4]) != fileMagic || data[4] != fileVersion {
		return nil, errors.New("secrets: file is not a MailSorter secret file")
	}
	c := cost{
		time:    binary.BigEndian.Uint32(data[5:9]),
		memory:  binary.BigEndian.Uint32(data[9:13]),
		threads: data[13],
	}
	if c.time == 0 || c.memory < 8 || c.threads == 0 {
		return nil, errors.New("secrets: file is not a MailSorter secret file")
	}
	aead, err := newAEAD(password, data[14:30], c)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, data[30:54], data[headerLen:], data[:headerLen])
	if err != nil {
		return nil, ErrMasterPassword
	}
	return plain, nil
}

func newAEAD(password string, salt []byte, c cost) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(password), salt, c.time, c.memory, c.threads, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, errors.New("secrets: cipher setup failed")
	}
	return aead, nil
}

func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("secrets: write: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets: write: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secrets: write: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("secrets: write: %w", err)
	}
	ok = true
	return os.Chmod(path, 0o600)
}
