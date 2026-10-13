package modernize

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize/program"
	adoparse "github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// Rewrite plans and checks byte splices for one file. It performs no I/O and
// never mutates src or cat. Parser errors leave the whole file unchanged;
// mapping failures leave only the affected step unchanged.
func Rewrite(ctx context.Context, src []byte, path string, cat *catalog.Catalog) ([]byte, []diag.Diagnostic, bool, error) {
	out, ds, _, err := rewrite(ctx, src, path, cat)
	return out, ds, !bytes.Equal(src, out), err
}

func parseRewrite(path string, src []byte) (*yamlx.Document, []diag.Diagnostic) {
	doc, ds := yamlx.ParseWithOptions(path, src, yamlx.WithAnchorPolicy(yamlx.AnchorError))
	if doc == nil || hasError(ds) {
		return doc, ds
	}
	c := diag.NewCollector()
	for _, d := range ds {
		c.Add(d)
	}
	_ = adoparse.Document(doc, c)
	return doc, c.List()
}

func rewrite(ctx context.Context, src []byte, path string, cat *catalog.Catalog) ([]byte, []diag.Diagnostic, []yamlx.Patch, error) {
	if err := ctx.Err(); err != nil {
		return src, nil, nil, err
	}
	doc, ds := parseRewrite(path, src)
	if doc == nil || hasError(ds) {
		return src, ds, nil, nil
	}
	// The structure reader may normalize malformed input. Never use its
	// coordinates for splices unless they still refer to the source buffer.
	if !validRewriteRanges(doc, len(src)) {
		return rewriteSelfCheckFailure(src, path)
	}
	idx := newIndex(cat)
	var edits []yamlx.Patch
	steps := 0
	var planErr error
	ending := dominantEnding(src)
	idx.onStep = func(step, task *yamlx.Node) {
		steps++
		raw := strings.TrimSpace(task.Value())
		name, major, full, ok := splitReference(raw)
		if !ok || dynamic(raw) || strings.Contains(name, ".") {
			return
		}
		row, state := idx.resolve(name, major)
		if state != retiredOnce {
			return
		}
		p, err := program.Parse(row.Program, row.Class)
		if err != nil {
			planErr = err
			return
		}
		switch row.Class {
		case "C":
			ds = append(ds, rewriteDiagnostic("TRL-MOD-0003", task, map[string]any{"from": row.Task, "replacements": row.Replacement, "reason": p.Message}))
		case "D":
			ds = append(ds, rewriteDiagnostic("TRL-MOD-0004", task, map[string]any{"from": row.Task, "guidance": p.Message}))
		default:
			s := stepPlan{src: src, step: step, task: task, row: row, program: p, cat: cat, eol: ending}
			patches, diagnostics, err := s.plan(raw, name, full)
			if err != nil {
				planErr = err
				return
			}
			edits = append(edits, patches...)
			ds = append(ds, diagnostics...)
		}
	}
	for _, root := range doc.Documents {
		walk(root, walkContext{}, func(f Finding) {
			if f.Code != "TRL-MOD-0005" && f.Code != "TRL-MOD-0010" {
				return
			}
			ds = append(ds, diag.MustNew(f.Code, diag.WithSource(diag.Source{File: path, Line: f.Line, Col: f.Column}), diag.WithParams(map[string]any{"value": f.RetiredTask})))
		}, idx)
	}
	if err := ctx.Err(); err != nil {
		return src, nil, nil, err
	}
	if planErr != nil {
		return src, nil, nil, planErr
	}
	sortRewriteDiagnostics(ds)
	if len(edits) == 0 {
		return src, ds, nil, nil
	}
	out, err := ApplyEdits(src, edits)
	if err == nil {
		err = selfCheck(path, src, out, doc, edits, steps)
	}
	if err != nil {
		return rewriteSelfCheckFailure(src, path)
	}
	remapDiagnostics(ds, src, out, edits)
	return out, ds, edits, nil
}

func rewriteSelfCheckFailure(src []byte, path string) ([]byte, []diag.Diagnostic, []yamlx.Patch, error) {
	return src, []diag.Diagnostic{selfCheckDiagnostic(path)}, nil, nil
}

func selfCheckDiagnostic(path string) diag.Diagnostic {
	return diag.MustNew("TRL-INT-0002", diag.WithSource(diag.Source{File: path}), diag.WithMessage("Modernization self-check failed"))
}

func validRewriteRanges(doc *yamlx.Document, length int) bool {
	valid := true
	visitNodes(doc.Documents, func(n *yamlx.Node) {
		r, err := n.ByteRange()
		if err != nil || r.Start < 0 || r.End < r.Start || r.End > length {
			valid = false
		}
	})
	return valid
}

func rewriteDiagnostic(code string, node *yamlx.Node, params map[string]any) diag.Diagnostic {
	return diag.MustNew(code, diag.WithSource(node.Position()), diag.WithParams(params))
}

