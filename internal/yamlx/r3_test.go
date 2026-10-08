package yamlx

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"testing/quick"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

func TestValueRangeRoundTrip(t *testing.T) {
	tests := []struct {
		name, input, key, raw string
	}{
		{"plain multiline after Unicode", "é😀: first\n  second\nkeep: yes\n", "é😀", "first\n  second"},
		{"flow multiline", "a: [x,\n  {target: y}, z]\nkeep: yes\n", "target", "y"},
		{"nested flow", "a: [{inner: [x,\n  {target: long\n    plain}]}]\nkeep: yes\n", "target", "long\n    plain"},
		{"quoted in flow", "a: [\"]\", {target: 'it''s'}, z]\nkeep: yes\n", "target", "'it''s'"},
		{"literal trailing blanks", "target: |+\n  one\n\n\nkeep: yes\n", "target", "|+\n  one\n\n\n"},
		{"folded CRLF", "😀: before\r\ntarget: >-\r\n  one\r\n  two\r\nkeep: yes\r\n", "target", ">-\r\n  one\r\n  two\r\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, diagnostics := Parse("range.yaml", []byte(test.input))
			if len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			pair := findPair(t, document.Root(), test.key)
			span, err := pair.ValueRange()
			if err != nil {
				t.Fatal(err)
			}
			if got := test.input[span.Start:span.End]; got != test.raw {
				t.Fatalf("ValueRange text = %q, want %q", got, test.raw)
			}

			before, selected := scalarValues(document, span.Start)
			replacement := []byte("yamlx_sentinel")
			if pair.Value.raw.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				if strings.Contains(test.raw, "\r\n") {
					replacement = append(replacement, '\r', '\n')
				} else {
					replacement = append(replacement, '\n')
				}
			}
			patched, err := document.ApplyPatches([]Patch{{Start: span.Start, End: span.End, Replacement: replacement}})
			if err != nil {
				t.Fatal(err)
			}
			assertOnlyScalarChanged(t, patched, before, selected, "yamlx_sentinel")
		})
	}
}

func TestPatchRangeValidation(t *testing.T) {
	var missing *Document
	if _, err := missing.ApplyPatches(nil); err == nil {
		t.Fatal("nil document accepted patches")
	}
	var missingNode *Node
	if _, err := missingNode.ByteRange(); err == nil {
		t.Fatal("nil node returned a range")
	}
	for _, patches := range [][]Patch{
		{{Start: -1, End: 0}},
		{{Start: 2, End: 1}},
		{{Start: 0, End: 4}},
		{{Start: 1, End: 1}, {Start: 1, End: 1}},
	} {
		if _, err := ApplyPatches([]byte("abc"), patches); err == nil {
			t.Fatalf("invalid patches accepted: %+v", patches)
		}
	}
}

func FuzzScalarValueRanges(f *testing.F) {
	for _, seed := range []struct {
		value string
		style uint8
	}{
		{"alpha beta", 0},
		{"é😀", 1},
		{"it's", 2},
		{"one two", 3},
		{"red blue", 4},
		{"long plain", 5},
	} {
		f.Add(seed.value, seed.style)
	}
	f.Fuzz(func(t *testing.T, fuzzValue string, style uint8) {
		word := fuzzWord(fuzzValue)
		var encoded string
		switch style % 6 {
		case 0:
			encoded = word
		case 1:
			encoded = "'" + strings.ReplaceAll(word, "'", "''") + "'"
		case 2:
			encoded = fmt.Sprintf("%q", word)
		case 3:
			encoded = "|-\n  " + word
		case 4:
			encoded = ">-\n  " + word + "\n  tail"
		default:
			encoded = word + "\n    tail"
		}
		input := []byte("before: é😀\ntarget: " + encoded + "\nkeep: unchanged\n")
		document, diagnostics := Parse("fuzz-range.yaml", input)
		if len(diagnostics) != 0 {
			t.Skip()
		}
		assertScalarRangeProperty(t, document)
	})
}

func assertScalarRangeProperty(t *testing.T, document *Document) {
	t.Helper()
	var nodes []*Node
	var visit func(*Node)
	visit = func(node *Node) {
		if node.raw.Kind == yaml.ScalarNode {
			nodes = append(nodes, node)
		}
		for _, child := range node.raw.Content {
			visit(wrap(child, node.ctx))
		}
	}
	visit(document.Root())
	for _, node := range nodes {
		span, err := node.ByteRange()
		if err != nil {
			t.Fatal(err)
		}
		raw := document.ctx.original[span.Start:span.End]
		probe, diagnostics := Parse("probe.yaml", append([]byte("probe: "), raw...))
		if len(diagnostics) != 0 || lookup(t, probe.Root(), "probe").Value() != node.Value() {
			t.Fatalf("range %q does not decode to %q: %v", raw, node.Value(), diagnostics)
		}

		before, selected := scalarValues(document, span.Start)
		replacement := []byte("yamlx_sentinel")
		if node.raw.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			replacement = append(replacement, '\n')
		}
		patched, err := document.ApplyPatches([]Patch{{Start: span.Start, End: span.End, Replacement: replacement}})
		if err != nil {
			t.Fatal(err)
		}
		assertOnlyScalarChanged(t, patched, before, selected, "yamlx_sentinel")
	}
}

