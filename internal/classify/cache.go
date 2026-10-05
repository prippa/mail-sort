package classify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	_ "modernc.org/sqlite"
)

// Cache stores one provider decision. The message body is not stored.
type Cache interface {
	Get(ctx context.Context, key string) (Decision, bool, error)
	Put(ctx context.Context, key string, decision Decision) error
}

// Memory is a process-local cache for tests.
type Memory struct {
	mu    sync.Mutex
	items map[string]Decision
}

// NewMemory returns an empty cache.
func NewMemory() *Memory {
	return &Memory{items: make(map[string]Decision)}
}

func (m *Memory) Get(_ context.Context, key string) (Decision, bool, error) {
	if m == nil {
		return Decision{}, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	decision, ok := m.items[key]
	return decision, ok, nil
}

func (m *Memory) Put(_ context.Context, key string, decision Decision) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = stored(decision)
	return nil
}

// SQLite is the classification cache in the state directory.
// Run and undo tables are created in the same file by internal/store.
type SQLite struct {
	db *sql.DB
}

// OpenCache creates or opens the classification cache. The file mode is 0600.
func OpenCache(ctx context.Context, path string) (*SQLite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("classify: cache path is empty")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("classify: open cache: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("classify: open cache: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS classification_cache (
		cache_key TEXT PRIMARY KEY,
		decision TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("classify: open cache: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("classify: open cache: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("classify: open cache: %w", err)
	}
	if mode != "wal" {
		_ = db.Close()
		return nil, errors.New("classify: open cache: journal mode")
	}
	if err := chmodDB(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

// Close closes the database.
func (s *SQLite) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("classify: close cache: %w", err)
	}
	return nil
}

func (s *SQLite) Get(ctx context.Context, key string) (Decision, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT decision FROM classification_cache WHERE cache_key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{}, false, nil
	}
	if err != nil {
		return Decision{}, false, fmt.Errorf("classify: read cache: %w", err)
	}
	var decision Decision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return Decision{}, false, fmt.Errorf("classify: read cache: %w", err)
	}
	return decision, true, nil
}

func (s *SQLite) Put(ctx context.Context, key string, decision Decision) error {
	raw, err := json.Marshal(stored(decision))
	if err != nil {
		return fmt.Errorf("classify: write cache: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO classification_cache (cache_key, decision) VALUES (?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET decision = excluded.decision`, key, string(raw))
	if err != nil {
		return fmt.Errorf("classify: write cache: %w", err)
	}
	return nil
}

func chmodDB(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("classify: open cache: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		extra := path + suffix
		if _, err := os.Stat(extra); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("classify: open cache: %w", err)
		}
		if err := os.Chmod(extra, 0o600); err != nil {
			return fmt.Errorf("classify: open cache: %w", err)
		}
	}
	return nil
}

func stored(decision Decision) Decision {
	decision.Source = decision.Provider
	decision.Action = ""
	decision.Folder = ""
	decision.PriceInput = 0
	decision.PriceOutput = 0
	return decision
}
