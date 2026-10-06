package yamlx

import (
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"go.yaml.in/yaml/v3"
)

// Node wraps a YAML node with safe typed access and source provenance.
type Node struct {
	raw             *yaml.Node
	commentFallback *yaml.Node
	ctx             *nodeContext
}

// Pair is one ordered mapping entry.
type Pair struct {
	Key   *Node
	Value *Node
}

// Kind returns the resolved node kind.
func (n *Node) Kind() Kind {
	resolved, _ := n.resolved()
	if resolved == nil {
		return 0
	}
	return Kind(resolved.Kind)
}

// Tag returns the resolved YAML tag, such as !!str.
func (n *Node) Tag() string {
	resolved, _ := n.resolved()
	if resolved == nil {
		return ""
	}
	return resolved.ShortTag()
}

// Value returns the resolved scalar value.
func (n *Node) Value() string {
	resolved, _ := n.resolved()
	if resolved == nil {
		return ""
	}
	return resolved.Value
}

// Position returns the position at which this node was written.
func (n *Node) Position() diag.Source {
	if n == nil {
		return diag.Source{}
	}
	return n.sourceOf(n.raw)
}

// Line returns the one-based source line.
func (n *Node) Line() int { return n.Position().Line }

// Column returns the one-based source column.
func (n *Node) Column() int { return n.Position().Col }

// Provenance returns the source position of the resolved value. For aliases and
// merge keys this is the anchor's original value position.
func (n *Node) Provenance() diag.Source {
	if n == nil {
		return diag.Source{}
	}
	resolved, _ := n.resolved()
	return n.sourceOf(resolved)
}

// Scalar returns a scalar's decoded text regardless of its YAML tag.
func (n *Node) Scalar() (string, []diag.Diagnostic) {
	resolved, problem := n.resolved()
	if problem != nil {
		return "", []diag.Diagnostic{*problem}
	}
	if resolved == nil || resolved.Kind != yaml.ScalarNode {
		return "", typeDiagnostic(n, "scalar")
	}
	return resolved.Value, nil
}

// HeadComment returns the comment preceding the node.
func (n *Node) HeadComment() string {
	if n == nil || n.raw == nil {
		return ""
	}
	if n.raw.HeadComment == "" && n.commentFallback != nil {
		return n.commentFallback.HeadComment
	}
	return n.raw.HeadComment
}

// LineComment returns the comment on the node's line.
func (n *Node) LineComment() string {
	if n == nil || n.raw == nil {
		return ""
	}
	if n.raw.LineComment == "" && n.commentFallback != nil {
		return n.commentFallback.LineComment
	}
	return n.raw.LineComment
}

// FootComment returns the comment following the node.
func (n *Node) FootComment() string {
	if n == nil || n.raw == nil {
		return ""
	}
	if n.raw.FootComment == "" && n.commentFallback != nil {
		return n.commentFallback.FootComment
	}
	return n.raw.FootComment
}

// String returns a string scalar or a positioned diagnostic.
func (n *Node) String() (string, []diag.Diagnostic) {
	resolved, problem := n.resolved()
	if problem != nil {
		return "", []diag.Diagnostic{*problem}
	}
	if resolved == nil || resolved.Kind != yaml.ScalarNode || resolved.ShortTag() != "!!str" {
		return "", typeDiagnostic(n, "string")
	}
	return resolved.Value, nil
}

// Bool returns a boolean scalar or a positioned diagnostic.
func (n *Node) Bool() (bool, []diag.Diagnostic) {
	resolved, problem := n.resolved()
	if problem != nil {
		return false, []diag.Diagnostic{*problem}
	}
	if resolved == nil || resolved.Kind != yaml.ScalarNode || resolved.ShortTag() != "!!bool" {
		return false, typeDiagnostic(n, "boolean")
	}
	value, err := strconv.ParseBool(resolved.Value)
	if err != nil {
		return false, typeDiagnostic(n, "boolean")
	}
	return value, nil
}

// Int returns an integer scalar or a positioned diagnostic.
func (n *Node) Int() (int64, []diag.Diagnostic) {
	resolved, problem := n.resolved()
	if problem != nil {
		return 0, []diag.Diagnostic{*problem}
	}
	if resolved == nil || resolved.Kind != yaml.ScalarNode || resolved.ShortTag() != "!!int" {
		return 0, typeDiagnostic(n, "integer")
	}
	value := strings.ReplaceAll(resolved.Value, "_", "")
	parsed, err := strconv.ParseInt(value, 0, 64)
	if err != nil {
		return 0, typeDiagnostic(n, "integer")
	}
	return parsed, nil
}

// Seq returns sequence items or a positioned diagnostic.
func (n *Node) Seq() ([]*Node, []diag.Diagnostic) {
	resolved, problem := n.resolved()
	if problem != nil {
		return nil, []diag.Diagnostic{*problem}
	}
	if resolved == nil || resolved.Kind != yaml.SequenceNode {
		return nil, typeDiagnostic(n, "sequence")
	}
	items := make([]*Node, 0, len(resolved.Content))
	for _, item := range resolved.Content {
		items = append(items, wrap(item, n.ctx))
	}
	return items, nil
}

