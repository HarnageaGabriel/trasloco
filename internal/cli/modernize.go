package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/spf13/cobra"

	catalogdata "github.com/HarnageaGabriel/trasloco/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize"
)

const retirementURL = "https://learn.microsoft.com/en-us/azure/devops/pipelines/tasks/task-retirement"

type modernizeOptions struct {
	write  bool
	check  bool
	strict bool
	format string
}

func newModernizeCmd(a *app) *cobra.Command {
	options := modernizeOptions{}
	cmd := &cobra.Command{
		Use:   "modernize [paths...]",
		Short: "Report or rewrite retired Azure Pipelines task versions with fail-closed guards",
		Long: `Find references to Azure Pipelines tasks that Microsoft retires on 2026-10-15
(removed 2027-03-15) in the YAML files under the given paths (default: the
current directory). Nothing is modified unless --write is given.

--write rewrites class A/B tasks with the specification's fail-closed guards;
class C/D tasks are never edited. Rejected rewrites need manual migration.
Unchanged files are not touched.
Symbolic links are skipped. UTF-16 files are reported but not rewritten.
Files are checked for size, mtime and content changes before replacement, but
a race window remains: do not run concurrently with other tools that edit the same files.
Replacement preserves permission bits and attempts uid/gid preservation on Unix
(permission-denied ownership changes are ignored). Unix directories are synced
after rename. ACLs, extended attributes and Windows alternate data streams (ADS)
are not preserved. Windows uses rename because portable in-place writes are not atomic.
Use --write --check to return exit 5 if actionable findings remain afterward.
With --write, every reported line and column refers to the file as it is on
disk after the run: the rewritten file when it was written, the original when
it was not. A step that could not be rewritten and is still reported as a
finding is listed once, with the reason as a note on that finding. If the
rewrite self-check fails, the original file is left untouched and the exit
code is 1.
All report formats, including SARIF, support --write.

Only files that look like Azure Pipelines files are examined; GitHub
workflows, Kubernetes manifests and other YAML are skipped silently.
Directory scans skip .git, node_modules and vendor directories.

UTF-16 files are decoded when they start with a byte order mark, or without
one when their first characters are in the Latin range.

Exit codes: 0 nothing to fix (or --check not given), 5 retired tasks found
with --check (after rewriting with --write), 64 usage error, 1 an I/O or
rewrite self-check failure (or, with
--strict, an Azure Pipelines file has parser errors, including constructs the
parser rejects).

Precedence: exit 1 wins over exit 5. With --check or --write, a discovered file
that cannot be read, decoded, or is too large to scan, or an unreadable
directory, makes the scan incomplete and exits 1. The report still lists every
finding produced.`,
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}
			return runModernize(cmd.Context(), a, options, args)
		},
	}
	cmd.Flags().BoolVar(&options.check, "check", false, "return exit code 5 when retired tasks are found")
	cmd.Flags().BoolVar(&options.write, "write", false, "rewrite class A/B tasks with fail-closed guards; report steps needing manual migration")
	cmd.Flags().BoolVar(&options.strict, "strict", false, "return exit code 1 when an Azure Pipelines file cannot be parsed")
	cmd.Flags().StringVar(&options.format, "format", "text", "report format: text, json or sarif")
	return cmd
}

// modernizeWarning is a parse problem in an Azure Pipelines file. It never
// changes the exit code unless --strict is given.
type modernizeWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
}

type modernizeResult struct {
	diagnostics []diag.Diagnostic
	summary     *modernizeSummary
	findings    []modernize.Finding
	warnings    []modernizeWarning
	scanned     int
	skipped     int
}

func (r modernizeResult) actionable() int {
	count := 0
	for _, finding := range r.findings {
		if finding.Actionable() {
			count++
		}
	}
	return count
}

