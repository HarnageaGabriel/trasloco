package corpus

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
	"github.com/google/go-cmp/cmp"
)

const corpusDir = "../../testdata/azurepipelines/corpus"

func TestParseHeader(t *testing.T) {
	t.Parallel()

	valid := "# source: https://example.com/spec\n# features: [root-steps, task-bash]\n# expect: ok\nsteps: []\n"
	tests := []struct {
		name    string
		content string
		want    Header
		wantErr bool
	}{
		{
			name:    "ok",
			content: valid,
			want: Header{
				Sources:  []string{"https://example.com/spec"},
				Features: []string{"root-steps", "task-bash"},
				Expect:   Expect{OK: true},
			},
		},
		{
			name:    "template placeholder",
			content: strings.Replace(valid, "# expect: ok", "# expect: template-placeholder", 1),
			want: Header{
				Sources:  []string{"https://example.com/spec"},
				Features: []string{"root-steps", "task-bash"},
				Expect:   Expect{TemplatePlaceholder: true},
			},
		},
		{
			name:    "diagnostics",
			content: "# source: https://example.com/spec\n# features: [invalid-yaml]\n# expect: diagnostics: [TRL-PARSE-0006, TRL-PARSE-0002]\n# stage: parse\nfoo: bar\n",
			want: Header{
				Sources:  []string{"https://example.com/spec"},
				Features: []string{"invalid-yaml"},
				Expect:   Expect{Diagnostics: []string{"TRL-PARSE-0006", "TRL-PARSE-0002"}},
				Stage:    "parse",
			},
		},
		{name: "BOM", content: "\ufeff" + valid, wantErr: true},
		{name: "CRLF", content: strings.ReplaceAll(valid, "\n", "\r\n"), wantErr: true},
		{name: "missing line", content: "# source: https://example.com\n# features: [root-steps]\n", wantErr: true},
		{name: "bad source", content: strings.Replace(valid, "# source: ", "# sources: ", 1), wantErr: true},
		{name: "empty source", content: strings.Replace(valid, "https://example.com/spec", "", 1), wantErr: true},
		{name: "bad features", content: strings.Replace(valid, "[root-steps, task-bash]", "root-steps", 1), wantErr: true},
		{name: "bad list separator", content: strings.Replace(valid, ", ", ",", 1), wantErr: true},
		{name: "bad expect", content: strings.Replace(valid, "# expect: ok", "# expect: warning", 1), wantErr: true},
		{name: "empty diagnostics", content: strings.Replace(valid, "# expect: ok", "# expect: diagnostics: []", 1), wantErr: true},
		{name: "negative missing stage", content: strings.Replace(valid, "# expect: ok", "# expect: diagnostics: [TRL-PARSE-0006]", 1), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseHeader([]byte(test.content))
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseHeader() error = nil, want error; got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHeader() error = %v", err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("ParseHeader() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	index, err := Load(os.DirFS(corpusDir))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(index.Files) != 319 {
		t.Errorf("len(Index.Files) = %d, want 319", len(index.Files))
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fsys fs.FS
	}{
		{name: "missing", fsys: fstest.MapFS{}},
		{name: "unknown field", fsys: fstest.MapFS{indexFile: {Data: []byte("unknown: true\n")}}},
		{name: "bad expect", fsys: fstest.MapFS{indexFile: {Data: []byte("files:\n  - expect: no\n")}}},
		{name: "empty diagnostics", fsys: fstest.MapFS{indexFile: {Data: []byte("files:\n  - expect: {diagnostics: []}\n")}}},
		{name: "bad diagnostics", fsys: fstest.MapFS{indexFile: {Data: []byte("files:\n  - expect: {diagnostics: value}\n")}}},
		{name: "invalid trailer", fsys: fstest.MapFS{indexFile: {Data: []byte("files: []\n---\n:\n")}}},
		{name: "multiple documents", fsys: fstest.MapFS{indexFile: {Data: []byte("files: []\n---\nfiles: []\n")}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(test.fsys); err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}

func TestCorpusIntegrity(t *testing.T) {
	t.Parallel()

	corpusFS := os.DirFS(corpusDir)
	index, err := Load(corpusFS)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	diskFiles := fixturePaths(t, corpusFS)
	indexedFiles := make([]string, 0, len(index.Files))
	entries := make(map[string]Entry, len(index.Files))
	for _, entry := range index.Files {
		indexedFiles = append(indexedFiles, entry.Path)
		if _, exists := entries[entry.Path]; exists {
			t.Errorf("duplicate indexed path %q", entry.Path)
		}
		entries[entry.Path] = entry
		if _, err := fs.Stat(corpusFS, entry.Path); err != nil {
			t.Errorf("indexed path %q: %v", entry.Path, err)
		}
	}
	wantOrder := append([]string(nil), indexedFiles...)
	sort.Strings(wantOrder)
	if diff := cmp.Diff(wantOrder, indexedFiles); diff != "" {
		t.Errorf("indexed paths are not sorted (-want +got):\n%s", diff)
	}
	sort.Strings(indexedFiles)
	if diff := cmp.Diff(diskFiles, indexedFiles); diff != "" {
		t.Errorf("fixture index mismatch (-disk +index):\n%s", diff)
	}

	positiveFeatures := make(map[string]int)
	positive, negative, vendored := 0, 0, 0
	for _, entry := range index.Files {
		entry := entry
		t.Run(entry.Path, func(t *testing.T) {
			content, err := fs.ReadFile(corpusFS, entry.Path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			header, err := ParseHeader(content)
			if err != nil {
				t.Fatalf("ParseHeader() error = %v", err)
			}
			want := Header{Sources: entry.Sources, Features: entry.Features, Expect: entry.Expect, Stage: entry.Stage}
			if diff := cmp.Diff(want, header); diff != "" {
				t.Errorf("header mismatch (-index +header):\n%s", diff)
			}
			if entry.Category != strings.SplitN(entry.Path, "/", 2)[0] {
				t.Errorf("category = %q, want first path component", entry.Category)
			}
		})

		if entry.Expect.OK || entry.Expect.TemplatePlaceholder {
			for _, feature := range entry.Features {
				positiveFeatures[feature]++
			}
		}
		switch entry.Category {
		case "invalid":
			negative++
			if entry.Expect.OK || entry.Expect.TemplatePlaceholder || entry.Stage == "" {
				t.Errorf("invalid fixture %q has invalid expectation/stage", entry.Path)
			}
		case "vendored":
			vendored++
			if !entry.Expect.TemplatePlaceholder {
				t.Errorf("vendored fixture %q is not marked template-placeholder", entry.Path)
			}
		default:
			positive++
			if !entry.Expect.OK || entry.Stage != "" {
				t.Errorf("positive fixture %q expects diagnostics", entry.Path)
			}
		}
	}
	if positive < 150 || negative < 25 {
		t.Errorf("fixture counts: positive=%d (want >=150), negative=%d (want >=25), vendored=%d", positive, negative, vendored)
	}
	for _, feature := range index.Checklist {
		if positiveFeatures[feature] == 0 {
			t.Errorf("checklist feature %q has no positive fixture", feature)
		}
	}
}

func TestCorpusMetadata(t *testing.T) {
	t.Parallel()

	index, err := Load(os.DirFS(corpusDir))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tagPattern := regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	for _, feature := range index.Checklist {
		if !tagPattern.MatchString(feature) {
			t.Errorf("checklist feature %q is not lowercase kebab-case", feature)
		}
	}
	for _, entry := range index.Files {
		for _, code := range entry.Expect.Diagnostics {
			if !strings.HasPrefix(code, "TRL-") {
				t.Errorf("%s: expected diagnostic %q is not a TRL code", entry.Path, code)
			}
		}
		for _, feature := range entry.Features {
			if !tagPattern.MatchString(feature) {
				t.Errorf("%s: feature %q is not lowercase kebab-case", entry.Path, feature)
			}
		}
		for _, source := range entry.Sources {
			parsed, err := url.Parse(source)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				t.Errorf("%s: source %q is not an https URL", entry.Path, source)
			}
		}
	}
}

func TestFixtureYAML(t *testing.T) {
	t.Parallel()

	corpusFS := os.DirFS(corpusDir)
	index, err := Load(corpusFS)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, entry := range index.Files {
		entry := entry
		if entry.Category == "vendored" {
			// Upstream templates contain {{ placeholder }} syntax and are intentionally exempt.
			continue
		}
		t.Run(entry.Path, func(t *testing.T) {
			content, err := fs.ReadFile(corpusFS, entry.Path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			policy := yamlx.AnchorError
			if entry.Category == "invalid" {
				policy = yamlx.AnchorAllow
			}
			document, diagnostics := yamlx.ParseWithOptions(entry.Path, content, yamlx.WithAnchorPolicy(policy))
			if entry.Category != "invalid" && (document == nil || len(diagnostics) != 0) {
				t.Fatalf("ParseWithOptions() document nil=%t, diagnostics=%v", document == nil, diagnostics)
			}
		})
	}
}

func TestVendoredNotice(t *testing.T) {
	t.Parallel()

	index, err := Load(os.DirFS(corpusDir))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	notice, err := os.ReadFile("../../testdata/NOTICE.md")
	if err != nil {
		t.Fatalf("ReadFile(NOTICE.md) error = %v", err)
	}
	shaPattern := regexp.MustCompile(`/([0-9a-f]{40})/`)
	for _, entry := range index.Files {
		if entry.Category != "vendored" {
			continue
		}
		if len(entry.Sources) != 1 {
			t.Errorf("%s: source count = %d, want 1", entry.Path, len(entry.Sources))
			continue
		}
		match := shaPattern.FindStringSubmatch(entry.Sources[0])
		if len(match) != 2 || !strings.Contains(string(notice), match[1]) {
			t.Errorf("%s: pinned source commit missing from NOTICE.md", entry.Path)
		}
	}
}

func fixturePaths(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	var paths []string
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && path.Ext(name) == ".yml" && name != indexFile {
			paths = append(paths, name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	sort.Strings(paths)
	return paths
}

func ExampleLoad() {
	index, err := Load(os.DirFS(corpusDir))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(index.Files))
	// Output: 319
}
