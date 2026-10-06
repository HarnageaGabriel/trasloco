package redact

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestStreamingBoundaries(t *testing.T) {
	values := []string{"split-secret", "ghp_12345678901234567890", strings.Repeat("a", 75) + "AZDO" + strings.Repeat("b", 5), "Authorization: Bearer sensitive", "-----BEGIN PRIVATE KEY-----\nprivate-line\n-----END PRIVATE KEY-----"}
	for _, secret := range values {
		for split := 0; split <= len(secret); split++ {
			var dst bytes.Buffer
			r := New()
			r.Add("split-secret")
			w := NewWriter(&dst, r)
			if _, err := w.Write([]byte(secret[:split])); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(secret[split:] + "\n")); err != nil {
				t.Fatal(err)
			}
			if dst.Len() == 0 {
				t.Fatal("complete line was not streamed")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(dst.String(), secret) || strings.Contains(dst.String(), "private-line") {
				t.Fatalf("leak at split %d", split)
			}
		}
	}
	var dst bytes.Buffer
	w := NewWriter(&dst, nil)
	for range 10 {
		if _, err := w.Write(bytes.Repeat([]byte("x"), MaxCarry)); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.buf) > MaxCarry {
		t.Fatal("unbounded carry")
	}
	if _, err := w.Write([]byte("\nnext\n")); err != nil {
		t.Fatal(err)
	}
	if dst.String() != replacement+"\nnext\n" {
		t.Fatal("overlong line not suppressed")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("closed writer accepted data")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestWriterFailureAndFlush(t *testing.T) {
	w := NewWriter(shortWriter{}, nil)
	if _, err := w.Write([]byte("record\n")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write lost")
	}
	if _, err := w.Write([]byte("again")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("sticky error lost")
	}
	if err := w.Flush(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("flush error lost")
	}
	if err := w.Close(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("close error lost")
	}
	var dst bytes.Buffer
	w = NewWriter(&dst, nil)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("last")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil || dst.String() != "last" {
		t.Fatal("final record lost")
	}
}

func TestAdditionalVariants(t *testing.T) {
	r := New()
	r.AddBytes([]byte("first-secret\r\nsecond-secret"))
	r.Add("abc")
	r.Add("")
	for _, line := range []string{"first-secret", "second-secret"} {
		for _, value := range []string{line, base64.URLEncoding.EncodeToString([]byte(line)), base64.RawURLEncoding.EncodeToString([]byte(line)), hex.EncodeToString([]byte(line)), strings.ToUpper(hex.EncodeToString([]byte(line)))} {
			if !r.Contains(value) || strings.Contains(string(r.RedactBytes([]byte(value))), value) {
				t.Fatal("variant survived")
			}
		}
	}
	if r.Contains("abc") {
		t.Fatal("short secret masked")
	}
}

func TestSlogLateRegistrationAndGroups(t *testing.T) {
	var dst bytes.Buffer
	r := New()
	h := NewSlogHandler(slog.NewJSONHandler(&dst, nil), r)
	log := slog.New(h).WithGroup("run-secret").With("key", "run-secret")
	r.Add("run-secret")
	log.Info("run-secret", slog.Group("group", slog.String("run-secret", "run-secret")), "error", errors.New("run-secret"))
	if strings.Contains(dst.String(), "run-secret") {
		t.Fatal("late secret leaked")
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("handler disabled")
	}
}