func runModernize(ctx context.Context, a *app, options modernizeOptions, paths []string) error {
	if options.format != "text" && options.format != "json" && options.format != "sarif" {
		return usageError(fmt.Errorf("invalid --format %q (want text, json or sarif)", options.format))
	}
	collector := diag.NewCollector(diag.WithRedactor(a.io.Redactor))
	cat, err := catalog.Load(ctx, catalogdata.FS, collector)
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}

	files, notices, pathFailures := pipelineFiles(paths, options.check || options.write)
	for _, notice := range notices {
		_, _ = fmt.Fprintf(a.io.Err, "trasloco: warning: %s\n", notice)
	}
	if len(files) == 0 {
		_, _ = fmt.Fprintln(a.io.Err, "trasloco: warning: 0 YAML files scanned")
	}

	var result modernizeResult
	if options.write {
		result.summary = &modernizeSummary{}
	}
	ioFailed, strictFailed := pathFailures, false
	rewriteFailed := false
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.write {
			if err := modernizeNoSymlinks(file.path); err != nil {
				_, _ = fmt.Fprintf(a.io.Err, "trasloco: warning: skipped %s: %v\n", file.display, err)
				if !errors.Is(err, errModernizeSymlink) {
					ioFailed++
				}
				continue
			}
		}
		var data []byte
		var initial os.FileInfo
		var readErr error
		data, initial, readErr = readModernizeWriteSource(file.path)
		if readErr != nil {
			var sizeErr *modernizeSizeError
			if errors.As(readErr, &sizeErr) {
				d := sizeErr.diagnostic(file.display)
				result.scanned++
				strictFailed = true
				result.warnings = append(result.warnings, modernizeWarning{Code: d.Code, Severity: string(d.Severity), Path: file.display, Line: 1, Column: 1, Message: d.Message})
				_, _ = fmt.Fprintf(a.io.Err, "warning: %s:1:1: %s: %s\n", file.display, d.Code, d.Message)
				if file.explicit || options.check || options.write {
					ioFailed++
				}
				continue
			}
			_, _ = fmt.Fprintf(a.io.Err, "trasloco: %s: %v\n", file.display, readErr)
			if file.explicit || options.check || options.write {
				ioFailed++
			}
			continue
		}
		original := data
		data, readErr = decodeModernizeData(data)
		if readErr != nil {
			_, _ = fmt.Fprintf(a.io.Err, "trasloco: warning: skipped %s: %v\n", file.display, readErr)
			if file.explicit || options.check || options.write {
				ioFailed++
			}
			continue
		}
		result.scanned++
		if !modernize.IsCandidate(file.display, data) {
			result.skipped++
			continue
		}
		if options.write {
			if !bytes.Equal(original, data) {
				warning := modernizeWarning{Code: "UTF16", Severity: "warning", Path: file.display, Line: 1, Column: 1, Message: "UTF-16 file not rewritten"}
				result.warnings = append(result.warnings, warning)
				_, _ = fmt.Fprintf(a.io.Err, "warning: %s: %s\n", file.display, warning.Message)
			} else {
				out, diagnostics, changed, err := modernize.Rewrite(ctx, data, file.display, cat)
				selfCheckFailed := false
				for _, d := range diagnostics {
					if d.Code == "TRL-INT-0002" {
						selfCheckFailed = true
					}
				}
				if err == nil && changed && !selfCheckFailed {
					err = writeModernizeFile(file.path, original, out, initial)
					if err == nil {
						data = out
						result.summary.FilesRewritten++
						for _, d := range diagnostics {
							if d.Code == "TRL-MOD-0001" || d.Code == "TRL-MOD-0002" {
								result.summary.StepsRewritten++
							}
						}
					}
				}
				if err != nil {
					ioFailed++
					_, _ = fmt.Fprintf(a.io.Err, "trasloco: %s: %v\n", file.display, err)
					// Do not report planned rewrites as completed when the write failed.
				} else {
					result.diagnostics = append(result.diagnostics, diagnostics...)
				}
				rewriteFailed = rewriteFailed || selfCheckFailed
			}
		}
		found, diagnostics := modernize.Scan(file.display, data, cat)
		result.findings = append(result.findings, found...)
		for _, diagnostic := range diagnostics {
			if diagnostic.Severity == diag.SeverityError {
				strictFailed = true
			}
			warning := modernizeWarning{Code: diagnostic.Code, Severity: string(diagnostic.Severity), Path: file.display, Line: 1, Column: 1, Message: diagnostic.Message}
			if diagnostic.Source != nil {
				warning.Path, warning.Line, warning.Column = diagnostic.Source.File, diagnostic.Source.Line, diagnostic.Source.Col
			}
			result.warnings = append(result.warnings, warning)
			_, _ = fmt.Fprintf(a.io.Err, "warning: %s:%d:%d: %s: %s\n", warning.Path, warning.Line, warning.Column, warning.Code, warning.Message)
		}
	}
	sort.SliceStable(result.findings, func(i, j int) bool {
		x, y := result.findings[i], result.findings[j]
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		if x.Line != y.Line {
			return x.Line < y.Line
		}
		if x.Column != y.Column {
			return x.Column < y.Column
		}
		return x.Code < y.Code
	})
	mergeRewriteDiagnostics(&result)
	if result.summary != nil {
		result.summary.ManualSteps = result.actionable()
	}
	if err := writeModernizeReport(a, options.format, result, !strictFailed && !rewriteFailed && ioFailed == 0); err != nil {
		return err
	}
	if ioFailed > 0 {
		return WithExitCode(ExitError, fmt.Errorf("%d file operation(s) failed", ioFailed))
	}
	if rewriteFailed {
		return WithExitCode(ExitError, errors.New("modernization self-check failed (TRL-INT-0002)"))
	}
	if options.strict && strictFailed {
		return WithExitCode(ExitError, errors.New("an Azure Pipelines file could not be parsed (--strict)"))
	}
	if options.check && result.actionable() > 0 {
		return WithExitCode(ExitPolicy, errors.New(""))
	}
	return nil
}

