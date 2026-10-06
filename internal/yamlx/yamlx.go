// Package yamlx provides position-aware, tolerant YAML parsing for dialect parsers.
package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"go.yaml.in/yaml/v3"
)

const (
	// DefaultSizeLimit is the maximum input accepted by Parse.
	DefaultSizeLimit int64 = 20 << 20
	// DefaultAliasExpansionLimit bounds the number of nodes reached through aliases.
	DefaultAliasExpansionLimit = 10_000
)

// Kind identifies a YAML node kind without exposing the underlying parser.
type Kind uint32

const (
	// DocumentKind is a YAML document root.
	DocumentKind Kind = Kind(yaml.DocumentNode)
	// SequenceKind is a YAML sequence.
	SequenceKind Kind = Kind(yaml.SequenceNode)
	// MappingKind is a YAML mapping.
	MappingKind Kind = Kind(yaml.MappingNode)
	// ScalarKind is a YAML scalar.
	ScalarKind Kind = Kind(yaml.ScalarNode)
	// AliasKind is a YAML alias before resolution.
	AliasKind Kind = Kind(yaml.AliasNode)
)

// Document contains every YAML document in one stream.
type Document struct {
	Name      string
	Documents []*Node

	raw      []*yaml.Node
	hadBOM   bool
	hadCRLF  bool
	indent   int
	aliasMax int
}

// Root returns the first document root, or nil for an empty stream.
func (d *Document) Root() *Node {
	if d == nil || len(d.Documents) == 0 {
		return nil
	}
	return d.Documents[0]
}

// ParseOption configures ParseWithOptions.
type ParseOption func(*parseConfig)

type parseConfig struct {
	sizeLimit  int64
	aliasLimit int
}

// WithSizeLimit sets the maximum input size in bytes. A non-positive value disables it.
func WithSizeLimit(limit int64) ParseOption {
	return func(config *parseConfig) { config.sizeLimit = limit }
}

// WithAliasExpansionLimit sets the maximum expanded node count. A non-positive value disables it.
func WithAliasExpansionLimit(limit int) ParseOption {
	return func(config *parseConfig) { config.aliasLimit = limit }
}

// Parse parses a YAML stream using safe default limits.
func Parse(name string, data []byte) (*Document, []diag.Diagnostic) {
	return ParseWithOptions(name, data)
}

// ParseWithOptions parses a YAML stream with explicit resource limits.
func ParseWithOptions(name string, data []byte, options ...ParseOption) (*Document, []diag.Diagnostic) {
	config := parseConfig{sizeLimit: DefaultSizeLimit, aliasLimit: DefaultAliasExpansionLimit}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.sizeLimit > 0 && int64(len(data)) > config.sizeLimit {
		diagnostic := newDiagnostic("TRL-PARSE-0005", diag.Source{File: name, Line: 1, Col: 1},
			diag.WithParams(map[string]any{"limit": config.sizeLimit, "size": len(data)}))
		return nil, []diag.Diagnostic{diagnostic}
	}

	hadBOM := bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if hadBOM {
		data = data[3:]
	}
	diagnostics := make([]diag.Diagnostic, 0)
	if !utf8.Valid(data) {
		offset := firstInvalidUTF8(data)
		line, column := offsetPosition(data, offset)
		diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0004", diag.Source{
			File: name, Line: line, Col: column,
		}))
		data = bytes.ToValidUTF8(data, []byte("\uFFFD"))
	}

	document := &Document{
		Name: name, hadBOM: hadBOM, hadCRLF: bytes.Contains(data, []byte("\r\n")),
		indent: detectIndent(data), aliasMax: config.aliasLimit,
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var raw yaml.Node
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			line, column := errorPosition(err)
			diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0001", diag.Source{
				File: name, Line: line, Col: column,
			}, diag.WithMessage(err.Error())))
			return nil, diagnostics
		}
		if len(raw.Content) == 0 {
			continue
		}
		document.raw = append(document.raw, &raw)
		root := wrap(raw.Content[0], name, config.aliasLimit)
		root.commentFallback = &raw
		document.Documents = append(document.Documents, root)
		diagnostics = append(diagnostics, duplicateDiagnostics(root)...)
		if config.aliasLimit > 0 {
			if source, expanded, exceeded := checkAliasExpansion(root, config.aliasLimit); exceeded {
				diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0007", source,
					diag.WithParams(map[string]any{"expanded": expanded, "limit": config.aliasLimit})))
			}
		}
	}
	return document, diagnostics
}

func wrap(raw *yaml.Node, name string, aliasMax int) *Node {
	if raw == nil {
		return nil
	}
	return &Node{raw: raw, name: name, aliasMax: aliasMax}
}

func newDiagnostic(code string, source diag.Source, options ...diag.Option) diag.Diagnostic {
	args := append([]diag.Option{diag.WithSource(source)}, options...)
	diagnostic, err := diag.New(code, args...)
	if err != nil {
		panic(fmt.Sprintf("yamlx: construct registered diagnostic %s: %v", code, err))
	}
	return diagnostic
}

