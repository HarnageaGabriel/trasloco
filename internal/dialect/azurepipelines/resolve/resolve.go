// Package resolve expands Azure Pipelines templates and compile-time
// expressions into a directive-free AST. It is deliberately I/O-free: callers
// provide template bytes through Source.
//
// Contract: Resolve returns a non-nil AST only when the resolution produced no
// error-severity diagnostic and the post-condition holds (no Cond, Insert,
// Insertion, Directive, template reference or compile-time ${{ }} remains).
// Any error-severity failure, including a failed post-condition, yields a nil
// AST. A budget abort additionally returns ErrExpansionLimit, and context
// cancellation returns the context error unchanged. Warnings never discard
// the AST.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

const (
	defaultMaxFiles = 100
	defaultMaxDepth = 100
	defaultMaxBytes = 20 << 20

	// maxEachIterations caps the total number of ${{ each }} iterations in one
	// resolution. maxExpandedNodes caps the total number of nodes produced.
	// minNodeCost is the least byte cost charged per produced node,
	// minIterationCost per each iteration and minOpenCost per template
	// instantiation, so that empty bodies cannot expand without bound. The
	// counts are defence-in-depth next to the byte budget, which is charged
	// incrementally as nodes are created.
	maxEachIterations = 1_000_000
	maxExpandedNodes  = 2_000_000
	minNodeCost       = 32
	minIterationCost  = 128
	minOpenCost       = 1024

	// maxWalkDepth bounds reflective recursion over the AST (several reflect
	// hops per YAML level); exceeding it is an error, never a silent stop.
	maxWalkDepth = 20000
)

// ErrExpansionLimit is returned by Resolve, with a nil AST, once an expansion
// limit (TRL-TPL-0003, recorded in the collector) aborts expansion.
var ErrExpansionLimit = errBudget

// errBudget aborts expansion once an expansion limit (TRL-TPL-0003) is hit.
var errBudget = errors.New("resolve: expansion budget exceeded")

// maxErrorDiagnostics is N: once this many error-severity diagnostics have
// been reported, expansion stops (the AST is nil anyway). It equals the
// collector's default per-code cap, so further findings would be suppressed.
const maxErrorDiagnostics = 1000

// errTooMany stops expansion after maxErrorDiagnostics errors; Resolve maps
// it to a nil AST without an error.
var errTooMany = errors.New("resolve: too many error diagnostics")

// Source supplies a template. Empty and "self" aliases name the root
// repository. origin.File is the name used for diagnostics; it never decides
// how later relative references are resolved.
type Source interface {
	Open(ctx context.Context, repoAlias, path string) (content []byte, origin diag.Source, err error)
}

// Options controls resolution. Zero limits select the documented Azure limits;
// a negative limit allows nothing.
type Options struct {
	Collector *diag.Collector
	Variables *expr.Object
	// RootPath is the repository-relative path of the root file. It defaults to
	// the root AST position and must not be absolute or escape the repository.
	RootPath         string
	MaxFiles         int
	MaxDepth         int
	MaxExpandedBytes int
}

// Resolve returns a deep-cloned, fully expanded AST. Ordinary source and
// semantic failures are diagnostics (and a nil AST); cancellation is returned
// unchanged.
func Resolve(ctx context.Context, root *ast.Pipeline, source Source, options Options) (*ast.Pipeline, error) {
	resolved, _, err := resolveWith(ctx, root, source, options)
	return resolved, err
}

func limit(raw, fallback int) int {
	switch {
	case raw == 0:
		return fallback
	case raw < 0:
		return 0
	default:
		return raw
	}
}

