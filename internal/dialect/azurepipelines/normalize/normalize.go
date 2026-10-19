// Package normalize maps a fully resolved Azure Pipelines syntax tree onto the
// provider-neutral IR (internal/ir).
//
// The input is an *ast.Pipeline whose compile-time constructs (template
// references, extends, `${{ }}` directives) were already expanded by the
// template resolver. A tree that still contains such a construct is rejected
// with ErrNeedsResolver; nothing here expands templates, evaluates variable
// precedence or evaluates macros. Every Azure Pipelines construct that the IR
// cannot represent exactly is recorded as a loss: the node fidelity is lowered,
// the source property is kept in the node's `x-azure-pipelines` extension and a
// registered TRL-CONV diagnostic is emitted. The normative mapping is
// docs/spec/normalize-ado.md.
package normalize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

const (
	// Platform is the diag.Source platform and Raw provider written by this package.
	Platform = "azure-pipelines"
	// ExtensionKey is the provider extension key used on IR nodes.
	ExtensionKey = "x-azure-pipelines"
	// implicitID names the implicit stage and job of shorthand pipelines.
	implicitID = "__default"
	// hashUnavailable tags an origin whose source text was not available.
	hashUnavailable = "unavailable"
)

// ErrNeedsResolver is returned (wrapped) when the tree still contains template
// references, `extends` or `${{ }}` directives.
var ErrNeedsResolver = errors.New("pipeline contains unresolved template constructs")

// RepositoryType is the hosting provider of the pipeline's repository. The
// caller supplies it; the normalizer never inspects remotes. The zero value is
// RepositoryUnknown.
type RepositoryType string

// Repository hosting types recognized for provider-default trigger synthesis.
const (
	RepositoryGitHub        RepositoryType = "github"
	RepositoryBitbucket     RepositoryType = "bitbucket"
	RepositoryAzureReposGit RepositoryType = "azureReposGit"
	RepositoryOther         RepositoryType = "other"
	RepositoryUnknown       RepositoryType = "unknown"
)

// Options configures Pipeline.
type Options struct {
	// ID is the stable pipeline identity. Empty derives it from the file name.
	ID string
	// Name is the display name. Empty derives it from the file name.
	Name string
	// SourceText returns the bytes of a source file so origins can carry the
	// SHA-256 of the exact source span (ir.Origin.OriginalTextHash). When it is
	// nil or reports false, the hash is the tag "unavailable".
	SourceText func(file string) ([]byte, bool)
	// SourceChain returns the resolver-provided template instantiation chain for
	// a source node, outermost call site first.
	SourceChain func(diag.Source) []diag.Source
	// Collector receives every finding. Nil keeps findings only in the result.
	Collector *diag.Collector
	// RepositoryType is the hosting provider of the pipeline's repository. It
	// selects the provider-default triggers the source omits. The zero value
	// RepositoryUnknown synthesizes the default PR trigger with approximated
	// fidelity (fire more often, never less).
	RepositoryType RepositoryType
}

// Pipeline normalizes a resolved pipeline. The returned IR passes ir.Validate.
// The context is checked between stages and jobs; its error is returned
// unchanged.
func Pipeline(ctx context.Context, root *ast.Pipeline, opts Options) (*ir.Pipeline, error) {
	out, _, err := pipelineWork(ctx, root, opts)
	return out, err
}

// pipelineWork is Pipeline that also reports the work counter.
func pipelineWork(ctx context.Context, root *ast.Pipeline, opts Options) (*ir.Pipeline, int, error) {
	c := &conv{}
	out, err := c.run(ctx, root, opts)
	return out, c.work, err
}

