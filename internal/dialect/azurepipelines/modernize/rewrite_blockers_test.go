package modernize

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRewriteStepDirectives(t *testing.T) {
	cat := testCatalog(t)
	for _, task := range []string{"FtpUpload@1", "CopyFiles@1"} {
		for _, directive := range []string{
			"${{ if true }}:\n    inputs:\n      serverEndpoint: ftp\n      rootFolder: out\n      overwrite: false",
			"${{ insert }}: ${{ parameters.props }}",
		} {
			src := "steps:\n- task: " + task + "\n  " + directive + "\n"
			out, ds, changed, err := Rewrite(context.Background(), []byte(src), "test.yml", cat)
			if err != nil || changed || string(out) != src || len(ds) != 1 {
				t.Fatalf("task=%s changed=%v err=%v diagnostics=%v", task, changed, err, ds)
			}
			d := ds[0]
			if d.Code != "TRL-MOD-0009" || d.Params["reason"] != "dynamic-input-key" || d.Source.Line != 2 || d.Source.Col != 9 {
				t.Fatalf("unexpected directive diagnostic: %+v", d)
			}
		}
	}
}

func TestRewriteManyInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("50,000-input regression")
	}
	var src strings.Builder
	src.WriteString("steps:\n- task: CopyFiles@1\n  inputs:\n")
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(&src, "    x%05d: v\n", i)
	}
	out, ds, changed, err := Rewrite(context.Background(), []byte(src.String()), "large.yml", testCatalog(t))
	if err != nil || !changed || len(ds) != 50001 || string(out) != strings.Replace(src.String(), "CopyFiles@1", "CopyFiles@2", 1) {
		t.Fatalf("changed=%v err=%v diagnostics=%d", changed, err, len(ds))
	}
}

const archiveStep = "- task: ArchiveFiles@1\n  inputs:\n    rootFolder: a\n    archiveType: default\n"

func TestRewriteEndingComputedOncePerFile(t *testing.T) {
	cat := testCatalog(t)
	for _, n := range []int{10, 200} {
		src := "steps:\n" + strings.Repeat(archiveStep, n)
		before := endingComputations
		_, _, changed, err := Rewrite(context.Background(), []byte(src), "large.yml", cat)
		if err != nil || !changed {
			t.Fatalf("n=%d changed=%v err=%v", n, changed, err)
		}
		if got := endingComputations - before; got != 1 {
			t.Fatalf("n=%d: line ending computed %d times, want 1", n, got)
		}
	}
}

func TestRewriteManyRetiredStepsTimeBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("wall-clock budget: skipped with -short or -race")
	}
	src := []byte("steps:\n" + strings.Repeat(archiveStep, 40000))
	// Linear cost is ~5s here (diagnostic redaction dominates); the quadratic
	// ending scan took ~57s. Rewrite returns the context error past the budget.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _, changed, err := Rewrite(ctx, src, "large.yml", testCatalog(t))
	if err != nil || !changed {
		t.Fatalf("within 20s budget: changed=%v err=%v", changed, err)
	}
}

func TestRewriteDuplicateFoldedInputs(t *testing.T) {
	src := "steps:\n- task: CopyFiles@1\n  inputs:\n    unknowns: a\n    unknownſ: b\n"
	out, ds, changed, err := Rewrite(context.Background(), []byte(src), "test.yml", testCatalog(t))
	if err != nil || changed || string(out) != src || len(ds) != 1 || ds[0].Params["reason"] != "duplicate-input" {
		t.Fatalf("changed=%v err=%v diagnostics=%v", changed, err, ds)
	}
}
