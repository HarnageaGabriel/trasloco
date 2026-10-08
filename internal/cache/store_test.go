package cache

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
)

type testClock struct {
	clock.Real
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Unix(1_700_000_000, 123).UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func openTestStore(t testing.TB, maxBytes int64) (*Store, *testClock) {
	t.Helper()
	c := newTestClock()
	store, err := Open(Options{Dir: filepath.Join(t.TempDir(), "cache"), MaxBytes: maxBytes, Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil && !strings.Contains(err.Error(), "cache is closed") {
			t.Errorf("Close: %v", err)
		}
	})
	return store, c
}

func TestOpenValidationAndLifecycle(t *testing.T) {
	if _, err := Open(Options{}); err == nil {
		t.Fatal("Open accepted an empty directory")
	}
	if _, err := Open(Options{Dir: t.TempDir(), MaxBytes: -1}); err == nil {
		t.Fatal("Open accepted a negative size cap")
	}

	dir := filepath.Join(t.TempDir(), "cache")
	store, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(t.Context(), "missing"); err != nil || found {
		t.Fatalf("Get missing = %v, %v; want false, nil", found, err)
	}
	assertPrivateMode(t, dir, true)
	assertPrivateMode(t, filepath.Join(dir, databaseName), false)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, _, err := store.Get(t.Context(), "closed"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Get after Close error = %v", err)
	}
	if err := store.Flush(t.Context()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Flush after Close error = %v", err)
	}
}

func assertPrivateMode(t *testing.T, path string, directory bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() != directory {
		t.Fatalf("%s directory = %v, want %v", path, info.IsDir(), directory)
	}
	// Windows reports synthesized 0666/0777 permission bits; the production
	// calls still apply the requested modes on platforms that expose them.
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got&0o077 != 0 {
		t.Fatalf("%s mode = %o, want no group/other permissions", path, got)
	}
}

func TestKeyCanonicalURLAndRevalidation(t *testing.T) {
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://example.com/items?z=2&access_token=secret&q=1#fragment", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	one := Key(request, "scope-a")
	request.URL.RawQuery = "q=1&access_token=other&z=2"
	if got := Key(request, "scope-a"); got != one {
		t.Fatalf("secret or query order changed key: %q != %q", got, one)
	}
	if got := Key(request, "scope-b"); got == one {
		t.Fatal("credential scope did not change key")
	}
	request.Header.Set("Accept", "text/plain")
	if got := Key(request, "scope-a"); got == one {
		t.Fatal("representation header did not change key")
	}

	canonical, err := CanonicalURL("https://example.com/p?z=2&a=1#frag")
	if err != nil || canonical != "https://example.com/p?a=1&z=2" {
		t.Fatalf("CanonicalURL = %q, %v", canonical, err)
	}
	if _, err := CanonicalURL("https://example.com/%zz"); err == nil {
		t.Fatal("CanonicalURL accepted invalid escaping")
	}

	entry := Entry{ETag: `"v2"`, LastModified: "yesterday"}
	conditional, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	Revalidate(conditional, entry)
	if conditional.Header.Get("If-None-Match") != `"v2"` || conditional.Header.Get("If-Modified-Since") != "yesterday" {
		t.Fatalf("Revalidate headers = %v", conditional.Header)
	}
	conditional.Header.Set("If-None-Match", `"caller"`)
	Revalidate(conditional, Entry{ETag: `"overwritten"`})
	if conditional.Header.Get("If-None-Match") != `"caller"` {
		t.Fatal("Revalidate overwrote caller validator")
	}
}

func TestPutGetFlushAndHeaderAllowlist(t *testing.T) {
	store, testTime := openTestStore(t, 1<<20)
	header := make(http.Header)
	header.Set("ETag", `"v1"`)
	header.Set("Last-Modified", "today")
	header.Set("Content-Type", "application/json")
	header.Set("X-Ms-Continuationtoken", "next-page")
	header.Set("Set-Cookie", "session=secret")
	header.Set("X-VSS-UserData", "identity")

	entry, err := store.Put(t.Context(), "key", http.StatusCreated, header, []byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Header.Get("Set-Cookie") != "" || entry.Header.Get("X-VSS-UserData") != "" {
		t.Fatalf("sensitive headers persisted: %v", entry.Header)
	}
	if entry.ETag != `"v1"` || entry.LastModified != "today" || entry.StatusCode != http.StatusCreated {
		t.Fatalf("Put metadata = %+v", entry)
	}
	if entry.Header.Get("X-Ms-Continuationtoken") != "next-page" {
		t.Fatalf("pagination header was not persisted: %v", entry.Header)
	}
	assertPrivateMode(t, store.casPath(entry.SHA256), false)
	assertPrivateMode(t, filepath.Dir(store.casPath(entry.SHA256)), true)

	testTime.advance(time.Minute)
	result, found, err := store.Get(t.Context(), "key")
	if err != nil || !found || string(result.Body) != `{"ok":true}` {
		t.Fatalf("Get = %q, %v, %v", result.Body, found, err)
	}
	if !result.Entry.AccessedAt.Equal(testTime.Now()) {
		t.Fatalf("access time = %v, want %v", result.Entry.AccessedAt, testTime.Now())
	}
	if err := store.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, found, err := loadEntry(t.Context(), store.db, "key")
	if err != nil || !found || !stored.AccessedAt.Equal(testTime.Now()) {
		t.Fatalf("flushed entry = %+v, %v, %v", stored, found, err)
	}
}

func TestEvictionUsesAccessTimeAndReferenceCounts(t *testing.T) {
	store, testTime := openTestStore(t, 8)
	ctx := t.Context()
	bodyA := []byte("aaaa")
	bodyB := []byte("bbbb")
	if _, err := store.Put(ctx, "a1", 200, nil, bodyA); err != nil {
		t.Fatal(err)
	}
	testTime.advance(time.Second)
	if _, err := store.Put(ctx, "a2", 200, nil, bodyA); err != nil {
		t.Fatal(err)
	}
	testTime.advance(time.Second)
	if _, err := store.Put(ctx, "b", 200, nil, bodyB); err != nil {
		t.Fatal(err)
	}

	var refs, blobs, total int64
	if err := store.db.QueryRowContext(ctx, `SELECT refs FROM blobs WHERE sha=?`, digest(bodyA)).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM blobs`).Scan(&blobs); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k='total_bytes'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if refs != 2 || blobs != 2 || total != 8 {
		t.Fatalf("refs/blobs/total = %d/%d/%d, want 2/2/8", refs, blobs, total)
	}

	// Refresh a1, then adding a third unique blob evicts a2 first. That does not
	// free its shared body, so b is the next oldest entry whose removal can bring
	// the unique-byte total back under the cap.
	testTime.advance(time.Second)
	if _, found, err := store.Get(ctx, "a1"); err != nil || !found {
		t.Fatalf("refresh a1 = %v, %v", found, err)
	}
	if err := store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	testTime.advance(time.Second)
	if _, err := store.Put(ctx, "c", 200, nil, []byte("cccc")); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a2", "b"} {
		if _, found, err := store.Get(ctx, key); err != nil || found {
			t.Fatalf("Get evicted %s = %v, %v", key, found, err)
		}
	}
	for _, key := range []string{"a1", "c"} {
		if _, found, err := store.Get(ctx, key); err != nil || !found {
			t.Fatalf("Get retained %s = %v, %v", key, found, err)
		}
	}
}

func TestReplacementAndUnlimitedStore(t *testing.T) {
	store, _ := openTestStore(t, 0)
	ctx := t.Context()
	first, err := store.Put(ctx, "key", 200, nil, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "key", 201, nil, []byte("first")); err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, "key", 202, nil, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("replacement digest did not change")
	}
	if _, err := os.Stat(store.casPath(first.SHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced body still exists: %v", err)
	}
	result, found, err := store.Get(ctx, "key")
	if err != nil || !found || string(result.Body) != "second" || result.Entry.StatusCode != 202 {
		t.Fatalf("replacement Get = %q, %+v, %v, %v", result.Body, result.Entry, found, err)
	}
}

func TestMissingCorruptAndInvalidEntriesSelfHeal(t *testing.T) {
	var (
		mu       sync.Mutex
		reported []error
	)
	store, err := Open(Options{
		Dir: filepath.Join(t.TempDir(), "cache"), Clock: newTestClock(),
		OnError: func(err error) {
			mu.Lock()
			reported = append(reported, err)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()

	entry, err := store.Put(ctx, "corrupt", 200, nil, []byte("valid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.casPath(entry.SHA256), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, "corrupt"); err != nil || found {
		t.Fatalf("Get corrupt = %v, %v", found, err)
	}

	missing, err := store.Put(ctx, "missing", 200, nil, []byte("gone"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.casPath(missing.SHA256)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, "missing"); err != nil || found {
		t.Fatalf("Get missing body = %v, %v", found, err)
	}

	invalid, err := store.Put(ctx, "invalid", 200, nil, []byte("digest"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE entries SET sha='not-a-digest' WHERE key='invalid'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, "invalid"); err != nil || found {
		t.Fatalf("Get invalid digest = %v, %v", found, err)
	}
	// The manual corruption leaves the original reference intentionally; the
	// test only verifies that an untrusted index path is never used.
	_ = invalid

	mu.Lock()
	defer mu.Unlock()
	if len(reported) < 2 || !strings.Contains(reported[0].Error(), "integrity") {
		t.Fatalf("reported errors = %v", reported)
	}
}

func TestMalformedHeaderAndUnsupportedSchema(t *testing.T) {
	store, _ := openTestStore(t, 0)
	ctx := t.Context()
	if _, err := store.Put(ctx, "key", 200, nil, []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE entries SET header=x'7b' WHERE key='key'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "key"); err == nil || !strings.Contains(err.Error(), "decode cached header") {
		t.Fatalf("malformed header error = %v", err)
	}

	dir := filepath.Join(t.TempDir(), "versioned")
	first, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.Get(ctx, "initialize"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.ExecContext(ctx, `UPDATE meta SET v=999 WHERE k='version'`); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Get(ctx, "key"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("schema version error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoresShareIndexConcurrently(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	stores := make([]*Store, 2)
	for i := range stores {
		store, err := Open(Options{Dir: dir, MaxBytes: 1 << 20, Clock: clock.Real{}})
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		t.Cleanup(func() { _ = store.Close() })
		if _, _, err := store.Get(t.Context(), "initialize"); err != nil {
			t.Fatal(err)
		}
	}

	const entries = 32
	var group sync.WaitGroup
	for i := range entries {
		group.Add(1)
		go func() {
			defer group.Done()
			key := fmt.Sprintf("key-%02d", i)
			body := []byte(fmt.Sprintf("body-%02d", i))
			if _, err := stores[i%len(stores)].Put(t.Context(), key, 200, nil, body); err != nil {
				t.Errorf("Put %s: %v", key, err)
			}
		}()
	}
	group.Wait()
	for i := range entries {
		key := fmt.Sprintf("key-%02d", i)
		result, found, err := stores[(i+1)%len(stores)].Get(t.Context(), key)
		if err != nil || !found || string(result.Body) != fmt.Sprintf("body-%02d", i) {
			t.Fatalf("cross-store Get %s = %q, %v, %v", key, result.Body, found, err)
		}
	}
}

func TestHelperErrorPaths(t *testing.T) {
	if validDigest("short") || validDigest(strings.Repeat("z", 64)) || !validDigest(digest([]byte("ok"))) {
		t.Fatal("validDigest classification failed")
	}
	if err := touch0600(filepath.Join(t.TempDir(), "missing", "index")); err == nil {
		t.Fatal("touch0600 unexpectedly created a missing parent")
	}

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Options{Dir: blocked})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "key"); err == nil || !strings.Contains(err.Error(), "cache directory") {
		t.Fatalf("blocked directory error = %v", err)
	}

	store, _ = openTestStore(t, 0)
	if _, _, err := store.Get(t.Context(), "initialize"); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := removeEntryTx(t.Context(), tx, "absent")
	if err != nil || len(removed) != 0 {
		t.Fatalf("remove absent = %v, %v", removed, err)
	}
	orphaned, err := releaseTx(t.Context(), tx, strings.Repeat("0", 64))
	if err != nil || orphaned {
		t.Fatalf("release absent = %v, %v", orphaned, err)
	}
	keys, err := oldestKeys(t.Context(), tx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("oldestKeys empty = %v, %v", keys, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func digest(body []byte) string {
	sum := sha256Sum(body)
	return fmt.Sprintf("%x", sum)
}

func sha256Sum(body []byte) [32]byte {
	// Kept behind a helper so test expectations read in terms of cache digests.
	return sha256.Sum256(body)
}
