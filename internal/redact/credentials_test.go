package redact

import (
	"bytes"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

func TestQueryCredentials(t *testing.T) {
	r := New()
	for _, test := range []struct{ input, want string }{
		{`Get "https://example.test/a?sv=version&sp=rw&se=expiry&sig=secret%2Bvalue&keep=yes": failed`, `Get "https://example.test/a?sv=[REDACTED]&sp=[REDACTED]&se=[REDACTED]&sig=[REDACTED]&keep=yes": failed`},
		{"https://a/?sig=first&sig=second&sp=rw#fragment", "https://a/?sig=[REDACTED]&sig=[REDACTED]&sp=[REDACTED]#fragment"},
		{"https://a/?se=keep&sp=keep&sv=keep", "https://a/?se=keep&sp=keep&sv=keep"},
		{"https://a/?%73IG=hidden&sP=rw&empty=&flag", "https://a/?%73IG=[REDACTED]&sP=[REDACTED]&empty=&flag"},
		{"https://a/?token=&access_token=x", "https://a/?token=&access_token=[REDACTED]"},
		{"https://a/?sig=first https://b/?sp=keep", "https://a/?sig=[REDACTED] https://b/?sp=keep"},
		{"basic setup; bearer of news", "basic setup; bearer of news"},
		{"Authorization: Basic YWJjOmRlZg==", "Authorization: [REDACTED]"},
	} {
		if got := r.Redact(test.input); got != test.want || r.Contains(got) {
			t.Errorf("Redact(%q) = %q; want %q (must be idempotent)", test.input, got, test.want)
		}
	}
	for _, key := range []string{"X-Amz-Signature", "X-Amz-Credential", "X-Amz-Security-Token", "X-Goog-Signature", "X-Goog-Credential", "signature", "token", "access_token"} {
		input := "https://example.test/?keep=yes&" + key + "=canary%2Fcredential&end=ok"
		want := "https://example.test/?keep=yes&" + key + "=[REDACTED]&end=ok"
		if got := r.Redact(input); got != want {
			t.Errorf("%s: %q", key, got)
		}
	}
}

func TestDocumentedTokenPrefixes(t *testing.T) {
	r := New()
	for _, prefix := range []string{"glpat-", "glptt-", "gldt-", "xoxb-"} {
		input := "token " + prefix + strings.Repeat("a1B-", 6) + " end"
		if got := r.Redact(input); strings.Contains(got, prefix) || r.Contains(got) {
			t.Errorf("unmasked token: %q", got)
		}
	}
}

func TestCredentialsInErrorsLogsAndStreams(t *testing.T) {
	r := New()
	err := &url.Error{Op: "Get", URL: "https://example.test/?keep=yes&X-Amz-Credential=canary-credential&X-Amz-Signature=canary-signature", Err: errors.New("connection failed")}
	var log bytes.Buffer
	slog.New(NewSlogHandler(slog.NewJSONHandler(&log, nil), r)).Error("download failed", "error", err)
	if strings.Contains(log.String(), "canary-") || !strings.Contains(log.String(), "connection failed") {
		t.Fatalf("log: %s", log.String())
	}
	for split := 0; split <= len(err.Error()); split++ {
		var dst bytes.Buffer
		writer := NewWriter(&dst, r)
		_, firstErr := writer.Write([]byte(err.Error()[:split]))
		_, secondErr := writer.Write([]byte(err.Error()[split:]))
		closeErr := writer.Close()
		if firstErr != nil || secondErr != nil || closeErr != nil || strings.Contains(dst.String(), "canary-") {
			t.Fatalf("split %d: %s; %v %v %v", split, dst.String(), firstErr, secondErr, closeErr)
		}
	}
}
