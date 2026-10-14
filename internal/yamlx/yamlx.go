// Package yamlx provides position-aware, tolerant YAML parsing for dialect parsers.
package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
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
	// DefaultDepthLimit bounds recursive YAML tree walks.
	DefaultDepthLimit = 1_000
)

// AnchorPolicy controls acceptance of YAML anchors, aliases, and merge keys.
type AnchorPolicy uint8

const (
	// AnchorAllow accepts anchors, aliases, and merge keys.
	AnchorAllow AnchorPolicy = iota
	// AnchorError reports anchors, aliases, and merge keys.
	AnchorError
	// AnchorAllowWithoutMerge accepts anchors and aliases but reports merge keys.
	AnchorAllowWithoutMerge
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
	depthMax int
	ctx      *nodeContext
}

type nodeContext struct {
	name         string
	data         []byte
	original     []byte
	baseOffset   int
	aliasMax     int
	depthMax     int
	lineStarts   []int
	nodeOffsets  []int
	nodeContexts map[*yaml.Node]lexicalContext
	// nodeOffset caches the start offset of every indexed node. Locating a node
	// by walking from the start of its line is linear in the column, which turns
	// a flow sequence (all items on one line) into quadratic work.
	nodeOffset map[*yaml.Node]int
	// runePrefixes holds, every runeStride bytes, the number of code-point
	// starts seen so far. It makes the byte-offset-to-line/column conversion
	// independent of the column, which is what keeps a flow sequence (many
	// nodes on one line) linear.
	runePrefixes []int32
	// lineIndents is the leading-space indentation of every line, in line order.
	lineIndents []int
	// scanWork counts the bytes walked while resolving positions. It is a
	// regression counter for tests; it has no effect on parsing.
	scanWork int
}

// runeStride is the block size of nodeContext.runePrefixes.
const runeStride = 128

// buildRunePrefixes records the number of code-point starts (excluding
// carriage returns) at every runeStride boundary of data.
func buildRunePrefixes(data []byte) []int32 {
	prefixes := make([]int32, 1, len(data)/runeStride+2)
	count := int32(0)
	for start := 0; start < len(data); start += runeStride {
		end := min(start+runeStride, len(data))
		for index := start; index < end; index++ {
			if data[index] != '\r' && utf8.RuneStart(data[index]) {
				count++
			}
		}
		prefixes = append(prefixes, count)
	}
	return prefixes
}

type lexicalContext struct {
	flow bool
	root bool
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
	sizeLimit    int64
	aliasLimit   int
	depthLimit   int
	anchorPolicy AnchorPolicy
}

// WithSizeLimit sets the maximum input size in bytes. A non-positive value disables it.
func WithSizeLimit(limit int64) ParseOption {
	return func(config *parseConfig) { config.sizeLimit = limit }
}

// WithAliasExpansionLimit sets the maximum expanded node count. A non-positive value disables it.
func WithAliasExpansionLimit(limit int) ParseOption {
	return func(config *parseConfig) { config.aliasLimit = limit }
}

// WithDepthLimit sets the maximum node nesting reached by YAML tree walkers.
// A non-positive value disables it.
func WithDepthLimit(limit int) ParseOption {
	return func(config *parseConfig) { config.depthLimit = limit }
}

// WithAnchorPolicy applies a dialect's anchor and merge-key policy.
func WithAnchorPolicy(policy AnchorPolicy) ParseOption {
	return func(config *parseConfig) { config.anchorPolicy = policy }
}

// Parse parses a YAML stream using safe default limits.
func Parse(name string, data []byte) (*Document, []diag.Diagnostic) {
	return ParseWithOptions(name, data)
}

