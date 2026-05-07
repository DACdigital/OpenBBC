// Package storage abstracts blob writes for uploaded artifacts.
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Storage writes blobs identified by a relative key.
type Storage interface {
	// Put writes the data at the given relative key. Implementations must be
	// atomic with respect to readers — partial writes must not be observable
	// under the final key.
	Put(ctx context.Context, key string, r io.Reader) error
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

// Put writes r to Root/<key+".tmp">, fsyncs, then renames to Root/<key>.
// The rename is atomic on POSIX filesystems.
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
