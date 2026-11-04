// Package model defines the typed GitHub Actions document model of the
// emitter (docs/spec/emit-gha.md section 4).
//
// Fixed structures use Go structs with yaml tags whose field order is the
// fixed key order of section 4.1. Dynamic mappings (on, env, permissions,
// jobs and step lists) are built as ordered *yaml.Node mappings so that no Go
// map can influence the observable output order (INV-01): the IR ordered
// object order flows straight into the node order.
//
// The package is pure: it is a value library, holds no state, and performs
// no I/O (CONVENTIONS.md, Layering).
package model

import (
	"strconv"

	goyaml "go.yaml.in/yaml/v3"
)

// Workflow is the root document shape. The key order of its yaml tags is
// emit-gha.md 4.1 document order: name, on, permissions, env, jobs.
type Workflow struct {
	// Name is the workflow name. It is omitted when the pipeline has none
	// (emit-gha.md 5.1).
	Name string `yaml:"name,omitempty"`
	// On holds the trigger mapping in IR trigger order.
	On *goyaml.Node `yaml:"on,omitempty"`
	// Permissions is the mandatory deny-all top-level mapping (INV-02).
	Permissions *goyaml.Node `yaml:"permissions,omitempty"`
	// Env holds workflow variables in IR declaration order.
	Env *goyaml.Node `yaml:"env,omitempty"`
	// Jobs holds jobs keyed by their normalized GitHub job ID, in IR order.
	Jobs *goyaml.Node `yaml:"jobs,omitempty"`
}

// Job is one job. The key order of its yaml tags is emit-gha.md 4.1 job
// order: name, permissions, needs, if, runs-on, timeout-minutes,
// continue-on-error, env, steps.
type Job struct {
	Name            string       `yaml:"name,omitempty"`
	Permissions     *goyaml.Node `yaml:"permissions,omitempty"`
	Needs           []string     `yaml:"needs,omitempty"`
	If              string       `yaml:"if,omitempty"`
	RunsOn          string       `yaml:"runs-on,omitempty"`
	TimeoutMinutes  int          `yaml:"timeout-minutes,omitempty"`
	ContinueOnError bool         `yaml:"continue-on-error,omitempty"`
	Env             *goyaml.Node `yaml:"env,omitempty"`
	Steps           *goyaml.Node `yaml:"steps,omitempty"`
}

// Step is one step. The key order of its yaml tags is emit-gha.md 4.1 step
// order: name, if, uses, with, shell, working-directory, env,
// timeout-minutes, continue-on-error, run.
type Step struct {
	Name             string       `yaml:"name,omitempty"`
	If               string       `yaml:"if,omitempty"`
	Uses             string       `yaml:"uses,omitempty"`
	With             *goyaml.Node `yaml:"with,omitempty"`
	Shell            string       `yaml:"shell,omitempty"`
	WorkingDirectory string       `yaml:"working-directory,omitempty"`
	Env              *goyaml.Node `yaml:"env,omitempty"`
	TimeoutMinutes   int          `yaml:"timeout-minutes,omitempty"`
	ContinueOnError  bool         `yaml:"continue-on-error,omitempty"`
	Run              string       `yaml:"run,omitempty"`
}

// Pair is one ordered entry of a dynamic mapping.
type Pair struct {
	Key   string
	Value *goyaml.Node
}

// Map builds an ordered mapping node. Entries keep the order of pairs; it
// never falls back to a Go map, so the result order is the declaration order
// of the caller (INV-01).
func Map(pairs ...Pair) *goyaml.Node {
	node := &goyaml.Node{Kind: goyaml.MappingNode, Tag: "!!map"}
	for _, pair := range pairs {
		node.Content = append(node.Content,
			String(pair.Key), pair.Value)
	}
	return node
}

// Seq builds a sequence node whose items keep the order of items.
func Seq(items ...*goyaml.Node) *goyaml.Node {
	node := &goyaml.Node{Kind: goyaml.SequenceNode, Tag: "!!seq"}
	node.Content = append(node.Content, items...)
	return node
}

// String builds a string scalar node. Its style is unambiguous; the emitter
// writer applies the styles of emit-gha.md 4.2 before encoding (INV-07).
func String(value string) *goyaml.Node {
	return &goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!!str", Value: value}
}

// Int builds an integer scalar node; YAML writes it as an integer
// (emit-gha.md 4.1 rule 4).
func Int(value int) *goyaml.Node {
	return &goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!!int", Value: itoa(value)}
}

// Bool builds a boolean scalar node; YAML writes it as true or false.
func Bool(value bool) *goyaml.Node {
	return &goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!!bool", Value: boolText(value)}
}

// Null builds the empty scalar of an empty-value event such as
// workflow_dispatch (emit-gha.md 5.2 rule 2).
func Null() *goyaml.Node {
	return &goyaml.Node{Kind: goyaml.ScalarNode, Tag: "!!null", Value: ""}
}

// EmptyMap builds an empty mapping that renders as `{}` (emit-gha.md 4.1
// rule 2 allows flow style only for an empty mapping a rule demands, such as
// the deny-all permissions of INV-02).
func EmptyMap() *goyaml.Node {
	return &goyaml.Node{Kind: goyaml.MappingNode, Tag: "!!map"}
}

func itoa(value int) string { return strconv.Itoa(value) }

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
