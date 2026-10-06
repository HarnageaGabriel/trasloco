package redact

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// MaxCarry is the maximum buffered line length. Overlong lines are suppressed
// entirely, rather than risking a partial credential at a streaming boundary.
const MaxCarry = 64 * 1024

// Writer redacts complete lines while retaining a bounded incomplete line.
// Flush ends the current logical record; secrets must not span explicit Flush calls.
// PEM blocks and overlong lines are suppressed until their closing boundary.
type Writer struct {
	mu       sync.Mutex
	dst      io.Writer
	redactor *Redactor
	buf      []byte
	closed   bool
	dropping bool
	pem      bool
	err      error
}

// NewWriter wraps dst. Close releases the final incomplete line.
func NewWriter(dst io.Writer, redactor *Redactor) *Writer {
	if redactor == nil {
		redactor = New()
	}
	return &Writer{dst: dst, redactor: redactor}
}

// Write emits completed records and retains at most MaxCarry bytes.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, fmt.Errorf("redact writer: %w", io.ErrClosedPipe)
	}
	if w.err != nil {
		return 0, w.err
	}
	for i, b := range p {
		if b == '\n' {
			if err := w.flushLocked(true); err != nil {
				return i, err
			}
			continue
		}
		if w.dropping {
			continue
		}
		if len(w.buf) == MaxCarry {
			// A possible PEM opener in an overlong line must still enter block mode.
			if strings.Contains(string(w.buf), "-----BEGIN ") {
				w.pem = true
			}
			w.buf = w.buf[:0]
			w.dropping = true
			continue
		}
		w.buf = append(w.buf, b)
	}
	return len(p), nil
}

// Flush ends a logical record, releasing its incomplete final line.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked(false)
}

func (w *Writer) flushLocked(newline bool) error {
	if w.err != nil {
		return w.err
	}
	if len(w.buf) == 0 && !w.dropping && !newline {
		return nil
	}
	line := string(w.buf)
	clean := w.redactor.Redact(line)
	if w.dropping || w.pem || strings.Contains(line, "-----BEGIN ") {
		clean = replacement
	}
	if strings.Contains(line, "-----BEGIN ") {
		w.pem = true
	}
	if strings.Contains(line, "-----END ") {
		w.pem = false
	}
	if newline {
		clean += "\n"
	}
	n, err := io.WriteString(w.dst, clean)
	if err == nil && n != len(clean) {
		err = io.ErrShortWrite
	}
	w.err = err
	w.buf = w.buf[:0]
	w.dropping = false
	return err
}

// Close flushes the final record without closing the destination.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	return w.flushLocked(false)
}
