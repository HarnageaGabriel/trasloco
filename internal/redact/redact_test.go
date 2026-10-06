package redact

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

func TestRedactVariantsAndPatterns(t *testing.T) {
	secret := "p@ss/word\nvalue"
	r := New()
	r.Add(secret)
	inputs := []string{
		secret,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		url.QueryEscape(secret),
		JSONEscaped(secret),
		"Authorization: Bearer abc.def.ghi",
		"ghp_12345678901234567890",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature",
		"-----BEGIN PRIVATE KEY-----\ncanary\n-----END PRIVATE KEY-----",
	}
	for _, input := range inputs {
		if got := r.Redact(input); strings.Contains(got, secret) || got == input {
			t.Fatalf("Redact(%q) = %q", input, got)
		}
	}
}

func TestWriterSplitSecret(t *testing.T) {
	var dst bytes.Buffer
	r := New()
	r.Add("split-secret")
	w := NewWriter(&dst, r)
	_, _ = w.Write([]byte("before split-"))
	_, _ = w.Write([]byte("secret after"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dst.String(), "split-secret") {
		t.Fatalf("secret leaked: %q", dst.String())
	}
}

func TestSlogHandler(t *testing.T) {
	var dst bytes.Buffer
	r := New()
	r.Add("canary-secret")
	logger := slog.New(NewSlogHandler(slog.NewTextHandler(&dst, nil), r))
	logger.Info("message canary-secret", "token", "canary-secret")
	if strings.Contains(dst.String(), "canary-secret") {
		t.Fatalf("secret leaked: %q", dst.String())
	}
}

func TestZero(t *testing.T) {
	value := []byte("secret")
	Zero(value)
	if !bytes.Equal(value, make([]byte, len(value))) {
		t.Fatalf("Zero() = %v", value)
	}
}

func FuzzRegisteredSecretNeverAppears(f *testing.F) {
	f.Add("prefix", "suffix", "secret-value")
	f.Fuzz(func(t *testing.T, before, after, secret string) {
		if len(secret) < 6 || strings.Contains(Replacement(), secret) {
			return
		}
		r := New()
		r.Add(secret)
		got := r.Redact(before + secret + after)
		if strings.Contains(got, secret) {
			t.Fatalf("registered secret survived")
		}
	})
}
