package yamlx

import (
	"bytes"
	"strings"
	"testing"
	"testing/quick"
	"unicode/utf8"
)

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
