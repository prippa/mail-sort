package logging

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

type rotator struct {
	path     string
	maxBytes int64
	maxFiles int
	mu       sync.Mutex
	file     *os.File
	size     int64
}

func openRotator(path string, maxBytes int64, maxFiles int) (*rotator, error) {
	if maxBytes < 1 || maxFiles < 2 {
		return nil, errors.New("log: invalid rotation limits")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("log: open %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("log: stat %s: %w", path, errors.Join(err, closeErr))
		}
		return nil, fmt.Errorf("log: stat %s: %w", path, err)
	}
	return &rotator{
		path:     path,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		file:     file,
		size:     info.Size(),
	}, nil
}

func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("log: write %s: %w", r.path, err)
	}
	return n, nil
}

func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	if err != nil {
		return fmt.Errorf("log: close %s: %w", r.path, err)
	}
	return nil
}

func (r *rotator) rotate() error {
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("log: close %s: %w", r.path, err)
	}
	r.file = nil
	oldest := fmt.Sprintf("%s.%d", r.path, r.maxFiles-1)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("log: remove %s: %w", oldest, err)
	}
	for i := r.maxFiles - 2; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", r.path, i)
		dst := fmt.Sprintf("%s.%d", r.path, i+1)
		if err := renameIfExists(src, dst); err != nil {
			return err
		}
	}
	if err := renameIfExists(r.path, r.path+".1"); err != nil {
		return err
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("log: open %s: %w", r.path, err)
	}
	r.file = file
	r.size = 0
	return nil
}

func renameIfExists(src, dst string) error {
	_, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("log: stat %s: %w", src, err)
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("log: remove %s: %w", dst, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("log: rename %s: %w", src, err)
	}
	return nil
}
