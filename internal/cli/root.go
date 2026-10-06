// Package cli wires the trasloco command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/HarnageaGabriel/trasloco/internal/config"
	"github.com/HarnageaGabriel/trasloco/internal/logging"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// Output modes accepted by --output.
const (
	OutputText  = "text"
	OutputJSON  = "json"
	OutputJSONL = "jsonl"
)

const annotationNeedsConfig = "trasloco/needs-config"

// IO bundles the process streams and environment so commands stay testable.
type IO struct {
	// Redactor belongs to this invocation and is shared with logs and diagnostics.
	Redactor *redact.Redactor
	// Out receives command results.
	Out io.Writer
	// Err receives diagnostics and logs. Never stdout.
	Err io.Writer
	// Getenv looks up environment variables; defaults to os.LookupEnv.
	Getenv func(string) (string, bool)
}

// app holds the state shared by all commands of one invocation.
type app struct {
	io IO

	configPath string
	output     string
	logLevel   string
	noColor    bool
	verbose    bool

	logger *slog.Logger
	cfg    *config.Config
}

// NewRootCmd builds the root command and its subcommands.
func NewRootCmd(streams IO) *cobra.Command {
	if streams.Redactor == nil {
		streams.Redactor = redact.New()
	}
	if streams.Getenv == nil {
		streams.Getenv = os.LookupEnv
	}
	a := &app{io: streams}

	root := &cobra.Command{
		Use:           "trasloco",
		Short:         "Migrate CI/CD pipelines and DevOps resources between platforms",
		Long:          "trasloco migrates pipelines and related DevOps resources between platforms.\n\nStatus: pre-alpha. Most commands are registered but not implemented yet.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          usageArgs(cobra.NoArgs),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.prepare(cmd)
		},
	}
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })

	pf := root.PersistentFlags()
	pf.StringVarP(&a.configPath, "config", "c", "", "path to the configuration file (default: ./"+config.DefaultFile+" if present)")
	pf.StringVar(&a.output, "output", OutputText, "output format: text, json or jsonl")
	pf.StringVar(&a.logLevel, "log-level", "info", "log level: debug, info, warn or error")
	pf.BoolVar(&a.noColor, "no-color", false, "disable colored output")
	pf.BoolVar(&a.verbose, "verbose", false, "verbose logging (shorthand for --log-level debug)")

	root.AddCommand(newVersionCmd(a))
	for _, s := range stubCommands() {
		root.AddCommand(newStubCmd(s))
	}
	return root
}

// Execute runs the CLI and returns the process exit code. Errors are printed
// to streams.Err.
func Execute(ctx context.Context, args []string, streams IO) int {
	if streams.Redactor == nil {
		streams.Redactor = redact.New()
	}
	if err := ctx.Err(); err != nil {
		return CodeFor(err)
	}
	root := NewRootCmd(streams)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *CodedError
	if !errors.As(err, &ee) && isCobraUsage(err) {
		err = usageError(err)
	}
	if msg := err.Error(); msg != "" {
		_, _ = fmt.Fprintf(streams.Err, "trasloco: %s\n", streams.Redactor.Redact(msg))
	}
	return CodeFor(err)
}

// usageArgs wraps a cobra positional-argument validator so failures map to
// the usage exit code.
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return usageError(err)
		}
		return nil
	}
}

// prepare validates global flags and sets up logging before any command runs.
func (a *app) prepare(cmd *cobra.Command) error {
	if !validOutput(a.output) {
		return usageError(fmt.Errorf("invalid --output %q (want text, json or jsonl)", a.output))
	}
	if !validLogLevel(a.logLevel) {
		return usageError(fmt.Errorf("invalid --log-level %q (want debug, info, warn or error)", a.logLevel))
	}
	level := a.logLevel
	if a.verbose && !cmd.Flags().Changed("log-level") {
		level = "debug"
	}
	lvl, err := logging.ParseLevel(level)
	if err != nil {
		return usageError(err)
	}
	envFormat, _ := a.io.Getenv("TRASLOCO_LOG_FORMAT")
	a.logger = logging.New(a.io.Err, lvl, logging.ResolveFormat(a.output, envFormat), a.io.Redactor)

	if cmd.Annotations[annotationNeedsConfig] != "true" {
		return nil
	}
	return a.loadConfig(cmd)
}

func validOutput(output string) bool {
	return output == OutputText || output == OutputJSON || output == OutputJSONL
}

func validLogLevel(level string) bool {
	return level == "debug" || level == "info" || level == "warn" || level == "error"
}

func (a *app) loadConfig(cmd *cobra.Command) error {
	path := a.configPath
	if path == "" && !cmd.Flags().Changed("config") {
		if env, ok := a.io.Getenv("TRASLOCO_CONFIG"); ok {
			path = env
		}
	}
	cfg, err := config.Load(config.Options{Path: path})
	if err != nil {
		return err
	}
	a.cfg = cfg
	a.logger.Debug("configuration loaded", "path", path)
	return nil
}