func resolveWith(ctx context.Context, root *ast.Pipeline, source Source, options Options) (*ast.Pipeline, *resolver, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if root == nil {
		return nil, nil, fmt.Errorf("resolve: nil root")
	}
	if options.Collector == nil {
		options.Collector = diag.NewCollector()
	}
	r := &resolver{
		source: source, opts: options, files: make(map[string]struct{}), cache: make(map[string]*parsedFile),
		origins: make(map[expr.Value]ast.Pos), scopes: make(map[expr.Value]scope), written: make(map[any]scope),
		aliases:  map[string]bool{"": true, "self": true},
		maxFiles: limit(options.MaxFiles, defaultMaxFiles), maxDepth: limit(options.MaxDepth, defaultMaxDepth),
		maxBytes: limit(options.MaxExpandedBytes, defaultMaxBytes),
	}
	for _, finding := range options.Collector.List() {
		if finding.Severity == diag.SeverityError {
			r.failed = true
			break
		}
	}
	rootFile := options.RootPath
	if rootFile == "" {
		rootFile = root.Position().File
	}
	if rootFile == "" {
		rootFile = "azure-pipelines.yml"
	}
	r.pos = root.Position()
	name, ok := cleanPath(rootFile)
	if !ok || isAbsoluteLike(rootFile) {
		r.add("TRL-TPL-0001", r.pos, "root file path must be repository-relative", nil)
		return nil, r, nil
	}
	expanded := clone(root)
	r.stack = append(r.stack, includeKey("", name))
	rootScope := scope{file: name}
	params, _, err := r.bind(ctx, expanded.Parameters, nil, nil, true, rootScope)
	if err != nil {
		return nil, r, r.fatal(err)
	}
	if err := r.pipeline(ctx, expanded, params, options.Variables, "", name, 0, nil); err != nil {
		if errors.Is(err, errTooMany) {
			return nil, r, nil
		}
		if errors.Is(err, errBudget) {
			// A partial AST must never reach later passes.
			return nil, r, ErrExpansionLimit
		}
		return nil, r, err
	}
	if err := r.validate(ctx, expanded); err != nil {
		if errors.Is(err, errTooMany) {
			return nil, r, nil
		}
		if errors.Is(err, errBudget) {
			return nil, r, ErrExpansionLimit
		}
		return nil, r, err
	}
	// No final size check: the budget is charged incrementally as nodes are
	// created, so the result is already bounded by r.maxBytes in those units.
	if !r.failed {
		r.postcondition(expanded)
	}
	if r.failed {
		return nil, r, nil
	}
	return expanded, r, nil
}

type parsedFile struct {
	tree     *ast.Pipeline
	findings []diag.Diagnostic
	length   int
}

type resolver struct {
	source  Source
	opts    Options
	stack   []string
	files   map[string]struct{}
	aliases map[string]bool
	cache   map[string]*parsedFile
	// origins records where composite expression values were written.
	origins map[expr.Value]ast.Pos
	// scopes records the file scope in which a composite value was written, and
	// written the scope of each template reference materialized from such a
	// value: a reference resolves relative to the file that wrote it.
	scopes  map[expr.Value]scope
	written map[any]scope
	// next is the frame prepared for the struct the walker enters next.
	next *frame

	maxFiles, maxDepth, maxBytes int

	scope      scope
	pos        diag.Source
	bytes      int
	nodes      int
	iterations int
	exceeded   bool
	failed     bool
	errorCount int
}

// trip records the single TRL-TPL-0003 diagnostic for an exceeded budget.
func (r *resolver) trip(message string, params map[string]any) error {
	if !r.exceeded {
		r.exceeded = true
		r.add("TRL-TPL-0003", r.pos, message, params)
	}
	return errBudget
}

// charge accounts for newly created nodes and approximate bytes, failing as
// soon as a limit is exceeded. Callers charge only what they create.
func (r *resolver) charge(nodes, bytes int) error {
	if r.exceeded {
		return errBudget
	}
	r.nodes += nodes
	// Each produced node also carries a reference to its template instance's
	// call-site chain, so a deep chain makes every node dearer.
	r.bytes += max(bytes, nodes*minNodeCost) + nodes*len(r.scope.chain)
	if r.bytes > r.maxBytes {
		return r.trip("expanded pipeline exceeds the 20 MB limit", map[string]any{"limit": r.maxBytes})
	}
	if r.nodes > maxExpandedNodes {
		return r.trip("expanded pipeline exceeds the node limit", map[string]any{"limit": maxExpandedNodes})
	}
	return nil
}

