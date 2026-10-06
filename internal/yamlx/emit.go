package yamlx

import (
	"bytes"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// EmitOption configures Emit.
type EmitOption uint8

const (
	// Normalize selects stable two-space indentation, LF line endings, and no BOM.
	Normalize EmitOption = iota + 1
)

// WithNormalize returns the Normalize option.
func WithNormalize() EmitOption {
	return Normalize
}

// Emit encodes a document while preserving ordering, comments, and safe strings.
func Emit(document *Document, options ...EmitOption) []byte {
	if document == nil {
		return nil
	}
	normalize := false
	for _, option := range options {
		if option == Normalize {
			normalize = true
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	indent := document.indent
	if normalize || indent < 2 {
		indent = 2
	}
	encoder.SetIndent(indent)
	for _, raw := range document.raw {
		clone := cloneNode(raw)
		quoteAmbiguousStrings(clone)
		if err := encoder.Encode(clone); err != nil {
			return nil
		}
	}
	if err := encoder.Close(); err != nil {
		return nil
	}
	data := output.Bytes()
	if !normalize && document.hadCRLF {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}
	if !normalize && document.hadBOM {
		data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	}
	return data
}

func cloneNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	clone.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		clone.Content[index] = cloneNode(child)
	}
	// Alias pointers deliberately target original anchors. Encoder uses the alias
	// name, while cloned content retains the authoritative anchor definitions.
	return &clone
}

var yaml11Ambiguous = regexp.MustCompile(`(?i)^(?:|y|yes|n|no|true|false|on|off|null|~|[-+]?(?:0b[01_]+|0o[0-7_]+|0x[0-9a-f_]+|[0-9][0-9_]*(?::[0-5]?[0-9])+)|[-+]?(?:[0-9][0-9_]*)(?:\.[0-9_]*)?(?:e[-+]?[0-9]+)?|[-+]?\.[0-9_]+(?:e[-+]?[0-9]+)?|[-+]?\.(?:inf|nan))$`)

func quoteAmbiguousStrings(node *yaml.Node) {
	if node == nil {
		return
	}
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!str" && yaml11Ambiguous.MatchString(strings.TrimSpace(node.Value)) {
		node.Style &^= yaml.TaggedStyle
		node.Style |= yaml.DoubleQuotedStyle
	}
	for _, child := range node.Content {
		quoteAmbiguousStrings(child)
	}
}