func sortRewriteDiagnostics(ds []diag.Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Source.Line != b.Source.Line {
			return a.Source.Line < b.Source.Line
		}
		if a.Source.Col != b.Source.Col {
			return a.Source.Col < b.Source.Col
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return fmt.Sprint(a.Params["reason"]) < fmt.Sprint(b.Params["reason"])
	})
}

func dynamic(s string) bool {
	return strings.Contains(s, "$(") || strings.Contains(s, "${{") || strings.Contains(s, "$[")
}

// selfCheck independently verifies untouched bytes and untouched node semantics.
// Parent ranges intersecting edits are excluded, but every unaffected child is
// compared at its shifted byte coordinates, including keys and script steps.
func selfCheck(path string, src, out []byte, before *yamlx.Document, edits []yamlx.Patch, steps int) error {
	after, ds := parseRewrite(path, out)
	if after == nil || hasError(ds) || !validRewriteRanges(after, len(out)) {
		return fmt.Errorf("invalid rewritten document")
	}
	count := 0
	idx := newIndex(nil)
	idx.onStep = func(_, _ *yamlx.Node) { count++ }
	for _, root := range after.Documents {
		walk(root, walkContext{}, func(Finding) {}, idx)
	}
	if count != steps {
		return fmt.Errorf("step count changed")
	}
	ordered := append([]yamlx.Patch(nil), edits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	oldPos, newPos := 0, 0
	for _, e := range ordered {
		if e.Start < oldPos || e.End < e.Start || e.End > len(src) {
			return fmt.Errorf("invalid edit range")
		}
		n := e.Start - oldPos
		if newPos+n+len(e.Replacement) > len(out) || !bytes.Equal(src[oldPos:e.Start], out[newPos:newPos+n]) {
			return fmt.Errorf("unplanned bytes changed")
		}
		oldPos = e.End
		newPos += n + len(e.Replacement)
	}
	if !bytes.Equal(src[oldPos:], out[newPos:]) {
		return fmt.Errorf("unplanned suffix changed")
	}
	type identity struct {
		kind       yamlx.Kind
		tag, value string
	}
	nodes := map[yamlx.ByteRange]identity{}
	visitNodes(after.Documents, func(n *yamlx.Node) {
		r, e := n.ByteRange()
		if e == nil {
			nodes[r] = identity{n.Kind(), n.Tag(), n.Value()}
		}
	})
	type originalNode struct {
		node *yamlx.Node
		span yamlx.ByteRange
	}
	var originals []originalNode
	var problem error
	visitNodes(before.Documents, func(n *yamlx.Node) {
		r, e := n.ByteRange()
		if e != nil {
			problem = e
			return
		}
		originals = append(originals, originalNode{n, r})
	})
	sort.Slice(originals, func(i, j int) bool { return originals[i].span.Start < originals[j].span.Start })
	// Non-overlapping edits have monotonic ends. Sweep once across sorted
	// nodes, accumulating offsets; only the next edit can intersect a node.
	delta, next := 0, 0
	for _, original := range originals {
		n, r := original.node, original.span
		for next < len(ordered) && ordered[next].End <= r.Start {
			edit := ordered[next]
			delta += len(edit.Replacement) - (edit.End - edit.Start)
			next++
		}
		if next > 0 && n.Kind() != yamlx.ScalarKind {
			previous := ordered[next-1]
			if previous.Start == previous.End && previous.End == r.End {
				continue
			}
		}
		if next < len(ordered) {
			edit := ordered[next]
			if edit.Start < r.End || n.Kind() != yamlx.ScalarKind && appendsToEntry(src, r.End, edit) {
				continue
			}
		}
		got, ok := nodes[yamlx.ByteRange{Start: r.Start + delta, End: r.End + delta}]
		if !ok || got != (identity{n.Kind(), n.Tag(), n.Value()}) {
			problem = fmt.Errorf("unplanned node changed at %d:%d", n.Line(), n.Column())
		}
	}
	return problem
}

// appendsToEntry reports whether e is a pure insertion at the end of a
// collection ending at end, either directly or after the rest of that last
// line including its terminator. Such an insertion extends the collection.
func appendsToEntry(src []byte, end int, e yamlx.Patch) bool {
	if e.Start != e.End || e.Start < end || e.Start > len(src) {
		return false
	}
	tail := src[end:e.Start]
	i := bytes.IndexByte(tail, '\n')
	return i < 0 || i == len(tail)-1
}

func visitNodes(roots []*yamlx.Node, visit func(*yamlx.Node)) {
	for _, n := range roots {
		if n == nil {
			continue
		}
		visit(n)
		switch n.Kind() {
		case yamlx.MappingKind:
			pairs, _ := n.Map()
			for _, p := range pairs {
				visitNodes([]*yamlx.Node{p.Key, p.Value}, visit)
			}
		case yamlx.SequenceKind:
			items, _ := n.Seq()
			visitNodes(items, visit)
		}
	}
}
