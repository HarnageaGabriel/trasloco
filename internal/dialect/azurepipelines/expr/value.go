package expr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Value is a closed set of Azure expression values. Collections have identity.
type Value interface{ expressionValue() }

// Null represents a dictionary miss.
type Null struct{}

// Bool is an expression boolean.
type Bool bool

// String is an expression string.
type String string

// Number stores a normalized, exact decimal, never a binary floating point value.
// The zero value is zero. Its range and scale match System.Decimal.
type Number struct{ text string }

// Array preserves element order and reference identity.
type Array struct{ Items []Value }

// Property is one entry in an ordered Object.
type Property struct {
	Name  string
	Value Value
}

// Object preserves source order. Callers must not mutate values during evaluation.
type Object struct{ Properties []Property }

// DateTime represents the runtime pipeline.startTime value from the reference lineage.
type DateTime struct{ Time time.Time }

func (Null) expressionValue()     {}
func (Bool) expressionValue()     {}
func (String) expressionValue()   {}
func (Number) expressionValue()   {}
func (Version) expressionValue()  {}
func (*Array) expressionValue()   {}
func (*Object) expressionValue()  {}
func (DateTime) expressionValue() {}

// Lookup implements Context with ordinal, case-insensitive ordered lookup.
func (o *Object) Lookup(name string) (Value, bool) {
	if o != nil {
		for _, p := range o.Properties {
			if ordinalFold(p.Name) == ordinalFold(name) {
				return p.Value, true
			}
		}
	}
	return Null{}, false
}

// ParseNumber parses a decimal without exponent notation or grouping separators.
func ParseNumber(s string) (Number, error) {
	if len(s) > MaxExpressionLength {
		return Number{}, fmt.Errorf("number too long")
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, fraction, _ := strings.Cut(s, ".")
	if whole+fraction == "" {
		return Number{}, fmt.Errorf("invalid decimal")
	}
	for _, r := range whole + fraction {
		if r < '0' || r > '9' {
			return Number{}, fmt.Errorf("invalid decimal")
		}
	}
	coefficient, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return Number{}, fmt.Errorf("invalid decimal")
	}
	scale := len(fraction)
	maxDec, _ := new(big.Int).SetString("79228162514264337593543950335", 10)
	original := new(big.Int).Set(coefficient)
	for removed := 0; scale > 28 || coefficient.Cmp(maxDec) > 0; removed++ {
		if scale == 0 {
			return Number{}, fmt.Errorf("decimal overflow")
		}
		scale--
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(removed+1)), nil)
		q, rem := new(big.Int), new(big.Int)
		q.QuoRem(original, divisor, rem)
		comparison := rem.Lsh(rem, 1).Cmp(divisor)
		if comparison > 0 || comparison == 0 && q.Bit(0) == 1 {
			q.Add(q, big.NewInt(1))
		}
		coefficient = q
	}
	text := coefficient.String()
	if scale > 0 {
		text = strings.Repeat("0", maxInt(0, scale+1-len(text))) + text
		text = text[:len(text)-scale] + "." + text[len(text)-scale:]
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if negative && coefficient.Sign() != 0 {
		text = "-" + text
	}
	return Number{text: text}, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ExactString returns the full decimal precision for JSON and AST printing.
func (n Number) ExactString() string {
	if n.text == "" {
		return "0"
	}
	return n.text
}

// String implements the reference engine's invariant 0.####### rendering.
func (n Number) String() string {
	s := n.rat().FloatString(7)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}
func (n Number) rat() *big.Rat { r, _ := new(big.Rat).SetString(n.ExactString()); return r }
func integer(n int64) Number   { return Number{text: strconv.FormatInt(n, 10)} }

func stringify(v Value) (string, bool) {
	switch v := v.(type) {
	case Null:
		return "", true
	case String:
		return string(v), true
	case Bool:
		if v {
			return "True", true
		}
		return "False", true
	case Number:
		return v.String(), true
	case Version:
		return v.String(), true
	case DateTime:
		return v.Time.Format("2006-01-02 15:04:05-07:00"), true
	default:
		return "", false
	}
}

// DecodeJSON reads typed JSON while preserving object order and decimal precision.
func DecodeJSON(data []byte) (Value, error) {
	if len(data) > MaxEvaluationMemory {
		return nil, fmt.Errorf("JSON exceeds evaluation memory")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := decodeJSON(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return v, nil
}

func decodeJSON(d *json.Decoder, depth int) (Value, error) {
	if depth >= MaxDepth {
		return nil, fmt.Errorf("JSON exceeds depth limit")
	}
	token, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("JSON token: %w", err)
	}
	switch t := token.(type) {
	case nil:
		return Null{}, nil
	case bool:
		return Bool(t), nil
	case string:
		return String(t), nil
	case json.Number:
		return ParseNumber(string(t))
	case json.Delim:
		if t == '[' {
			a := &Array{}
			for d.More() {
				v, err := decodeJSON(d, depth+1)
				if err != nil {
					return nil, err
				}
				a.Items = append(a.Items, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, fmt.Errorf("JSON array: %w", err)
			}
			return a, nil
		}
		if t == '{' {
			o := &Object{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, fmt.Errorf("JSON key: %w", err)
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON key")
				}
				v, err := decodeJSON(d, depth+1)
				if err != nil {
					return nil, err
				}
				o.Properties = append(o.Properties, Property{name, v})
			}
			if _, err := d.Token(); err != nil {
				return nil, fmt.Errorf("JSON object: %w", err)
			}
			return o, nil
		}
	}
	return nil, fmt.Errorf("invalid JSON value")
}

// EncodeJSON serializes ordered values with Newtonsoft-compatible default escaping.
func EncodeJSON(v Value, indent bool) (string, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, v, 0); err != nil {
		return "", err
	}
	if indent {
		var out bytes.Buffer
		if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
			return "", fmt.Errorf("indent JSON: %w", err)
		}
		b = out
	}
	if 26+2*utf16Length(b.String()) > MaxEvaluationMemory {
		return "", fmt.Errorf("JSON exceeds evaluation memory")
	}
	return b.String(), nil
}

func writeJSON(b *bytes.Buffer, v Value, depth int) error {
	if depth >= MaxDepth || b.Len() > MaxEvaluationMemory {
		return fmt.Errorf("JSON exceeds resource limit")
	}
	switch v := v.(type) {
	case Null:
		b.WriteString("null")
	case Bool:
		b.WriteString(strconv.FormatBool(bool(v)))
	case Number:
		b.WriteString(v.ExactString())
	case String:
		writeJSONString(b, string(v))
	case Version:
		writeJSONString(b, v.String())
	case DateTime:
		writeJSONString(b, v.Time.Format(time.RFC3339Nano))
	case *Array:
		b.WriteByte('[')
		for i, item := range v.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, item, depth+1); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case *Object:
		b.WriteByte('{')
		for i, p := range v.Properties {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, p.Name)
			b.WriteByte(':')
			if err := writeJSON(b, p.Value, depth+1); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("invalid expression value")
	}
	return nil
}

func writeJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 32 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
