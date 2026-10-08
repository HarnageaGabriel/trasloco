package cache

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
)

// BenchmarkMillionIndexEntries exercises the million-row scale required by
// ADR 0008. Run it once (the benchmark performs its own fixed-size workload):
//
//	go test ./internal/cache -run=^$ -bench=BenchmarkMillionIndexEntries -benchtime=1x -benchmem
//
// Inserts are deliberately batched because access-time batching and indexed
// eviction, rather than one fsync per fixture row, are the behavior under test.
func BenchmarkMillionIndexEntries(b *testing.B) {
	const (
		entryCount = 1_000_000
		batchSize  = 10_000
	)
	store, _ := openTestStore(b, 0)
	ctx := b.Context()
	wall := clock.Real{}
	body := []byte("shared benchmark body")
	sha := digest(body)
	if err := store.writeCAS(sha, body); err != nil {
		b.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "initialize"); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	putStarted := wall.Now()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO blobs(sha,size,refs) VALUES(?,?,?)`, sha, len(body), entryCount); err != nil {
		b.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meta SET v=? WHERE k='total_bytes'`, len(body)); err != nil {
		b.Fatal(err)
	}
	for first := 0; first < entryCount; first += batchSize {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			b.Fatal(err)
		}
		statement, err := tx.PrepareContext(ctx, `INSERT INTO entries
(key,sha,etag,last_modified,stored_at,accessed_at,status,header,size) VALUES(?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			_ = tx.Rollback()
			b.Fatal(err)
		}
		last := min(first+batchSize, entryCount)
		for i := first; i < last; i++ {
			key := "key-" + strconv.Itoa(i)
			if _, err := statement.ExecContext(ctx, key, sha, `"bench"`, "", int64(i), int64(i), 200, []byte("{}"), len(body)); err != nil {
				_ = statement.Close()
				_ = tx.Rollback()
				b.Fatal(err)
			}
		}
		if err := statement.Close(); err != nil {
			_ = tx.Rollback()
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
	putElapsed := wall.Now().Sub(putStarted)

	getStarted := wall.Now()
	for i := 0; i < entryCount; i++ {
		key := "key-" + strconv.Itoa(i)
		if _, found, err := store.Get(ctx, key); err != nil || !found {
			b.Fatalf("Get %s = %v, %v", key, found, err)
		}
	}
	if err := store.Flush(ctx); err != nil {
		b.Fatal(err)
	}
	getElapsed := wall.Now().Sub(getStarted)

	evictStarted := wall.Now()
	store.maxBytes = 1
	if _, err := store.Put(ctx, "eviction-trigger", 200, nil, []byte("different body")); err != nil {
		b.Fatal(err)
	}
	evictElapsed := wall.Now().Sub(evictStarted)
	b.StopTimer()

	var entries int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM entries`).Scan(&entries); err != nil {
		b.Fatal(err)
	}
	if entries != 0 {
		b.Fatalf("entries after eviction = %d, want 0", entries)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	b.ReportMetric(float64(entryCount)/putElapsed.Seconds(), "put-ops/s")
	b.ReportMetric(float64(entryCount)/getElapsed.Seconds(), "get-ops/s")
	b.ReportMetric(float64(entryCount)/evictElapsed.Seconds(), "evict-ops/s")
	b.ReportMetric(float64(memory.Sys)/(1<<20), "sys-MiB")
	b.ReportMetric(float64(putElapsed.Milliseconds()), "put-ms")
	b.ReportMetric(float64(getElapsed.Milliseconds()), "get-ms")
	b.ReportMetric(float64(evictElapsed.Milliseconds()), "evict-ms")
	b.SetBytes(int64(entryCount))
}
