package cli

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"trasloco": func() {
			os.Exit(Execute(context.Background(), os.Args[1:], IO{
				Out: os.Stdout, Err: os.Stderr, Getenv: os.LookupEnv,
			}))
		},
	})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata/script"})
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"version"}, ExitOK},
		{"unknown flag", []string{"--unknown"}, ExitUsage},
		{"stub", []string{"init"}, ExitError},
		{"invalid config", []string{"--config", "testdata/script/invalid.yaml", "audit"}, ExitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			got := Execute(context.Background(), tt.args, IO{
				Out: &out, Err: &errOut, Getenv: func(string) (string, bool) { return "", false },
			})
			if got != tt.want {
				t.Fatalf("Execute() = %d, want %d; stderr: %s", got, tt.want, errOut.String())
			}
		})
	}
}
