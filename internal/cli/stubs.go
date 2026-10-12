package cli

import (
	"github.com/spf13/cobra"
)

type stub struct {
	name  string
	short string
	// needsConfig is false for commands that run before a config exists.
	needsConfig bool
}

func stubCommands() []stub {
	return []stub{
		{"audit", "Inventory the source platform and assess migration readiness", true},
		{"convert", "Convert pipeline definitions to the target dialect", true},
		{"plan", "Compute the migration plan without changing anything", true},
		{"apply", "Execute a migration plan", true},
		{"resume", "Resume an interrupted apply", true},
		{"verify", "Verify the migrated resources against the plan", true},
		{"rollback", "Roll back resources created by a previous apply", true},
		{"cutover", "Switch traffic and ownership to the target platform", true},
		{"secrets", "Manage secret migration", true},
		{"report", "Produce migration reports", true},
		{"catalog", "Inspect the task and action mapping catalog", true},
		{"init", "Create a starter configuration file", false},
		{"auth", "Manage platform credentials", false},
	}
}

func newStubCmd(s stub) *cobra.Command {
	cmd := &cobra.Command{
		Use:   s.name,
		Short: s.short + " (not implemented)",
		Args:  usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return &NotImplementedError{Command: cmd.CommandPath()}
		},
	}
	if s.needsConfig {
		cmd.Annotations = map[string]string{annotationNeedsConfig: "true"}
	}
	return cmd
}