type scanFile struct {
	path, display string
	explicit      bool
}

// pipelineFiles lists the YAML files under paths. Notices are problems with
// single paths that do not stop the scan; failures counts unreadable named
// paths (and traversal failures in fail-closed modes) for exit 1 after the report.
func pipelineFiles(paths []string, failClosed bool) ([]scanFile, []string, int) {
	cwd, _ := os.Getwd()
	seen := make(map[string]bool)
	var files []scanFile
	var notices []string
	failures := 0
	add := func(path string, explicit bool) {
		absolute, err := filepath.Abs(path)
		if err != nil || seen[absolute] {
			return
		}
		seen[absolute] = true
		files = append(files, scanFile{path: absolute, display: displayPath(cwd, absolute), explicit: explicit})
	}
	for _, root := range paths {
		if err := modernizeNoSymlinks(root); errors.Is(err, errModernizeSymlink) {
			notices = append(notices, fmt.Sprintf("skipped %s: symbolic links are not followed", root))
			continue
		}
		info, err := os.Lstat(root)
		if err != nil {
			notices = append(notices, fmt.Sprintf("cannot read %s: %v", root, err))
			failures++
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			notices = append(notices, fmt.Sprintf("skipped %s: symbolic links are not followed", root))
			continue
		}
		if !info.IsDir() {
			if pipelineExtension(root) {
				add(root, true)
			} else {
				notices = append(notices, fmt.Sprintf("skipped %s: not a .yml or .yaml file", root))
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				notices = append(notices, fmt.Sprintf("cannot read %s: %v", path, walkErr))
				if failClosed {
					failures++
				}
				return nil
			}
			if entry.IsDir() && path != root {
				switch strings.ToLower(entry.Name()) {
				case ".git", "node_modules", "vendor":
					return filepath.SkipDir
				}
			}
			if entry.Type()&os.ModeSymlink != 0 {
				notices = append(notices, fmt.Sprintf("skipped %s: symbolic links are not followed", path))
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() && pipelineExtension(path) {
				add(path, false)
			}
			return nil
		})
		if err != nil {
			notices = append(notices, fmt.Sprintf("cannot read %s: %v", root, err))
			failures++
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].display != files[j].display {
			return files[i].display < files[j].display
		}
		return files[i].path < files[j].path
	})
	return files, notices, failures
}

func decodeModernizeData(data []byte) ([]byte, error) {
	if len(data) < 2 {
		return data, nil
	}
	littleEndian := data[0] == 0xff && data[1] == 0xfe
	bigEndian := data[0] == 0xfe && data[1] == 0xff
	start := 2
	if !littleEndian && !bigEndian {
		littleEndian, bigEndian = sniffBOMlessUTF16(data)
		if (!littleEndian && !bigEndian) || len(data)%2 != 0 {
			// Not recognisably UTF-16: leave it to the normal (skipped) path.
			return data, nil
		}
		start = 0
	}
	if (len(data)-start)%2 != 0 {
		return nil, errors.New("malformed UTF-16 file")
	}
	units := make([]uint16, 0, (len(data)-start)/2)
	for offset := start; offset < len(data); offset += 2 {
		value := uint16(data[offset])<<8 | uint16(data[offset+1])
		if littleEndian {
			value = uint16(data[offset+1])<<8 | uint16(data[offset])
		}
		units = append(units, value)
	}
	return []byte(string(utf16.Decode(units))), nil
}

// sniffBOMlessUTF16 recognises the common BOM-less UTF-16 case: Latin-range
// text where, within the first 64 bytes, every byte at one parity is 0x00 and
// the bytes at the other parity are not all zero. Text using characters above
// U+00FF is not detected and is left to the normal (skipped) path.
func sniffBOMlessUTF16(data []byte) (littleEndian, bigEndian bool) {
	window := len(data)
	if window > 64 {
		window = 64
	}
	window -= window % 2
	if window < 4 {
		return false, false
	}
	oddZero, evenZero, oddAny, evenAny := true, true, false, false
	for i := 0; i < window; i++ {
		if i%2 == 0 {
			if data[i] != 0 {
				evenAny = true
				evenZero = false
			}
		} else if data[i] != 0 {
			oddAny = true
			oddZero = false
		}
	}
	// Little-endian ASCII has zero high (odd) bytes; big-endian has zero even bytes.
	return oddZero && evenAny, evenZero && oddAny
}

// displayPath is the path shown in reports: relative to the current
// directory with forward slashes, or absolute when no relative path exists.
func displayPath(cwd, absolute string) string {
	if cwd != "" {
		if relative, err := filepath.Rel(cwd, absolute); err == nil {
			return filepath.ToSlash(relative)
		}
	}
	return filepath.ToSlash(absolute)
}

func pipelineExtension(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".yml" || extension == ".yaml"
}