// iterate charges one ${{ each }} iteration whose body costs bodyBytes.
func (r *resolver) iterate(bodyBytes int) error {
	r.iterations++
	if r.iterations > maxEachIterations {
		return r.trip("each iterations exceed the limit", map[string]any{"limit": maxEachIterations})
	}
	return r.charge(1, max(bodyBytes, minIterationCost))
}

// collectAliases records the repository aliases declared by the resources of
// p. It runs after the resources were expanded, so a repository written under
// a false condition, or produced by a parameter or an each, is judged by what
// the resolved file really declares.
func (r *resolver) collectAliases(p *ast.Pipeline) {
	if r.aliases == nil {
		r.aliases = map[string]bool{"": true, "self": true}
	}
	if p.Resources == nil || p.Resources.Repositories == nil {
		return
	}
	for _, item := range p.Resources.Repositories.Items {
		if item.Value != nil && item.Value.Repository != nil {
			r.aliases[item.Value.Repository.Value] = true
		}
	}
}

// pipeline resolves one file in place. params are the bound parameters and
// inherited the variables visible from the including file.
func (r *resolver) pipeline(ctx context.Context, p *ast.Pipeline, params, inherited *expr.Object, repo, file string, depth int, chain []diag.Source) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(chain) > 0 {
		// One shared chain per instance; each stamped position costs len(chain).
		stamped := appendChain(reflect.ValueOf(p), chain)
		if err := r.charge(0, stamped*len(chain)); err != nil {
			return err
		}
	}
	saved := r.scope
	r.scope = scope{repo: repo, file: file, depth: depth, chain: chain}
	defer func() { r.scope = saved }()
	env := environment(params, inherited2(inherited))
	if p.Variables == nil {
		// A conditional `variables:` key must be merged before the variables
		// are collected. With a literal block present, conditions may depend
		// on it, so the key directives stay in the normal walk order.
		if err := r.templateTail(ctx, p, env); err != nil {
			return err
		}
	}
	if err := r.expandVariables(ctx, p.Variables, env); err != nil {
		return err
	}
	env = environment(params, r.variables(p.Variables, inherited))
	// Resources are expanded first so that template references of this file
	// (including extends) can name the aliases they declare.
	if p.Resources != nil {
		if err := r.walk(ctx, reflect.ValueOf(p.Resources), env, 0); err != nil {
			return err
		}
	}
	r.collectAliases(p)
	var base *ast.Pipeline
	if p.Extends != nil && p.Extends.Template != nil {
		if err := r.templateTail(ctx, p.Extends, env); err != nil {
			return err
		}
		var err error
		base, err = r.open(ctx, p.Extends.TemplateRef, env)
		if err != nil {
			return err
		}
	}
	if err := r.walk(ctx, reflect.ValueOf(p), env, 0); err != nil {
		return err
	}
	if base != nil {
		overlayPipeline(base, p)
		keepPos, keepParams := p.Pos, p.Parameters
		*p = *base
		p.Pos, p.Parameters = keepPos, keepParams
	}
	return nil
}

func inherited2(vars *expr.Object) *expr.Object {
	if vars == nil {
		return &expr.Object{}
	}
	return vars
}

type scope struct {
	repo, file string
	depth      int
	chain      []diag.Source
}