// ParseWithOptions parses a YAML stream with explicit resource limits.
func ParseWithOptions(name string, data []byte, options ...ParseOption) (*Document, []diag.Diagnostic) {
	config := parseConfig{
		sizeLimit: DefaultSizeLimit, aliasLimit: DefaultAliasExpansionLimit,
		depthLimit: DefaultDepthLimit, anchorPolicy: AnchorAllow,
	}
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

	original := append([]byte(nil), data...)
	hadBOM := bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	baseOffset := 0
	if hadBOM {
		data = data[3:]
		baseOffset = 3
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

	lineStarts := sourceLineStarts(data)
	document := &Document{
		Name: name, hadBOM: hadBOM, hadCRLF: bytes.Contains(data, []byte("\r\n")),
		indent: detectIndent(data), aliasMax: config.aliasLimit, depthMax: config.depthLimit,
	}
	document.ctx = &nodeContext{
		name: name, data: data, original: original, baseOffset: baseOffset,
		aliasMax: config.aliasLimit, depthMax: config.depthLimit,
		lineStarts: lineStarts, nodeContexts: make(map[*yaml.Node]lexicalContext),
		nodeOffset:   make(map[*yaml.Node]int),
		runePrefixes: buildRunePrefixes(data), lineIndents: sourceLineIndents(data, lineStarts),
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
		document.ctx.indexNodes(raw.Content[0])
		root := wrap(raw.Content[0], document.ctx)
		root.commentFallback = &raw
		document.Documents = append(document.Documents, root)
		offender, depthExceeded := checkDepth(root, config.depthLimit)
		if depthExceeded {
			diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0010", root.sourceOf(offender),
				diag.WithParams(map[string]any{"limit": config.depthLimit})))
		} else {
			diagnostics = append(diagnostics, duplicateDiagnostics(root)...)
		}
		diagnostics = append(diagnostics, anchorDiagnostics(root, config.anchorPolicy)...)
		if config.aliasLimit > 0 && !depthExceeded {
			if source, expanded, exceeded := checkAliasExpansion(root, config.aliasLimit); exceeded {
				diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0007", source,
					diag.WithParams(map[string]any{"expanded": expanded, "limit": config.aliasLimit})))
			}
		}
	}
	return document, diagnostics
}

func wrap(raw *yaml.Node, ctx *nodeContext) *Node {
	if raw == nil {
		return nil
	}
	return &Node{raw: raw, ctx: ctx}
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
					source := root.sourceOf(key)
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
	return root.sourceOf(offender), count, exceeded
}

func sourceOf(name string, node *yaml.Node) diag.Source {
	if node == nil {
		return diag.Source{File: name}
	}
	return diag.Source{File: name, Line: node.Line, Col: node.Column}
}

func (n *Node) sourceOf(node *yaml.Node) diag.Source {
	if n == nil || n.ctx == nil {
		return sourceOf("", node)
	}
	if node == nil {
		return diag.Source{File: n.ctx.name}
	}
	start := n.ctx.offsetOf(node) + n.ctx.baseOffset
	end := start
	if byteRange, err := rawNodeRange(n.ctx, node); err == nil {
		start, end = byteRange.Start, byteRange.End
	}
	start = min(start, len(n.ctx.original))
	end = min(end, len(n.ctx.original))
	endLine, endCol := n.ctx.offsetPosition(max(0, end-n.ctx.baseOffset))
	return diag.Source{
		File: n.ctx.name, Line: node.Line, Col: node.Column,
		EndLine: endLine, EndCol: endCol, Offset: start, EndOffset: end,
	}
}

func (ctx *nodeContext) positionOffset(line, column int) int {
	if line < 1 {
		return 0
	}
	if line > len(ctx.lineStarts) {
		return len(ctx.data)
	}
	offset := ctx.lineStarts[line-1]
	for current := 1; current < column && offset < len(ctx.data) && ctx.data[offset] != '\n'; current++ {
		_, size := utf8.DecodeRune(ctx.data[offset:])
		offset += size
		ctx.scanWork++
	}
	return offset
}

func checkDepth(root *Node, limit int) (*yaml.Node, bool) {
	if root == nil || root.raw == nil || limit <= 0 {
		return nil, false
	}
	type item struct {
		node  *yaml.Node
		depth int
	}
	stack := []item{{root.raw, 1}}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current.depth > limit {
			return current.node, true
		}
		for index := len(current.node.Content) - 1; index >= 0; index-- {
			stack = append(stack, item{current.node.Content[index], current.depth + 1})
		}
	}
	return nil, false
}

func anchorDiagnostics(root *Node, policy AnchorPolicy) []diag.Diagnostic {
	if root == nil || root.raw == nil || policy == AnchorAllow {
		return nil
	}
	var diagnostics []diag.Diagnostic
	stack := []*yaml.Node{root.raw}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		isMerge := node.ShortTag() == "!!merge" || node.Value == "<<"
		if isMerge {
			diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0009", root.sourceOf(node)))
		} else if policy == AnchorError && (node.Anchor != "" || node.Kind == yaml.AliasNode) {
			diagnostics = append(diagnostics, newDiagnostic("TRL-PARSE-0008", root.sourceOf(node)))
		}
		for index := len(node.Content) - 1; index >= 0; index-- {
			stack = append(stack, node.Content[index])
		}
	}
	return diagnostics
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
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	line, column := 1, 1
	for index := 0; index < offset; index++ {
		if data[index] == '\n' {
			line, column = line+1, 1
		} else if data[index] != '\r' && utf8.RuneStart(data[index]) {
			column++
		}
	}
	return line, column
}

