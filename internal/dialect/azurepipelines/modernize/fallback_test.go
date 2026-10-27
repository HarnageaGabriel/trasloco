package modernize

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	catalogdata "github.com/HarnageaGabriel/trasloco/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// naivePosition is the position computation the fallback used before the line
// index existed: it rescans the file from the start for every finding.
func naivePosition(data []byte, offset int) (int, int) {
	offset = min(max(offset, 0), len(data))
	line := 1 + strings.Count(string(data[:offset]), "\n")
	lineStart := strings.LastIndex(string(data[:offset]), "\n") + 1
	return line, offset - lineStart + 1
}

// legacyFallbackFindings is the fallback scan as it was before the line index
// existed: it rescans the file from the start for every finding.
func legacyFallbackFindings(name string, data []byte, index catalogIndex) []Finding {
	var findings []Finding
	for _, match := range fallbackTask.FindAllSubmatchIndex(data, -1) {
		start, end := match[2], match[3]
		raw := string(data[start:end])
		task, major, _, ok := splitReference(raw)
		if !ok {
			continue
		}
		if _, state := index.resolve(task, major); state == notRetired {
			continue
		}
		line := 1 + strings.Count(string(data[:start]), "\n")
		lineStart := strings.LastIndex(string(data[:start]), "\n") + 1
		findings = append(findings, Finding{
			Code: "TRL-MOD-0005", Severity: SeverityWarning, Path: name,
			Line: line, Column: start - lineStart + 1, RetiredTask: raw,
			Action: "Cannot determine the task version; replace the retired task reference by hand.",
		})
	}
	return findings
}

func TestLineIndexPosition(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		offset     int
		wantLine   int
		wantColumn int
	}{
		{"empty file", "", 0, 1, 1},
		{"start of file", "abc", 0, 1, 1},
		{"inside the first line", "abc", 1, 1, 2},
		{"after a newline", "a\nb", 2, 2, 1},
		{"after a carriage return and newline", "a\r\nb", 3, 2, 1},
		{"last byte", "a\nb", 2, 2, 1},
		{"end of file", "a\nb", 3, 2, 2},
		{"offset beyond the end of the file", "a\nb", 99, 2, 2},
		{"offset before the start of the file", "a\nb", -1, 1, 1},
		{"blank line", "a\n\nb", 2, 2, 1},
		{"trailing newline", "a\n", 2, 2, 1},
		{"byte column after a multibyte rune", "aé\nb", 2, 1, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(test.data)
			line, column := newLineIndex(data).position(test.offset)
			if line != test.wantLine || column != test.wantColumn {
				t.Fatalf("position(%d) = %d:%d, want %d:%d", test.offset, line, column, test.wantLine, test.wantColumn)
			}
			wantLine, wantColumn := naivePosition(data, test.offset)
			if line != wantLine || column != wantColumn {
				t.Fatalf("position(%d) = %d:%d, want the legacy %d:%d", test.offset, line, column, wantLine, wantColumn)
			}
		})
	}
}

func TestFallbackTaskStringsMatchesLegacyFindings(t *testing.T) {
	index := newIndex(testCatalog(t))
	tests := []struct {
		name, input string
	}{
		{"empty file", ""},
		{"no task", "steps:\n- script: echo hi\n"},
		{"one task", "steps:\n- task: CopyFiles@1\n"},
		{"task on the first line", "task: CopyFiles@1\n"},
		{"carriage return and newline", "steps:\r\n- task: CopyFiles@1\r\n"},
		{"many tasks", "steps:\n" + strings.Repeat("- task: CopyFiles@1\n", 50)},
		{"flow mapping", "extends: {template: t.yml, parameters: {list: [{task: CopyFiles@1}]}}\n"},
		{"comment before the task", "# comment\n\n\nsteps:\n- task: CopyFiles@1\n"},
		{"task that is not retired", "steps:\n- task: CmdLine@1\n"},
		{"task without a version", "steps:\n- task: CopyFiles\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(test.input)
			want := legacyFallbackFindings("p.yml", data, index)
			got := newLineIndex(data).fallbackTaskStrings("p.yml", index)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("findings differ (-want +got):\n%s", diff)
			}
		})
	}
}

