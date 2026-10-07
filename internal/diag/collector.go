package diag

import (
	"bytes"
	"encoding/json"
	"sort"
	"sync"

	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

const defaultPerCodeCap = 1000

// Collector accumulates diagnostics with exact-instance deduplication and a
// per-code cap. It is safe for concurrent use.
type Collector struct {
	mu         sync.Mutex
	perCodeCap int
	items      []Diagnostic
	seen       map[string]struct{}
	counts     map[string]int
	suppressed map[string]int
	redactor   *redact.Redactor
}

// WithPerCodeCap sets the collector limit. A non-positive cap disables it.
func WithPerCodeCap(capacity int) Option {
	return func(d *construction) error { d.perCodeCap = capacity; return nil }
}

// NewCollector creates a collector with a default cap of 1000 per code.
// WithRedactor supplies the run's secrets; every Add is redacted before storage.
func NewCollector(options ...Option) *Collector {
	config := construction{redactor: redact.New(), perCodeCap: defaultPerCodeCap}
	for _, option := range options {
		if option == nil {
			panic("nil collector option")
		}
		if err := option(&config); err != nil {
			panic(err)
		}
	}
	return &Collector{
		perCodeCap: config.perCodeCap,
		redactor:   config.redactor,
		seen:       make(map[string]struct{}),
		counts:     make(map[string]int),
		suppressed: make(map[string]int),
	}
}

// Add records d. It returns false when d is a duplicate or over the cap.
func (c *Collector) Add(d Diagnostic) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.redactor == nil {
		c.redactor = redact.New()
	}
	d, err := redactDiagnostic(d, c.redactor)
	if err != nil {
		return false
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return false
	}
	key := string(encoded)
	if c.seen == nil {
		c.seen = make(map[string]struct{})
		c.counts = make(map[string]int)
		c.suppressed = make(map[string]int)
		c.perCodeCap = defaultPerCodeCap
	}
	if _, exists := c.seen[key]; exists {
		return false
	}
	if c.perCodeCap > 0 && c.counts[d.Code] >= c.perCodeCap {
		c.suppressed[d.Code]++
		return false
	}
	var snapshot Diagnostic
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return false
	}
	c.seen[key] = struct{}{}
	c.counts[d.Code]++
	c.items = append(c.items, snapshot)
	return true
}

// List returns a sorted snapshot using the normative report order.
func (c *Collector) List() []Diagnostic {
	c.mu.Lock()
	items := make([]Diagnostic, len(c.items))
	for i, item := range c.items {
		encoded, _ := json.Marshal(item)
		_ = json.Unmarshal(encoded, &items[i])
	}
	c.mu.Unlock()
	sort.SliceStable(items, func(i, j int) bool { return lessDiagnostic(items[i], items[j]) })
	return items
}

// Suppressed returns per-code counts omitted by the cap.
func (c *Collector) Suppressed() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]int, len(c.suppressed))
	for code, count := range c.suppressed {
		result[code] = count
	}
	return result
}

func lessDiagnostic(a, b Diagnostic) bool {
	if a.Source == nil || b.Source == nil {
		if a.Source == nil && b.Source != nil {
			return false
		}
		if a.Source != nil && b.Source == nil {
			return true
		}
	} else {
		if a.Source.File != b.Source.File {
			return a.Source.File < b.Source.File
		}
		if a.Source.Line != b.Source.Line {
			return a.Source.Line < b.Source.Line
		}
		if a.Source.Col != b.Source.Col {
			return a.Source.Col < b.Source.Col
		}
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	aKey, bKey := "", ""
	if a.Resource != nil {
		aKey = a.Resource.Key
	}
	if b.Resource != nil {
		bKey = b.Resource.Key
	}
	if aKey != bKey {
		return aKey < bKey
	}
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return bytes.Compare(aJSON, bJSON) < 0
}
