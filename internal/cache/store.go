// Package cache provides a content-addressed HTTP response cache (ADR 0008).
//
// Metadata lives in a SQLite index (WAL, immediate write transactions) and
// bodies in a content-addressed store cas/<2 hex>/<sha256>. Bodies are written
// outside any lock with a temporary file and rename; the index only records
// references and reference counts. Several processes may share a directory:
// SQLite serializes index writes, and a body deleted by another process while a
// new reference to it is committed is detected on read and treated as a miss.
//
// The cache is best-effort. A failure never prevents a request: Get and Put
// return errors that callers treat as non-fatal, and self-healing events
// (corrupt or missing bodies, background flush or eviction failures) go to the
// OnError hook.
package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
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

	_ "modernc.org/sqlite" // pure Go SQLite driver, shared with the ledger.

	"github.com/HarnageaGabriel/trasloco/internal/clock"
)

const (
	databaseName = "index.sqlite"
	// accessBatch is the number of pending access-time updates that triggers a flush.
	accessBatch = 256
	// evictBatch is the number of entries examined per eviction round.
	evictBatch = 64

	schemaVersion = 2
)

const schema = `
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v INTEGER NOT NULL) WITHOUT ROWID;
INSERT OR IGNORE INTO meta(k, v) VALUES('version', 2), ('total_bytes', 0);
CREATE TABLE IF NOT EXISTS blobs(sha TEXT PRIMARY KEY, size INTEGER NOT NULL, refs INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS entries(
  key TEXT PRIMARY KEY,
  sha TEXT NOT NULL,
  etag TEXT NOT NULL,
  last_modified TEXT NOT NULL,
  stored_at INTEGER NOT NULL,
  accessed_at INTEGER NOT NULL,
  status INTEGER NOT NULL,
  header BLOB NOT NULL,
  size INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS entries_accessed ON entries(accessed_at, key);
`

// Options configures a Store.
type Options struct {
	Dir      string
	MaxBytes int64
	Clock    clock.Clock
	// OnError receives non-fatal internal errors. It may be nil and must not block.
	OnError func(error)
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

// Store owns a CAS directory and the SQLite request-key index.
type Store struct {
	dir      string
	maxBytes int64
	clock    clock.Clock
	onError  func(error)

	initMu sync.Mutex
	db     *sql.DB
	closed bool

	pendingMu sync.Mutex
	pending   map[string]int64
}

// Open prepares a cache. Files are created lazily on first use.
func Open(opts Options) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("cache directory is required")
	}
	if opts.MaxBytes < 0 {
		return nil, errors.New("cache size cap cannot be negative")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	return &Store{
		dir: opts.Dir, maxBytes: opts.MaxBytes, clock: opts.Clock, onError: opts.OnError,
		pending: make(map[string]int64),
	}, nil
}

