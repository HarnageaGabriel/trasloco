//go:build unix

package cli

import (
	"errors"
	"os"
	"syscall"
)

func preserveModernizeOwner(file *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	err := file.Chown(int(stat.Uid), int(stat.Gid))
	if errors.Is(err, syscall.EPERM) {
		return nil
	}
	return err
}

func syncModernizeDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
