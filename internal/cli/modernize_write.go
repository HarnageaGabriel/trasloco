package cli

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errModernizeSymlink = errors.New("symbolic links are not followed")

// Check ancestors too: an explicitly named file may be under a linked directory.
func modernizeNoSymlinks(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errModernizeSymlink
		}
	}
	return nil
}

// Descend through directory handles, checking identities after opening each
// component. Root confinement also rejects symlinks swapped in during descent.
func openModernizeDirectory(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	base := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(absolute, base), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		info, statErr := root.Lstat(component)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, errModernizeSymlink
		}
		next, openErr := root.OpenRoot(component)
		_ = root.Close()
		if openErr != nil {
			return nil, openErr
		}
		opened, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, fmt.Errorf("directory changed during modernization")
		}
		root = next
	}
	return root, nil
}

func readModernizeFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errModernizeSymlink
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to read a non-regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed during modernization")
	}
	return io.ReadAll(file)
}

func readModernizeWriteSource(path string) ([]byte, error) {
	root, err := openModernizeDirectory(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return readModernizeFile(root, filepath.Base(path))
}

// Keep replacement operations relative to an open directory, so a directory
// substitution cannot redirect the temporary file or rename outside that root.
func writeModernizeFile(path string, original, out []byte) error {
	if err := modernizeNoSymlinks(path); err != nil {
		return err
	}
	root, err := openModernizeDirectory(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace a non-regular file")
	}
	tempName := ".trasloco-" + rand.Text()
	temp, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = temp.Close()
		_ = root.Remove(tempName)
	}()
	if _, err := temp.Write(out); err != nil {
		return err
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return fmt.Errorf("file changed during modernization")
	}
	data, err := readModernizeFile(root, name)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, original) {
		return fmt.Errorf("file changed during modernization")
	}
	return root.Rename(tempName, name)
}
