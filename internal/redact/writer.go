package redact

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// Writer redacts a stream without exposing values split across Write calls.
// Buffered data is released by Flush or Close.
type Writer struct {
	mu       sync.Mutex
	dst      io.Writer
	redactor *Redactor
	buf      bytes.Buffer
	closed   bool
}

// NewWriter wraps dst. Call Close or Flush to release buffered output.
func NewWriter(dst io.Writer, redactor *Redactor) *Writer {
	if redactor == nil {
		redactor = New()
	}
	return &Writer{dst: dst, redactor: redactor}
}

// Write buffers p so secrets spanning calls are treated as one value.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, fmt.Errorf("redact writer: %w", io.ErrClosedPipe)
	}
	return w.buf.Write(p)
}

// Flush redacts and writes all buffered bytes.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *Writer) flushLocked() error {
	if w.buf.Len() == 0 {
		return nil
	}
	redacted := w.redactor.RedactBytes(w.buf.Bytes())
	w.buf.Reset()
	_, err := w.dst.Write(redacted)
	return err
}

// Close flushes buffered output. It does not close the destination.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.flushLocked()
}
