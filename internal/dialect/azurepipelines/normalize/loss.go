package normalize

import (
	"encoding/json"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

const (
	codeExt      = "TRL-CONV-0008"
	codeDeferred = "TRL-CONV-0009"
	// codeSynth records implicit source semantics made explicit (ir.md section 4).
	codeSynth = "TRL-CONV-0004"
	// codeApprox records a conversion approximation, here the VERIFY-01 star widening.
	codeApprox = "TRL-CONV-0001"
)

type loss struct {
	Construct string `json:"construct"`
	Property  string `json:"property"`
	Detail    string `json:"detail,omitempty"`
}

type acc struct {
	c        *conv
	source   diag.Source
	chain    []diag.Source
	losses   []loss
	deferred []loss
	values   ir.OrderedMapBuilder[any]
	f        diag.Fidelity
}

func (c *conv) newAcc(source diag.Source) *acc {
	return &acc{c: c, source: source, f: diag.FidelityExact}
}

// in records the template call-site chain of the node being converted.
func (a *acc) in(chain []diag.Source) *acc {
	a.chain = chain
	return a
}

func (a *acc) keep(key string, value any) { a.values.Set(key, value) }

func (a *acc) lose(code, category, property string, source diag.Source, detail string) {
	// Category, property and detail may carry user text (key names): scrub them
	// before any loss or finding is built.
	category, property, detail = a.c.sanitize(category), a.c.sanitize(property), a.c.sanitize(detail)
	record := loss{Construct: category, Property: property, Detail: detail}
	if code == codeDeferred {
		a.deferred = append(a.deferred, record)
	} else {
		a.losses = append(a.losses, record)
		a.f = maxFidelity(a.f, diag.FidelityApproximated)
	}
	a.finding(code, category, property, source, detail)
}

// note reports a finding that makes implicit source semantics explicit
// (TRL-CONV-0004) without lowering the node fidelity or recording a loss.
func (a *acc) note(code, category, property string, source diag.Source, detail string) {
	a.finding(code, a.c.sanitize(category), a.c.sanitize(property), source, a.c.sanitize(detail))
}

// finding reports one diagnostic anchored at the node's source and resource.
func (a *acc) finding(code, category, property string, source diag.Source, detail string) {
	finding := diag.MustNew(code, diag.WithSource(a.c.source(source)), diag.WithResource(a.c.resource),
		diag.WithMessage(detail), diag.WithParams(map[string]any{"construct": category, "property": property}))
	a.c.own.Add(finding)
	if a.c.opts.Collector != nil {
		a.c.opts.Collector.Add(finding)
	}
}

// withFidelity raises the accumulator fidelity without recording a loss. It is
// used for synthesized nodes whose fidelity the reader policy sets.
func (a *acc) withFidelity(f diag.Fidelity) *acc {
	a.f = maxFidelity(a.f, f)
	return a
}

func (a *acc) metadata() ir.Metadata {
	m := ir.DefaultMetadata()
	m.Origin = a.c.origin(a.source, a.chain)
	m.Fidelity = a.f
	if len(a.losses) != 0 || len(a.deferred) != 0 || a.values.Len() != 0 {
		bag := struct {
			Values   ir.OrderedMap[any] `json:"values,omitempty"`
			Losses   []loss             `json:"losses,omitempty"`
			Deferred []loss             `json:"deferred,omitempty"`
		}{a.values.Build(), a.losses, a.deferred}
		encoded, err := json.Marshal(bag)
		if err != nil {
			a.c.err = err
			return m
		}
		m.Extensions = ir.Extensions{ExtensionKey: encoded}
	}
	return m
}

func maxFidelity(left, right diag.Fidelity) diag.Fidelity {
	rank := func(value diag.Fidelity) int {
		switch value {
		case diag.FidelityApproximated:
			return 1
		case diag.FidelityNeedsManual:
			return 2
		case diag.FidelityUnsupported:
			return 3
		default:
			return 0
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}
