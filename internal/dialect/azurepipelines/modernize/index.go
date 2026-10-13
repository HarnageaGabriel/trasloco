package modernize

import (
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

type catalogIndex struct {
	cat    *catalog.Catalog
	byGUID map[string][]catalog.Modernization
	// onStep shares detection's discovery with the pure rewrite engine.
	onStep func(*yamlx.Node, *yamlx.Node)
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

type resolution int

const (
	// notRetired: the (identity, major) pair is not in the retirement catalog.
	notRetired resolution = iota
	// retiredOnce: exactly one retired task matches.
	retiredOnce
	// retiredAmbiguous: several retired tasks share the GUID and major.
	retiredAmbiguous
)

func (i catalogIndex) reference(node *yamlx.Node) (Finding, bool) {
	raw := strings.TrimSpace(node.Value())
	if strings.Contains(raw, "${{") || strings.Contains(raw, "$(") || strings.Contains(raw, "$[") {
		return manualFinding(node, raw), true
	}
	name, major, full, ok := splitReference(raw)
	if !ok {
		return Finding{}, false
	}
	if strings.Contains(name, ".") {
		last := name[strings.LastIndexByte(name, '.')+1:]
		if i.hasRetiredName(last) {
			f := manualFinding(node, raw)
			f.Code, f.Severity = "TRL-MOD-0010", SeverityInfo
			f.Action = "This looks like an extension task that shares its name with a retired built-in task. Check the extension publisher's retirement guidance."
			return f, true
		}
		return Finding{}, false
	}
	entry, state := i.resolve(name, major)
	switch state {
	case notRetired:
		return Finding{}, false
	case retiredAmbiguous:
		return manualFinding(node, raw), true
	}
	f := fromEntry(node, raw, entry)
	if full {
		f.Action += " Replace the full version with the recommended major version."
		f.Diagnostics = append(f.Diagnostics, "TRL-MOD-0011")
	}
	return f, true
}

func (i catalogIndex) retiredString(node *yamlx.Node) (Finding, bool) {
	raw := strings.TrimSpace(node.Value())
	name, major, _, ok := splitReference(raw)
	if !ok {
		return Finding{}, false
	}
	if _, state := i.resolve(name, major); state == notRetired {
		return Finding{}, false
	}
	return manualFinding(node, raw), true
}

// resolve looks up the pair (identity, major). A name is compared case
// insensitively; a GUID is resolved against every retired task that has this
// GUID and this major, because several tasks share a GUID (spec 3.2.3).
func (i catalogIndex) resolve(name string, major int) (catalog.Modernization, resolution) {
	if i.cat == nil {
		return catalog.Modernization{}, notRetired
	}
	if entry, ok := i.cat.Modernization(name, major); ok {
		return *entry, retiredOnce
	}
	var match *catalog.Modernization
	entries := i.byGUID[strings.ToLower(name)]
	for n := range entries {
		_, taskMajor, _, ok := splitReference(entries[n].Task)
		if !ok || taskMajor != major {
			continue
		}
		if match != nil && !strings.EqualFold(match.Task, entries[n].Task) {
			return catalog.Modernization{}, retiredAmbiguous
		}
		candidate := entries[n]
		match = &candidate
	}
	if match == nil {
		return catalog.Modernization{}, notRetired
	}
	return *match, retiredOnce
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
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return "", 0, false, false
		}
	}
	major, err := strconv.Atoi(parts[0])
	return raw[:at], major, len(parts) > 1, err == nil
}

func itoa(value int) string { return strconv.Itoa(value) }

func fromEntry(node *yamlx.Node, raw string, entry catalog.Modernization) Finding {
	code := map[string]string{"A": "TRL-MOD-0001", "B": "TRL-MOD-0002", "C": "TRL-MOD-0003", "D": "TRL-MOD-0004"}[entry.Class]
	severity := SeverityWarning
	if entry.Class == "D" {
		severity = SeverityError
	}
	position := node.Position()
	source := ""
	if len(entry.Sources) > 0 {
		source = entry.Sources[0]
	}
	return Finding{
		Code: code, Severity: severity, Line: position.Line, Column: position.Col,
		RetiredTask: raw, Class: entry.Class, Replacements: append([]string(nil), entry.Replacement...),
		Action: entry.Action, SourceURL: source, Help: HelpURL(entry.Task),
	}
}

func manualFinding(node *yamlx.Node, raw string) Finding {
	position := node.Position()
	return Finding{
		Code: "TRL-MOD-0005", Severity: SeverityWarning, Line: position.Line, Column: position.Col,
		RetiredTask: raw, Action: "Cannot determine the task version; replace the retired task reference by hand.",
	}
}
