// This file fuzzes the normalizer and the resolver. The seeds are the inputs of
// the spec table plus adversarial literals the table does not contain, so a
// failure is always reproducible from a spec vector or a one-line seed.
package naming

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// fuzzKinds is the index order of the fuzz seeds, because a fuzz function can
// only take basic-typed arguments.
var fuzzKinds = []Kind{KindSecret, KindWorkflowFile, KindJobID, KindStepID}

// FuzzNormalize checks the structural guarantees of normalization on arbitrary
// input: it never panics, it is deterministic, its output is ASCII and legal for
// the kind, it respects the endpoint limit, it never emits a reserved name and
// it never emits a secret with the GITHUB_ prefix or a leading digit.
func FuzzNormalize(f *testing.F) {
	vectors := loadSpecVectors(f)
	for _, vector := range namingVectors {
		if vector.run == nil {
			continue
		}
		row, ok := vectors[vector.id]
		if !ok {
			continue
		}
		parts := strings.Split(row.kindCell, " / ")
		if len(parts) != 3 {
			continue
		}
		kind, ok := kindOf(parts[0])
		if !ok {
			continue
		}
		f.Add(slices.Index(fuzzKinds, kind), strings.Trim(strings.TrimSpace(parts[2]), "`"), 64)
	}
	// Literals the table does not cover.
	f.Add(0, "", 8)
	f.Add(0, "   ", 8)
	f.Add(0, "ÿþ", 8)
	f.Add(0, "ǰ", 64)
	f.Add(0, "ẖ", 64)
	f.Add(0, "ẗ", 64)
	f.Add(0, "ẘ", 64)
	f.Add(0, "ẙ", 64)
	f.Add(0, "ẚ", 64)
	f.Add(1, "CON", 24)
	f.Add(1, "2-build", 24)
	f.Add(1, "_build", 24)
	f.Add(1, "\u06c0", 64)
	f.Add(1, strings.Repeat("a", 300), 24)
	f.Add(2, "2-build", 24)
	f.Add(3, "_deploy", 24)
	f.Add(0, "github_token", 64)

	f.Fuzz(func(t *testing.T, kindIndex int, name string, limit int) {
		if kindIndex < 0 || kindIndex >= len(fuzzKinds) {
			return
		}
		if limit < 0 || limit > 512 {
			return
		}
		if !utf8.ValidString(name) {
			return
		}
		kind := fuzzKinds[kindIndex]
		first, err := normalizeFor(kind, "1", name, limit)
		if err != nil {
			return
		}
		second, err := normalizeFor(kind, "1", name, limit)
		if err != nil {
			t.Fatalf("second normalization of %s %q failed after the first succeeded: %v", kind, name, err)
		}
		switch {
		case first.TargetName == "":
			t.Fatalf("%s %q normalized to the empty name", kind, name)
		case first.TargetName != second.TargetName:
			t.Fatalf("%s %q is not deterministic: %q then %q", kind, name, first.TargetName, second.TargetName)
		case !isASCII(first.TargetName):
			t.Fatalf("%s %q normalized to the non-ASCII %q", kind, name, first.TargetName)
		case !allowedOutput(kind, first.TargetName):
			t.Fatalf("%s %q normalized to %q, which uses characters the kind forbids", kind, name, first.TargetName)
		case !startsLegal(kind, first.TargetName[0]):
			t.Fatalf("%s %q normalized to %q, whose first character is illegal", kind, name, first.TargetName)
		case reservedOutput(kind, first.TargetName):
			t.Fatalf("%s %q normalized to the reserved name %q", kind, name, first.TargetName)
		case kind == KindSecret && strings.HasPrefix(strings.ToUpper(first.TargetName), "GITHUB_"):
			t.Fatalf("%s %q normalized to %q, which keeps the reserved GITHUB_ prefix", kind, name, first.TargetName)
		case limit > 0 && len(first.TargetName) > limit:
			t.Fatalf("%s %q normalized to %q, over the limit %d", kind, name, first.TargetName, limit)
		}
	})
}

