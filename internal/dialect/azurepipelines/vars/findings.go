package vars

import (
	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

// findingKey identifies a diagnostic the evaluator is about to construct: its
// code, its exact origin, and its name parameter. Every finding keyed here
// carries "name" as its only parameter, so equal keys render to equal
// instances.
type findingKey struct {
	code   string
	origin diag.Source
	name   string
}

// findingCache constructs each distinct finding once. The collector already
// drops exact duplicate instances, but only after the caller built and
// redacted them; a step whose script repeats one logging-command marker would
// otherwise pay that cost for every occurrence. A finding is remembered only
// once the collector stored it: a repeat the per-code cap suppressed is passed
// through again, so the suppressed count keeps counting distinct occurrences.
//
// The cache assumes a precondition that holds for every run today: the
// collector's redactor variant set is fixed while a table is evaluated, so no
// secret is added between two equal keys and the instance the cache skips would
// have rendered exactly like the stored one. Adding a secret during evaluation
// would break that (the cached hit is never re-redacted), so a future
// Redactor.Add on this path must invalidate the cache or bypass it.
type findingCache struct {
	seen map[findingKey]bool //trasloco:unordered-ok membership lookups only
}

// addName records finding code at origin with its single "name" parameter,
// constructing it only when the collector has not already stored that
// instance. It returns what the collector reported.
func (c *findingCache) addName(collector *diag.Collector, code string, origin diag.Source, name string) bool {
	key := findingKey{code: code, origin: origin, name: name}
	if stored, exists := c.seen[key]; exists && stored {
		return false
	}
	stored := collector.Add(diag.MustNew(code, diag.WithSource(origin), diag.WithArgs(map[string]any{"name": name})))
	if c.seen == nil {
		c.seen = make(map[findingKey]bool)
	}
	c.seen[key] = stored
	return stored
}
