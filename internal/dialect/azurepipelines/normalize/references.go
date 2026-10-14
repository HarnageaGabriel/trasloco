package normalize

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// Free-text reference detection (issues #12 and #13).
//
// #12: when secret scrubbing collapses two declared IDs into one spelling, the
// de-duplication renames the later declaration. Typed references (needs,
// dependsOn, scope IDs, outputs) follow the declaration; references inside
// free text (an opaque condition, a runtime `$[ ]` string, a `succeeded('X')`
// argument) cannot be re-pointed and stay ambiguous.
//
// #13: a declared secret whose value is short, small-numeric or boolean-like is
// replaced on whole-leaf equality only, so its value inside longer text is not
// replaced. A string that references such a variable by name is flagged.
//
// Both are recorded as needs-manual losses on the owning node. The diagnostic
// never carries a secret value: the detail names the ID or variable only, and
// every detail is passed through the same scrubber as other findings.

const (
	propertyRenamedReference = "renamedReference"
	propertyShortSecretRef   = "shortSecretReference"
)

type refKind uint8

const (
	refRenamed refKind = iota
	refShort
)

// refHit is one free-text reference detected on the owning node.
type refHit struct {
	node *ir.Metadata
	kind refKind
	name string
}

// nameNeedle is one spelling to search for in free text; lower is the
// case-folded form and orig the original spelling for the diagnostic.
type nameNeedle struct {
	lower string
	orig  string
}

// referenceNeedles returns the renamed-ID spellings (#12) and the secret
// variable names (#13) to search for while the backstop walks the free text.
func (c *conv) referenceNeedles(ids *idPlan) (renamed, short []nameNeedle) {
	// One spelling can be both a renamed ID and a declared short-secret
	// name; each list de-duplicates on its own so both reasons are reported.
	seen := map[refKind]map[string]bool{}
	add := func(kind refKind, list *[]nameNeedle, spelling, orig string) {
		if seen[kind] == nil {
			seen[kind] = map[string]bool{}
		}
		if spelling == "" || seen[kind][spelling] {
			return
		}
		seen[kind][spelling] = true
		*list = append(*list, nameNeedle{lower: strings.ToLower(spelling), orig: orig})
	}
	for _, orig := range c.predictedRenamed(ids.stages) {
		add(refRenamed, &renamed, orig, orig)
	}
	for _, orig := range c.predictedRenamed(ids.jobs) {
		add(refRenamed, &renamed, orig, orig)
	}
	// Components scrubbed before composition keep the unqualified spelling in
	// free text (a qualified job ID would otherwise hide it).
	for _, namespace := range []string{"stage", "job", "step"} {
		for _, orig := range slices.Sorted(maps.Keys(c.parts[namespace])) {
			if c.parts[namespace][orig] != c.ts.apply(orig) {
				add(refRenamed, &renamed, orig, orig)
			}
		}
	}
	for _, name := range c.shortSecretNames {
		add(refShort, &short, name, name)
	}
	return renamed, short
}

// predictedRenamed returns the pre-scrub spellings of the declarations the
// backstop de-duplication will rename after a secret scrubbing collision. It
// mirrors the allocateUnique pass over the scrubbed spellings, so the free-text
// scan can run inside the same walk.
func (c *conv) predictedRenamed(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	scrubbed := make([]string, len(raw))
	changed := false
	for i, id := range raw {
		scrubbed[i] = c.ts.apply(id)
		changed = changed || scrubbed[i] != id
	}
	if !changed {
		return nil
	}
	finals := slices.Clone(scrubbed)
	allocateUnique(finals)
	var out []string
	for i := range raw {
		if finals[i] != scrubbed[i] && scrubbed[i] != raw[i] {
			out = append(out, raw[i])
		}
	}
	return out
}

// noteReferences records a free-text reference on the nearest owning node.
func (b *backstop) noteReferences(value string) {
	if value == "" || len(b.renameNeedles) == 0 && len(b.shortNames) == 0 {
		return
	}
	// The scan is needles x text. Bound it with the pair scanner budget and
	// charge the passes actually made to the shared work counter, so it is
	// bounded and visible to the scaling tests. A leaf above the pair scanner
	// bound is not scanned at all.
	if len(value) > maxScanBytes {
		b.work += maxScanBytes
		return
	}
	limit := (workFactor*len(value) + workSlack) / len(value)
	renamed := b.renameNeedles
	if len(renamed) > limit {
		renamed = renamed[:limit]
	}
	short := b.shortNames
	if extra := limit - len(renamed); len(short) > extra {
		short = short[:max(extra, 0)]
	}
	b.work += (len(renamed) + len(short)) * len(value)
	lower := strings.ToLower(value)
	if name, ok := matchIdentifier(lower, renamed); ok {
		b.noteReference(refRenamed, name)
	}
	if name, ok := matchNameReference(lower, short); ok {
		b.noteReference(refShort, name)
	}
}

