package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	catalogdata "github.com/HarnageaGabriel/trasloco/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/modernize"
)

const retirementURL = "https://learn.microsoft.com/en-us/azure/devops/pipelines/tasks/task-retirement"

type modernizeOptions struct {
	check  bool
	format string
}

func newModernizeCmd(a *app) *cobra.Command {
	options := modernizeOptions{}
	cmd := &cobra.Command{
		Use:   "modernize [paths...]",
		Short: "Report retired Azure Pipelines task versions",
		Args:  usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}
			return runModernize(cmd.Context(), a, options, args)
		},
	}
	cmd.Flags().BoolVar(&options.check, "check", false, "return exit code 5 when retired tasks are found")
	cmd.Flags().StringVar(&options.format, "format", "text", "report format: text, json or sarif")
	return cmd
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

	files, err := pipelineFiles(paths)
	if err != nil {
		return err
	}
	var findings []modernize.Finding
	failed := 0
	for _, file := range files {
		data, readErr := os.ReadFile(file.path) //nolint:gosec // paths are the command's explicit scan targets
		if readErr != nil {
			failed++
			_, _ = fmt.Fprintf(a.io.Err, "trasloco: %s: %v\n", file.path, readErr)
			continue
		}
		display := file.display
		found, diagnostics := modernize.Scan(display, data, cat)
		findings = append(findings, found...)
		fileFailed := false
		for _, diagnostic := range diagnostics {
			if diagnostic.Severity == diag.SeverityError {
				fileFailed = true
			}
			location := display
			if diagnostic.Source != nil {
				location = fmt.Sprintf("%s:%d:%d", diagnostic.Source.File, diagnostic.Source.Line, diagnostic.Source.Col)
			}
			_, _ = fmt.Fprintf(a.io.Err, "%s: %s: %s\n", location, diagnostic.Code, diagnostic.Message)
		}
		if fileFailed {
			failed++
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Code < b.Code
	})
	if err := writeModernizeReport(a, options.format, findings); err != nil {
		return err
	}
	if failed > 0 {
		code := ExitError
		if failed < len(files) {
			code = ExitPartial
		}
		return WithExitCode(code, fmt.Errorf("%d pipeline file(s) could not be parsed", failed))
	}
	if options.check && len(findings) > 0 {
		return WithExitCode(ExitPolicy, errors.New(""))
	}
	return nil
}

type scanFile struct {
	path, display string
}

func pipelineFiles(paths []string) ([]scanFile, error) {
	seen := make(map[string]bool)
	var files []scanFile
	for _, root := range paths {
		info, err := os.Lstat(root)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			if pipelineExtension(root) {
				absolute, _ := filepath.Abs(root)
				if !seen[absolute] {
					seen[absolute] = true
					files = append(files, scanFile{path: absolute, display: filepath.ToSlash(filepath.Base(root))})
				}
			}
			continue
		}
		absoluteRoot, _ := filepath.Abs(root)
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
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
				absolute, _ := filepath.Abs(path)
				if !seen[absolute] {
					seen[absolute] = true
					relative, _ := filepath.Rel(absoluteRoot, absolute)
					files = append(files, scanFile{path: absolute, display: filepath.ToSlash(relative)})
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].display != files[j].display {
			return files[i].display < files[j].display
		}
		return files[i].path < files[j].path
	})
	return files, nil
}

func pipelineExtension(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".yml" || extension == ".yaml"
}

func writeModernizeReport(a *app, format string, findings []modernize.Finding) error {
	switch format {
	case "text":
		for _, finding := range findings {
			replacement := "none"
			if len(finding.Replacements) > 0 {
				replacement = strings.Join(finding.Replacements, ", ")
			}
			codes := append([]string{finding.Code}, finding.Diagnostics...)
			_, err := fmt.Fprintf(a.io.Out, "%s:%d:%d: %s retired=%s class=%s replacement=%s action=%s source=%s\n",
				finding.Path, finding.Line, finding.Column, strings.Join(codes, ","), finding.RetiredTask, valueOr(finding.Class, "manual"),
				replacement, finding.Action, valueOr(finding.SourceURL, retirementURL))
			if err != nil {
				return err
			}
		}
		return nil
	case "json":
		return writeJSON(a.io.Out, struct {
			Findings []modernize.Finding `json:"findings"`
		}{Findings: findings})
	case "sarif":
		return writeJSON(a.io.Out, makeSARIF(findings))
	default:
		return errors.New("unreachable report format")
	}
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeJSON(writer interface{ Write([]byte) (int, error) }, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}
type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}
type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}
type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}
type sarifRule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	HelpURI string `json:"helpUri"`
}
type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations"`
}
type sarifMessage struct {
	Text string `json:"text"`
}
type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}
type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}
type sarifArtifactLocation struct {
	URI string `json:"uri"`
}
type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
}

func makeSARIF(findings []modernize.Finding) sarifLog {
	ruleMap := make(map[string]sarifRule)
	for number := 1; number <= 11; number++ {
		id := fmt.Sprintf("TRL-MOD-%04d", number)
		ruleMap[id] = sarifRule{ID: id, Name: id, HelpURI: retirementURL}
	}
	results := make([]sarifResult, 0, len(findings))
	for _, finding := range findings {
		help := valueOr(finding.SourceURL, retirementURL)
		ruleMap[finding.Code] = sarifRule{ID: finding.Code, Name: finding.Code, HelpURI: help}
		text := fmt.Sprintf("Retired task %s (class %s): %s", finding.RetiredTask, valueOr(finding.Class, "manual"), finding.Action)
		codes := append([]string{finding.Code}, finding.Diagnostics...)
		for _, code := range codes {
			results = append(results, sarifResult{
				RuleID: code, Level: "warning", Message: sarifMessage{Text: text},
				Locations: []sarifLocation{{PhysicalLocation: sarifPhysicalLocation{ArtifactLocation: sarifArtifactLocation{URI: finding.Path}, Region: sarifRegion{StartLine: finding.Line, StartColumn: finding.Column}}}},
			})
		}
	}
	ids := make([]string, 0, len(ruleMap))
	for id := range ruleMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rules := make([]sarifRule, 0, len(ids))
	for _, id := range ids {
		rules = append(rules, ruleMap[id])
	}
	return sarifLog{Version: "2.1.0", Schema: "https://json.schemastore.org/sarif-2.1.0.json", Runs: []sarifRun{{Tool: sarifTool{Driver: sarifDriver{Name: "trasloco modernize", InformationURI: retirementURL, Rules: rules}}, Results: results}}}
}
