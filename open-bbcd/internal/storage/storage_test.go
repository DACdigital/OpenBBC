package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalDisk_NewCreatesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "discovery")
	if _, err := NewLocalDisk(root); err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("root is not a directory")
	}
}

func TestLocalDisk_PutWritesFile(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}

	want := []byte("hello flow-map")
	if err := s.Put(context.Background(), "abc.zip", bytes.NewReader(want)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "abc.zip"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("contents mismatch: got %q want %q", got, want)
	}
}

func TestLocalDisk_PutAtomic_NoTmpVisibleAtFinalKey(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalDisk(root)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}

	// 1 MB blob — large enough that a non-atomic implementation would have a
	// window where the final filename exists with partial contents.
	payload := bytes.Repeat([]byte{'x'}, 1<<20)
	if err := s.Put(context.Background(), "big.zip", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	final := filepath.Join(root, "big.zip")
	info, err := os.Stat(final)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(len(payload)) {
		t.Errorf("size = %d, want %d", info.Size(), len(payload))
	}

	// No leftover .tmp at the final key.
	if _, err := os.Stat(final + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file should not remain: %v", err)
	}
}

func TestLocalDisk_OpenReturnsContents(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalDisk(dir)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	if err := s.Put(t.Context(), "abc.zip", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := s.Open(t.Context(), "abc.zip")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(b) != "hello" {
		t.Errorf("contents = %q, want %q", string(b), "hello")
	}
}

func TestLocalDisk_OpenMissingKeyReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalDisk(dir)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	_, err = s.Open(t.Context(), "missing.zip")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLocalDisk_OpenRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocalDisk(dir)
	if err != nil {
		t.Fatalf("NewLocalDisk: %v", err)
	}
	for _, bad := range []string{"../escape", "sub/dir/key", "/abs/path", ""} {
		_, err := s.Open(t.Context(), bad)
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q: err = %v, want ErrInvalidKey", bad, err)
		}
	}
}