func (c *conv) run(ctx context.Context, root *ast.Pipeline, opts Options) (*ir.Pipeline, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("normalize: nil pipeline")
	}
	*c = *newConv(ctx, root, opts)
	if n := c.findUnresolved(root); n > 0 {
		finding := diag.MustNew("TRL-CONV-0010", diag.WithSource(c.source(root.Source)), diag.WithResource(c.resource),
			diag.WithParams(map[string]any{"constructs": n}))
		c.own.Add(finding)
		if opts.Collector != nil {
			opts.Collector.Add(finding)
		}
		return nil, fmt.Errorf("%w: %d construct(s)", ErrNeedsResolver, n)
	}
	out := c.pipeline(root)
	if c.err != nil {
		return nil, c.err
	}
	c.finalScrub(out)
	if c.err != nil {
		return nil, c.err
	}
	out.Diagnostics = c.own.List()
	for _, finding := range ir.Validate(out) {
		if opts.Collector != nil {
			opts.Collector.Add(finding)
		}
		if finding.Severity == diag.SeverityError {
			return nil, &diag.Error{Finding: finding}
		}
	}
	return out, nil
}

// conv carries the state of one normalization run.
type conv struct {
	ctx      context.Context
	opts     Options
	own      *diag.Collector
	file     string
	id, name string
	resource diag.Resource
	err      error
	// work counts bytes examined by the bounded scanners (backstop leaves and
	// logging-command scripts); tests assert deterministic bounds on it.
	work int
	// taint holds the literal values of secret-classified root parameters and
	// variables; taintWhole holds the short and all-digit ones that are
	// replaced on whole-leaf equality only. ts is built once from both and
	// replaces every occurrence in the backstop and in findings.
	taint      []string
	taintWhole []string
	ts         *taintSet
	// shortSecretNames holds the declared secret keys whose literal value is
	// short, small-numeric or boolean-like (the classes the taint pass replaces
	// on whole-leaf equality only). References to them by name are flagged.
	shortSecretNames []string
	shortSecretSet   map[string]bool
	// macroScopes holds effective variable declarations visible to the node
	// being converted. Outer scopes stay underneath inner ones, and later
	// declarations in one block replace earlier names.
	macroScopes []map[string]macroDeclaration
	// activeRunner is the resolved job runner used by generic script steps.
	activeRunner ir.RunnerReq
	// parts memoizes scrubbed components (see part): distinct tainted
	// components keep distinct scrubbed spellings, and partsUsed holds them.
	parts     map[string]map[string]string
	partsUsed map[string]map[string]bool
	// partNext is the next suffix to try per namespace and scrubbed text.
	partNext map[string]map[string]int

	files            map[string][]byte
	containerByAlias map[string]*ast.ContainerResource
	repoByAlias      map[string]*ast.RepositoryResource
	rootPool         *ast.Pool
	// pa is the pipeline-level accumulator.
	pa *acc
}

func newConv(ctx context.Context, root *ast.Pipeline, opts Options) *conv {
	c := &conv{ctx: ctx, opts: opts, own: diag.NewCollector(), file: root.Source.File, files: map[string][]byte{}}
	stem := strings.TrimSuffix(path.Base(strings.ReplaceAll(c.file, "\\", "/")), path.Ext(c.file))
	if stem == "" || stem == "." || stem == "/" {
		stem = "pipeline"
	}
	c.id, c.name = opts.ID, opts.Name
	if c.id == "" {
		c.id = stem
	}
	if c.name == "" {
		c.name = stem
	}
	c.resource = diag.Resource{Kind: "pipeline", Key: c.id, Name: c.name}
	return c
}

// cancelled records a context error once and reports whether to stop.
func (c *conv) cancelled() bool {
	if c.err == nil {
		c.err = c.ctx.Err()
	}
	return c.err != nil
}

// source returns src with the platform and (when missing) the file filled in.
func (c *conv) source(src diag.Source) diag.Source {
	src.Platform = Platform
	if src.File == "" {
		src.File = c.file
	}
	return src
}

// origin builds the ADR 0003 origin for a source position.
func (c *conv) origin(src diag.Source, chain []diag.Source) *ir.Origin {
	s := c.source(src)
	origin := &ir.Origin{Source: s, OriginalTextHash: c.textHash(s)}
	if c.opts.SourceChain != nil {
		chain = c.opts.SourceChain(src)
	}
	if len(chain) != 0 {
		origin.Chain = make([]diag.Source, len(chain))
		for i := range chain {
			origin.Chain[i] = c.source(chain[i])
		}
	}
	return origin
}

