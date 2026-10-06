// Package redact removes credentials and secret material from output.
package redact

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const replacement = "[REDACTED]"

var builtinPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), replacement},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), replacement},
	{regexp.MustCompile(`\bgh[opusr]_[A-Za-z0-9_]{20,}\b`), replacement},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`), replacement},
	{regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)(?:basic|bearer)?\s*[A-Za-z0-9._~+/=-]+`), `${1}` + replacement},
	{regexp.MustCompile(`(?i)\b(basic|bearer)\s+[A-Za-z0-9._~+/=-]+`), `${1} ` + replacement},
	{regexp.MustCompile(`(?i)(cookie|set-cookie)(["']?\s*[:=]\s*["']?)[^\s,"'}]+`), `${1}${2}` + replacement},
}

// Redactor holds runtime secrets and built-in credential patterns. It is safe
// for concurrent use.
type Redactor struct {
	mu       sync.RWMutex
	variants []string
}

// New constructs an empty redactor. Secrets may be added at any time.
func New() *Redactor { return &Redactor{} }

// Add registers a secret and its common encodings. Empty values are ignored.
func (r *Redactor) Add(secret string) {
	if secret == "" {
		return
	}
	values := []string{
		secret,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
		url.QueryEscape(secret),
		url.PathEscape(secret),
	}
	if quoted, err := json.Marshal(secret); err == nil && len(quoted) >= 2 {
		values = append(values, string(quoted[1:len(quoted)-1]))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(r.variants)+len(values))
	for _, value := range r.variants {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			r.variants = append(r.variants, value)
			seen[value] = struct{}{}
		}
	}
	sort.Slice(r.variants, func(i, j int) bool { return len(r.variants[i]) > len(r.variants[j]) })
}

// AddBytes registers a copy of secret. The caller remains responsible for
// zeroing its own buffer.
func (r *Redactor) AddBytes(secret []byte) { r.Add(string(secret)) }

// Redact replaces registered values, their encodings, and known credential
// patterns with a constant marker that reveals no secret length.
func (r *Redactor) Redact(input string) string {
	r.mu.RLock()
	variants := append([]string(nil), r.variants...)
	r.mu.RUnlock()
	for _, value := range variants {
		input = strings.ReplaceAll(input, value, replacement)
	}
	for _, pattern := range builtinPatterns {
		input = pattern.re.ReplaceAllString(input, pattern.repl)
	}
	return input
}

// RedactBytes returns a redacted copy of input.
func (r *Redactor) RedactBytes(input []byte) []byte { return []byte(r.Redact(string(input))) }

// Contains reports whether any known secret or built-in secret pattern remains.
func (r *Redactor) Contains(input string) bool { return r.Redact(input) != input }

// Replacement returns the constant marker used for removed material.
func Replacement() string { return replacement }

// Zero overwrites a byte slice in place.
func Zero(secret []byte) {
	for i := range secret {
		secret[i] = 0
	}
}

// JSONEscaped returns the JSON string contents for value. It is useful in
// tests and protocol adapters that register pre-escaped secret forms.
func JSONEscaped(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}
