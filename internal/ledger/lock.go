package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

const lockName = "ledger.lock"

// processLock is an OS-enforced exclusive lock protecting one output directory.
type processLock struct{ file *os.File }

// acquireLock holds an exclusive kernel lock on <dir>/ledger.lock. The file is
// persistent; its PID is informational and never used to decide lock ownership.
func acquireLock(dir string) (*processLock, error) {
	path := filepath.Join(dir, lockName)
	holder := recordedPID(path)
	file, contended, err := openProcessLock(path)
	if err != nil {
		return nil, fmt.Errorf("open ledger lock: %w", err)
	}
	if contended {
		if current := recordedPID(path); current != 0 {
			holder = current
		}
		return nil, lockError(holder)
	}
	//nolint:gosec // Path is the fixed lock filename inside the caller-selected ledger directory.
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = file.Close()
		return nil, fmt.Errorf("set ledger lock permissions: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("truncate ledger lock: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("seek ledger lock: %w", err)
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write ledger lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync ledger lock: %w", err)
	}
	return &processLock{file: file}, nil
}

func lockError(pid int) error {
	finding := diag.MustNew("TRL-APPLY-0005", diag.WithArgs(map[string]any{"pid": pid}))
	return fmt.Errorf("output directory is locked by process %d: %w", pid, &diag.Error{Finding: finding})
}

func recordedPID(path string) int {
	//nolint:gosec // Path is the fixed lock filename inside the caller-selected ledger directory.
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func (l *processLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := l.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("close ledger lock: %w", err)
	}
	l.file = nil
	return nil
}
