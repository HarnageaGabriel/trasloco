// Package localfs implements the Azure template Source contract over bounded,
// repository-rooted local files.
//
// Containment is checked with filepath.EvalSymlinks and the file is opened
// afterwards, so a TOCTOU window exists in which a concurrent writer could
// swap a path component for a symlink. This is acceptable for a local CLI: the
// input repository is trusted by the user running the tool, and the tool is not
// a security boundary against a concurrently hostile local process.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
)

const defaultMaxSize int64 = 20 << 20

// Source maps Azure repository aliases to local roots.
type Source struct {
	roots   []mapping
	maxSize int64
}
type (
	mapping struct{ alias, root, real string }
	// Option configures a Source.
	Option func(*Source)
)

// WithMaxSize sets the per-file read cap.
func WithMaxSize(bytes int64) Option { return func(source *Source) { source.maxSize = bytes } }

// New validates and snapshots repository roots. The empty alias is the root
// repository; "self" is the same alias, so mapping both is an error.
func New(roots map[string]string, options ...Option) (*Source, error) {
	source := &Source{maxSize: defaultMaxSize}
	for _, option := range options {
		option(source)
	}
	aliases := make([]string, 0, len(roots))
	for alias := range roots {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	seen := map[string]bool{}
	for _, alias := range aliases {
		normalized := normAlias(alias)
		if seen[normalized] {
			return nil, fmt.Errorf("localfs: repository alias %q is mapped more than once", displayAlias(normalized))
		}
		seen[normalized] = true
		root, err := filepath.Abs(roots[alias])
		if err != nil {
			return nil, fmt.Errorf("localfs root %q: invalid path", displayAlias(normalized))
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("localfs root %q: %w", displayAlias(normalized), sanitize(err))
		}
		source.roots = append(source.roots, mapping{alias: normalized, root: filepath.Clean(root), real: filepath.Clean(resolvedRoot)})
	}
	return source, nil
}

func displayAlias(alias string) string {
	if alias == "" {
		return "self"
	}
	return alias
}

// sanitize drops the on-disk path that os errors carry; callers see only the
// repository-relative name they asked for.
func sanitize(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fs.ErrNotExist
	case errors.Is(err, fs.ErrPermission):
		return fs.ErrPermission
	default:
		return errors.New("cannot be accessed")
	}
}

// checkName rejects names that are not plain repository-relative paths:
// absolute and drive-relative paths, parent escapes, Windows reserved names
// (via filepath.IsLocal), alternate data streams (':'), control characters and
// segments ending in a dot or a space, which Windows silently strips.
func checkName(name string) (string, error) {
	if name == "" {
		return "", errors.New("template path is empty")
	}
	normalized := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", errors.New("template path must be repository-relative")
	}
	for _, r := range normalized {
		if r == ':' || r < 0x20 || r == 0x7f {
			return "", errors.New("template path contains an invalid character")
		}
	}
	for _, segment := range strings.Split(normalized, "/") {
		if runtime.GOOS == "windows" && reservedName(segment) {
			return "", errors.New("template path uses a reserved device name")
		}
		if segment != "." && segment != ".." && (strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ")) {
			return "", errors.New("template path has a segment ending in a dot or space")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(normalized))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("template path escapes repository root")
	}
	if !filepath.IsLocal(clean) {
		return "", errors.New("template path is not a local relative path")
	}
	return clean, nil
}

// Open reads one repository-relative regular file after lexical and symlink
// containment checks. Absolute paths and parent escapes are rejected, and no
// error text contains an absolute on-disk path. For a repository other than
// the root, origin.File is "alias@path" so diagnostics can tell it apart from a
// root file of the same name.
func (s *Source) Open(ctx context.Context, alias, name string) ([]byte, diag.Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, diag.Source{}, err
	}
	alias = normAlias(alias)
	root, ok := s.root(alias)
	if !ok {
		return nil, diag.Source{}, fmt.Errorf("unknown repository alias %q", alias)
	}
	clean, err := checkName(name)
	if err != nil {
		return nil, diag.Source{}, err
	}
	filename := filepath.Join(root.root, clean)
	resolvedFile, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return nil, diag.Source{}, fmt.Errorf("open template %s: %w", filepath.ToSlash(clean), sanitize(err))
	}
	if !contained(root.real, resolvedFile) {
		return nil, diag.Source{}, fmt.Errorf("template %s: symlink escapes repository root", filepath.ToSlash(clean))
	}
	info, err := os.Stat(resolvedFile)
	if err != nil {
		return nil, diag.Source{}, fmt.Errorf("open template %s: %w", filepath.ToSlash(clean), sanitize(err))
	}
	if !info.Mode().IsRegular() {
		return nil, diag.Source{}, fmt.Errorf("template %s is not a regular file", filepath.ToSlash(clean))
	}
	file, err := os.Open(resolvedFile)
	if err != nil {
		return nil, diag.Source{}, fmt.Errorf("open template %s: %w", filepath.ToSlash(clean), sanitize(err))
	}
	defer func() { _ = file.Close() }()
	limit := s.maxSize
	if limit <= 0 {
		limit = defaultMaxSize
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, diag.Source{}, fmt.Errorf("read template %s: %w", filepath.ToSlash(clean), sanitize(err))
	}
	if int64(len(data)) > limit {
		return nil, diag.Source{}, fmt.Errorf("template %s exceeds %d-byte size limit", filepath.ToSlash(clean), limit)
	}
	display := filepath.ToSlash(clean)
	if alias != "" {
		display = alias + "@" + display
	}
	return data, diag.Source{Platform: "azure-pipelines", File: display}, nil
}

func (s *Source) root(alias string) (mapping, bool) {
	for _, root := range s.roots {
		if root.alias == alias {
			return root, true
		}
	}
	return mapping{}, false
}

func contained(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func normAlias(alias string) string {
	if alias == "self" {
		return ""
	}
	return alias
}

// reservedName reports a Windows device name, with or without an extension
// ("NUL", "con.yml", "COM1.txt"), which cannot name a regular file there.
func reservedName(segment string) bool {
	base, _, _ := strings.Cut(segment, ".")
	switch strings.ToUpper(strings.TrimRight(base, " ")) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	upper := strings.ToUpper(strings.TrimRight(base, " "))
	return len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '0' && upper[3] <= '9'
}
