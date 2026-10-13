// Package program parses the modernization mapping language in modernize-ado.md.
// It performs no I/O and does not authorize or apply YAML edits.
package program

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Program is a class-checked mapping program. Operations retain source order.
type Program struct {
	Kind       string
	Message    string
	Operations []Operation
}

// Operation represents one mapping instruction. Only fields belonging to Kind
// are populated. Review uses Mode to distinguish default and type instructions.
type Operation struct {
	Kind   string
	Input  string
	Target string
	Value  Value
	Pairs  []Pair
	If     *Condition
	Unless *Condition
	Mode   string
	From   string
	To     string
}

// Pair maps one decoded string value to another.
type Pair struct {
	From string
	To   string
}

// Condition tests an original input, using =, != or multiline.
type Condition struct {
	Input string
	Op    string
	Value string
}

// Value preserves the program token type separately from its decoded text.
// Integer text is retained without a machine-sized integer conversion.
type Value struct {
	Kind string
	Text string
}

// Parse parses the entire program, enforcing the exact spacing and class rules
// of section 5.1. Null is restricted to review instructions by E10.
func Parse(source, class string) (Program, error) {
	p := parser{source: source}
	result := Program{Operations: []Operation{}}
	switch {
	case p.take("pass"):
		result.Kind = "pass"
		p.class(class, "A")
	case p.take("manual "):
		result.Kind = "manual"
		result.Message = p.qstring()
		p.class(class, "C")
	case p.take("guidance "):
		result.Kind = "guidance"
		result.Message = p.qstring()
		p.class(class, "D")
	default:
		result.Kind = "operations"
		p.class(class, "B")
		for p.err == nil {
			result.Operations = append(result.Operations, p.operation())
			if !p.take("; ") {
				break
			}
		}
	}
	if p.offset != len(source) {
		p.fail("end of program")
	}
	if p.err != nil {
		return Program{}, p.err
	}
	return result, nil
}

type parser struct {
	source string
	offset int
	err    error
}

func (p *parser) fail(expected string) {
	if p.err == nil {
		// Do not include program values in errors: catalog text could be untrusted.
		p.err = fmt.Errorf("mapping program at byte %d: expected %s", p.offset, expected)
	}
}

func (p *parser) class(actual, expected string) {
	if actual != expected {
		p.fail("class " + expected)
	}
}

func (p *parser) take(token string) bool {
	if p.err != nil || !strings.HasPrefix(p.source[p.offset:], token) {
		return false
	}
	p.offset += len(token)
	return true
}

func (p *parser) need(token string) {
	if !p.take(token) {
		p.fail(token)
	}
}

func letter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
func digit(b byte) bool  { return b >= '0' && b <= '9' }

func (p *parser) ident() string {
	start := p.offset
	if p.err != nil || start == len(p.source) || !letter(p.source[start]) {
		p.fail("ASCII identifier")
		return ""
	}
	p.offset++
	for p.offset < len(p.source) {
		b := p.source[p.offset]
		if !letter(b) && !digit(b) && b != '_' {
			break
		}
		p.offset++
	}
	return p.source[start:p.offset]
}

func (p *parser) qstring() string {
	start := p.offset
	if !p.take("\"") {
		p.fail("JSON string")
		return ""
	}
	for p.offset < len(p.source) {
		b := p.source[p.offset]
		p.offset++
		if b == '\\' {
			if p.offset < len(p.source) {
				p.offset++
			}
			continue
		}
		if b == '"' {
			token := p.source[start:p.offset]
			var value string
			if !utf8.ValidString(token) || json.Unmarshal([]byte(token), &value) != nil {
				p.fail("valid JSON string")
			}
			return value
		}
	}
	p.fail("closing JSON quote")
	return ""
}

func (p *parser) value() Value {
	if p.offset < len(p.source) && p.source[p.offset] == '"' {
		return Value{Kind: "string", Text: p.qstring()}
	}
	for _, token := range []string{"true", "false", "null", "K1"} {
		if p.take(token) {
			kind := "boolean"
			if token == "null" || token == "K1" {
				kind = token
			}
			return Value{Kind: kind, Text: token}
		}
	}
	start := p.offset
	p.take("-")
	digits := p.offset
	for p.offset < len(p.source) && digit(p.source[p.offset]) {
		p.offset++
	}
	if p.offset == digits {
		p.fail("program value")
	}
	return Value{Kind: "integer", Text: p.source[start:p.offset]}
}

func (p *parser) condition() *Condition {
	c := &Condition{Input: p.ident()}
	switch {
	case p.take(" = "):
		c.Op = "="
	case p.take(" != "):
		c.Op = "!="
	default:
		p.fail("comparison operator")
	}
	c.Value = p.qstring()
	return c
}

func (p *parser) operation() Operation {
	op := Operation{Kind: p.ident()}
	p.need(" ")
	if op.Kind == "guard" {
		if p.take("multiline(") {
			op.If = &Condition{Input: p.ident(), Op: "multiline"}
			p.need(")")
		} else {
			op.If = p.condition()
		}
		return op
	}
	op.Input = p.ident()
	switch op.Kind {
	case "rename", "invert":
		p.need(" -> ")
		op.Target = p.ident()
	case "map":
		p.need(" { ")
		for p.err == nil {
			pair := Pair{From: p.qstring()}
			p.need(" -> ")
			pair.To = p.qstring()
			op.Pairs = append(op.Pairs, pair)
			if !p.take(", ") {
				break
			}
		}
		p.need(" }")
	case "pin", "add":
		p.need(" = ")
		op.Value = p.value()
		if op.Value.Kind == "null" {
			p.fail("non-null inserted value")
		}
		if op.Kind == "pin" {
			if p.take(" if ") {
				op.If = p.condition()
			}
			if p.take(" unless ") {
				op.Unless = p.condition()
			}
		}
	case "drop", "require":
		if p.take(" if ") {
			op.If = p.condition()
		}
	case "literal":
	case "review":
		switch {
		case p.take(" default = "):
			op.Mode = "default"
			op.Value = p.value()
		case p.take(" type "):
			op.Mode = "type"
			op.From = p.qstring()
			p.need(" -> ")
			op.To = p.qstring()
		default:
			p.fail("review default or type")
		}
	default:
		p.fail("known operation")
	}
	return op
}
