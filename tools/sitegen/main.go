// Command sitegen renders the static documentation site for retired Azure Pipelines tasks
// from the catalog data.
//
// Usage: sitegen [-out _site] [-catalog catalog] [-assets docs/assets] -date YYYY-MM-DD
//
//	[-base-url URL] [-tool-available] [-rewrite-available]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/HarnageaGabriel/trasloco/internal/catalog"
	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"go.yaml.in/yaml/v3"
)

const defaultBaseURL = "https://harnageagabriel.github.io/trasloco"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("sitegen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "_site", "output directory")
	catalogDir := flags.String("catalog", "catalog", "catalog directory")
	assets := flags.String("assets", "docs/assets", "directory with trasloco-social-preview.png and trasloco-favicon.svg")
	date := flags.String("date", "", "data generation date, YYYY-MM-DD (required; no wall clock is used)")
	base := flags.String("base-url", defaultBaseURL, "public base URL of the site")
	tool := flags.Bool("tool-available", false, "state that the trasloco modernization command is released")
	rewrite := flags.Bool("rewrite-available", false, "state that trasloco modernize --write ships (pages say 'planned' when false)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *date == "" {
		return errors.New("-date is required")
	}
	fsys := os.DirFS(*catalogDir)
	cat, err := catalog.Load(ctx, fsys, diag.NewCollector())
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	header, err := readHeader(fsys)
	if err != nil {
		return err
	}
	cfg := Config{
		RetirementDate:   header.RetirementDate,
		RemovalDate:      header.RemovalDate,
		AnnouncementURL:  header.Source.RetirementURL,
		Generated:        *date,
		BaseURL:          *base,
		ToolAvailable:    *tool,
		RewriteAvailable: *rewrite,
	}
	files, err := generate(cat, cfg)
	if err != nil {
		return err
	}
	for _, copied := range [][2]string{{"trasloco-social-preview.png", "og-image.png"}, {"trasloco-favicon.svg", "favicon.svg"}} {
		data, err := os.ReadFile(filepath.Join(*assets, copied[0]))
		if err != nil {
			return fmt.Errorf("read asset: %w", err)
		}
		files = append(files, file{Path: copied[1], Data: data})
	}
	return writeFiles(*out, files)
}

// readHeader reads the dates and the announcement URL that the loaded catalog does not keep.
func readHeader(fsys fs.FS) (*catalog.ModernizationFile, error) {
	data, err := fs.ReadFile(fsys, "azure-pipelines/modernize.yaml")
	if err != nil {
		return nil, fmt.Errorf("read modernization header: %w", err)
	}
	var header catalog.ModernizationFile
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("decode modernization header: %w", err)
	}
	return &header, nil
}

func writeFiles(root string, files []file) error {
	for _, f := range files {
		target := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
		if err := os.WriteFile(target, f.Data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	return nil
}
