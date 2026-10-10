package resolve

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/corpus"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/printer"
	"github.com/HarnageaGabriel/trasloco/internal/source/localfs"
	"github.com/google/go-cmp/cmp"
)

// addCorpusSeeds seeds a fuzz target with up to n positive corpus fixtures.
func addCorpusSeeds(f *testing.F, n int) {
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	index, err := corpus.Load(os.DirFS(root))
	if err != nil {
		f.Fatal(err)
	}
	for _, entry := range index.Files {
		if n == 0 {
			return
		}
		if !entry.Expect.OK || strings.HasPrefix(entry.Path, "_repos/") {
			continue
		}
		data, err := fs.ReadFile(os.DirFS(root), entry.Path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(string(data), "steps:\n- script: body\n")
		n--
	}
}

// Blocking item 1: a conditional key inside a pointer-to-struct mapping
// (job container:) must merge into the sibling keys, not replace them.
func TestConditionalKeyInContainerKeepsSiblings(t *testing.T) {
	tests := []struct{ name, body string }{
		{"if-after", "    image: ubuntu\n    env:\n      A: b\n    ${{ if true }}:\n      options: --x\n"},
		{"if-before", "    ${{ if true }}:\n      options: --x\n    image: ubuntu\n    env:\n      A: b\n"},
		{"elseif", "    image: ubuntu\n    env:\n      A: b\n    ${{ if false }}:\n      ports: [1]\n    ${{ elseif true }}:\n      options: --x\n"},
		{"else", "    ${{ if false }}:\n      ports: [1]\n    ${{ else }}:\n      options: --x\n    image: ubuntu\n    env:\n      A: b\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := "jobs:\n- job: J\n  container:\n" + test.body + "  steps:\n  - script: a\n"
			_, collector, printed := resolveText(t, input, nil, Options{})
			assertNoErrors(t, collector)
			for _, want := range []string{"image: ubuntu", "A: b", "options: --x"} {
				if !strings.Contains(printed, want) {
					t.Fatalf("%q lost:\n%s", want, printed)
				}
			}
		})
	}
}

// Cheap item 2: runtime and macro forms after expansion are not booleans to reject.
func TestExpandBoolToleratesRuntimeForms(t *testing.T) {
	for _, value := range []string{
		"$[ eq('${{ parameters.v }}','True') ]",
		"$(flag)",
	} {
		input := "parameters:\n- name: v\n  type: string\n  default: x\nsteps:\n- script: a\n  continueOnError: " + value + "\n"
		_, collector, printed := resolveText(t, input, nil, Options{})
		assertNoErrors(t, collector)
		if !strings.Contains(printed, "continueOnError") {
			t.Fatalf("%s: continueOnError lost:\n%s", value, printed)
		}
	}
	input := "parameters:\n- name: v\n  type: string\n  default: maybe\nsteps:\n- script: a\n  continueOnError: ${{ parameters.v }}\n"
	if resolved, collector, _ := resolveText(t, input, nil, Options{}); resolved != nil || !hasCode(collector, "TRL-PARSE-0012") {
		t.Fatalf("plain non-boolean must still fail: %v", codes(collector))
	}
}

// Cheap item 3: expansion stops after maxErrorDiagnostics error diagnostics.
func TestExpansionStopsAfterManyErrors(t *testing.T) {
	var b strings.Builder
	b.WriteString("steps:\n")
	for i := 0; i < 5000; i++ {
		b.WriteString("- script: ${{ nosuchfunction() }}\n")
	}
	collector := diag.NewCollector(diag.WithPerCodeCap(0))
	root := parse.Source("root.yml", []byte(b.String()), collector)
	resolved, state, err := resolveWith(context.Background(), root, nil, Options{Collector: collector})
	if err != nil || resolved != nil {
		t.Fatalf("resolved=%v err=%v", resolved != nil, err)
	}
	if got := len(errorCodes(collector)); got > maxErrorDiagnostics+10 || got < maxErrorDiagnostics {
		t.Fatalf("error diagnostics = %d, want about %d", got, maxErrorDiagnostics)
	}
	if state.errorCount < maxErrorDiagnostics {
		t.Fatalf("errorCount = %d", state.errorCount)
	}
}

func TestManyErroringDirectivesTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("informational timing")
	}
	var b strings.Builder
	b.WriteString("steps:\n")
	for i := 0; i < 50000; i++ {
		b.WriteString("- script: ${{ nosuchfunction() }}\n")
	}
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte(b.String()), collector)
	resolved, state, err := resolveWith(context.Background(), root, nil, Options{Collector: collector})
	if resolved != nil || err != nil || state.errorCount != maxErrorDiagnostics {
		t.Fatalf("errors counted %d, resolved=%v err=%v", state.errorCount, resolved != nil, err)
	}
}

func TestBudgetUnitsVersusHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("informational measurement")
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte(nestedEachPipeline(150, 2)), collector)
	resolved, state, err := resolveWith(context.Background(), root, nil, Options{Collector: collector})
	runtime.ReadMemStats(&after)
	t.Logf("budget units %d, nodes %d, heap delta %d bytes, ok=%v err=%v", state.bytes, state.nodes, int64(after.HeapAlloc)-int64(before.HeapAlloc), resolved != nil, err)
	runtime.KeepAlive(resolved)
}

