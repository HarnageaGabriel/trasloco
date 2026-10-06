package yamlx

import (
	"bytes"
	"strings"
	"testing"
	"testing/quick"
)

func TestScalarIgnoresTag(t *testing.T) {
	document, diagnostics := Parse("scalar.yaml", []byte("flag: true\ncount: 42\n"))
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	for key, want := range map[string]string{"flag": "true", "count": "42"} {
		got, problems := lookup(t, document.Root(), key).Scalar()
		if len(problems) != 0 || got != want {
			t.Fatalf("Scalar(%q) = %q, %#v; want %q", key, got, problems, want)
		}
	}
}

func TestAnchorPolicies(t *testing.T) {
	input := []byte("base: &base {x: 1}\ncopy: *base\nmerged:\n  <<: *base\n")
	tests := []struct {
		name   string
		policy AnchorPolicy
		codes  []string
	}{
		{"allow", AnchorAllow, nil},
		{"error", AnchorError, []string{"TRL-PARSE-0008", "TRL-PARSE-0008", "TRL-PARSE-0009", "TRL-PARSE-0008"}},
		{"without merge", AnchorAllowWithoutMerge, []string{"TRL-PARSE-0009"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, diagnostics := ParseWithOptions("anchors.yaml", input, WithAnchorPolicy(test.policy))
			codes := make([]string, len(diagnostics))
			for index := range diagnostics {
				codes[index] = diagnostics[index].Code
			}
			if strings.Join(codes, ",") != strings.Join(test.codes, ",") {
				t.Fatalf("codes = %v, want %v", codes, test.codes)
			}
		})
	}
}

func TestDepthLimit(t *testing.T) {
	_, diagnostics := ParseWithOptions("deep.yaml", []byte("a: {b: {c: value}}\n"), WithDepthLimit(3))
	if len(diagnostics) != 1 || diagnostics[0].Code != "TRL-PARSE-0010" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestPatchesProduceMinimalDiff(t *testing.T) {
	input := []byte("# untouched\r\nname: old # keep\r\ncount: 2\r\n")
	document, diagnostics := Parse("modernize.yaml", input)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	pairs, problems := document.Root().Map()
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	rangeToReplace, err := pairs[0].ValueRange()
	if err != nil {
		t.Fatal(err)
	}
	got, err := document.ApplyPatches([]Patch{{
		Start: rangeToReplace.Start, End: rangeToReplace.End, Replacement: []byte("new"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("# untouched\r\nname: new # keep\r\ncount: 2\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("patched bytes = %q, want %q", got, want)
	}
	keyRange, _ := pairs[0].KeyRange()
	entryRange, _ := pairs[0].EntryRange()
	if string(input[keyRange.Start:keyRange.End]) != "name" || string(input[entryRange.Start:entryRange.End]) != "name: old" {
		t.Fatalf("ranges key=%q entry=%q", input[keyRange.Start:keyRange.End], input[entryRange.Start:entryRange.End])
	}
	if _, err := ApplyPatches(input, []Patch{{Start: 1, End: 4}, {Start: 3, End: 5}}); err == nil {
		t.Fatal("overlapping patches accepted")
	}
}

func TestScalarMapperStyles(t *testing.T) {
	input := []byte("plain: alpha beta\nsingle: 'it''s'\ndouble: \"a\\n\\u0042\"\nliteral: |-\n  one\n  two\nfolded: >-\n  red\n  blue\n")
	document, diagnostics := Parse("styles.yaml", input)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	tests := []struct {
		key       string
		decodedAt string
		rawAt     string
	}{
		{"plain", "beta", "beta"},
		{"single", "'", "''"},
		{"double", "\n", "\\n"},
		{"double", "B", "\\u0042"},
		{"literal", "two", "two"},
		{"folded", "blue", "blue"},
	}
	for _, test := range tests {
		t.Run(test.key+test.rawAt, func(t *testing.T) {
			node := lookup(t, document.Root(), test.key)
			value, problems := node.Scalar()
			if len(problems) != 0 {
				t.Fatal(problems)
			}
			decodedOffset := strings.Index(value, test.decodedAt)
			wantOffset := bytes.Index(input, []byte(test.rawAt))
			position, err := MapScalarOffset(node, decodedOffset)
			if err != nil {
				t.Fatal(err)
			}
			if position.Offset != wantOffset {
				t.Fatalf("Map(%d) = %#v, want offset %d", decodedOffset, position, wantOffset)
			}
		})
	}
}

func TestScalarMapperPlainProperty(t *testing.T) {
	property := func(value string) bool {
		value = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' {
				return r
			}
			return -1
		}, value)
		if value == "" {
			value = "x"
		}
		input := []byte("key: " + value + "\n")
		document, diagnostics := Parse("property.yaml", input)
		if len(diagnostics) != 0 {
			return false
		}
		node, diagnostics := document.Root().Lookup("key")
		if len(diagnostics) != 0 || node == nil {
			return false
		}
		mapper, err := NewScalarMapper(node)
		if err != nil {
			return false
		}
		for index := range value {
			position, mapErr := mapper.Map(index)
			if mapErr != nil || position.Offset >= len(input) || input[position.Offset] != value[index] {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}