func (b *backstop) noteReference(kind refKind, name string) {
	if name == "" || len(b.stack) == 0 {
		return
	}
	node := b.stack[len(b.stack)-1]
	if b.refSeen[node] == nil {
		b.refSeen[node] = map[refKind]bool{}
	}
	if b.refSeen[node][kind] {
		return
	}
	b.refSeen[node][kind] = true
	b.refOrder = append(b.refOrder, refHit{node: node, kind: kind, name: name})
	for _, ancestor := range b.stack {
		b.raise[ancestor] = true
	}
}

// matchIdentifier reports the first needle that appears in text at identifier
// boundaries (text is lower case).
func matchIdentifier(text string, needles []nameNeedle) (string, bool) {
	for _, needle := range needles {
		if containsBounded(text, needle.lower) {
			return needle.orig, true
		}
	}
	return "", false
}

// matchNameReference reports the first secret variable referenced by name, in
// the `$(name)`, `variables.name` or `variables['name']` form (text is lower
// case).
func matchNameReference(text string, needles []nameNeedle) (string, bool) {
	for _, needle := range needles {
		if strings.Contains(text, "$("+needle.lower+")") ||
			containsBounded(text, "variables."+needle.lower) ||
			strings.Contains(text, "variables['"+needle.lower+"']") ||
			strings.Contains(text, "variables[\""+needle.lower+"\"]") {
			return needle.orig, true
		}
	}
	return "", false
}

// containsBounded reports whether text contains needle delimited by
// non-identifier bytes (an ID never appears inside a longer word).
func containsBounded(text, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		index := strings.Index(text[from:], needle)
		if index < 0 {
			return false
		}
		index += from
		end := index + len(needle)
		if (index == 0 || !identifierByte(text[index-1])) && (end == len(text) || !identifierByte(text[end])) {
			return true
		}
		from = index + 1
	}
}

// identifierByte reports a byte an Azure Pipelines identifier can contain
// (text compared here is already folded to lower case).
func identifierByte(char byte) bool {
	return char == '_' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}

// shortSecretRefDetail is the finding detail of a reference to a short,
// small-numeric or boolean-like secret variable. The variable name is scrubbed
// like every other finding, so the message never carries a value.
func shortSecretRefDetail(name string) string {
	return fmt.Sprintf("the secret variable %q is short, numeric or boolean-like, so its value is not replaced inside longer text; verify this reference by name manually", name)
}

// shortSecretReference reports whether a portable expression names a declared
// short, small-numeric or boolean-like secret variable. A structured reference
// embeds no value, so this is a review prompt, not a leak.
func (c *conv) shortSecretReference(e *ir.Expr) (string, bool) {
	if e == nil || len(c.shortSecretNames) == 0 {
		return "", false
	}
	stack := []*ir.Expr{e}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch node.Kind {
		case ir.ExprRef:
			if node.Ref != nil && len(node.Ref.Path) > 0 && c.isShortSecretName(node.Ref.Path[len(node.Ref.Path)-1]) {
				return node.Ref.Path[len(node.Ref.Path)-1], true
			}
		case ir.ExprCall:
			if node.Call != nil {
				for i := range node.Call.Args {
					stack = append(stack, &node.Call.Args[i])
				}
			}
		}
	}
	return "", false
}

// isShortSecretName reports whether name is a declared short secret variable
// name. Azure variable names are case-insensitive.
func (c *conv) isShortSecretName(name string) bool {
	for _, candidate := range c.shortSecretNames {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

// renamedReferenceDetail is the finding detail of a free-text reference to
// an ID renamed after a secret scrubbing collision (#12). It names no ID: a
// short, small-numeric or boolean-like secret value survives scrubbing
// inside an ID spelling, so any spelling carried here could be a declared
// secret value. The finding source position identifies the node.
const renamedReferenceDetail = "free-text reference to an ID renamed after a secret scrubbing collision cannot be re-pointed; verify the references in this node manually"

// emitReferenceLoss records the loss and finding for one detected reference.
func (c *conv) emitReferenceLoss(hit refHit, p *ir.Pipeline) {
	var property, detail string
	switch hit.kind {
	case refRenamed:
		property = propertyRenamedReference
		detail = renamedReferenceDetail
	case refShort:
		property = propertyShortSecretRef
		detail = shortSecretRefDetail(hit.name)
	default:
		return
	}
	c.addNodeLoss(hit.node, "ir", property, detail, p.Origin.Source, p.Origin.Chain)
}

// referenceField reports the free-text fields whose references are scanned: a
// run script, an opaque provider value (a condition, a matrix, a run name) and
// an expression string literal.
func referenceField(typeName, field string) bool {
	switch typeName {
	case "RunStep":
		return field == "Script"
	case "Raw":
		return field == "Text"
	case "Literal":
		return field == "String"
	}
	return false
}
