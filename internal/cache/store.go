// Package cache provides a content-addressed HTTP response cache.
//
// Store is safe across goroutines. CAS writes are atomic across processes;
// index updates use atomic replacement but concurrent processes are last-writer
// wins. Callers needing strict cross-process index serialization must provide
// an external lock.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const indexVersion = 1

// Clock supplies cache timestamps.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Options configures a Store.
type Options struct {
	Dir      string
	MaxBytes int64
	Clock    Clock
}

// Entry is persisted metadata for a cached representation.
type Entry struct {
	SHA256       string      `json:"sha256"`
	ETag         string      `json:"etag,omitempty"`
	LastModified string      `json:"lastModified,omitempty"`
	StoredAt     time.Time   `json:"storedAt"`
	AccessedAt   time.Time   `json:"accessedAt"`
	StatusCode   int         `json:"statusCode"`
	Header       http.Header `json:"header,omitempty"`
	Size         int64       `json:"size"`
}

// Result combines metadata with immutable body bytes.
type Result struct {
	Entry Entry
	Body  []byte
}

type diskIndex struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// Store owns a CAS directory and request-key index.
type Store struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	clock    Clock
	loaded   bool
	index    diskIndex
}

// Open opens a cache, creating it lazily on the first write.
func Open(opts Options) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("cache directory is required")
	}
	if opts.MaxBytes < 0 {
		return nil, errors.New("cache size cap cannot be negative")
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	return &Store{
		dir: opts.Dir, maxBytes: opts.MaxBytes, clock: opts.Clock,
		index: diskIndex{Version: indexVersion, Entries: make(map[string]Entry)},
	}, nil
}

// Key returns a stable request key. Credential scope separates permissions
// without persisting authentication material. Known secret query parameters
// are removed defensively.
func Key(request *http.Request, scopeID string) string {
	canonical := *request.URL
	query := canonical.Query()
	for name := range query {
		if secretQueryParameter(name) {
			query.Del(name)
		}
	}
	canonical.RawQuery = query.Encode()
	canonical.Fragment = ""
	var builder strings.Builder
	builder.WriteString(request.Method)
	builder.WriteByte('\n')
	builder.WriteString(canonical.String())
	builder.WriteByte('\n')
	builder.WriteString(scopeID)
	for _, name := range []string{"Accept", "Content-Type", "X-GitHub-Api-Version", "Api-Version"} {
		if value := request.Header.Values(name); len(value) > 0 {
			builder.WriteByte('\n')
			builder.WriteString(strings.ToLower(name))
			builder.WriteByte(':')
			builder.WriteString(strings.Join(value, ","))
		}
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func secretQueryParameter(name string) bool {
	switch strings.ToLower(name) {
	case "access_token", "token", "api_key", "apikey", "key", "client_secret", "sig", "signature":
		return true
	default:
		return false
	}
}

// Get returns a copy of a cached response.
func (s *Store) Get(key string) (Result, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Result{}, false, err
	}
	entry, ok := s.index.Entries[key]
	if !ok {
		return Result{}, false, nil
	}
	if len(entry.SHA256) != sha256.Size*2 {
		delete(s.index.Entries, key)
		return Result{}, false, errors.New("invalid digest in cache index")
	}
	body, err := os.ReadFile(s.casPath(entry.SHA256))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			delete(s.index.Entries, key)
			return Result{}, false, s.writeIndexLocked()
		}
		return Result{}, false, fmt.Errorf("read cached body: %w", err)
	}
	entry.AccessedAt = s.clock.Now().UTC()
	s.index.Entries[key] = entry
	return Result{Entry: entry, Body: body}, true, nil
}

// Put atomically stores body and updates key metadata.
func (s *Store) Put(key string, statusCode int, header http.Header, body []byte) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Entry{}, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err := s.writeCASLocked(digest, body); err != nil {
		return Entry{}, err
	}
	now := s.clock.Now().UTC()
	entry := Entry{
		SHA256: digest, ETag: header.Get("ETag"), LastModified: header.Get("Last-Modified"),
		StoredAt: now, AccessedAt: now, StatusCode: statusCode,
		Header: cloneHeader(header), Size: int64(len(body)),
	}
	s.index.Entries[key] = entry
	if err := s.evictLocked(); err != nil {
		return Entry{}, err
	}
	if err := s.writeIndexLocked(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Revalidate applies validators from a cached entry to request.
func Revalidate(request *http.Request, entry Entry) {
	if entry.ETag != "" && request.Header.Get("If-None-Match") == "" {
		request.Header.Set("If-None-Match", entry.ETag)
	}
	if entry.LastModified != "" && request.Header.Get("If-Modified-Since") == "" {
		request.Header.Set("If-Modified-Since", entry.LastModified)
	}
}

func (s *Store) loadLocked() error {
	if s.loaded {
		return nil
	}
	s.loaded = true
	raw, err := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cache index: %w", err)
	}
	if err := json.Unmarshal(raw, &s.index); err != nil {
		return fmt.Errorf("decode cache index: %w", err)
	}
	if s.index.Version != indexVersion || s.index.Entries == nil {
		return errors.New("unsupported cache index version")
	}
	return nil
}

func (s *Store) writeCASLocked(digest string, body []byte) error {
	path := s.casPath(digest)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create CAS directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".cas-*")
	if err != nil {
		return fmt.Errorf("create CAS temporary file: %w", err)
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure CAS file: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("write CAS file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync CAS file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close CAS file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil
		}
		return fmt.Errorf("publish CAS file: %w", err)
	}
	return nil
}

func (s *Store) writeIndexLocked() error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	raw, err := json.MarshalIndent(s.index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache index: %w", err)
	}
	temporary, err := os.CreateTemp(s.dir, ".index-*")
	if err != nil {
		return fmt.Errorf("create index temporary file: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write cache index: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close cache index: %w", err)
	}
	indexPath := filepath.Join(s.dir, "index.json")
	if err := replaceFile(name, indexPath); err != nil {
		return fmt.Errorf("publish cache index: %w", err)
	}
	return nil
}

func replaceFile(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	// Windows cannot always rename over an existing file. The index remains
	// reconstructable from future requests if interruption occurs here.
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}

func (s *Store) evictLocked() error {
	if s.maxBytes == 0 {
		return nil
	}
	type candidate struct {
		key   string
		entry Entry
	}
	items := make([]candidate, 0, len(s.index.Entries))
	var total int64
	for key, entry := range s.index.Entries {
		total += entry.Size
		items = append(items, candidate{key: key, entry: entry})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].entry.AccessedAt.Before(items[j].entry.AccessedAt) })
	for _, item := range items {
		if total <= s.maxBytes {
			break
		}
		delete(s.index.Entries, item.key)
		total -= item.entry.Size
		if !s.digestReferencedLocked(item.entry.SHA256) {
			if err := os.Remove(s.casPath(item.entry.SHA256)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("evict cached body: %w", err)
			}
		}
	}
	return nil
}

func (s *Store) digestReferencedLocked(digest string) bool {
	for _, entry := range s.index.Entries {
		if entry.SHA256 == digest {
			return true
		}
	}
	return false
}

func (s *Store) casPath(digest string) string {
	return filepath.Join(s.dir, "cas", digest[:2], digest)
}

func cloneHeader(header http.Header) http.Header {
	cloned := make(http.Header, len(header))
	for name, values := range header {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

// CanonicalURL exposes URL normalization for tests and callers constructing
// keys without an http.Request.
func CanonicalURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	parsed.RawQuery = parsed.Query().Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}
