// Package modernize detects retired Azure Pipelines task references without
// modifying pipeline source.
package modernize

import (
	"regexp"
	"sort"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	adoparse "github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// HelpBaseURL is the documentation site that explains every retired task. It
// is the only place the site address is written down.
const HelpBaseURL = "https://harnageagabriel.github.io/trasloco/tasks/"

// Severities of a Finding. Warning and error findings are actionable.
const (
	SeverityWarning = "warning"
	SeverityError   = "error"
	SeverityInfo    = "info"
)

// Finding is one retired or indeterminate task reference.
type Finding struct {
	Code         string   `json:"code"`
	Severity     string   `json:"severity"`
	Path         string   `json:"path"`
	Line         int      `json:"line"`
	Column       int      `json:"column"`
	RetiredTask  string   `json:"retiredTask"`
	Class        string   `json:"class,omitempty"`
	Replacements []string `json:"replacements,omitempty"`
	Action       string   `json:"action"`
	SourceURL    string   `json:"sourceUrl,omitempty"`
	Help         string   `json:"help,omitempty"`
	Diagnostics  []string `json:"diagnostics,omitempty"`
	// RewriteNotes say why --write left the step unchanged.
	RewriteNotes []string `json:"rewriteNotes,omitempty"`
}

// Actionable reports whether the finding is something the user has to fix.
// Informational notes (for example extension tasks) are not.
func (f Finding) Actionable() bool { return f.Severity != SeverityInfo }

// HelpURL returns the documentation page of a retired task such as
// "CopyFiles@1", or "" when the reference is not of the form Name@Major.
func HelpURL(task string) string {
	name, major, _, ok := splitReference(task)
	if !ok {
		return ""
	}
	return HelpBaseURL + strings.ToLower(name) + "-" + itoa(major) + "/"
}

// Scan parses one Azure Pipelines file and returns deterministic findings and
// parser diagnostics. The input bytes are never changed.
func Scan(name string, data []byte, cat *catalog.Catalog) ([]Finding, []diag.Diagnostic) {
	collector := diag.NewCollector()
	document, yamlDiagnostics := yamlx.ParseWithOptions(name, data, yamlx.WithAnchorPolicy(yamlx.AnchorError))
	for _, diagnostic := range yamlDiagnostics {
		collector.Add(diagnostic)
	}
	if document == nil || document.Root() == nil {
		return newLineIndex(data).fallbackTaskStrings(name, newIndex(cat)), collector.List()
	}
	_ = adoparse.Document(document, collector)

	index := newIndex(cat)
	var findings []Finding
	for _, root := range document.Documents {
		walk(root, walkContext{}, func(finding Finding) {
			finding.Path = name
			findings = append(findings, finding)
		}, index)
	}
	if len(findings) == 0 && hasError(collector.List()) {
		findings = append(findings, newLineIndex(data).fallbackTaskStrings(name, index)...)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Code < b.Code
	})
	return findings, collector.List()
}

func hasError(diagnostics []diag.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == diag.SeverityError {
			return true
		}
	}
	return false
}

// fallbackTask is the line-based scan used for files the YAML parser rejects.
// Accepted trade-off: without a parse tree it cannot tell a real step from
// text inside a block scalar, so a `task: X@1` line within a script body of a
// broken file is also reported. A false positive on an already-broken file is
// preferred to silently missing a retired task.
var fallbackTask = regexp.MustCompile(`(?im)(?:^|[,{])\s*-?\s*["']?task["']?\s*:\s*["']?([^\s,}\]"']+)`)

// lineIndex resolves byte offsets to one-based line and byte column numbers.
// The line-start table is built once per file, so resolving many offsets costs
// a binary search each instead of a rescan of the file.
type lineIndex struct {
	data []byte
	// starts holds the byte offset at which every line begins.
	starts []int
	// work counts the bytes scanned and the table entries compared while
	// resolving positions. It is a regression counter for tests; it has no
	// effect on the result.
	work int
}

// newLineIndex returns the position index of a file.
func newLineIndex(data []byte) *lineIndex {
	return &lineIndex{data: data}
}

