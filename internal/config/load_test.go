package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/pflag"
)

func TestLoadPrecedence(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("mode", "strict", "")
	if err := flags.Set("mode", "best-effort"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(Options{
		Path: filepath.Join("..", "..", "testdata", "config", "valid.yaml"),
		Environ: func() []string {
			return []string{
				"TRASLOCO_CONVERSION__MODE=strict",
				"TRASLOCO_OUTPUT__DIR=environment-output",
				"TRASLOCO_SOURCE__SELECT=one,two",
			}
		},
		Flags:    flags,
		FlagKeys: map[string]string{"mode": "conversion.mode"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := struct {
		Mode   string
		Dir    string
		Select []string
	}{"best-effort", "environment-output", []string{"one", "two"}}
	got := struct {
		Mode   string
		Dir    string
		Select []string
	}{cfg.Conversion.Mode, cfg.Output.Dir, cfg.Source.Select}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Load() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{"unknown key", "unknown-key.yaml", "source.unexpected"},
		{"literal secret", "literal-secret.yaml", "source.auth.token"},
		{"semantic auth", "invalid-auth.yaml", "source.auth"},
		{"invalid URL", "invalid-url.yaml", "source.url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(Options{
				Path:    filepath.Join("..", "..", "testdata", "config", tt.file),
				Environ: func() []string { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want field path %q", err, tt.want)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Load() error type = %T, want *ValidationError in chain", err)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(Options{Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	if diff := cmp.Diff(&want, cfg); diff != "" {
		t.Fatalf("Load() defaults mismatch (-want +got):\n%s", diff)
	}
}

func TestExplicitMissingFile(t *testing.T) {
	_, err := Load(Options{Path: filepath.Join(t.TempDir(), "missing.yaml"), Environ: func() []string { return nil }})
	if err == nil {
		t.Fatal("Load() unexpectedly succeeded")
	}
}
