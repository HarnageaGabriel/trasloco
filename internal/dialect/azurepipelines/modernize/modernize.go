// Package modernize detects retired Azure Pipelines task references without
// modifying pipeline source.
package modernize

import (
	"sort"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	adoparse "github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

// Finding is one retired or indeterminate task reference.
type Finding struct {
	Code         string   `json:"code"`
	Path         string   `json:"path"`
	Line         int      `json:"line"`
	Column       int      `json:"column"`
	RetiredTask  string   `json:"retiredTask"`
	Class        string   `json:"class,omitempty"`
	Replacements []string `json:"replacements,omitempty"`
	Action       string   `json:"action"`
	SourceURL    string   `json:"sourceUrl,omitempty"`
	Diagnostics  []string `json:"diagnostics,omitempty"`
}

// Scan parses one Azure Pipelines file and returns deterministic findings and
// parser diagnostics. The input bytes are never changed.
func Scan(name string, data []byte, cat *catalog.Catalog) ([]Finding, []diag.Diagnostic) {
	collector := diag.NewCollector()
	_ = adoparse.Source(name, data, collector)

	document, yamlDiagnostics := yamlx.ParseWithOptions(name, data, yamlx.WithAnchorPolicy(yamlx.AnchorError))
	for _, diagnostic := range yamlDiagnostics {
		collector.Add(diagnostic)
	}
	if document == nil || document.Root() == nil {
		return nil, collector.List()
	}

	index := newIndex(cat)
	var findings []Finding
	walk(document.Root(), walkContext{}, func(node *yamlx.Node, context walkContext) {
		if node.Kind() == yamlx.MappingKind && context.tasks {
			if task := taskValue(node); task != nil {
				if finding, ok := index.reference(name, task); ok {
					findings = append(findings, finding)
				}
				return
			}
		}
		if node.Kind() == yamlx.ScalarKind && context.stringValues && !context.taskValue {
			if finding, ok := index.retiredString(name, node); ok {
				findings = append(findings, finding)
			}
		}
	})
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
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

type walkContext struct {
	tasks, stringValues, taskValue bool
}

func walk(node *yamlx.Node, context walkContext, visit func(*yamlx.Node, walkContext)) {
	if node == nil {
		return
	}
	visit(node, context)
	switch node.Kind() {
	case yamlx.SequenceKind:
		items, _ := node.Seq()
		for _, item := range items {
			walk(item, context, visit)
		}
	case yamlx.MappingKind:
		pairs, _ := node.Map()
		for _, pair := range pairs {
			key := strings.TrimSpace(pair.Key.Value())
			next := context
			if strings.EqualFold(key, "steps") {
				next.tasks = true
			}
			if strings.EqualFold(key, "parameters") {
				next.tasks = true
				next.stringValues = true
			}
			if strings.EqualFold(key, "variables") {
				next.stringValues = true
			}
			next.taskValue = strings.EqualFold(key, "task")
			walk(pair.Value, next, visit)
		}
	}
}

func taskValue(mapping *yamlx.Node) *yamlx.Node {
	pairs, diagnostics := mapping.Map()
	if len(diagnostics) != 0 {
		return nil
	}
	allowed := map[string]bool{
		"task": true, "inputs": true, "displayname": true, "name": true, "condition": true,
		"continueonerror": true, "enabled": true, "env": true, "timeoutinminutes": true,
		"retrycountontaskfailure": true, "target": true,
	}
	var task *yamlx.Node
	for _, pair := range pairs {
		key := strings.ToLower(strings.TrimSpace(pair.Key.Value()))
		if !allowed[key] {
			return nil
		}
		if strings.EqualFold(pair.Key.Value(), "task") && pair.Value.Kind() == yamlx.ScalarKind {
			task = pair.Value
		}
	}
	return task
}

type catalogIndex struct {
	cat    *catalog.Catalog
	byGUID map[string][]catalog.Modernization
}

func newIndex(cat *catalog.Catalog) catalogIndex {
	idx := catalogIndex{cat: cat, byGUID: make(map[string][]catalog.Modernization)}
	if cat != nil {
		for _, entry := range cat.Modernizations {
			idx.byGUID[strings.ToLower(entry.GUID)] = append(idx.byGUID[strings.ToLower(entry.GUID)], entry)
		}
	}
	return idx
}

func (i catalogIndex) reference(path string, node *yamlx.Node) (Finding, bool) {
	raw := strings.TrimSpace(node.Value())
	if strings.Contains(raw, "${{") || strings.Contains(raw, "$(") || strings.Contains(raw, "$[") {
		return manualFinding(path, node, raw), true
	}
	name, major, full, ok := splitReference(raw)
	if !ok {
		return Finding{}, false
	}
	if strings.Contains(name, ".") {
		last := name[strings.LastIndexByte(name, '.')+1:]
		if i.hasRetiredName(last) {
			f := manualFinding(path, node, raw)
			f.Code, f.Action = "TRL-MOD-0010", "Check the extension publisher's retirement guidance."
			return f, true
		}
		return Finding{}, false
	}
	entry, found := i.lookup(name, major)
	if !found {
		if len(i.byGUID[strings.ToLower(name)]) > 0 {
			return manualFinding(path, node, raw), true
		}
		return Finding{}, false
	}
	f := fromEntry(path, node, raw, entry)
	if full {
		f.Action += " Replace the full version with the recommended major version."
		f.Diagnostics = append(f.Diagnostics, "TRL-MOD-0011")
	}
	return f, true
}

func (i catalogIndex) retiredString(path string, node *yamlx.Node) (Finding, bool) {
	raw := strings.TrimSpace(node.Value())
	name, major, _, ok := splitReference(raw)
	if !ok {
		return Finding{}, false
	}
	if _, found := i.lookup(name, major); !found {
		return Finding{}, false
	}
	return manualFinding(path, node, raw), true
}

func (i catalogIndex) lookup(name string, major int) (catalog.Modernization, bool) {
	if i.cat == nil {
		return catalog.Modernization{}, false
	}
	if entry, ok := i.cat.Modernization(name, major); ok {
		return *entry, true
	}
	entries := i.byGUID[strings.ToLower(name)]
	var match *catalog.Modernization
	for n := range entries {
		taskName, taskMajor, _, ok := splitReference(entries[n].Task)
		if !ok || taskMajor != major {
			continue
		}
		if match != nil && !strings.EqualFold(match.Task, entries[n].Task) {
			return catalog.Modernization{}, false
		}
		candidate := entries[n]
		match = &candidate
		_ = taskName
	}
	if match == nil {
		return catalog.Modernization{}, false
	}
	return *match, true
}

func (i catalogIndex) hasRetiredName(name string) bool {
	if i.cat == nil {
		return false
	}
	for _, entry := range i.cat.Modernizations {
		task, _, _, ok := splitReference(entry.Task)
		if ok && strings.EqualFold(task, name) {
			return true
		}
	}
	return false
}

func splitReference(raw string) (name string, major int, full, ok bool) {
	at := strings.LastIndexByte(raw, '@')
	if at <= 0 || at == len(raw)-1 || strings.ContainsAny(raw, " \t") {
		return "", 0, false, false
	}
	parts := strings.Split(raw[at+1:], ".")
	if len(parts) > 3 {
		return "", 0, false, false
	}
	for _, part := range parts {
		if part == "" {
			return "", 0, false, false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return "", 0, false, false
		}
	}
	major, err := strconv.Atoi(parts[0])
	return raw[:at], major, len(parts) > 1, err == nil
}

func fromEntry(path string, node *yamlx.Node, raw string, entry catalog.Modernization) Finding {
	code := map[string]string{"A": "TRL-MOD-0001", "B": "TRL-MOD-0002", "C": "TRL-MOD-0003", "D": "TRL-MOD-0004"}[entry.Class]
	position := node.Position()
	source := ""
	if len(entry.Sources) > 0 {
		source = entry.Sources[0]
	}
	return Finding{
		Code: code, Path: path, Line: position.Line, Column: position.Col,
		RetiredTask: raw, Class: entry.Class, Replacements: append([]string(nil), entry.Replacement...),
		Action: entry.Action, SourceURL: source,
	}
}

func manualFinding(path string, node *yamlx.Node, raw string) Finding {
	position := node.Position()
	return Finding{
		Code: "TRL-MOD-0005", Path: path, Line: position.Line, Column: position.Col,
		RetiredTask: raw, Action: "Cannot determine the task version; replace the retired task reference by hand.",
	}
}