func duplicateDiagnostics(root *Node) []diag.Diagnostic {
	var diagnostics []diag.Diagnostic
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node == nil {
			return
		}
		if node.Kind == yaml.MappingNode {
			seen := make(map[string]*yaml.Node, len(node.Content)/2)
			for index := 0; index+1 < len(node.Content); index += 2 {
				key, value := node.Content[index], node.Content[index+1]
				identity := nodeIdentity(key)
				if first, ok := seen[identity]; ok {
					source := sourceOf(root.name, key)
					diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0003", source,
						diag.WithMessage(fmt.Sprintf("duplicate mapping key %q; first defined at %d:%d", key.Value, first.Line, first.Column)),
						diag.WithParams(map[string]any{
							"key": key.Value, "firstLine": first.Line, "firstCol": first.Column,
							"duplicateLine": key.Line, "duplicateCol": key.Column,
						})))
				} else {
					seen[identity] = key
				}
				visit(key)
				visit(value)
			}
			return
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(root.raw)
	return diagnostics
}

func nodeIdentity(node *yaml.Node) string {
	if node == nil {
		return "nil"
	}
	var identity strings.Builder
	identity.WriteString(strconv.Itoa(int(node.Kind)))
	identity.WriteByte(':')
	identity.WriteString(strconv.Quote(node.ShortTag()))
	identity.WriteByte(':')
	identity.WriteString(strconv.Quote(node.Value))
	identity.WriteByte('[')
	for _, child := range node.Content {
		identity.WriteString(nodeIdentity(child))
		identity.WriteByte(',')
	}
	identity.WriteByte(']')
	return identity.String()
}

func detectIndent(data []byte) int {
	indent := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		spaces := 0
		for spaces < len(line) && line[spaces] == ' ' {
			spaces++
		}
		if spaces > 0 && spaces < len(line) && line[spaces] != '#' && (indent == 0 || spaces < indent) {
			indent = spaces
		}
	}
	if indent < 2 {
		return 2
	}
	return indent
}

func checkAliasExpansion(root *Node, limit int) (diag.Source, int, bool) {
	count := 0
	active := make(map[*yaml.Node]bool)
	offender := root.raw
	var expand func(*yaml.Node) bool
	expand = func(node *yaml.Node) bool {
		if node == nil {
			return false
		}
		count++
		if count > limit {
			offender = node
			return true
		}
		if node.Kind == yaml.AliasNode {
			if node.Alias == nil || active[node.Alias] {
				count = limit + 1
				offender = node
				return true
			}
			active[node.Alias] = true
			exceeded := expand(node.Alias)
			delete(active, node.Alias)
			return exceeded
		}
		for _, child := range node.Content {
			if expand(child) {
				return true
			}
		}
		return false
	}
	var scan func(*yaml.Node) bool
	scan = func(node *yaml.Node) bool {
		if node == nil {
			return false
		}
		if node.Kind == yaml.AliasNode {
			offender = node
			return expand(node.Alias)
		}
		for _, child := range node.Content {
			if scan(child) {
				return true
			}
		}
		return false
	}
	exceeded := scan(root.raw)
	return sourceOf(root.name, offender), count, exceeded
}

func sourceOf(name string, node *yaml.Node) diag.Source {
	if node == nil {
		return diag.Source{File: name}
	}
	return diag.Source{File: name, Line: node.Line, Col: node.Column}
}

func firstInvalidUTF8(data []byte) int {
	for offset := 0; offset < len(data); {
		_, size := utf8.DecodeRune(data[offset:])
		if size == 1 && data[offset] >= utf8.RuneSelf {
			return offset
		}
		offset += size
	}
	return 0
}

func offsetPosition(data []byte, offset int) (int, int) {
	line, column := 1, 1
	for index := 0; index < offset; index++ {
		if data[index] == '\n' {
			line, column = line+1, 1
		} else if data[index] != '\r' {
			column++
		}
	}
	return line, column
}

var linePattern = regexp.MustCompile(`line ([0-9]+)(?::([0-9]+))?`)

func errorPosition(err error) (int, int) {
	match := linePattern.FindStringSubmatch(err.Error())
	if len(match) == 0 {
		return 1, 1
	}
	line, _ := strconv.Atoi(match[1])
	column := 1
	if len(match) > 2 && match[2] != "" {
		column, _ = strconv.Atoi(match[2])
	}
	return line, column
}

func typeDiagnostic(node *Node, want string) []diag.Diagnostic {
	if node == nil {
		return []diag.Diagnostic{newDiagnostic("TRL-PARSE-0006", diag.Source{}, diag.WithParams(map[string]any{
			"actual": "missing", "expected": want,
		}))}
	}
	actual := strings.TrimPrefix(node.Tag(), "!!")
	if actual == "" {
		actual = strings.ToLower(node.Kind().String())
	}
	return []diag.Diagnostic{newDiagnostic("TRL-PARSE-0006", node.Position(), diag.WithParams(map[string]any{
		"actual": actual, "expected": want,
	}))}
}

// String returns a stable display name for a Kind.
func (kind Kind) String() string {
	switch kind {
	case DocumentKind:
		return "document"
	case SequenceKind:
		return "sequence"
	case MappingKind:
		return "mapping"
	case ScalarKind:
		return "scalar"
	case AliasKind:
		return "alias"
	default:
		return "unknown"
	}
}
