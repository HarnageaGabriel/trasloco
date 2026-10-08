//go:build !windows

package ledger

import (
	"runtime"
	"syscall"
)

func processRSS() uint64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	rss := uint64(usage.Maxrss) // #nosec G115 -- Maxrss is non-negative for a live process.
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	return rss
}
