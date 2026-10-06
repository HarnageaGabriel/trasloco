package fake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config controls deterministic datasets, limits and faults.
type Config struct {
	Seed                  int64
	GitHubRepos           int
	AzureProjects         int
	AzurePipelines        int
	PageSize              int
	GitHubPrimaryLimit    int
	GitHubSecondaryPoints int
	GitHubMaxConcurrent   int
	AzureTSTULimit        int
	AzureDelayRemaining   int
	AzureDelay            time.Duration
	Latency               time.Duration
	FaultEvery            int
	ResetEvery            int
	Clock                 *Clock
}

// Stats is a snapshot of observable server behavior.
type Stats struct {
	Requests       int
	NotModified    int
	Non304         int
	GitHubThrottle int
	AzureThrottle  int
	AzureDelays    int
	Resets         int
}

type usage struct {
	window         time.Time
	used           int
	active         int
	secondaryStart time.Time
	secondaryUsed  int
}

// Server hosts both API dialects on one httptest endpoint.
type Server struct {
	HTTP  *httptest.Server
	Clock *Clock

	mu      sync.Mutex
	config  Config
	stats   Stats
	github  map[string]*usage
	azure   map[string]*usage
	request int
}

// NewServer starts a deterministic fake platform server.
func NewServer(config Config) *Server {
	if config.Clock == nil {
		config.Clock = NewClock()
	}
	if config.PageSize <= 0 {
		config.PageSize = 100
	}
	if config.GitHubPrimaryLimit <= 0 {
		config.GitHubPrimaryLimit = 5000
	}
	if config.GitHubSecondaryPoints <= 0 {
		config.GitHubSecondaryPoints = 900
	}
	if config.GitHubMaxConcurrent <= 0 {
		config.GitHubMaxConcurrent = 100
	}
	if config.AzureTSTULimit <= 0 {
		config.AzureTSTULimit = 200
	}
	if config.AzureDelay <= 0 {
		config.AzureDelay = 301 * time.Second
	}
	server := &Server{
		Clock: config.Clock, config: config,
		github: make(map[string]*usage), azure: make(map[string]*usage),
	}
	server.HTTP = httptest.NewServer(http.HandlerFunc(server.serveHTTP))
	return server
}

// Close stops the HTTP server.
func (server *Server) Close() { server.HTTP.Close() }

// URL returns the server base URL.
func (server *Server) URL() string { return server.HTTP.URL }

// Snapshot returns counters without exposing mutable state.
func (server *Server) Snapshot() Stats {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.stats
}

func (server *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	server.mu.Lock()
	server.request++
	ordinal := server.request
	server.stats.Requests++
	server.mu.Unlock()
	if server.config.Latency > 0 {
		_ = server.Clock.Sleep(request.Context(), server.config.Latency)
	}
	if server.config.ResetEvery > 0 && ordinal%server.config.ResetEvery == 0 {
		server.mu.Lock()
		server.stats.Resets++
		server.mu.Unlock()
		if hijacker, ok := writer.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
				return
			}
		}
	}
	if server.config.FaultEvery > 0 && ordinal%server.config.FaultEvery == 0 {
		http.Error(writer, "injected fault", http.StatusServiceUnavailable)
		server.recordStatus(http.StatusServiceUnavailable)
		return
	}
	if server.isAzure(request.URL.Path) {
		server.serveAzure(writer, request)
		return
	}
	server.serveGitHub(writer, request)
}

