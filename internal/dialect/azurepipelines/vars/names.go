package vars

import (
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

var reservedPrefixes = []string{"endpoint", "input", "secret", "path", "securefile"}

// ValidName reports the variable-name grammar: letters, numbers, periods and
// underscores (learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#variable-naming-restrictions),
// plus hyphens, which Microsoft's own variable-group example uses; a hyphenated
// name is accepted with a warning, see HyphenatedName.
// Variable-group identifiers follow a different rule, see ValidGroupName.
func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r != '.' && r != '_' && r != '-' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// HyphenatedName reports a name Microsoft's naming restriction excludes but the
// evaluator accepts (maintainer decision, TRL-VAR-0018).
func HyphenatedName(name string) bool { return strings.Contains(name, "-") }

func warnHyphen(name string, source diag.Source, findings *diag.Collector) {
	if HyphenatedName(name) {
		findings.Add(diag.MustNew("TRL-VAR-0018", diag.WithSource(source), diag.WithArgs(map[string]any{"name": displayName(name)})))
	}
}

// ValidGroupName reports whether a variable-group identifier is a usable
// literal. Hyphens are allowed (the documented group names are hyphenated,
// VARV-025); an empty name, control characters or unexpanded template, macro or
// runtime-expression syntax are not.
func ValidGroupName(name string) bool {
	if strings.TrimSpace(name) == "" || strings.Contains(name, "${{") || strings.Contains(name, "$(") || strings.Contains(name, "$[") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ReservedName reports names Azure excludes from tasks and scripts.
func ReservedName(name string) bool {
	folded := fold(name)
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(folded, fold(prefix)) {
			return true
		}
	}
	return false
}

// EnvironmentName applies Azure's automatic non-secret environment projection.
// Only "." (and case) changes; the other legal characters are letters, digits
// and "_".
func EnvironmentName(name string) string { return fold(strings.ReplaceAll(name, ".", "_")) }

// CompileVisibleSystem reports the predefined variables documented as available
// to compile-time ${{ variables.x }} expressions. Everything else a run supplies
// (UI, group, queue-time and other system values) is runtime-only.
func CompileVisibleSystem(name string) bool {
	folded := fold(name)
	return folded == "BUILD.SOURCEBRANCH" || folded == "BUILD.SOURCEBRANCHNAME"
}