// Map returns ordered explicit entries followed by inherited merge entries.
func (n *Node) Map() ([]Pair, []diag.Diagnostic) {
	resolved, problem := n.resolved()
	if problem != nil {
		return nil, []diag.Diagnostic{*problem}
	}
	if resolved == nil || resolved.Kind != yaml.MappingNode {
		return nil, typeDiagnostic(n, "mapping")
	}
	budget := n.aliasLimit()
	if budget <= 0 {
		budget = int(^uint(0) >> 1)
	}
	pairs, diagnostic := n.mappingPairs(resolved, make(map[*yaml.Node]bool), &budget, false, 1)
	if diagnostic != nil {
		return nil, []diag.Diagnostic{*diagnostic}
	}
	return pairs, nil
}

// Lookup returns a mapping value. Pass true for case-insensitive key matching.
func (n *Node) Lookup(key string, caseInsensitive ...bool) (*Node, []diag.Diagnostic) {
	pairs, diagnostics := n.Map()
	if len(diagnostics) > 0 {
		return nil, diagnostics
	}
	insensitive := len(caseInsensitive) > 0 && caseInsensitive[0]
	for _, pair := range pairs {
		candidate, problems := pair.Key.String()
		if len(problems) > 0 {
			continue
		}
		if candidate == key || (insensitive && strings.EqualFold(candidate, key)) {
			return pair.Value, nil
		}
	}
	return nil, nil
}

func (n *Node) mappingPairs(
	mapping *yaml.Node,
	active map[*yaml.Node]bool,
	budget *int,
	chargeExpansion bool,
	depth int,
) ([]Pair, *diag.Diagnostic) {
	if n.depthExceeded(depth) {
		diagnostic := newDiagnostic("TRL-PARSE-0010", n.sourceOf(mapping),
			diag.WithParams(map[string]any{"limit": n.depthLimit()}))
		return nil, &diagnostic
	}
	if active[mapping] {
		diagnostic := newDiagnostic("TRL-PARSE-0007", n.sourceOf(mapping))
		return nil, &diagnostic
	}
	active[mapping] = true
	defer delete(active, mapping)

	explicit := make([]Pair, 0, len(mapping.Content)/2)
	merged := make([]Pair, 0)
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if chargeExpansion {
			if *budget == 0 {
				diagnostic := newDiagnostic("TRL-PARSE-0007", n.sourceOf(mapping))
				return nil, &diagnostic
			}
			*budget--
		}
		key, value := mapping.Content[index], mapping.Content[index+1]
		if key.ShortTag() == "!!merge" || key.Value == "<<" {
			mergePairs, problem := n.mergedPairs(value, active, budget, depth+1)
			if problem != nil {
				return nil, problem
			}
			merged = append(merged, mergePairs...)
			continue
		}
		seen[key.Value] = struct{}{}
		explicit = append(explicit, Pair{Key: wrap(key, n.ctx), Value: wrap(value, n.ctx)})
	}
	for _, pair := range merged {
		key, problems := pair.Key.String()
		if len(problems) > 0 {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		explicit = append(explicit, pair)
	}
	return explicit, nil
}

func (n *Node) mergedPairs(
	node *yaml.Node,
	active map[*yaml.Node]bool,
	budget *int,
	depth int,
) ([]Pair, *diag.Diagnostic) {
	resolved, diagnostic := n.resolveRaw(node)
	if diagnostic != nil {
		return nil, diagnostic
	}
	switch resolved.Kind {
	case yaml.MappingNode:
		return n.mappingPairs(resolved, active, budget, true, depth)
	case yaml.SequenceNode:
		var result []Pair
		for _, child := range resolved.Content {
			mapping, problem := n.resolveRaw(child)
			if problem != nil {
				return nil, problem
			}
			if mapping.Kind != yaml.MappingNode {
				diagnostic := newDiagnostic("TRL-PARSE-0006", n.sourceOf(child),
					diag.WithParams(map[string]any{"actual": Kind(mapping.Kind).String(), "expected": "mapping"}))
				return nil, &diagnostic
			}
			pairs, problem := n.mappingPairs(mapping, active, budget, true, depth)
			if problem != nil {
				return nil, problem
			}
			result = append(result, pairs...)
		}
		return result, nil
	default:
		diagnostic := newDiagnostic("TRL-PARSE-0006", n.sourceOf(node),
			diag.WithParams(map[string]any{"actual": Kind(resolved.Kind).String(), "expected": "mapping"}))
		return nil, &diagnostic
	}
}

func (n *Node) resolved() (*yaml.Node, *diag.Diagnostic) {
	if n == nil {
		return nil, nil
	}
	return n.resolveRaw(n.raw)
}

func (n *Node) resolveRaw(node *yaml.Node) (*yaml.Node, *diag.Diagnostic) {
	seen := make(map[*yaml.Node]bool)
	for expansions := 0; node != nil && node.Kind == yaml.AliasNode; expansions++ {
		if node.Alias == nil || seen[node] || (n.aliasLimit() > 0 && expansions >= n.aliasLimit()) {
			diagnostic := newDiagnostic("TRL-PARSE-0007", n.sourceOf(node))
			return nil, &diagnostic
		}
		seen[node] = true
		node = node.Alias
	}
	return node, nil
}

func (n *Node) aliasLimit() int {
	if n == nil || n.ctx == nil {
		return 0
	}
	return n.ctx.aliasMax
}

func (n *Node) depthLimit() int {
	if n == nil || n.ctx == nil {
		return 0
	}
	return n.ctx.depthMax
}

func (n *Node) depthExceeded(depth int) bool {
	return n.depthLimit() > 0 && depth > n.depthLimit()
}
