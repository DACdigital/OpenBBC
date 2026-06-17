// Package storage abstracts blob reads and writes for uploaded artifacts.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned by Open when the key does not exist.
var ErrNotFound = errors.New("storage: key not found")

// ErrInvalidKey is returned when the key contains path separators, is empty,
// or otherwise resolves outside the storage root.
var ErrInvalidKey = errors.New("storage: invalid key")

// Storage reads and writes blobs identified by a relative key.
type Storage interface {
	// Put writes the data at the given relative key. Implementations must be
	// atomic with respect to readers — partial writes must not be observable
	// under the final key.
	Put(ctx context.Context, key string, r io.Reader) error
	// Open returns a ReadCloser for the blob at key. Callers must Close.
	// Returns ErrNotFound if the key does not exist and ErrInvalidKey for
	// malformed keys.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// LocalDisk is a Storage backed by a directory on the local filesystem.
type LocalDisk struct {
	Root string
}

// NewLocalDisk returns a LocalDisk rooted at the given path, creating the
// directory if it does not exist.
func NewLocalDisk(root string) (*LocalDisk, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("storage: mkdir %q: %w", root, err)
	}
	return &LocalDisk{Root: root}, nil
}

// validateKey rejects empty keys, absolute paths, and anything containing a
// separator. Storage keys are intentionally flat — callers must namespace via
// the key contents (e.g. UUIDs), not subdirectories.
func validateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}
	if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
		return ErrInvalidKey
	}
	return nil
}

// Put writes r to Root/<key+".tmp">, fsyncs, then renames to Root/<key>.
// The rename is atomic on POSIX filesystems. ctx is accepted for interface
// compatibility but is not honoured during I/O; a future S3 implementation
// will respect cancellation. Concurrent calls with the same key are not
// safe — callers must ensure unique keys (the wizard handler uses UUIDs).
func (s *LocalDisk) Put(ctx context.Context, key string, r io.Reader) error {
	final := filepath.Join(s.Root, key)
	tmp := final + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("storage: open tmp: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("storage: copy: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("storage: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("storage: close: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("storage: rename: %w", err)
	}
	return nil
}

// Open returns a ReadCloser for the blob at key. Caller must Close. Returns
// ErrNotFound if no such key exists, ErrInvalidKey for malformed keys.
func (s *LocalDisk) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.Root, key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	return f, nil
}