// position returns the one-based line and the one-based byte column of
// offset. An offset before the start of the file is clamped to the start and
// an offset past the end of the file to the end.
func (positions *lineIndex) position(offset int) (int, int) {
	if positions.starts == nil {
		positions.starts = lineStarts(positions.data)
		positions.work += len(positions.data)
	}
	offset = min(max(offset, 0), len(positions.data))
	low, high := 0, len(positions.starts)
	for low < high {
		middle := (low + high) / 2
		positions.work++
		if positions.starts[middle] > offset {
			high = middle
		} else {
			low = middle + 1
		}
	}
	line := low
	return line, offset - positions.starts[line-1] + 1
}

// fallbackTaskStrings conservatively scans parser-rejected documents. These
// values are manual findings because the surrounding step shape is invalid.
func (positions *lineIndex) fallbackTaskStrings(name string, index catalogIndex) []Finding {
	var findings []Finding
	for _, match := range fallbackTask.FindAllSubmatchIndex(positions.data, -1) {
		start, end := match[2], match[3]
		raw := string(positions.data[start:end])
		task, major, _, ok := splitReference(raw)
		if !ok {
			continue
		}
		if _, state := index.resolve(task, major); state == notRetired {
			continue
		}
		line, column := positions.position(start)
		findings = append(findings, Finding{
			Code: "TRL-MOD-0005", Severity: SeverityWarning, Path: name,
			Line: line, Column: column, RetiredTask: raw,
			Action: "Cannot determine the task version; replace the retired task reference by hand.",
		})
	}
	return findings
}

type walkContext struct {
	// tasks: mappings here may be steps.
	tasks bool
	// freeform: the values come from `parameters`, so a mapping is a step
	// only when it passes the key allowlist of the specification (3.1).
	freeform bool
	// stringValues: a scalar naming a retired task is reported (3.2.6).
	stringValues bool
	// taskValue: the scalar is the value of a `task` key.
	taskValue bool
	// stepBody: the mapping is the body of a directive inside a step, so its
	// keys are step properties, not new steps.
	stepBody bool
}

var freeformStepKeys = map[string]bool{
	"task": true, "inputs": true, "displayname": true, "name": true, "condition": true,
	"continueonerror": true, "enabled": true, "env": true, "timeoutinminutes": true,
	"retrycountontaskfailure": true, "target": true,
}

func walk(node *yamlx.Node, context walkContext, emit func(Finding), index catalogIndex) {
	if node == nil {
		return
	}
	switch node.Kind() {
	case yamlx.ScalarKind:
		if context.stringValues && !context.taskValue {
			if finding, ok := index.retiredString(node); ok {
				emit(finding)
			}
		}
	case yamlx.SequenceKind:
		items, _ := node.Seq()
		for _, item := range items {
			walk(item, context, emit, index)
		}
	case yamlx.MappingKind:
		walkMapping(node, context, emit, index)
	}
}

func walkMapping(node *yamlx.Node, context walkContext, emit func(Finding), index catalogIndex) {
	pairs, _ := node.Map()
	isStep := false
	if context.tasks {
		if task := stepTask(pairs, context.freeform); task != nil {
			isStep = true
			if index.onStep != nil {
				index.onStep(node, task)
			}
			if finding, ok := index.reference(task); ok {
				emit(finding)
			}
		} else if context.freeform {
			if task := scalarTask(pairs); task != nil {
				if finding, ok := index.retiredString(task); ok {
					emit(finding)
				}
			}
		} else if !context.stepBody {
			if key := dynamicTaskKey(pairs); key != nil {
				emit(manualFinding(key, strings.TrimSpace(key.Value())))
			}
		}
	}
	for _, pair := range pairs {
		key := strings.TrimSpace(pair.Key.Value())
		next := context
		next.taskValue = false
		switch {
		case isStep:
			// The contents of a step (inputs, displayName, ...) are not
			// task references. Only directive bodies can hold more steps.
			if !isDirective(key) {
				continue
			}
			next = walkContext{tasks: true, freeform: context.freeform, stepBody: true}
		case context.tasks && !context.freeform && !isDirective(key):
			next = walkContext{}
		}
		if !isStep {
			if strings.EqualFold(key, "steps") {
				next.tasks = true
			}
			if strings.EqualFold(key, "parameters") {
				next.tasks, next.freeform, next.stringValues = true, true, true
			}
			if strings.EqualFold(key, "variables") {
				next.stringValues = true
			}
			next.taskValue = strings.EqualFold(key, "task")
		}
		walk(pair.Value, next, emit, index)
	}
}

func isDirective(key string) bool { return strings.HasPrefix(strings.TrimSpace(key), "${{") }

