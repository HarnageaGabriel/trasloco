package corpus

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"go.yaml.in/yaml/v3"
)

const indexFile = "index.yaml"

// Expect describes whether a fixture is accepted or which diagnostics it produces.
type Expect struct {
	OK          bool
	Diagnostics []string
}

// Entry describes one fixture in an Index.
type Entry struct {
	Path     string   `yaml:"path"`
	Category string   `yaml:"category"`
	Features []string `yaml:"features"`
	Expect   Expect   `yaml:"expect"`
	Sources  []string `yaml:"source"`
}

// Index is the corpus manifest.
type Index struct {
	Checklist []string `yaml:"checklist"`
	Files     []Entry  `yaml:"files"`
}

// Header is the metadata encoded in a fixture's first three lines.
type Header struct {
	Sources  []string
	Features []string
	Expect   Expect
}

// Load reads index.yaml from fsys.
func Load(fsys fs.FS) (*Index, error) {
	data, err := fs.ReadFile(fsys, indexFile)
	if err != nil {
		return nil, fmt.Errorf("read corpus index: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var index Index
	if err := decoder.Decode(&index); err != nil {
		return nil, fmt.Errorf("decode corpus index: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode corpus index trailer: %w", err)
		}
		return nil, errors.New("decode corpus index: multiple YAML documents")
	}
	return &index, nil
}

// ParseHeader parses the strict three-line metadata header at the start of a fixture.
func ParseHeader(content []byte) (Header, error) {
	if bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		return Header{}, errors.New("parse corpus header: BOM is not allowed")
	}
	if bytes.Contains(content, []byte("\r\n")) {
		return Header{}, errors.New("parse corpus header: CRLF is not allowed")
	}

	lines := bytes.SplitN(content, []byte("\n"), 4)
	if len(lines) < 4 {
		return Header{}, errors.New("parse corpus header: missing header line or fixture body")
	}

	source, err := parseScalarLine(string(lines[0]), "# source: ")
	if err != nil {
		return Header{}, err
	}
	features, err := parseListLine(string(lines[1]), "# features: ")
	if err != nil {
		return Header{}, err
	}

	expectLine := string(lines[2])
	expect := Expect{}
	switch {
	case expectLine == "# expect: ok":
		expect.OK = true
	case strings.HasPrefix(expectLine, "# expect: diagnostics: "):
		expect.Diagnostics, err = parseInlineList(strings.TrimPrefix(expectLine, "# expect: diagnostics: "))
		if err != nil {
			return Header{}, fmt.Errorf("parse corpus header expect: %w", err)
		}
	default:
		return Header{}, fmt.Errorf("parse corpus header: invalid expect line %q", expectLine)
	}

	return Header{Sources: []string{source}, Features: features, Expect: expect}, nil
}

func parseScalarLine(line, prefix string) (string, error) {
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("parse corpus header: want prefix %q", prefix)
	}
	value := strings.TrimPrefix(line, prefix)
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("parse corpus header: invalid value for %q", prefix)
	}
	return value, nil
}

func parseListLine(line, prefix string) ([]string, error) {
	if !strings.HasPrefix(line, prefix) {
		return nil, fmt.Errorf("parse corpus header: want prefix %q", prefix)
	}
	values, err := parseInlineList(strings.TrimPrefix(line, prefix))
	if err != nil {
		return nil, fmt.Errorf("parse corpus header %q: %w", prefix, err)
	}
	return values, nil
}

func parseInlineList(value string) ([]string, error) {
	if len(value) < 3 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, fmt.Errorf("invalid inline list %q", value)
	}
	items := strings.Split(value[1:len(value)-1], ", ")
	for _, item := range items {
		if item == "" || strings.TrimSpace(item) != item || strings.ContainsAny(item, "[],") {
			return nil, fmt.Errorf("invalid inline list %q", value)
		}
	}
	return items, nil
}

// UnmarshalYAML decodes the scalar "ok" or a diagnostics mapping.
func (expect *Expect) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Value == "ok" {
		*expect = Expect{OK: true}
		return nil
	}
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 || node.Content[0].Value != "diagnostics" {
		return errors.New("expect must be 'ok' or a diagnostics mapping")
	}
	var diagnostics []string
	if err := node.Content[1].Decode(&diagnostics); err != nil {
		return fmt.Errorf("decode expected diagnostics: %w", err)
	}
	if len(diagnostics) == 0 {
		return errors.New("expected diagnostics must not be empty")
	}
	*expect = Expect{Diagnostics: diagnostics}
	return nil
}