// Cheap item 4: surviving mutants.
func TestValueToAnyChargesText(t *testing.T) {
	big := strings.Repeat("x", 5000)
	input := "parameters:\n- name: p\n  type: object\n  default:\n    k: " + big + "\nsteps:\n- script: a\n  ${{ insert }}: ${{ parameters.p }}\n"
	_, state, err := resolveBudget(t, input, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, base, err := resolveBudget(t, strings.Replace(input, big, "x", 1), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.bytes-base.bytes < len(big)-100 {
		t.Fatalf("inserted text not charged: %d vs %d", state.bytes, base.bytes)
	}
}

func TestDuplicateTemplateParameter(t *testing.T) {
	source := memorySource{"@t.yml": "parameters:\n- name: a\n  type: string\n- name: A\n  type: string\nsteps:\n- script: x\n"}
	collector := diag.NewCollector()
	root := parse.Source("root.yml", []byte("steps:\n- template: t.yml\n  parameters:\n    a: v\n"), collector)
	resolved, err := Resolve(context.Background(), root, source, Options{Collector: collector})
	if err != nil || resolved != nil || !hasCode(collector, "TRL-VAL-0008") {
		t.Fatalf("resolved=%v err=%v codes=%v", resolved != nil, err, codes(collector))
	}
}

func TestInterpolateChargesDeltaNotCumulative(t *testing.T) {
	embeds := strings.Repeat("${{ parameters.s }}", 40)
	input := "parameters:\n- name: s\n  type: string\n  default: " + strings.Repeat("y", 200) + "\nsteps:\n- script: " + embeds + "\n"
	// 40*200 = 8000 bytes of text; a cumulative charge would cost ~164000.
	_, _, err := resolveBudget(t, input, nil, Options{MaxExpandedBytes: 40000})
	if err != nil {
		t.Fatalf("delta charge exceeded budget: %v", err)
	}
	_, _, err = resolveBudget(t, input, nil, Options{MaxExpandedBytes: 4000})
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("err = %v, want ErrExpansionLimit", err)
	}
}

// Materialized data containing "${{" must stay literal through the re-parse
// that stepList insertion performs.
func TestMaterializedDataStaysLiteralThroughReparse(t *testing.T) {
	source := memorySource{"@t.yml": "parameters:\n- name: s\n  type: stepList\nsteps:\n- ${{ parameters.s }}\n"}
	input := "steps:\n- template: t.yml\n  parameters:\n    s:\n    - script: echo ${{ format('{0}{1}{1} 1 {2}{2}', '$', '{', '}') }}\n"
	_, collector, printed := resolveText(t, input, source, Options{})
	assertNoErrors(t, collector)
	if !strings.Contains(printed, "echo ${{ 1 }}") {
		t.Fatalf("literal directive text was evaluated or lost:\n%s", printed)
	}
}

// ---------------------------------------------------------------------------
// Differential wrapping over the corpus.

type outcome struct {
	parsed  bool
	ok      bool
	printed string
	codes   string
}

func resolveOutcome(name, text string, source Source) outcome {
	collector := diag.NewCollector()
	root := parse.Source(name, []byte(text), collector)
	if root == nil {
		return outcome{}
	}
	resolved, err := Resolve(context.Background(), root, source, Options{Collector: collector})
	out := outcome{parsed: true, codes: strings.Join(errorCodes(collector), ",")}
	if err != nil || resolved == nil {
		return out
	}
	printed, perr := printer.Print(resolved)
	if perr != nil {
		return out
	}
	out.ok, out.printed = true, string(printed)
	return out
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

// keyLines returns, per line, whether it starts a plain "key:" mapping key
// outside block scalars, and the column of that key (after an optional "- ").
func keyLines(lines []string) map[int]int {
	keys := map[int]int{}
	blockIndent := -1
	inParameters := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if blockIndent >= 0 {
			if trimmed == "" || indentOf(line) > blockIndent {
				continue
			}
			blockIndent = -1
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		col := indentOf(line)
		if col == 0 && !strings.HasPrefix(trimmed, "- ") {
			inParameters = strings.HasPrefix(trimmed, "parameters:")
		}
		if inParameters {
			continue // conditional parameter definitions are not allowed
		}
		body := trimmed
		if strings.HasPrefix(body, "- ") {
			col += 2
			body = strings.TrimLeft(body[2:], " ")
		}
		if strings.HasSuffix(body, "|") || strings.HasSuffix(body, ">") || strings.HasSuffix(body, "|-") || strings.HasSuffix(body, ">-") || strings.HasSuffix(body, "|+") || strings.HasSuffix(body, ">+") {
			blockIndent = indentOf(line)
		}
		idx := strings.Index(body, ":")
		if idx <= 0 || strings.ContainsAny(body[:idx], " ${[{\"'") || (idx+1 < len(body) && body[idx+1] != ' ') {
			continue
		}
		keys[i] = col
	}
	return keys
}

// wrap builds the true/false variants for the key on line i. It returns false
// when the transformation does not apply.
func wrap(lines []string, i, col int) (withIf, without string, ok bool) {
	end := i + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) > col) {
		end++
	}
	block := lines[i:end]
	first := strings.HasPrefix(strings.TrimSpace(lines[i]), "- ")
	pad := strings.Repeat(" ", col)
	var wrapped, reduced []string
	wrapped = append(wrapped, lines[:i]...)
	reduced = append(reduced, lines[:i]...)
	if first {
		dash := strings.Repeat(" ", col-2)
		wrapped = append(wrapped, dash+"- ${{ if true }}:")
		wrapped = append(wrapped, pad+"  "+strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(block[0]), "- ")))
		for _, l := range block[1:] {
			wrapped = append(wrapped, "  "+l)
		}
		// Removing the first key promotes the next sibling to the item head.
		if end >= len(lines) || indentOf(lines[end]) != col || strings.HasPrefix(strings.TrimSpace(lines[end]), "- ") {
			return "", "", false
		}
		reduced = append(reduced, dash+"- "+strings.TrimSpace(lines[end]))
		end++
	} else {
		wrapped = append(wrapped, pad+"${{ if true }}:")
		for _, l := range block {
			wrapped = append(wrapped, "  "+l)
		}
	}
	wrapped = append(wrapped, lines[end:]...)
	reduced = append(reduced, lines[end:]...)
	if first {
		// the wrapped tail must not re-emit the promoted sibling twice
		wrapped = append(append([]string{}, wrapped[:len(wrapped)-len(lines[end:])]...), lines[end-1:]...)
	}
	// Keep the final newline so block-scalar chomping does not change.
	return strings.TrimRight(strings.Join(wrapped, "\n"), "\n") + "\n", strings.TrimRight(strings.Join(reduced, "\n"), "\n") + "\n", true
}

