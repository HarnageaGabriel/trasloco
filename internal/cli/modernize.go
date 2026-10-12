package cli

import (
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
	check  bool
	strict bool
	format string
}

func newModernizeCmd(a *app) *cobra.Command {
	options := modernizeOptions{}
	cmd := &cobra.Command{
		Use:   "modernize [paths...]",
		Short: "Report retired Azure Pipelines task versions",
		Long: `Find references to Azure Pipelines tasks that Microsoft retires on 2026-10-15
(removed 2027-03-15) in the YAML files under the given paths (default: the
current directory). Nothing is modified.

Only files that look like Azure Pipelines files are examined; GitHub
workflows, Kubernetes manifests and other YAML are skipped silently.
Directory scans skip .git, node_modules and vendor directories.

Exit codes: 0 nothing to fix (or --check not given), 5 retired tasks found
with --check, 64 usage error, 1 a named path cannot be read (or, with
--strict, an Azure Pipelines file has parser errors, including constructs the
parser rejects).`,
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}
			return runModernize(cmd.Context(), a, options, args)
		},
	}
	cmd.Flags().BoolVar(&options.check, "check", false, "return exit code 5 when retired tasks are found")
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
	findings []modernize.Finding
	warnings []modernizeWarning
	scanned  int
	skipped  int
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

	files, notices, pathFailures := pipelineFiles(paths)
	for _, notice := range notices {
		_, _ = fmt.Fprintf(a.io.Err, "trasloco: warning: %s\n", notice)
	}
	if len(files) == 0 {
		_, _ = fmt.Fprintln(a.io.Err, "trasloco: warning: 0 YAML files scanned")
	}

	var result modernizeResult
	ioFailed, strictFailed := pathFailures, false
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, readErr := os.ReadFile(file.path) //nolint:gosec // paths are the command's explicit scan targets
		if readErr != nil {
			_, _ = fmt.Fprintf(a.io.Err, "trasloco: %s: %v\n", file.display, readErr)
			if file.explicit {
				ioFailed++
			}
			continue
		}
		data, readErr = decodeModernizeData(data)
		if readErr != nil {
			_, _ = fmt.Fprintf(a.io.Err, "trasloco: warning: skipped %s: %v\n", file.display, readErr)
			if file.explicit {
				ioFailed++
			}
			continue
		}
		result.scanned++
		if !modernize.IsCandidate(file.display, data) {
			result.skipped++
			continue
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
	if err := writeModernizeReport(a, options.format, result, !strictFailed); err != nil {
		return err
	}
	if ioFailed > 0 {
		return WithExitCode(ExitError, fmt.Errorf("%d named file(s) could not be read", ioFailed))
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
// paths so the caller can return exit 1 after writing the report.
func pipelineFiles(paths []string) ([]scanFile, []string, int) {
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
				return nil
			}
			if entry.IsDir() && path != root {
				switch strings.ToLower(entry.Name()) {
				case ".git", "node_modules", "vendor":
					return filepath.SkipDir
				}
			}
			if entry.Type()&os.ModeSymlink != 0 {
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
	if !littleEndian && !bigEndian {
		return data, nil
	}
	if (len(data)-2)%2 != 0 {
		return nil, errors.New("malformed UTF-16 file")
	}
	units := make([]uint16, 0, (len(data)-2)/2)
	for offset := 2; offset < len(data); offset += 2 {
		value := uint16(data[offset])<<8 | uint16(data[offset+1])
		if littleEndian {
			value = uint16(data[offset+1])<<8 | uint16(data[offset])
		}
		units = append(units, value)
	}
	return []byte(string(utf16.Decode(units))), nil
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
