package yamlx

import (
	"bytes"
	"errors"
	"fmt"
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
func Emit(document *Document, options ...EmitOption) ([]byte, error) {
	if document == nil {
		return nil, errors.New("yamlx: cannot emit a nil document")
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
		clone, err := cloneNode(raw, document.depthMax, 1)
		if err != nil {
			return nil, err
		}
		if err := quoteAmbiguousStrings(clone, document.depthMax, 1); err != nil {
			return nil, err
		}
		if err := encoder.Encode(clone); err != nil {
			return nil, fmt.Errorf("yamlx: encode document: %w", err)
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("yamlx: close encoder: %w", err)
	}
	data := output.Bytes()
	if !normalize && document.hadCRLF {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}
	if !normalize && document.hadBOM {
		data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	}
	return data, nil
}

func cloneNode(node *yaml.Node, depthLimit, depth int) (*yaml.Node, error) {
	if node == nil {
		return nil, nil
	}
	if depthLimit > 0 && depth > depthLimit {
		return nil, fmt.Errorf("yamlx: node depth exceeds limit %d", depthLimit)
	}
	clone := *node
	clone.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		var err error
		clone.Content[index], err = cloneNode(child, depthLimit, depth+1)
		if err != nil {
			return nil, err
		}
	}
	// Alias pointers deliberately target original anchors. Encoder uses the alias
	// name, while cloned content retains the authoritative anchor definitions.
	return &clone, nil
}

var yaml11Ambiguous = regexp.MustCompile(`(?i)^(?:|y|yes|n|no|true|false|on|off|null|~|[-+]?(?:0b[01_]+|0o[0-7_]+|0x[0-9a-f_]+|[0-9][0-9_]*(?::[0-5]?[0-9])+)|[-+]?(?:[0-9][0-9_]*)(?:\.[0-9_]*)?(?:e[-+]?[0-9]+)?|[-+]?\.[0-9_]+(?:e[-+]?[0-9]+)?|[-+]?\.(?:inf|nan))$`)

func quoteAmbiguousStrings(node *yaml.Node, depthLimit, depth int) error {
	if node == nil {
		return nil
	}
	if depthLimit > 0 && depth > depthLimit {
		return fmt.Errorf("yamlx: node depth exceeds limit %d", depthLimit)
	}
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!str" && yaml11Ambiguous.MatchString(strings.TrimSpace(node.Value)) {
		node.Style &^= yaml.TaggedStyle
		node.Style |= yaml.DoubleQuotedStyle
	}
	for _, child := range node.Content {
		if err := quoteAmbiguousStrings(child, depthLimit, depth+1); err != nil {
			return err
		}
	}
	return nil
}
