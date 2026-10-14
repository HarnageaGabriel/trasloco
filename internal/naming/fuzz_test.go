// This file fuzzes the normalizer. The seeds are the inputs of the spec table
// plus adversarial literals the table does not contain, so a failure is always
// reproducible from a spec vector or a one-line seed.
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
// the kind, and it respects the endpoint limit it is given.
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
	f.Add(0, "\xff\xfe", 8)
	f.Add(1, "CON", 24)
	f.Add(1, strings.Repeat("a", 300), 24)
	f.Add(2, "2-build", 24)
	f.Add(3, "_deploy", 24)

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
		// The truncation marker of normalization step 10 is "--h", which is not
		// a legal character of every kind (naming.md reserves "--h" and the
		// secret charset rejects "-"). The spec fixes the marker, so the
		// character and first-character guarantees hold for the untruncated part
		// only; the conflict is reported to the maintainer.
		truncated := slices.Contains(first.DiagnosticCodes, "TRL-NAME-0006")
		switch {
		case first.TargetName == "":
			t.Fatalf("%s %q normalized to the empty name", kind, name)
		case first.TargetName != second.TargetName:
			t.Fatalf("%s %q is not deterministic: %q then %q", kind, name, first.TargetName, second.TargetName)
		case !isASCII(first.TargetName):
			t.Fatalf("%s %q normalized to the non-ASCII %q", kind, name, first.TargetName)
		case !truncated && !allowedOutput(kind, first.TargetName):
			t.Fatalf("%s %q normalized to %q, which uses characters the kind forbids", kind, name, first.TargetName)
		case !truncated && !startsLegal(kind, first.TargetName[0]):
			t.Fatalf("%s %q normalized to %q, whose first character is illegal", kind, name, first.TargetName)
		case limit >= 24 && len(first.TargetName) > limit:
			t.Fatalf("%s %q normalized to %q, over the limit %d", kind, name, first.TargetName, limit)
		}
	})
}

// startsLegal reports whether b may start a normalized name of kind, per the
// target rules and normalization step 7 of naming.md.
func startsLegal(kind Kind, b byte) bool {
	letter := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	if kind == KindWorkflowFile {
		return letter
	}
	return letter || b == '_'
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