func fuzzWord(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			char >= utf8.RuneSelf && unicode.IsGraphic(char) && !unicode.IsSpace(char) {
			builder.WriteRune(char)
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "value"
	}
	return builder.String()
}

func findPair(t *testing.T, node *Node, key string) Pair {
	t.Helper()
	if node.raw.Kind == yaml.MappingNode {
		pairs, diagnostics := node.Map()
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		for _, pair := range pairs {
			if pair.Key.Value() == key {
				return pair
			}
			if pair.Value.raw.Kind == yaml.MappingNode || pair.Value.raw.Kind == yaml.SequenceNode {
				if found, ok := findPairOptional(pair.Value, key); ok {
					return found
				}
			}
		}
	} else if found, ok := findPairOptional(node, key); ok {
		return found
	}
	t.Fatalf("pair %q not found", key)
	return Pair{}
}

func findPairOptional(node *Node, key string) (Pair, bool) {
	if node.raw.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(node.raw.Content); index += 2 {
			if node.raw.Content[index].Value == key {
				return Pair{Key: wrap(node.raw.Content[index], node.ctx), Value: wrap(node.raw.Content[index+1], node.ctx)}, true
			}
			if found, ok := findPairOptional(wrap(node.raw.Content[index+1], node.ctx), key); ok {
				return found, true
			}
		}
	}
	if node.raw.Kind == yaml.SequenceNode {
		for _, child := range node.raw.Content {
			if found, ok := findPairOptional(wrap(child, node.ctx), key); ok {
				return found, true
			}
		}
	}
	return Pair{}, false
}

