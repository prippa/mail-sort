package main

import (
	"errors"
	"os"
	"strings"

	"github.com/prippa/mail-sort/internal/config"
	"github.com/prippa/mail-sort/internal/secrets"
)

type secretLookup struct {
	store secrets.Store
	err   error
}

func openSecrets() (secrets.Store, error) {
	dir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return secrets.Open(dir)
}

func newSecretLookup() (*secretLookup, error) {
	store, err := openSecrets()
	if err != nil {
		return nil, err
	}
	return &secretLookup{store: store}, nil
}

func (s *secretLookup) get(name string) (string, bool) {
	if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true
	}
	if s == nil || s.store == nil || name == "" {
		return "", false
	}
	value, err := s.store.Get(secrets.APIKeyAccount(name))
	if errors.Is(err, secrets.ErrNotFound) {
		return "", false
	}
	if err != nil {
		s.err = err
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}
