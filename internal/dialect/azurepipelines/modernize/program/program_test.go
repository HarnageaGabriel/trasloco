package program

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type specProgram struct {
	task, class, source string
}

func specPrograms(t testing.TB) []specProgram {
	t.Helper()
	data, err := os.ReadFile("../../../../../docs/spec/modernize-ado.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(data), "## 8. The 73 retired task versions")
	if !found {
		t.Fatal("section 8 heading not found")
	}
	section = strings.SplitN(section, "## 9. ", 2)[0]
	// The mapping is the first backtick-delimited program in the fifth cell.
	// Do not split on pipes: strings in programs and evidence may contain them.
	row := regexp.MustCompile("(?m)^\\| [0-9]+ \\| `([^`]+)`[^\\n]*? \\| ([ABCD]) \\| `([^`]+)`")
	matches := row.FindAllStringSubmatch(section, -1)
	if len(matches) != 73 {
		t.Fatalf("section 8 has %d programs, want 73", len(matches))
	}
	result := make([]specProgram, 0, len(matches))
	for _, match := range matches {
		result = append(result, specProgram{task: match[1], class: match[2], source: match[3]})
	}
	return result
}

func TestProgramVectors(t *testing.T) {
	for _, vector := range specPrograms(t) {
		t.Run(vector.task, func(t *testing.T) {
			got, err := Parse(vector.source, vector.class)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind == "operations" && len(got.Operations) == 0 {
				t.Fatal("empty operations")
			}
			for _, other := range []string{"A", "B", "C", "D", "", "b"} {
				if other == vector.class {
					continue
				}
				if _, err := Parse(vector.source, other); err == nil {
					t.Fatalf("accepted mismatched class %q", other)
				}
			}
		})
	}
}

func TestProgramAST(t *testing.T) {
	source := `rename old -> new; map mode { "a; b" -> "c, d", "\u0065" -> "f\n" }; pin enabled = false if mode != "x" unless other = "y"; review auth default = null; guard multiline(key); review path type "filePath" -> "string"`
	want := Program{Kind: "operations", Operations: []Operation{
		{Kind: "rename", Input: "old", Target: "new"},
		{Kind: "map", Input: "mode", Pairs: []Pair{{From: "a; b", To: "c, d"}, {From: "e", To: "f\n"}}},
		{Kind: "pin", Input: "enabled", Value: Value{Kind: "boolean", Text: "false"}, If: &Condition{Input: "mode", Op: "!=", Value: "x"}, Unless: &Condition{Input: "other", Op: "=", Value: "y"}},
		{Kind: "review", Input: "auth", Mode: "default", Value: Value{Kind: "null", Text: "null"}},
		{Kind: "guard", If: &Condition{Input: "key", Op: "multiline"}},
		{Kind: "review", Input: "path", Mode: "type", From: "filePath", To: "string"},
	}}
	got, err := Parse(source, "B")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestInvalidPrograms(t *testing.T) {
	for _, source := range []string{
		"", " pass", "pass ", "literal", "literal ", "literal x;", "literal x; ",
		"literal  x", "literal x ; literal y", "literal x;literal y", "literal x\n",
		"rename x ->", "rename x => y", "rename x -> _y", "rename x -> é",
		`map x { }`, `map x { "a" -> "b", }`, `map x { "a" -> "\q" }`,
		`map x { "a" -> "b"}`, `pin x = 1.5`, `pin x = -`, `pin x = +1`,
		`pin x = null`, `add x = null`, `add x = true if y = "z"`,
		`pin x = false unless y = "z" if a = "b"`, `guard x == "y"`,
		`guard multiline( x)`, `guard multiline(x )`, `review x bogus = true`,
		`review x type "string"-> "boolean"`, `unknown x`, "literal x\t", "literal x\x00",
		"review x default = \"line\nbreak\"", "review x default = \"\xff\"",
	} {
		t.Run(source, func(t *testing.T) {
			if _, err := Parse(source, "B"); err == nil {
				t.Fatal("invalid program accepted")
			}
		})
	}
}

func FuzzProgram(f *testing.F) {
	for _, vector := range specPrograms(f) {
		f.Add(vector.source, vector.class)
	}
	f.Fuzz(func(t *testing.T, source, class string) {
		first, err := Parse(source, class)
		second, again := Parse(source, class)
		if (err == nil) != (again == nil) {
			t.Fatal("nondeterministic error")
		}
		if diff := cmp.Diff(first, second); diff != "" {
			t.Fatal(diff)
		}
	})
}