// stepChain returns the resolver call-site chain of a step node.
func stepChain(step ast.Step) []diag.Source {
	v := reflect.ValueOf(step)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		if f := v.Elem().FieldByName("Pos"); f.IsValid() {
			if pos, ok := f.Interface().(ast.Pos); ok {
				return pos.Chain
			}
		}
	}
	return nil
}

func (c *conv) textHash(s diag.Source) string {
	text, ok := c.span(s)
	if !ok {
		// Nothing was hashed: never present a digest of a placeholder as if it
		// were the source hash.
		return hashUnavailable
	}
	sum := sha256.Sum256(text)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (c *conv) span(s diag.Source) ([]byte, bool) {
	if c.opts.SourceText == nil {
		return nil, false
	}
	data, loaded := c.files[s.File]
	if !loaded {
		if fetched, ok := c.opts.SourceText(s.File); ok {
			data = fetched
		}
		c.files[s.File] = data
	}
	if data == nil || s.Offset < 0 || s.EndOffset <= s.Offset || s.EndOffset > len(data) {
		return nil, false
	}
	return data[s.Offset:s.EndOffset], true
}

// pipeline builds the IR root.
func (c *conv) pipeline(root *ast.Pipeline) *ir.Pipeline {
	out := ir.NewPipeline(c.id, c.name)
	a := c.newAcc(root.Source).in(root.Chain)
	c.pa = a
	c.rootPool = root.Pool
	c.collectTaint(root)
	c.collectResources(root)

	if root.Name != nil && !root.Name.Null {
		a.keep("runName", root.Name.Value)
		a.lose(codeExt, "run-name", "name", root.Name.Source, "run number format has no IR field")
	}
	if root.AppendCommitMessageToRunName != nil {
		a.keep("appendCommitMessageToRunName", root.AppendCommitMessageToRunName.Value)
		a.lose(codeExt, "run-name", "appendCommitMessageToRunName", root.AppendCommitMessageToRunName.Source, "run name option has no IR field")
	}
	if root.LockBehavior != nil {
		a.keep("lockBehavior", root.LockBehavior.Value)
		a.lose(codeExt, "lock-behavior", "lockBehavior", root.LockBehavior.Source, "exclusive-lock behavior has no IR field")
	}
	out.Inputs = c.inputs(root.Parameters)
	out.Triggers = c.triggers(root)
	out.Variables = c.variables(root.Variables, ir.ScopePipeline, "")
	c.resourcesExtension(a, root)
	c.unknown(a, "pipeline", root.Extra)
	if root.Shape != ast.ShapeSteps && (root.Strategy != nil || root.Container != nil || root.Services != nil || root.ServiceSpecs != nil || root.Workspace != nil || root.ContinueOnError != nil) {
		a.lose(codeExt, "pipeline", "rootJobProperties", root.Source, "root-level job properties are only valid for steps-shaped pipelines and were ignored")
	}

	c.pushMacroScope(root.Variables)
	defer c.popMacroScope()
	c.structure(out, root)
	if c.err != nil {
		return out
	}
	out.Metadata = a.metadata()
	c.rollUpPipeline(out)
	return out
}

func (c *conv) rollUpPipeline(out *ir.Pipeline) {
	f := out.Fidelity
	for i := range out.Inputs {
		f = maxFidelity(f, out.Inputs[i].Fidelity)
	}
	for i := range out.Triggers {
		f = maxFidelity(f, out.Triggers[i].Fidelity)
	}
	for i := range out.Variables {
		f = maxFidelity(f, out.Variables[i].Fidelity)
	}
	for i := range out.Stages {
		f = maxFidelity(f, out.Stages[i].Fidelity)
		for j := range out.Stages[i].Variables {
			f = maxFidelity(f, out.Stages[i].Variables[j].Fidelity)
		}
	}
	for i := range out.Jobs {
		f = maxFidelity(f, out.Jobs[i].Fidelity)
	}
	out.Fidelity = f
}
