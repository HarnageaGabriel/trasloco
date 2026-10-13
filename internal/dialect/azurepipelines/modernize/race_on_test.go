//go:build race

package modernize

// raceEnabled skips wall-clock budget tests, which the race detector slows down several times.
const raceEnabled = true
