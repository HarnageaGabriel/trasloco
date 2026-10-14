package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

func TestModernizeWriteRejectsChangedSnapshot(t *testing.T) {
	for _, change := range []string{"mtime", "size", "content"} {
		t.Run(change, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
			path := filepath.Join(root, "pipeline.yml")
			original, initial, err := readModernizeWriteSource(path)
			if err != nil {
				t.Fatal(err)
			}
			current := original
			if change != "mtime" {
				current = bytes.ReplaceAll(original, []byte("@1"), []byte("@2"))
				if change == "size" {
					current = append(current, '\n')
				}
				if err := os.WriteFile(path, current, initial.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
			}
			mtime := initial.ModTime()
			if change == "mtime" {
				mtime = mtime.Add(time.Hour)
			}
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			if err := writeModernizeFile(path, original, []byte("replacement"), initial); err == nil {
				t.Fatal("accepted changed source")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, current) {
				t.Fatalf("changed source overwritten: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary file leaked: %v", err)
			}
		})
	}
}

// Infinite input simulates growth after stat without allocating the source.
type modernizeGrowingReader struct{ read int64 }

func (r *modernizeGrowingReader) Read(p []byte) (int, error) {
	clear(p)
	r.read += int64(len(p))
	return len(p), nil
}

func TestModernizeBoundedRead(t *testing.T) {
	reader := &modernizeGrowingReader{}
	data, err := readModernizeBounded(reader, yamlx.DefaultSizeLimit+1)
	var sizeErr *modernizeSizeError
	if data != nil || !errors.As(err, &sizeErr) || reader.read != 0 {
		t.Fatalf("oversized stat read data: bytes=%d err=%v", reader.read, err)
	}
	data, err = readModernizeBounded(reader, 0)
	if data != nil || !errors.As(err, &sizeErr) || reader.read != yamlx.DefaultSizeLimit+1 {
		t.Fatalf("growing read escaped limit: bytes=%d err=%v", reader.read, err)
	}
	exact := strings.NewReader(strings.Repeat(" ", int(yamlx.DefaultSizeLimit)))
	data, err = readModernizeBounded(exact, yamlx.DefaultSizeLimit)
	if err != nil || int64(len(data)) != yamlx.DefaultSizeLimit {
		t.Fatalf("rejected limit boundary: bytes=%d err=%v", len(data), err)
	}
}

func TestModernizeOversizedDiagnostic(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
	path := filepath.Join(root, "pipeline.yml")
	if err := os.Truncate(path, yamlx.DefaultSizeLimit+1); err != nil {
		t.Fatal(err)
	}
	_, expected := yamlx.Parse(path, make([]byte, yamlx.DefaultSizeLimit+1))
	for _, mode := range [][]string{{}, {"--write"}, {"--write", "--strict"}} {
		args := append([]string{"modernize", "--format", "json", path}, mode...)
		code, output, stderr := executeModernize(t, args...)
		// An oversized file is an incomplete scan wherever the path is
		// named explicitly, so every mode fails closed.
		wantExit := ExitError
		var report modernizeJSONReport
		if err := json.Unmarshal([]byte(output), &report); err != nil {
			t.Fatal(err)
		}
		if code != wantExit || !strings.Contains(stderr, expected[0].Code+": "+expected[0].Message) {
			t.Fatalf("exit=%d want=%d stderr=%s", code, wantExit, stderr)
		}
		if len(report.Warnings) != 1 || report.Warnings[0].Code != expected[0].Code || report.Warnings[0].Message != expected[0].Message {
			t.Fatalf("wrong parser diagnostic: %s", output)
		}
	}
}