// TestScanFallbackPositions checks the positions the fallback reports for a
// file the YAML parser rejects.
func TestScanFallbackPositions(t *testing.T) {
	input := []byte("steps:\n- task: CopyFiles@1\n  inputs: [unclosed\n")
	findings, _ := Scan("p.yml", input, testCatalog(t))
	if len(findings) != 1 {
		t.Fatalf("findings = %#v", findings)
	}
	if findings[0].Line != 2 || findings[0].Column != 9 {
		t.Fatalf("finding at %d:%d, want 2:9", findings[0].Line, findings[0].Column)
	}
}

// fallbackFile builds a file with the given number of task references.
func fallbackFile(tasks int) []byte {
	var builder strings.Builder
	builder.WriteString("steps:\n")
	for index := 0; index < tasks; index++ {
		builder.WriteString("- task: CopyFiles@1\n")
	}
	return []byte(builder.String())
}

// Issue #73: the fallback recomputed the line and the column of every finding
// by rescanning the file from the start, which is quadratic in the number of
// findings. The line index is built once per file, so the work stays
// proportional to the length of the file.
func TestFallbackPositionsAreLinear(t *testing.T) {
	index := newIndex(testCatalog(t))
	var previousWork, previousLen int
	for _, tasks := range []int{500, 2000, 8000} {
		data := fallbackFile(tasks)
		positions := newLineIndex(data)
		findings := positions.fallbackTaskStrings("p.yml", index)
		if len(findings) != tasks {
			t.Fatalf("tasks=%d: got %d findings", tasks, len(findings))
		}
		work := positions.work
		if limit := 4 * len(data); work > limit {
			t.Fatalf("tasks=%d len=%d: position work %d exceeds the linear budget %d", tasks, len(data), work, limit)
		}
		if previousLen != 0 {
			workRatio := float64(work) / float64(previousWork)
			lenRatio := float64(len(data)) / float64(previousLen)
			if workRatio > 2*lenRatio {
				t.Fatalf("tasks=%d: position work grew %.1fx for %.1fx input: not linear", tasks, workRatio, lenRatio)
			}
		}
		previousWork, previousLen = work, len(data)
	}
}

// allocatedBytesPerRun returns the bytes a call allocates per run.
func allocatedBytesPerRun(runs int, call func()) float64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for run := 0; run < runs; run++ {
		call()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

// TestFallbackTaskStringsAllocationsAreLinear proves that the fallback does
// not copy the file prefix once per finding any more.
func TestFallbackTaskStringsAllocationsAreLinear(t *testing.T) {
	index := newIndex(testCatalog(t))
	data := fallbackFile(2000)
	var findings []Finding
	legacy := allocatedBytesPerRun(3, func() {
		findings = legacyFallbackFindings("p.yml", data, index)
	})
	if len(findings) != 2000 {
		t.Fatalf("got %d findings", len(findings))
	}
	current := allocatedBytesPerRun(3, func() {
		findings = newLineIndex(data).fallbackTaskStrings("p.yml", index)
	})
	if len(findings) != 2000 {
		t.Fatalf("got %d findings", len(findings))
	}
	if current*4 > legacy {
		t.Fatalf("the fallback allocates %.0f bytes per run, the legacy scan %.0f: the copy of the file prefix is back",
			current, legacy)
	}
}

func benchmarkCatalog(b *testing.B) *catalog.Catalog {
	b.Helper()
	loaded, err := catalog.Load(context.Background(), catalogdata.FS, diag.NewCollector())
	if err != nil {
		b.Fatal(err)
	}
	return loaded
}

// BenchmarkLineIndexPosition measures the cost of the position lookup.
func BenchmarkLineIndexPosition(b *testing.B) {
	data := fallbackFile(2000)
	offsets := make([]int, 0, 2000)
	for _, match := range fallbackTask.FindAllSubmatchIndex(data, -1) {
		offsets = append(offsets, match[2])
	}
	positions := newLineIndex(data)
	var sink int
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for _, offset := range offsets {
			line, column := positions.position(offset)
			sink += line + column
		}
	}
	if sink == 0 {
		b.Fatal("no position resolved")
	}
}

// BenchmarkFallbackTaskStrings measures the whole fallback scan of a file with
// many findings.
func BenchmarkFallbackTaskStrings(b *testing.B) {
	data := fallbackFile(2000)
	index := newIndex(benchmarkCatalog(b))
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		findings := newLineIndex(data).fallbackTaskStrings("p.yml", index)
		if len(findings) != 2000 {
			b.Fatalf("got %d findings", len(findings))
		}
	}
}