func (s *Store) report(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
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

// storedHeaders is the allowlist of response headers persisted in the cache.
// Everything else (Set-Cookie, X-VSS-UserData, challenges...) is dropped.
var storedHeaders = []string{
	"Cache-Control", "Content-Language", "Content-Type", "Etag", "Expires", "Last-Modified", "Link", "Vary",
	"X-Ms-Continuationtoken",
}

func allowlistHeader(header http.Header) http.Header {
	out := make(http.Header, len(storedHeaders))
	for _, name := range storedHeaders {
		if values := header.Values(name); len(values) > 0 {
			out[name] = append([]string(nil), values...)
		}
	}
	return out
}

// ensure opens the database on first use.
func (s *Store) ensure(ctx context.Context) (*sql.DB, error) {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.closed {
		return nil, errors.New("cache is closed")
	}
	if s.db != nil {
		return s.db, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	path := filepath.Join(s.dir, databaseName)
	if err := touch0600(path); err != nil {
		return nil, err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil { // #nosec G302 -- cache directories are required to be 0700.
		return nil, fmt.Errorf("secure cache directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_txlock=immediate" +
		"&_pragma=" + url.QueryEscape("busy_timeout(5000)") +
		"&_pragma=" + url.QueryEscape("synchronous(FULL)")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cache index: %w", err)
	}
	db.SetMaxOpenConns(4)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create cache schema: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k='version'`).Scan(&version); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read cache index version: %w", err)
	}
	if version != schemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("unsupported cache index version %d", version)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = db.Close()
			return nil, fmt.Errorf("secure cache index: %w", err)
		}
	}
	s.db = db
	return db, nil
}

func touch0600(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- fixed filename in the cache directory.
	if err != nil {
		return fmt.Errorf("create cache index: %w", err)
	}
	return file.Close()
}

// Get returns a copy of a cached response. A missing or corrupt body is a
// miss; corrupt entries are removed and reported through OnError.
func (s *Store) Get(ctx context.Context, key string) (Result, bool, error) {
	db, err := s.ensure(ctx)
	if err != nil {
		return Result{}, false, err
	}
	entry, ok, err := loadEntry(ctx, db, key)
	if err != nil || !ok {
		return Result{}, false, err
	}
	if !validDigest(entry.SHA256) {
		s.drop(ctx, key)
		s.report(errors.New("cache: invalid digest in index; entry dropped"))
		return Result{}, false, nil
	}
	// Bound the read by the indexed size: a body of any other size is corrupt.
	if info, statErr := os.Stat(s.casPath(entry.SHA256)); statErr == nil && info.Size() != entry.Size {
		s.drop(ctx, key)
		s.removeBody(entry.SHA256)
		s.report(errors.New("cache: body failed integrity check (size differs from index); entry dropped"))
		return Result{}, false, nil
	}
	body, err := os.ReadFile(s.casPath(entry.SHA256)) // #nosec G304 -- digest length validated, path under the cache directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.drop(ctx, key)
			return Result{}, false, nil
		}
		return Result{}, false, fmt.Errorf("read cached body: %w", err)
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != entry.SHA256 {
		s.drop(ctx, key)
		s.removeBody(entry.SHA256)
		s.report(errors.New("cache: body failed integrity check; entry dropped"))
		return Result{}, false, nil
	}
	now := s.clock.Now().UTC()
	entry.AccessedAt = now
	s.touch(ctx, key, now)
	return Result{Entry: entry, Body: body}, true, nil
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func loadEntry(ctx context.Context, db *sql.DB, key string) (Entry, bool, error) {
	var (
		entry            Entry
		stored, accessed int64
		rawHeader        []byte
	)
	err := db.QueryRowContext(ctx,
		`SELECT sha, etag, last_modified, stored_at, accessed_at, status, header, size FROM entries WHERE key=?`, key).
		Scan(&entry.SHA256, &entry.ETag, &entry.LastModified, &stored, &accessed, &entry.StatusCode, &rawHeader, &entry.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("query cache index: %w", err)
	}
	entry.StoredAt, entry.AccessedAt = time.Unix(0, stored).UTC(), time.Unix(0, accessed).UTC()
	if len(rawHeader) > 0 {
		if err := json.Unmarshal(rawHeader, &entry.Header); err != nil {
			return Entry{}, false, fmt.Errorf("decode cached header: %w", err)
		}
	}
	return entry, true, nil
}

// touch records an access time; the index is updated in batches.
func (s *Store) touch(ctx context.Context, key string, now time.Time) {
	s.pendingMu.Lock()
	s.pending[key] = now.UnixNano()
	flush := len(s.pending) >= accessBatch
	s.pendingMu.Unlock()
	if flush {
		s.report(s.Flush(ctx))
	}
}

// Flush writes pending access times to the index.
func (s *Store) Flush(ctx context.Context) error {
	db, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	return s.flush(ctx, db)
}

func (s *Store) flush(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin access flush: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.flushTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// flushTx applies pending access times inside tx. A lost batch only makes
// eviction order slightly less precise.
func (s *Store) flushTx(ctx context.Context, tx *sql.Tx) error {
	s.pendingMu.Lock()
	batch := s.pending
	s.pending = make(map[string]int64)
	s.pendingMu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE entries SET accessed_at=max(accessed_at, ?) WHERE key=?`)
	if err != nil {
		return fmt.Errorf("prepare access update: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	keys := make([]string, 0, len(batch))
	for key := range batch {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		at := batch[key]
		if _, err := stmt.ExecContext(ctx, at, key); err != nil {
			return fmt.Errorf("update access time: %w", err)
		}
	}
	return nil
}

// drop removes an entry whose body is unusable. Failures are only reported.
func (s *Store) drop(ctx context.Context, key string) {
	db, err := s.ensure(ctx)
	if err != nil {
		s.report(err)
		return
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		s.report(fmt.Errorf("begin drop: %w", err))
		return
	}
	defer func() { _ = tx.Rollback() }()
	orphans, err := removeEntryTx(ctx, tx, key)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.report(fmt.Errorf("drop cache entry: %w", err))
		return
	}
	for _, sha := range orphans {
		s.removeBody(sha)
	}
}

// removeEntryTx deletes key and returns digests whose last reference vanished.
func removeEntryTx(ctx context.Context, tx *sql.Tx, key string) ([]string, error) {
	var sha string
	err := tx.QueryRowContext(ctx, `SELECT sha FROM entries WHERE key=?`, key).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE key=?`, key); err != nil {
		return nil, err
	}
	orphaned, err := releaseTx(ctx, tx, sha)
	if err != nil || !orphaned {
		return nil, err
	}
	return []string{sha}, nil
}

// releaseTx drops one reference to sha and reports whether the blob is orphaned.
func releaseTx(ctx context.Context, tx *sql.Tx, sha string) (bool, error) {
	var size, refs int64
	if err := tx.QueryRowContext(ctx, `UPDATE blobs SET refs=refs-1 WHERE sha=? RETURNING size, refs`, sha).Scan(&size, &refs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if refs > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE sha=?`, sha); err != nil {
		return false, err
	}
	_, err := tx.ExecContext(ctx, `UPDATE meta SET v=v-? WHERE k='total_bytes'`, size)
	return true, err
}

// acquireTx adds one reference to sha, creating the blob row if needed.
func acquireTx(ctx context.Context, tx *sql.Tx, sha string, size int64) error {
	var refs int64
	err := tx.QueryRowContext(ctx, `UPDATE blobs SET refs=refs+1 WHERE sha=? RETURNING refs`, sha).Scan(&refs)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO blobs(sha, size, refs) VALUES(?,?,1)`, sha, size); err != nil {
			return fmt.Errorf("insert blob: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meta SET v=v+? WHERE k='total_bytes'`, size); err != nil {
			return fmt.Errorf("update cache size: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reference blob: %w", err)
	}
	return nil
}

// Put stores body and metadata for key. Only allowlisted headers are kept.
func (s *Store) Put(ctx context.Context, key string, statusCode int, header http.Header, body []byte) (Entry, error) {
	db, err := s.ensure(ctx)
	if err != nil {
		return Entry{}, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if err := s.writeCAS(digest, body); err != nil {
		return Entry{}, err
	}
	now := s.clock.Now().UTC()
	kept := allowlistHeader(header)
	entry := Entry{
		SHA256: digest, ETag: kept.Get("ETag"), LastModified: kept.Get("Last-Modified"),
		StoredAt: now, AccessedAt: now, StatusCode: statusCode, Header: kept, Size: int64(len(body)),
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("begin cache write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	orphans, err := s.putTx(ctx, tx, key, &entry)
	if err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("commit cache write: %w", err)
	}
	for _, sha := range orphans {
		s.removeBody(sha)
	}
	return entry, nil
}

func (s *Store) putTx(ctx context.Context, tx *sql.Tx, key string, entry *Entry) ([]string, error) {
	if err := s.flushTx(ctx, tx); err != nil {
		return nil, err
	}
	var orphans []string
	var oldSHA string
	err := tx.QueryRowContext(ctx, `SELECT sha FROM entries WHERE key=?`, key).Scan(&oldSHA)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query previous entry: %w", err)
	}
	if oldSHA != entry.SHA256 {
		if err := acquireTx(ctx, tx, entry.SHA256, entry.Size); err != nil {
			return nil, err
		}
		if oldSHA != "" {
			orphaned, err := releaseTx(ctx, tx, oldSHA)
			if err != nil {
				return nil, fmt.Errorf("release previous body: %w", err)
			}
			if orphaned {
				orphans = append(orphans, oldSHA)
			}
		}
	}
	rawHeader, err := json.Marshal(entry.Header)
	if err != nil {
		return nil, fmt.Errorf("encode cached header: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entries(key, sha, etag, last_modified, stored_at, accessed_at, status, header, size)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET sha=excluded.sha, etag=excluded.etag, last_modified=excluded.last_modified,
 stored_at=excluded.stored_at, accessed_at=excluded.accessed_at, status=excluded.status, header=excluded.header, size=excluded.size`,
		key, entry.SHA256, entry.ETag, entry.LastModified, entry.StoredAt.UnixNano(), entry.AccessedAt.UnixNano(),
		entry.StatusCode, rawHeader, entry.Size); err != nil {
		return nil, fmt.Errorf("write cache entry: %w", err)
	}
	evicted, err := s.evictTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return append(orphans, evicted...), nil
}

func totalBytes(ctx context.Context, tx *sql.Tx) (int64, error) {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k='total_bytes'`).Scan(&total); err != nil {
		return 0, fmt.Errorf("read cache size: %w", err)
	}
	return total, nil
}

// evictTx removes least recently accessed entries until the size cap holds.
func (s *Store) evictTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	if s.maxBytes == 0 {
		return nil, nil
	}
	var orphans []string
	for {
		total, err := totalBytes(ctx, tx)
		if err != nil {
			return nil, err
		}
		if total <= s.maxBytes {
			return orphans, nil
		}
		keys, err := oldestKeys(ctx, tx)
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			return orphans, nil
		}
		for _, key := range keys {
			removed, err := removeEntryTx(ctx, tx, key)
			if err != nil {
				return nil, fmt.Errorf("evict entry: %w", err)
			}
			orphans = append(orphans, removed...)
			if total, err = totalBytes(ctx, tx); err != nil {
				return nil, err
			}
			if total <= s.maxBytes {
				return orphans, nil
			}
		}
	}
}

func oldestKeys(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key FROM entries ORDER BY accessed_at, key LIMIT ?`, evictBatch)
	if err != nil {
		return nil, fmt.Errorf("select eviction candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan eviction candidate: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// Close flushes pending access times and releases the index.
func (s *Store) Close() error {
	s.initMu.Lock()
	db := s.db
	s.db = nil
	s.closed = true
	s.initMu.Unlock()
	if db == nil {
		return nil
	}
	return errors.Join(s.flush(context.Background(), db), db.Close())
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

// writeCAS stores body under its digest without holding any lock.
func (s *Store) writeCAS(digest string, body []byte) error {
	path := s.casPath(digest)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create CAS directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil { // #nosec G302 -- CAS directories are required to be 0700.
		return fmt.Errorf("secure CAS directory: %w", err)
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

func (s *Store) removeBody(sha string) {
	if len(sha) != sha256.Size*2 {
		return
	}
	if err := os.Remove(s.casPath(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.report(fmt.Errorf("remove cached body: %w", err))
	}
}

func (s *Store) casPath(digest string) string {
	return filepath.Join(s.dir, "cas", digest[:2], digest)
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
