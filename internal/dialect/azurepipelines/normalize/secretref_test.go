package normalize

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// collectStrings appends every string leaf of a decoded JSON document.
func collectStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectStrings(item, out)
		}
	case map[string]any:
		for _, item := range typed { //trasloco:unordered-ok collecting strings, order irrelevant
			collectStrings(item, out)
		}
	}
}

// requireNoSecretText asserts that no case or JSON-escaped form of a secret
// value appears as an identifier-bounded token in any string carried by the
// canonical IR or the diagnostics. Decoded string leaves are inspected, so a
// bare JSON boolean or number (for example the boolean literal true for a
// bool-like secret) is not a false positive, and the identifier boundary stops
// a two-byte secret from matching inside an unrelated word such as
// "unavailable".
func requireNoSecretText(t *testing.T, got *ir.Pipeline, collector *diag.Collector, secrets ...string) {
	t.Helper()
	var leaves []string
	for _, document := range []string{canonical(t, got), diagnosticsJSON(t, got, collector)} {
		var decoded any
		if err := json.Unmarshal([]byte(document), &decoded); err != nil {
			t.Fatalf("decode IR/diagnostics: %v", err)
		}
		collectStrings(decoded, &leaves)
	}
	for _, secret := range secrets {
		forms := []string{secret, strings.ToUpper(secret), strings.ToLower(secret), jsonEscaped(secret, true), jsonEscaped(secret, false)}
		for _, form := range forms {
			if form == "" {
				continue
			}
			for _, leaf := range leaves {
				if containsBounded(leaf, form) {
					t.Fatalf("secret %q (form %q) reached %q", secret, form, leaf)
				}
			}
		}
	}
}

// collidingStageReference builds a pipeline whose declared secret value is the
// spelling of two colliding stage IDs (so the later stage is renamed after
// scrubbing) and whose free text names the renamed spelling.
func collidingStageReference(t *testing.T, secret string) (*ir.Pipeline, *diag.Collector) {
	t.Helper()
	upper := strings.ToUpper(secret)
	var out strings.Builder
	out.WriteString("parameters:\n- name: dbPassword\n  type: string\n  default: \"" + secret + "\"\n")
	out.WriteString("stages:\n")
	out.WriteString("- stage: " + secret + "\n  jobs:\n  - job: a\n    steps:\n    - script: echo hi\n")
	out.WriteString("- stage: " + upper + "\n  jobs:\n  - job: b\n    steps:\n    - script: echo ok\n      name: ref\n      condition: succeeded('" + upper + "')\n")
	return parsedPipeline(t, out.String())
}

// A free-text reference to a renamed ID never carries the ID spelling, so a
// short, numeric, bool-like or long secret value used as that ID cannot reach
// the IR, the extensions bag or the diagnostics (issue #12).
func TestRenamedReferenceDetailCarriesNoID(t *testing.T) {
	for _, secret := range []string{"ab", "42", "true", "MixedS3cretPw"} {
		t.Run(secret, func(t *testing.T) {
			got, collector := collidingStageReference(t, secret)
			if len(got.Stages) != 2 || got.Stages[0].ID != "[REDACTED]" || got.Stages[1].ID != "[REDACTED]-2" {
				t.Fatalf("stage IDs = %q, %q, want the later collision renamed", got.Stages[0].ID, got.Stages[1].ID)
			}
			ref := stepNamed(t, got, "ref")
			if !hasLoss(t, ref.Metadata, propertyRenamedReference) {
				t.Fatalf("referencing step losses = %v, want %s", lossProperties(t, ref.Metadata), propertyRenamedReference)
			}
			if ref.Fidelity != diag.FidelityNeedsManual {
				t.Fatalf("fidelity = %s, want needs-manual", ref.Fidelity)
			}
			requireNoSecretText(t, got, collector, secret)
			if findings := ir.Validate(got); len(findings) != 0 {
				t.Fatalf("IR validation: %v", findings)
			}
		})
	}
}

// referenceScanShape builds count stages named after count distinct secrets, so
// the backstop derives count-1 renamed-ID needles, plus one long free-text leaf
// in the first stage.
func referenceScanShape(count, leaf int) string {
	var out strings.Builder
	out.WriteString("parameters:\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "- name: db_password_%d\n  type: string\n  default: secret%04d\n", i, i)
	}
	out.WriteString("stages:\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "- stage: secret%04d\n  jobs:\n  - job: j%d\n    steps:\n", i, i)
		if i == 0 && leaf > 0 {
			fmt.Fprintf(&out, "    - script: echo %s\n", strings.Repeat("x", leaf))
		} else {
			out.WriteString("    - script: echo hi\n")
		}
	}
	return out.String()
}

// The reference scan costs needles x text passes; the backstop charges the
// passes actually made to its shared work counter and caps them with the pair
// scanner budget, and a leaf above the pair-scan bound is not scanned at all.
func TestReferenceScanChargesWork(t *testing.T) {
	b := &backstop{}
	for i := 0; i < 64; i++ {
		b.renameNeedles = append(b.renameNeedles, nameNeedle{lower: fmt.Sprintf("id%d", i), orig: fmt.Sprintf("id%d", i)})
		b.shortNames = append(b.shortNames, nameNeedle{lower: fmt.Sprintf("pw%d", i), orig: fmt.Sprintf("pw%d", i)})
	}
	const size = 512
	b.noteReferences(strings.Repeat("x", size))
	passes := (workFactor*size + workSlack) / size
	if want := passes * size; b.work != want {
		t.Fatalf("scan work = %d, want %d", b.work, want)
	}
	if b.work > workFactor*size+workSlack {
		t.Fatalf("scan work %d is above the per-leaf budget", b.work)
	}
	b.work = 0
	b.noteReferences(strings.Repeat("x", maxScanBytes+1))
	if b.work != maxScanBytes {
		t.Fatalf("oversized leaf work = %d, want %d", b.work, maxScanBytes)
	}
}

// A pipeline whose needle count is below the per-leaf budget shows every pass
// of the reference scan in the pipeline work counter, so the scaling tests can
// see it.
func TestReferenceScanVisibleInPipelineWork(t *testing.T) {
	const count, leaf = 5, 8192
	got, work := pipelineWorkOf(t, referenceScanShape(count, leaf))
	if len(got.Stages) != count {
		t.Fatalf("stages = %d, want %d", len(got.Stages), count)
	}
	if work < (count-1)*leaf {
		t.Fatalf("work %d does not include the reference scan (%d needles x %d bytes)", work, count-1, leaf)
	}
}
