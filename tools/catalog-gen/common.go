package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	maxResponseBytes = 16 << 20
	httpTimeout      = 60 * time.Second
)

type httpClient struct {
	client         *http.Client
	token          string
	apiHost        string
	allowHTTPToken bool // tests only: production credentials must only travel over HTTPS.
}

func newHTTPClient(token, apiBase string) httpClient {
	parsed, _ := url.Parse(apiBase)
	result := httpClient{token: token, apiHost: parsed.Host}
	result.client = &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if !result.shouldAuthorize(req.URL) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
	return result
}

func (c httpClient) get(ctx context.Context, url string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G704 -- endpoints are explicit operator configuration.
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "trasloco-catalog-gen")
	if c.shouldAuthorize(req.URL) {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req) // #nosec G704 -- endpoint scope and credential forwarding are enforced above.
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, fmt.Errorf("GET request failed: %s", c.redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, resp.Header, readErr
	}
	if len(data) > maxResponseBytes {
		return nil, resp.Header, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := c.redact(strings.TrimSpace(string(data)))
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, resp.Header, fmt.Errorf("GitHub API rate limit or authorization failure (%s, remaining=%s): %s", resp.Status, resp.Header.Get("X-RateLimit-Remaining"), message)
		}
		return nil, resp.Header, fmt.Errorf("GET %s: %s: %s", url, resp.Status, message)
	}
	return data, resp.Header, nil
}

func (c httpClient) shouldAuthorize(target *url.URL) bool {
	if c.token == "" || (target.Scheme != "https" && !c.allowHTTPToken) {
		return false
	}
	hostname := strings.ToLower(target.Hostname())
	return strings.EqualFold(target.Host, c.apiHost) || hostname == "api.github.com" || hostname == "raw.githubusercontent.com"
}

func (c httpClient) redact(message string) string {
	if c.token == "" {
		return message
	}
	return strings.ReplaceAll(message, c.token, "[REDACTED]")
}

func writeYAML(ctx context.Context, name string, header string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode YAML: %w", err)
	}
	var out bytes.Buffer
	if header != "" {
		out.WriteString(header)
		if !strings.HasSuffix(header, "\n") {
			out.WriteByte('\n')
		}
	}
	out.Write(data)
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- directories need execute permission; CONVENTIONS mandates 0700.
		return fmt.Errorf("secure output directory: %w", err)
	}
	if err := os.WriteFile(name, out.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return os.Chmod(name, 0o600)
}

func stringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case json.Number:
		return v.String()
	case float64:
		return fmt.Sprintf("%v", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		data, _ := yaml.Marshal(v)
		return strings.TrimSpace(string(data))
	}
}

func lookup(object map[string]any, key string) (any, bool, error) {
	var matches []string
	for candidate := range object {
		if strings.EqualFold(candidate, key) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	sort.Strings(matches)
	selected := matches[0]
	if _, exact := object[key]; exact {
		selected = key
	}
	value := object[selected]
	for _, candidate := range matches {
		if candidate == selected {
			continue
		}
		if !reflect.DeepEqual(value, object[candidate]) {
			return nil, false, fmt.Errorf("key %q has conflicting case-insensitive spellings %q and %q", key, selected, candidate)
		}
	}
	return value, true, nil
}

func boolValue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	case float64:
		return v != 0
	default:
		return false
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case json.Number:
		result, _ := strconv.Atoi(v.String())
		return result
	case int:
		return v
	case string:
		var result int
		_, _ = fmt.Sscanf(v, "%d", &result)
		return result
	default:
		return 0
	}
}