func (server *Server) serveGitHub(writer http.ResponseWriter, request *http.Request) {
	token := request.Header.Get("Authorization")
	cost := 1
	if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions {
		cost = 5
	}
	now := server.Clock.Now()
	server.mu.Lock()
	state := server.github[token]
	if state == nil || now.Sub(state.window) >= time.Hour {
		state = &usage{window: now, secondaryStart: now}
		server.github[token] = state
	}
	if now.Sub(state.secondaryStart) >= time.Minute {
		state.secondaryStart = now
		state.secondaryUsed = 0
	}
	state.active++
	secondary := state.active > server.config.GitHubMaxConcurrent || state.secondaryUsed+cost > server.config.GitHubSecondaryPoints
	server.mu.Unlock()
	defer func() {
		server.mu.Lock()
		state.active--
		server.mu.Unlock()
	}()

	etag := responseETag(request.URL)
	conditional := request.Header.Get("If-None-Match") == etag
	if conditional {
		writer.Header().Set("ETag", etag)
		writer.WriteHeader(http.StatusNotModified)
		server.recordStatus(http.StatusNotModified)
		return
	}
	server.mu.Lock()
	remaining := server.config.GitHubPrimaryLimit - state.used
	if secondary {
		server.stats.GitHubThrottle++
		snapshot := *state
		server.mu.Unlock()
		server.githubHeaders(writer.Header(), snapshot, remaining)
		writer.Header().Set("Retry-After", "60")
		http.Error(writer, `{"message":"secondary rate limit"}`, http.StatusForbidden)
		server.recordStatus(http.StatusForbidden)
		return
	}
	if remaining < cost {
		server.stats.GitHubThrottle++
		snapshot := *state
		server.mu.Unlock()
		server.githubHeaders(writer.Header(), snapshot, 0)
		http.Error(writer, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		server.recordStatus(http.StatusForbidden)
		return
	}
	state.used += cost
	state.secondaryUsed += cost
	remaining = server.config.GitHubPrimaryLimit - state.used
	snapshot := *state
	server.mu.Unlock()
	server.githubHeaders(writer.Header(), snapshot, remaining)
	writer.Header().Set("ETag", etag)
	writer.Header().Set("Content-Type", "application/json")

	switch {
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/orgs/") && strings.HasSuffix(request.URL.Path, "/repos"):
		server.writeList(writer, request, server.config.GitHubRepos, "repo", true)
	case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/contents"):
		writeJSON(writer, map[string]any{"type": "file", "path": "pipeline.yml", "content": "bmFtZTogY2k="})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/repos/"):
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		name := "repo"
		if len(parts) >= 3 {
			name = parts[2]
		}
		writeJSON(writer, map[string]any{"id": seededID(server.config.Seed, name), "name": name})
	case request.Method == http.MethodPost:
		writer.WriteHeader(http.StatusCreated)
		writeJSON(writer, map[string]any{"created": true})
	default:
		http.NotFound(writer, request)
	}
	server.recordStatus(http.StatusOK)
}

func (server *Server) githubHeaders(header http.Header, state usage, remaining int) {
	header.Set("X-RateLimit-Limit", strconv.Itoa(server.config.GitHubPrimaryLimit))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(max(0, remaining)))
	header.Set("X-RateLimit-Used", strconv.Itoa(state.used))
	header.Set("X-RateLimit-Reset", strconv.FormatInt(state.window.Add(time.Hour).Unix(), 10))
	header.Set("X-RateLimit-Resource", "core")
}

