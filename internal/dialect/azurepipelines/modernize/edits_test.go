package modernize

import (
	"bytes"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
	"github.com/google/go-cmp/cmp"
)

func TestApplyEdits(t *testing.T) {
	tests := []struct {
		name, input, want string
		edits             []yamlx.Patch
	}{
		{name: "unchanged", input: "# comment\r\nsteps: []", want: "# comment\r\nsteps: []"},
		{
			name: "original coordinates and program order", input: "abcd", want: "aXYBc!",
			edits: []yamlx.Patch{
				{Start: 3, End: 4, Replacement: []byte("!")},
				{Start: 1, End: 2, Replacement: []byte("B")},
				{Start: 1, End: 1, Replacement: []byte("X")},
				{Start: 1, End: 1, Replacement: []byte("Y")},
			},
		},
		{
			name: "append without final newline", input: "a", want: "aXY",
			edits: []yamlx.Patch{{Start: 1, End: 1, Replacement: []byte("X")}, {Start: 1, End: 1, Replacement: []byte("Y")}},
		},
		{
			name: "bytes outside unicode edit stay exact", input: "# café\r\n'é': old # keep\r\n", want: "# café\r\n'é': 'new' # keep\r\n",
			edits: []yamlx.Patch{{Start: 15, End: 18, Replacement: []byte("'new'")}},
		},
		{
			name: "coalesced deletion and append", input: "a: 1\r\nb: 2\r\n# keep\r\n", want: "a: 1\r\nc: 3\r\n# keep\r\n",
			edits: []yamlx.Patch{{Start: 6, End: 12, Replacement: []byte("c: 3\r\n")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplyEdits([]byte(tt.input), tt.edits)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, string(got)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestApplyEditsRejectsInvalidPlan(t *testing.T) {
	for _, tt := range []struct {
		name  string
		edits []yamlx.Patch
	}{
		{"negative", []yamlx.Patch{{Start: -1, End: 0}}},
		{"reversed", []yamlx.Patch{{Start: 2, End: 1}}},
		{"past end", []yamlx.Patch{{Start: 4, End: 4}}},
		{"overlap", []yamlx.Patch{{Start: 0, End: 2}, {Start: 1, End: 3}}},
		{"insertion inside deletion", []yamlx.Patch{{Start: 0, End: 3}, {Start: 1, End: 1}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplyEdits([]byte("abc"), tt.edits)
			if err == nil || got != nil {
				t.Fatalf("invalid plan returned %q, %v", got, err)
			}
		})
	}
}

func TestApplyEditsDoesNotMutatePlan(t *testing.T) {
	replacement := make([]byte, 1, 16)
	replacement[0] = 'X'
	backing := replacement[:cap(replacement)]
	original := []byte("abc")
	edits := []yamlx.Patch{
		{Start: 1, End: 1, Replacement: replacement},
		{Start: 1, End: 1, Replacement: []byte("Y")},
	}
	before := bytes.Clone(backing)
	for range 2 {
		got, err := ApplyEdits(original, edits)
		if err != nil || string(got) != "aXYbc" {
			t.Fatalf("ApplyEdits = %q, %v", got, err)
		}
		got[0] = '!'
	}
	if string(original) != "abc" || !bytes.Equal(backing, before) {
		t.Fatal("input or replacement backing array mutated")
	}
}

// FuzzApplyEdits checks the byte-splicing boundary, independently of the future
// rewrite planner: invalid ranges fail closed and untouched bytes stay exact.
func FuzzApplyEdits(f *testing.F) {
	f.Add([]byte("steps:\n- task: CopyFiles@1\n"), []byte("CopyFiles@2"), 15, 26)
	f.Add([]byte("# café\r\nsteps: []"), []byte("\r\n"), -1, 0)
	f.Fuzz(func(t *testing.T, original, replacement []byte, start, end int) {
		plan := []yamlx.Patch{{Start: start, End: end, Replacement: replacement}}
		got, err := ApplyEdits(original, plan)
		valid := start >= 0 && end >= start && end <= len(original)
		if (err == nil) != valid {
			t.Fatalf("range validation: start=%d end=%d len=%d error=%v", start, end, len(original), err)
		}
		if !valid {
			if got != nil {
				t.Fatal("invalid range returned output")
			}
			return
		}
		want := append(bytes.Clone(original[:start]), replacement...)
		want = append(want, original[end:]...)
		if !bytes.Equal(want, got) {
			t.Fatal("bytes outside the edit changed")
		}
		again, err := ApplyEdits(original, plan)
		if err != nil || !bytes.Equal(got, again) {
			t.Fatal("nondeterministic result")
		}
	})
}