func (r *resolver) open(ctx context.Context, ref ast.TemplateRef, parent *expr.Object) (*ast.Pipeline, error) {
	currentRepo, currentFile, depth, chain := r.scope.repo, r.scope.file, r.scope.depth, r.scope.chain
	if ref.Template == nil {
		r.add("TRL-TPL-0001", diag.Source{}, "template path is empty", nil)
		return nil, nil
	}
	at := ref.Template.Pos
	if ref.Dynamic {
		// Azure evaluates a template path at compile time; so do we.
		resolvedRef, ok, err := r.dynamicRef(ctx, ref, parent)
		if err != nil || !ok {
			return nil, err
		}
		ref = resolvedRef
	}
	repo := ref.Alias
	if repo == "" {
		repo = currentRepo
	}
	if repo == "self" {
		repo = ""
	}
	if ref.Alias != "" && ref.Alias != "self" && !r.aliases[ref.Alias] {
		r.addAt("TRL-TPL-0007", at, "unknown template repository alias "+ref.Alias, map[string]any{"alias": ref.Alias})
		return nil, nil
	}
	baseFile := currentFile
	if ref.Alias != "" {
		baseFile = ""
	}
	name, ok := resolveRef(baseFile, ref.File)
	if !ok {
		r.addAt("TRL-TPL-0001", at, "template path escapes the repository root: "+ref.File, map[string]any{"path": ref.File})
		return nil, nil
	}
	key := includeKey(repo, name)
	for _, active := range r.stack {
		if active == key {
			cycle := append(append([]string(nil), r.stack...), key)
			r.addAt("TRL-TPL-0002", at, "template cycle: "+strings.Join(cycle, " -> "), map[string]any{"chain": cycle})
			return nil, nil
		}
	}
	if depth+1 > r.maxDepth {
		r.addAt("TRL-TPL-0003", at, "template nesting exceeds 100 levels", map[string]any{"limit": r.maxDepth})
		return nil, nil
	}
	if _, seen := r.files[key]; !seen {
		if len(r.files) >= r.maxFiles {
			r.addAt("TRL-TPL-0003", at, "pipeline includes more than 100 YAML files", map[string]any{"limit": r.maxFiles})
			return nil, nil
		}
		r.files[key] = struct{}{}
	}
	cached, size, err := r.load(ctx, key, repo, name, at)
	if err != nil || cached == nil {
		return nil, err
	}
	if err := r.charge(1, max(size, minOpenCost)); err != nil {
		return nil, err
	}
	parsed := clone(cached.tree)
	nextChain := append(append([]diag.Source(nil), chain...), at.Source)
	calleeScope := scope{repo: repo, file: name, depth: depth + 1, chain: nextChain}
	args, valid, err := r.bind(ctx, parsed.Parameters, ref.Parameters, parent, false, calleeScope)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, nil
	}
	r.stack = append(r.stack, key)
	err = r.pipeline(ctx, parsed, args, lookupObject(parent, "variables"), repo, name, depth+1, nextChain)
	r.stack = r.stack[:len(r.stack)-1]
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// dynamicRef evaluates a template path written with ${{ }} against the
// reference-site environment. A path that is exactly one expression must
// evaluate to a string; embedded expressions are interpolated as text.
func (r *resolver) dynamicRef(ctx context.Context, ref ast.TemplateRef, env *expr.Object) (ast.TemplateRef, bool, error) {
	text := ""
	at := ref.Template.Pos
	path := clone(ref.Template)
	if whole := path.Whole(); whole != nil && whole.Kind == ast.DirectiveInsert {
		value, ok, err := r.evalOK(ctx, whole, env)
		if err != nil || !ok {
			return ref, false, err
		}
		str, isString := value.(expr.String)
		if !isString {
			r.addAt("TRL-EXPR-0005", at, "template path must evaluate to a string", nil)
			return ref, false, nil
		}
		text = string(str)
	} else {
		failed := r.failed
		if err := r.expandScalar(ctx, path, env); err != nil {
			return ref, false, err
		}
		if r.failed && !failed {
			return ref, false, nil
		}
		text = path.Value
	}
	text = strings.TrimSpace(text)
	if text == "" {
		r.addAt("TRL-TPL-0001", at, "template path is empty", nil)
		return ref, false, nil
	}
	ref.File, ref.Alias, ref.Dynamic = text, "", false
	if i := strings.LastIndex(text, "@"); i > 0 && i < len(text)-1 {
		ref.File, ref.Alias = text[:i], text[i+1:]
	}
	return ref, true, nil
}