// FuzzResolver checks the collision contract on arbitrary inventories: a
// collision is always disambiguated, no output is illegal or reserved, and a
// second resolver over the same inventory is idempotent.
func FuzzResolver(f *testing.F) {
	f.Add(0, "db.password", "a", "b")
	f.Add(0, "github_token", "a", "b")
	f.Add(1, "Build", "a", "b")
	f.Add(2, "2-build", "a", "b")
	f.Add(3, "_deploy", "a", "b")

	f.Fuzz(func(t *testing.T, kindIndex int, name, firstID, secondID string) {
		if kindIndex < 0 || kindIndex >= len(fuzzKinds) {
			return
		}
		if !utf8.ValidString(name) || !utf8.ValidString(firstID) || !utf8.ValidString(secondID) {
			return
		}
		if firstID == "" {
			firstID = "a"
		}
		if secondID == "" || secondID == firstID {
			secondID = firstID + "x"
		}
		kind := fuzzKinds[kindIndex]
		scope := "one-namespace"
		entries := []Entry{
			{Kind: kind, Platform: "ado", StableID: firstID, SourceName: name, ScopeKey: scope},
			{Kind: kind, Platform: "ado", StableID: secondID, SourceName: name, ScopeKey: scope},
		}
		resolver, err := NewResolver(entries, DefaultPolicy())
		if err != nil {
			return
		}
		decisions := make([]Decision, len(entries))
		for index, entry := range entries {
			decision, ok := resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
			if !ok {
				t.Fatalf("%s %q: no decision for %q", kind, name, entry.StableID)
			}
			if !allowedOutput(kind, decision.TargetName) || !startsLegal(kind, decision.TargetName[0]) || reservedOutput(kind, decision.TargetName) {
				t.Fatalf("%s %q: distinct entry %q received the illegal name %q", kind, name, entry.StableID, decision.TargetName)
			}
			decisions[index] = decision
		}
		if strings.EqualFold(decisions[0].TargetName, decisions[1].TargetName) {
			t.Fatalf("%s %q: distinct entries received the same name %q", kind, name, decisions[0].TargetName)
		}
		again, err := NewResolver(entries, DefaultPolicy())
		if err != nil {
			t.Fatalf("%s %q: second resolver failed: %v", kind, name, err)
		}
		for index, entry := range entries {
			decision, ok := again.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
			if !ok {
				t.Fatalf("%s %q: second resolver has no decision for %q", kind, name, entry.StableID)
			}
			if decision.TargetName != decisions[index].TargetName || !slices.Equal(decision.DiagnosticCodes, decisions[index].DiagnosticCodes) {
				t.Fatalf("%s %q: resolver is not idempotent for %q: %q %v then %q %v",
					kind, name, entry.StableID,
					decisions[index].TargetName, decisions[index].DiagnosticCodes,
					decision.TargetName, decision.DiagnosticCodes)
			}
		}
	})
}

// startsLegal reports whether b may start a normalized name of kind, per the
// target rules and normalization step 7 of naming.md. Workflow filenames accept
// a digit or underscore; job and step IDs need a letter or underscore; secrets
// need an upper-case letter or underscore.
func startsLegal(kind Kind, b byte) bool {
	letter := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	switch kind {
	case KindWorkflowFile:
		return letter || asciiDigit(b) || b == '_'
	case KindSecret:
		return asciiUpper(b) || b == '_'
	default:
		return letter || b == '_'
	}
}

// allowedOutput reports whether name only uses the characters its kind permits.
func allowedOutput(kind Kind, name string) bool {
	if kind == KindWorkflowFile {
		if !strings.HasSuffix(name, ".yml") {
			return false
		}
		name = strings.TrimSuffix(name, ".yml")
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch kind {
		case KindSecret:
			if !asciiUpper(b) && !asciiDigit(b) && b != '_' {
				return false
			}
		case KindWorkflowFile:
			if !asciiLower(b) && !asciiDigit(b) && b != '-' && b != '_' {
				return false
			}
		default:
			if !asciiLower(b) && !asciiUpper(b) && !asciiDigit(b) && b != '-' && b != '_' {
				return false
			}
		}
	}
	return true
}

// reservedOutput reports whether name is one of the reserved names of its kind.
func reservedOutput(kind Kind, name string) bool {
	if kind == KindWorkflowFile {
		name = strings.TrimSuffix(name, ".yml")
	}
	return reserved(kind, name)
}

// asciiLower reports whether b is a lower-case ASCII letter.
func asciiLower(b byte) bool { return b >= 'a' && b <= 'z' }

// asciiUpper reports whether b is an upper-case ASCII letter.
func asciiUpper(b byte) bool { return b >= 'A' && b <= 'Z' }

// asciiDigit reports whether b is an ASCII digit.
func asciiDigit(b byte) bool { return b >= '0' && b <= '9' }

// isASCII reports whether s contains only ASCII bytes.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