func sourceLineStarts(data []byte) []int {
	starts := []int{0}
	for index, char := range data {
		if char == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}

// runeCount returns the number of code-point starts (excluding carriage returns)
// in the data before offset. The strided prefix index bounds the work to one
// block, so a node late in a long line costs the same as the first one.
func (ctx *nodeContext) runeCount(offset int) int {
	offset = max(0, min(offset, len(ctx.data)))
	block := offset / runeStride
	if block >= len(ctx.runePrefixes) {
		block = len(ctx.runePrefixes) - 1
	}
	count := int(ctx.runePrefixes[block])
	for index := block * runeStride; index < offset; index++ {
		if ctx.data[index] != '\r' && utf8.RuneStart(ctx.data[index]) {
			count++
		}
		ctx.scanWork++
	}
	return count
}

// offsetPosition converts a byte offset into a one-based line and column.
func (ctx *nodeContext) offsetPosition(offset int) (int, int) {
	offset = max(0, min(offset, len(ctx.data)))
	line := sort.Search(len(ctx.lineStarts), func(i int) bool { return ctx.lineStarts[i] > offset })
	if line == 0 {
		return 1, 1 + ctx.runeCount(offset)
	}
	return line, 1 + ctx.runeCount(offset) - ctx.runeCount(ctx.lineStarts[line-1])
}

// indexNodes records the lexical context and the start offset of every node of
// one document. Offsets are resolved in a single pass over the source instead of
// walking from the start of each line: a flow sequence keeps every item on one
// line, and walking to each column separately is quadratic in the item count.
func (ctx *nodeContext) indexNodes(root *yaml.Node) {
	type item struct {
		node *yaml.Node
		flow bool
	}
	stack := []item{{node: root}}
	var nodes []*yaml.Node
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		flow := current.flow || current.node.Style&yaml.FlowStyle != 0
		ctx.nodeContexts[current.node] = lexicalContext{flow: flow, root: current.node == root}
		nodes = append(nodes, current.node)
		for _, child := range current.node.Content {
			stack = append(stack, item{node: child, flow: flow})
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Line != nodes[j].Line {
			return nodes[i].Line < nodes[j].Line
		}
		return nodes[i].Column < nodes[j].Column
	})
	line, column, offset := -1, 1, 0
	for _, node := range nodes {
		if node.Line != line {
			line, column, offset = node.Line, 1, ctx.lineStart(node.Line)
		}
		for column < node.Column && offset < len(ctx.data) && ctx.data[offset] != '\n' {
			_, size := utf8.DecodeRune(ctx.data[offset:])
			if size <= 0 {
				size = 1
			}
			offset += size
			column++
			ctx.scanWork++
		}
		ctx.nodeOffset[node] = offset
		ctx.nodeOffsets = append(ctx.nodeOffsets, offset)
	}
	sort.Ints(ctx.nodeOffsets)
}

// sourceLineIndents records the leading-space indentation of every line.
func sourceLineIndents(data []byte, lineStarts []int) []int {
	indents := make([]int, len(lineStarts))
	for index, start := range lineStarts {
		indent := 0
		for start+indent < len(data) && data[start+indent] == ' ' {
			indent++
		}
		indents[index] = indent
	}
	return indents
}

// lineIndentAt returns the indentation of the line that contains offset. The
// per-line indentation is precomputed: walking back to the line start for every
// node makes a flow sequence (all nodes on one line) quadratic.
func (ctx *nodeContext) lineIndentAt(offset int) int {
	line := sort.Search(len(ctx.lineStarts), func(i int) bool { return ctx.lineStarts[i] > offset })
	if line == 0 || line > len(ctx.lineIndents) {
		return lineIndent(ctx.data, offset)
	}
	return ctx.lineIndents[line-1]
}

// lineStart returns the byte offset at which a one-based line begins.
func (ctx *nodeContext) lineStart(line int) int {
	if line < 1 {
		return 0
	}
	if line > len(ctx.lineStarts) {
		return len(ctx.data)
	}
	return ctx.lineStarts[line-1]
}

// offsetOf returns the start offset of a node, falling back to a column walk for
// nodes that were not indexed (for example a synthetic node).
func (ctx *nodeContext) offsetOf(node *yaml.Node) int {
	if offset, ok := ctx.nodeOffset[node]; ok {
		return offset
	}
	return ctx.positionOffset(node.Line, node.Column)
}

func (ctx *nodeContext) nextOffset(start int) int {
	index := sort.Search(len(ctx.nodeOffsets), func(i int) bool { return ctx.nodeOffsets[i] > start })
	if index == len(ctx.nodeOffsets) {
		return len(ctx.data)
	}
	return ctx.nodeOffsets[index]
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