func (server *Server) serveAzure(writer http.ResponseWriter, request *http.Request) {
	token := request.Header.Get("Authorization")
	now := server.Clock.Now()
	server.mu.Lock()
	state := server.azure[token]
	if state == nil || now.Sub(state.window) >= 5*time.Minute {
		state = &usage{window: now}
		server.azure[token] = state
	}
	remaining := server.config.AzureTSTULimit - state.used
	if remaining <= 0 {
		server.stats.AzureThrottle++
		snapshot := *state
		server.mu.Unlock()
		server.azureHeaders(writer.Header(), snapshot, 0)
		writer.Header().Set("Retry-After", "300")
		writer.WriteHeader(http.StatusTooManyRequests)
		writeJSON(writer, map[string]any{"message": "TF400733: The request has been throttled"})
		server.recordStatus(http.StatusTooManyRequests)
		return
	}
	state.used++
	remaining--
	delay := server.config.AzureDelayRemaining > 0 && remaining <= server.config.AzureDelayRemaining
	if delay {
		server.stats.AzureDelays++
	}
	snapshot := *state
	server.mu.Unlock()
	server.azureHeaders(writer.Header(), snapshot, remaining)
	if delay {
		seconds := server.config.AzureDelay.Seconds()
		writer.Header().Set("Retry-After", strconv.FormatFloat(seconds, 'f', -1, 64))
		writer.Header().Set("X-RateLimit-Delay", strconv.FormatFloat(seconds, 'f', -1, 64))
	}
	etag := responseETag(request.URL)
	writer.Header().Set("ETag", etag)
	if request.Header.Get("If-None-Match") == etag {
		writer.WriteHeader(http.StatusNotModified)
		server.recordStatus(http.StatusNotModified)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(request.URL.Path, "/_apis/projects"):
		server.writeList(writer, request, server.config.AzureProjects, "project", false)
	case strings.HasSuffix(request.URL.Path, "/_apis/pipelines"):
		server.writeList(writer, request, server.config.AzurePipelines, "pipeline", false)
	case strings.Contains(request.URL.Path, "/_apis/git/"):
		writeJSON(writer, map[string]any{"path": "/azure-pipelines.yml", "content": "name: ci"})
	default:
		http.NotFound(writer, request)
	}
	server.recordStatus(http.StatusOK)
}

func (server *Server) azureHeaders(header http.Header, state usage, remaining int) {
	header.Set("X-RateLimit-Resource", "TSTU")
	header.Set("X-RateLimit-Limit", strconv.Itoa(server.config.AzureTSTULimit))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(max(0, remaining)))
	header.Set("X-RateLimit-Reset", strconv.FormatInt(state.window.Add(5*time.Minute).Unix(), 10))
	header.Set("X-RateLimit-Cost", "1")
}

func (server *Server) writeList(writer http.ResponseWriter, request *http.Request, total int, prefix string, github bool) {
	pageSize := server.config.PageSize
	if value, err := strconv.Atoi(request.URL.Query().Get("per_page")); err == nil && value > 0 {
		pageSize = value
	}
	start := 0
	if github {
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}
		start = (page - 1) * pageSize
	} else if token := request.URL.Query().Get("continuationToken"); token != "" {
		start, _ = strconv.Atoi(token)
	}
	end := min(total, start+pageSize)
	items := make([]map[string]any, 0, max(0, end-start))
	for index := start; index < end; index++ {
		name := fmt.Sprintf("%s-%06d", prefix, index)
		items = append(items, map[string]any{"id": seededID(server.config.Seed, name), "name": name})
	}
	if end < total {
		if github {
			next := *request.URL
			query := next.Query()
			query.Set("page", strconv.Itoa(end/pageSize+1))
			query.Set("per_page", strconv.Itoa(pageSize))
			next.RawQuery = query.Encode()
			next.Scheme = scheme(request)
			next.Host = request.Host
			writer.Header().Set("Link", "<"+next.String()+">; rel=\"next\"")
		} else {
			writer.Header().Set("x-ms-continuationtoken", strconv.Itoa(end))
		}
	}
	if github {
		writeJSON(writer, items)
	} else {
		writeJSON(writer, map[string]any{"count": len(items), "value": items})
	}
}

func (server *Server) isAzure(path string) bool {
	return strings.Contains(path, "/_apis/projects") || strings.Contains(path, "/_apis/pipelines") || strings.Contains(path, "/_apis/git/")
}

func (server *Server) recordStatus(status int) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if status == http.StatusNotModified {
		server.stats.NotModified++
	} else {
		server.stats.Non304++
	}
}

func responseETag(value *url.URL) string {
	copyURL := *value
	copyURL.RawQuery = copyURL.Query().Encode()
	sum := sha256.Sum256([]byte(copyURL.String()))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func seededID(seed int64, name string) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", seed, name)))
	var value int64
	for _, octet := range sum[:7] {
		value = value<<8 | int64(octet)
	}
	return value
}

func writeJSON(writer http.ResponseWriter, value any) {
	_ = json.NewEncoder(writer).Encode(value)
}

func scheme(request *http.Request) string {
	if request.TLS != nil {
		return "https"
	}
	return "http"
}
