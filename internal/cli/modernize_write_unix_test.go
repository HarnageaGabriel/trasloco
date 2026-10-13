//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestModernizeWriteUnixMetadata(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
	path := filepath.Join(root, "pipeline.yml")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	data, initial, err := readModernizeWriteSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModernizeFile(path, data, []byte("replacement"), initial); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat := initial.Sys().(*syscall.Stat_t)
	afterStat := after.Sys().(*syscall.Stat_t)
	if beforeStat.Uid != afterStat.Uid || beforeStat.Gid != afterStat.Gid || after.Mode().Perm() != 0o640 {
		t.Fatal("ownership or permissions changed")
	}
}
