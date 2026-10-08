package ledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/clock"
)

// BenchmarkMillionResources is the opt-in ledger load test. Run with:
// go test ./internal/ledger -run '^$' -bench '^BenchmarkMillionResources$' -benchtime=1x
func BenchmarkMillionResources(b *testing.B) {
	const total = 1_000_000
	const loadBatchSize = 1_000
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		dir := filepath.Join(b.TempDir(), fmt.Sprintf("run-%d", iteration))
		ctx := context.Background()
		store, err := Open(ctx, dir)
		if err != nil {
			b.Fatal(err)
		}
		if err := store.BeginRun(ctx, Run{ID: testRun, MigrationID: testMigration, Command: "load"}); err != nil {
			b.Fatal(err)
		}
		started := clock.Real{}.Now()
		peakRSS := processRSS()
		for offset := 0; offset < total; offset += loadBatchSize {
			resources := make([]Resource, loadBatchSize)
			for index := range resources {
				resources[index].Key = fmt.Sprintf("resource-%07d", offset+index)
			}
			if err := store.UpsertResources(ctx, testRun, resources); err != nil {
				b.Fatal(err)
			}
			if rss := processRSS(); rss > peakRSS {
				peakRSS = rss
			}
		}
		seen := 0
		for _, err := range store.ListByState(ctx, testRun, StatePlanned) {
			if err != nil {
				b.Fatal(err)
			}
			seen++
		}
		if seen != total {
			b.Fatalf("streamed %d resources, want %d", seen, total)
		}
		for offset := 0; offset < total; offset += loadBatchSize {
			transitions := make([]ResourceTransition, loadBatchSize)
			for index := range transitions {
				transitions[index] = ResourceTransition{ResourceKey: fmt.Sprintf("resource-%07d", offset+index), To: StateApplying}
			}
			if err := store.TransitionResources(ctx, testRun, transitions); err != nil {
				b.Fatal(err)
			}
			if rss := processRSS(); rss > peakRSS {
				peakRSS = rss
			}
		}
		elapsed := clock.Real{}.Now().Sub(started)
		if err := store.Close(); err != nil {
			b.Fatal(err)
		}
		size, err := ledgerDiskSize(dir)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(elapsed.Seconds(), "wall_s")
		b.ReportMetric(float64(size), "db_bytes")
		b.ReportMetric(float64(peakRSS), "peak_rss_bytes")
	}
}

func ledgerDiskSize(dir string) (int64, error) {
	var total int64
	for _, name := range []string{databaseName, databaseName + "-wal", databaseName + "-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil {
			total += info.Size()
			continue
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
	}
	return total, nil
}