// stepTask returns the scalar `task` value when the mapping is a step.
func stepTask(pairs []yamlx.Pair, freeform bool) *yamlx.Node {
	var task *yamlx.Node
	for _, pair := range pairs {
		key := strings.ToLower(strings.TrimSpace(pair.Key.Value()))
		if freeform && !freeformStepKeys[key] && !isDirective(key) {
			return nil
		}
		if key == "task" && pair.Value.Kind() == yamlx.ScalarKind {
			task = pair.Value
		}
	}
	return task
}

func scalarTask(pairs []yamlx.Pair) *yamlx.Node {
	for _, pair := range pairs {
		if strings.EqualFold(strings.TrimSpace(pair.Key.Value()), "task") && pair.Value.Kind() == yamlx.ScalarKind {
			return pair.Value
		}
	}
	return nil
}

// dynamicTaskKey finds a key produced by an expression (not a control
// directive) whose value is a scalar: the step's `task` key may come from it.
func dynamicTaskKey(pairs []yamlx.Pair) *yamlx.Node {
	for _, pair := range pairs {
		key := strings.TrimSpace(pair.Key.Value())
		if !isDirective(key) || pair.Value.Kind() != yamlx.ScalarKind {
			continue
		}
		expression := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(key, "${{")))
		switch {
		case hasWordPrefix(expression, "if"), hasWordPrefix(expression, "elseif"), hasWordPrefix(expression, "else"),
			hasWordPrefix(expression, "each"), hasWordPrefix(expression, "insert"):
			continue
		}
		return pair.Key
	}
	return nil
}

func hasWordPrefix(text, word string) bool {
	if !strings.HasPrefix(text, word) {
		return false
	}
	rest := text[len(word):]
	return rest == "" || rest[0] == ' ' || rest[0] == '}' || rest[0] == '('
}

// IsCandidate reports whether a YAML file may be an Azure Pipelines file: it
// parses as YAML and has a pipeline key at the top level or a `task:` key
// anywhere. GitHub workflows (`on:`) and Kubernetes manifests (`apiVersion:`
// with `kind:`) are never pipelines at the top level. A file that does not
// parse is a candidate only when a line looks like a `task:` key, so that a
// broken pipeline is reported instead of being skipped.
func IsCandidate(name string, data []byte) bool {
	document, _ := yamlx.Parse(name, data)
	if document == nil || len(document.Documents) == 0 {
		return taskLine.Match(data) || fallbackTask.Match(data)
	}
	for _, root := range document.Documents {
		if root == nil {
			continue
		}
		if !hasExcludedTopLevelKey(root) && (hasPipelineKey(root) || hasTaskKey(root, 0)) {
			return true
		}
	}
	return false
}

var taskLine = regexp.MustCompile(`(?im)^\s*(?:-\s+)*["']?task["']?\s*:`)

var pipelineKeys = map[string]bool{
	"task": true, "steps": true, "jobs": true, "stages": true, "extends": true, "template": true,
	"trigger": true, "pool": true, "pr": true, "resources": true, "variables": true, "parameters": true,
}

func hasPipelineKey(root *yamlx.Node) bool {
	if root.Kind() != yamlx.MappingKind {
		return false
	}
	pairs, _ := root.Map()
	found := false
	for _, pair := range pairs {
		switch key := strings.ToLower(strings.TrimSpace(pair.Key.Value())); key {
		case "on", "apiversion":
			return false
		default:
			found = found || pipelineKeys[key] || isDirective(key)
		}
	}
	return found
}

func hasExcludedTopLevelKey(root *yamlx.Node) bool {
	if root.Kind() != yamlx.MappingKind {
		return false
	}
	pairs, _ := root.Map()
	for _, pair := range pairs {
		key := strings.ToLower(strings.TrimSpace(pair.Key.Value()))
		if key == "on" || key == "apiversion" {
			return true
		}
	}
	return false
}

func hasTaskKey(node *yamlx.Node, depth int) bool {
	if node == nil || depth > 64 {
		return false
	}
	switch node.Kind() {
	case yamlx.SequenceKind:
		items, _ := node.Seq()
		for _, item := range items {
			if hasTaskKey(item, depth+1) {
				return true
			}
		}
	case yamlx.MappingKind:
		pairs, _ := node.Map()
		for _, pair := range pairs {
			if strings.EqualFold(strings.TrimSpace(pair.Key.Value()), "task") || hasTaskKey(pair.Value, depth+1) {
				return true
			}
		}
	}
	return false
}
