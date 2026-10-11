package normalize

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// plainDecimal is the decimal shape the expression lexer canonicalizes:
// optional sign, digits with an optional fraction, leading or trailing dot.
var plainDecimal = regexp.MustCompile(`^([+-]?)([0-9]*)(?:\.([0-9]*))?$`)

// numericText reports a decimal (plain or exponent form) value.
func numericText(text string) bool {
	if _, ok := canonicalDecimal(text); ok {
		return true
	}
	return decimalText.MatchString(text)
}

// canonicalDecimal returns the text the expression lexer produces for a decimal
// literal (leading zeros, trailing fractional zeros, a trailing dot and a
// negative zero removed, a leading zero added). ok is false for other shapes.
func canonicalDecimal(text string) (string, bool) {
	m := plainDecimal.FindStringSubmatch(text)
	if m == nil || m[2] == "" && m[3] == "" {
		return "", false
	}
	whole := strings.TrimLeft(m[2], "0")
	fraction := strings.TrimRight(m[3], "0")
	if whole == "" {
		whole = "0"
	}
	out := whole
	if fraction != "" {
		out += "." + fraction
	}
	if m[1] == "-" && out != "0" {
		out = "-" + out
	}
	return out, true
}

// partText returns a user-derived component with declared secrets replaced, so a
// composite built from it (prefix, qualified ID, suffix) is made of scrubbed
// parts instead of relying on whole-leaf matching of the composite.
//
// Distinct tainted components never collapse to one spelling ("[REDACTED]",
// "[REDACTED]-2", ...), so IDs built from them stay unique and references keep
// following the declaration they named. The same component always maps to the
// same spelling.
//
// Spellings are allocated per namespace (stage, job, step, ...), in first-use
// order, so a prefix follows the order its owners are de-duplicated in.
func (c *conv) partText(namespace, text string) (string, bool) {
	out := c.ts.apply(text)
	if out == text {
		return text, false
	}
	if known, ok := c.parts[namespace][text]; ok {
		return known, true
	}
	if c.parts == nil {
		c.parts, c.partsUsed = map[string]map[string]string{}, map[string]map[string]bool{}
	}
	if c.parts[namespace] == nil {
		c.parts[namespace], c.partsUsed[namespace] = map[string]string{}, map[string]bool{}
	}
	used := c.partsUsed[namespace]
	final := out
	c.work++
	if used[out] {
		// The next free suffix is remembered per scrubbed text, so N distinct
		// components that collapse to one spelling cost O(N), not O(N^2).
		if c.partNext == nil {
			c.partNext = map[string]map[string]int{}
		}
		if c.partNext[namespace] == nil {
			c.partNext[namespace] = map[string]int{}
		}
		n := max(c.partNext[namespace][out], 2)
		for ; used[out+"-"+strconv.Itoa(n)]; n++ {
			c.work++
		}
		final = out + "-" + strconv.Itoa(n)
		c.partNext[namespace][out] = n + 1
	}
	c.parts[namespace][text], used[final] = final, true
	return final, true
}

// secretDetail is the loss detail of every replacement of declared secret
// material after normalization.
const secretDetail = "secret material found in the normalized IR was replaced by the redaction placeholder; supply it from a secret store"

// part is partText that records a secretMaterial needs-manual loss on the
// owning accumulator whenever it changes the component. It must be called
// before the accumulator's metadata is built.
func (c *conv) part(a *acc, namespace, text string, src diag.Source) string {
	out, changed := c.partText(namespace, text)
	if changed {
		a.lose(codeExt, "ir", "secretMaterial", src, secretDetail)
		a.needsManual()
	}
	return out
}

// markSecret raises a built node to needs-manual and records the (single)
// secretMaterial loss.
func (c *conv) markSecret(m *ir.Metadata, src diag.Source) {
	m.Fidelity = maxFidelity(m.Fidelity, diag.FidelityNeedsManual)
	c.addSecretLoss(m, src, nil)
}

// sourceTextTainted reports whether source text, or the canonical text obtained
// by trimming it before a typed conversion, matches a declared secret.
func (c *conv) sourceTextTainted(text string) bool {
	if c.ts == nil {
		return false
	}
	if c.ts.apply(text) != text {
		return true
	}
	trimmed := strings.TrimSpace(text)
	return trimmed != "" && c.ts.apply(trimmed) != trimmed
}

// checkedAtoi is the only source-string to integer conversion in this package.
// It rejects tainted source before conversion because the resulting integer no
// longer contains the bytes that the final scrub can recognize.
func (c *conv) checkedAtoi(a *acc, text string, source diag.Source, extensionKey, construct, property string) (int, bool, error) {
	if c.sourceTextTainted(text) {
		a.keep(extensionKey, redact.Replacement())
		a.lose(codeExt, construct, property, source, "declared secret numeric value was replaced by the redaction placeholder; restore it from a secret store")
		a.needsManual()
		return 0, true, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return n, false, err
}