func onlyChild(lines []string, i, col int) bool {
	if strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") {
		return false
	}
	prev := i - 1
	for prev >= 0 && strings.TrimSpace(lines[prev]) == "" {
		prev--
	}
	if prev >= 0 && indentOf(lines[prev]) >= col {
		return false
	}
	next := i + 1
	for next < len(lines) && (strings.TrimSpace(lines[next]) == "" || indentOf(lines[next]) > col) {
		next++
	}
	return next >= len(lines) || indentOf(lines[next]) < col
}

func TestDifferentialKeyWrapping(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "corpus")
	index, err := corpus.Load(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	source, err := localfs.New(map[string]string{"": root})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, divergences := 0, 0
	for _, entry := range index.Files {
		data, err := fs.ReadFile(os.DirFS(root), entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		// The parser drops ${{ if }} inside repository trigger: (documented
		// parser follow-up), so those keys cannot be wrapped.
		if !entry.Expect.OK || entry.Path == "resources/repositories-trigger.yml" {
			continue
		}
		original := resolveOutcome(entry.Path, strings.TrimRight(string(data), "\n")+"\n", source)
		if !original.ok {
			continue
		}
		lines := strings.Split(string(data), "\n")
		keys := keyLines(lines)
		for i := 0; i < len(lines); i++ {
			col, isKey := keys[i]
			if !isKey {
				continue
			}
			withIf, without, ok := wrap(lines, i, col)
			if !ok {
				continue
			}
			gotTrue := resolveOutcome(entry.Path, withIf, source)
			// An error diagnostic means the position does not allow a
			// conditional (parameters:, resources:, ...); a silent change in
			// the output is the defect this test hunts.
			if !gotTrue.parsed || (!gotTrue.ok && gotTrue.codes != "") {
				continue
			}
			wrapped++
			if gotTrue.ok != original.ok || gotTrue.printed != original.printed {
				divergences++
				t.Errorf("%s line %d: if true diverges from original (codes %q)\n%s\n%s", entry.Path, i+1, gotTrue.codes, strings.TrimSpace(lines[i]), cmp.Diff(original.printed, gotTrue.printed))
			}
			falseText := strings.Replace(withIf, "${{ if true }}:", "${{ if false }}:", 1)
			gotFalse := resolveOutcome(entry.Path, falseText, source)
			wantFalse := resolveOutcome(entry.Path, without, source)
			// Removing the only key of a mapping leaves a null value in the
			// text, while "if false" leaves an empty mapping: not comparable.
			if onlyChild(lines, i, col) {
				continue
			}
			if gotFalse.parsed && wantFalse.parsed && (wantFalse.ok || gotFalse.ok) && (gotFalse.ok != wantFalse.ok || gotFalse.printed != wantFalse.printed) {
				divergences++
				t.Errorf("%s line %d: if false diverges from original minus key (codes %q vs %q)\n%s\n%s", entry.Path, i+1, gotFalse.codes, wantFalse.codes, strings.TrimSpace(lines[i]), cmp.Diff(wantFalse.printed, gotFalse.printed))
			}
		}
	}
	t.Logf("wrapped %d keys, %d divergences", wrapped, divergences)
	if wrapped < 100 {
		t.Fatalf("only %d keys wrapped; the corpus walk is broken", wrapped)
	}
}