// load reads and parses a template once per key; callers clone the result.
func (r *resolver) load(ctx context.Context, key, repo, name string, at ast.Pos) (*parsedFile, int, error) {
	if cached, ok := r.cache[key]; ok {
		if cached.tree == nil {
			return nil, 0, nil
		}
		return cached, cached.length, nil
	}
	if r.source == nil {
		r.addAt("TRL-TPL-0001", at, "template source is unavailable", map[string]any{"path": name})
		return nil, 0, nil
	}
	data, origin, err := r.source.Open(ctx, repo, name)
	if err != nil {
		if isFatal(ctx, err) {
			return nil, 0, err
		}
		r.addAt("TRL-TPL-0001", at, "cannot open template "+name+": "+err.Error(), map[string]any{"path": name, "alias": repo})
		return nil, 0, nil
	}
	if origin.File == "" {
		origin.File = name
	}
	// The size is charged once per instantiation in open, not here as well.
	private := diag.NewCollector()
	tree := parse.Source(origin.File, data, private)
	entry := &parsedFile{tree: tree, findings: private.List(), length: len(data)}
	r.cache[key] = entry
	for _, finding := range entry.findings {
		r.report(finding)
	}
	if tree == nil {
		if !r.failed {
			r.addAt("TRL-TPL-0001", at, "template could not be parsed: "+name, map[string]any{"path": name})
		}
		return nil, 0, nil
	}
	return entry, entry.length, nil
}

// cleanPath normalizes a repository-relative name; ok is false when it escapes
// the repository root.
func cleanPath(name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	var parts []string
	for _, part := range strings.Split(name, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return "", false
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/"), true
}

func isAbsoluteLike(name string) bool {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") {
		return true
	}
	return len(name) >= 2 && name[1] == ':' && (name[0]|0x20) >= 'a' && (name[0]|0x20) <= 'z'
}

// resolveRef resolves a template reference against the including file.
func resolveRef(current, reference string) (string, bool) {
	reference = strings.ReplaceAll(reference, "\\", "/")
	if strings.HasPrefix(reference, "/") {
		return cleanPath(reference)
	}
	current = strings.ReplaceAll(current, "\\", "/")
	dir := ""
	if i := strings.LastIndex(current, "/"); i >= 0 {
		dir = current[:i]
	}
	return cleanPath(dir + "/" + reference)
}

func includeKey(repo, file string) string {
	// Windows filesystems are normally case-insensitive. Normalize the path so
	// a cycle cannot evade detection by changing only letter case.
	if runtime.GOOS == "windows" {
		file = strings.ToLower(file)
	}
	return repo + "@" + file
}

func (r *resolver) add(code string, source diag.Source, message string, params map[string]any) {
	opts := []diag.Option{diag.WithSource(source), diag.WithMessage(message)}
	if params != nil {
		opts = append(opts, diag.WithParams(params))
	}
	r.report(diag.MustNew(code, opts...))
}

// addAt reports at a node position; the call-site chain is added as a param.
func (r *resolver) addAt(code string, pos ast.Pos, message string, params map[string]any) {
	if len(pos.Chain) > 0 {
		merged := map[string]any{}
		//trasloco:unordered-ok copying map entries; insertion order is unobservable
		for k, v := range params {
			merged[k] = v
		}
		if _, ok := merged["chain"]; !ok {
			merged["callSites"] = callSites(pos.Chain)
		}
		params = merged
	}
	r.add(code, pos.Source, message, params)
}

// report records a finding and remembers whether any error-severity one occurred.
func (r *resolver) report(d diag.Diagnostic) {
	if d.Severity == diag.SeverityError {
		r.failed = true
		r.errorCount++
	}
	r.opts.Collector.Add(d)
}

func sourceOf(s *ast.Scalar) diag.Source {
	if s == nil {
		return diag.Source{}
	}
	return s.Position()
}

// fatal maps the internal budget error to the exported one.
func (r *resolver) fatal(err error) error {
	if errors.Is(err, errBudget) {
		return ErrExpansionLimit
	}
	if errors.Is(err, errTooMany) {
		return nil
	}
	return err
}

func callSites(chain []diag.Source) []string {
	trail := make([]string, 0, len(chain))
	for _, source := range chain {
		trail = append(trail, fmt.Sprintf("%s:%d", source.File, source.Line))
	}
	return trail
}
