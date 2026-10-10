//go:build !windows

package localfs

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRejectsFIFO(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "pipe.yml")
	if err := syscall.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := New(map[string]string{"": root})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Open(context.Background(), "", "pipe.yml"); err == nil {
		t.Fatal("FIFO opened as a template")
	}
}