func scalarValues(document *Document, selectedOffset int) ([]string, int) {
	values, selected := []string{}, -1
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode {
			span, err := rawNodeRange(document.ctx, node)
			if err == nil && span.Start == selectedOffset {
				selected = len(values)
			}
			values = append(values, node.Value)
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(document.Root().raw)
	return values, selected
}

func assertOnlyScalarChanged(t *testing.T, patched []byte, before []string, selected int, sentinel string) {
	t.Helper()
	if selected < 0 {
		t.Fatal("selected scalar not found")
	}
	afterDocument, diagnostics := Parse("patched.yaml", patched)
	if len(diagnostics) != 0 {
		t.Fatalf("patched YAML does not parse: %v\n%s", diagnostics, patched)
	}
	after, _ := scalarValues(afterDocument, -1)
	if len(after) != len(before) {
		t.Fatalf("scalar count changed from %d to %d", len(before), len(after))
	}
	for index := range before {
		want := before[index]
		if index == selected {
			want = sentinel
		}
		if after[index] != want {
			t.Fatalf("scalar %d = %q, want %q (selected %d)", index, after[index], want, selected)
		}
	}
}

func TestMultilinePatchRanges(t *testing.T) {
	for _, test := range []struct{ input, target string }{
		{"a: this is a\n  long plain\nb: keep\n", "this is a\n  long plain"},
		{"a: first\n\n  second # keep comment\nb: keep\n", "first\n\n  second"},
		{"a: first # keep comment\n# another comment\nb: keep\n", "first"},
		{"a: [x,\n  y]\nb: keep\n", "y"},
		{"a: [x, # ] } \n  y]\nb: keep\n", "y"},
		{"a: [x,\n  long\n  plain]\nb: keep\n", "long\n  plain"},
		{"a: {x: keep,\n  y: long\n    plain}\nb: keep\n", "long\n    plain"},
		{"a: [{x: keep},\n  y]\nb: keep\n", "y"},
		{"a: ['[',\n  y]\nb: keep\n", "y"},
		{"a: plain [bracket\n  continuation\nb: keep\n", "plain [bracket\n  continuation"},
		{"a: 'bracket ['\nb: plain ] kept\n", "plain ] kept"},
		{"a:\n- key: first\n    second\n  other: keep\n", "first\n    second"},
		{"first\nsecond\n", "first\nsecond"},
		{"# comment\nfirst\nsecond\n", "first\nsecond"},
		{"--- first\nsecond\n...\n", "first\nsecond"},
		{"first\n---second\n", "first\n---second"},
	} {
		for _, crlf := range []bool{false, true} {
			for _, bom := range []bool{false, true} {
				input, target := test.input, test.target
				if crlf {
					input = strings.ReplaceAll(input, "\n", "\r\n")
					target = strings.ReplaceAll(target, "\n", "\r\n")
				}
				if bom {
					input = "\ufeff" + input
				}
				t.Run(input, func(t *testing.T) {
					doc, findings := Parse("patch.yml", []byte(input))
					if len(findings) != 0 {
						t.Fatal(findings)
					}
					start := strings.Index(input, target)
					var selected *Node
					var visit func(*Node)
					visit = func(n *Node) {
						if n.Kind() == ScalarKind && n.Position().Offset == start {
							selected = n
						}
						for _, child := range n.raw.Content {
							visit(wrap(child, n.ctx))
						}
					}
					visit(doc.Root())
					if selected == nil {
						t.Fatal("target node not found")
					}
					span, err := selected.ByteRange()
					if err != nil || input[span.Start:span.End] != target {
						t.Fatalf("range %+v err %v, want %q", span, err, target)
					}
					patched, err := doc.ApplyPatches([]Patch{{Start: span.Start, End: span.End, Replacement: []byte("replacement")}})
					want := input[:start] + "replacement" + input[start+len(target):]
					if err != nil || string(patched) != want {
						t.Fatalf("patch = %q, %v; want %q", patched, err, want)
					}
					if _, findings := Parse("patch.yml", patched); len(findings) != 0 {
						t.Fatal(findings)
					}
				})
			}
		}
	}
}

func TestMultilinePatchProperty(t *testing.T) {
	property := func(a, b uint32, flow bool) bool {
		first, second := strings.Repeat("é", int(a%20)+1), strings.Repeat("😀", int(b%20)+1)
		value := first + "\n  " + second
		prefix, suffix := "a: ", " # comment\nb: keep\n"
		if flow {
			prefix, suffix = "a: [keep,\n  ", "] # comment\nb: keep\n"
		}
		input := []byte(prefix + value + suffix)
		doc, findings := Parse("property.yml", input)
		if len(findings) != 0 {
			return false
		}
		node := lookup(t, doc.Root(), "a")
		if flow {
			items, ds := node.Seq()
			if len(ds) != 0 {
				return false
			}
			node = items[1]
		}
		span, err := node.ByteRange()
		if err != nil || string(input[span.Start:span.End]) != value {
			return false
		}
		patched, err := doc.ApplyPatches([]Patch{{Start: span.Start, End: span.End, Replacement: []byte("changed")}})
		if err != nil || string(patched) != prefix+"changed"+suffix {
			return false
		}
		updated, ds := Parse("property.yml", patched)
		return len(ds) == 0 && lookup(t, updated.Root(), "b").Value() == "keep"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestUnicodeColumns(t *testing.T) {
	for _, scalar := range []string{"é😀é marker", "'é😀é marker'", `"é😀é marker"`, "|-\n  é😀é marker", ">-\n  é😀é marker", "é😀é\n  marker"} {
		input := []byte("\ufeffbefore: ok\r\nkey: " + strings.ReplaceAll(scalar, "\n", "\r\n") + "\r\n")
		doc, ds := Parse("unicode.yml", input)
		if len(ds) != 0 {
			t.Fatal(ds)
		}
		node := lookup(t, doc.Root(), "key")
		if node.Line() != 2 || node.Column() != 6 {
			t.Fatalf("node position = %d:%d, want 2:6", node.Line(), node.Column())
		}
		mapper, err := node.ScalarMapper()
		if err != nil {
			t.Fatal(err)
		}
		value := node.Value()
		at := strings.Index(value, "marker")
		position, err := mapper.MapRange(at, at+5)
		start := bytes.Index(input, []byte("marker"))
		lineStart := bytes.LastIndexByte(input[:start], '\n') + 1
		col := utf8.RuneCount(input[lineStart:start]) + 1
		if err != nil || position.Col != col || position.EndCol != col+5 || position.Offset != start || position.EndOffset != start+5 {
			t.Fatalf("%q: %+v, %v; want col %d offset %d", scalar, position, err, col, start)
		}
		end := node.Position()
		if strings.Contains(scalar, "|-") || strings.Contains(scalar, ">-") {
			continue
		}
		if end.EndCol != col+6+boolInt(strings.HasPrefix(scalar, "'") || strings.HasPrefix(scalar, `"`)) {
			t.Fatalf("end position %+v", end)
		}
	}
	_, ds := Parse("bad.yml", append([]byte("a: é😀"), 0xff))
	if len(ds) == 0 || ds[0].Code != "TRL-PARSE-0004" || ds[0].Source.Col != 6 {
		t.Fatalf("invalid UTF-8 position: %+v", ds)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
