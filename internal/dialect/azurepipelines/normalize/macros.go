package normalize

import (
	"regexp"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/hosted"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

var (
	providerMacroWhole = regexp.MustCompile(`^\$\(([A-Za-z_][A-Za-z0-9_.-]*)\)$`)
	providerMacroScan  = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_.-]*)\)`)
)

type macroDeclaration struct{ secret bool }

func (c *conv) pushMacroScope(values *ast.Variables) {
	scope := map[string]macroDeclaration{}
	if values != nil {
		if values.Map != nil {
			for _, entry := range values.Map.Entries {
				if entry.Cond != nil || entry.Key == nil || entry.Value == nil {
					continue
				}
				scope[strings.ToLower(entry.Key.Value)] = macroDeclaration{secret: c.sourceTextTainted(entry.Value.Value) || literalSecret(entry.Key.Value, entry.Value.Value)}
			}
		}
		if values.List != nil {
			for _, item := range values.List.Items {
				def, ok := item.Value.(*ast.VariableDef)
				if !ok || def.Name == nil {
					continue
				}
				declaration := macroDeclaration{}
				if def.Value != nil {
					declaration.secret = c.sourceTextTainted(def.Value.Value) || literalSecret(def.Name.Value, def.Value.Value)
				}
				scope[strings.ToLower(def.Name.Value)] = declaration
			}
		}
	}
	c.macroScopes = append(c.macroScopes, scope)
}

func (c *conv) popMacroScope() {
	c.macroScopes = c.macroScopes[:len(c.macroScopes)-1]
}

func (c *conv) classifyMacro(name string) (declared, secret bool) {
	for index := len(c.macroScopes) - 1; index >= 0; index-- {
		if declaration, ok := c.macroScopes[index][strings.ToLower(name)]; ok {
			return true, declaration.secret
		}
	}
	return false, false
}

// classifyMacroRoot classifies a macro against the pipeline-root variable
// scope only. Resources are pipeline-level declarations, so a repository
// resource ref cannot depend on a stage or job variable.
func (c *conv) classifyMacroRoot(name string) (declared, secret bool) {
	if len(c.macroScopes) == 0 {
		return false, false
	}
	if declaration, ok := c.macroScopes[0][strings.ToLower(name)]; ok {
		return true, declaration.secret
	}
	return false, false
}

func (c *conv) wholeMacro(a *acc, construct, property string, source diag.Source, value string) (ir.Expr, bool) {
	return c.wholeMacroWith(a, construct, property, source, value, c.classifyMacro)
}

func (c *conv) wholeMacroRoot(a *acc, construct, property string, source diag.Source, value string) (ir.Expr, bool) {
	return c.wholeMacroWith(a, construct, property, source, value, c.classifyMacroRoot)
}

func (c *conv) wholeMacroWith(a *acc, construct, property string, source diag.Source, value string, classify func(string) (bool, bool)) (ir.Expr, bool) {
	match := providerMacroWhole.FindStringSubmatch(value)
	if match == nil {
		return ir.Expr{}, false
	}
	name := match[1]
	if safeText(name) != name {
		// The name is credential-shaped: never put it in a MacroExpr. The
		// caller's literal path records the usual scrub loss.
		return ir.Expr{}, false
	}
	declared, secret := classify(name)
	if !declared {
		a.lose(codeDeferred, construct, property, source, "undeclared Azure macro is retained with unresolved classification (VERIFY-22)")
	}
	return ir.Expr{Kind: ir.ExprMacro, Macro: &ir.MacroExpr{Name: name, Declared: declared, Secret: secret}}, true
}

func (c *conv) macroRefs(a *acc, property string, source diag.Source, text string) []ir.MacroRef {
	matches := providerMacroScan.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	// Every occurrence is scanned and listed, but an undeclared macro records
	// at most one deferred loss per property and distinct name.
	c.work += len(text) + len(matches)
	out := make([]ir.MacroRef, 0, len(matches))
	lost := map[string]bool{}
	for _, match := range matches {
		name := match[1]
		if safeText(name) != name {
			// Credential-shaped name: skip the ref; the caller scrubbed the
			// text and recorded the usual scrub loss.
			continue
		}
		declared, secret := c.classifyMacro(name)
		if !declared && !lost[strings.ToLower(name)] {
			a.lose(codeDeferred, "script", property, source, "undeclared Azure macro is retained with unresolved classification (VERIFY-22)")
			lost[strings.ToLower(name)] = true
		}
		out = append(out, ir.MacroRef{Name: name, Declared: declared, Secret: secret})
	}
	return out
}

// hostedShellOS returns the OS only for a resolved documented
// Microsoft-hosted runner. Named self-hosted pools and unknown images stay
// on the Azure default shell.
func hostedShellOS(runner ir.RunnerReq) (string, bool) {
	os, ok := hosted.ImageOS(runner.Image)
	if !ok {
		return "", false
	}
	if runner.Pool != "" && !hosted.IsHostedPoolName(runner.Pool) {
		return "", false
	}
	return os, true
}

// hookRunner returns only the runner fields needed to resolve a hook's generic
// script shell. Hook pools have no IR field and are already retained as the
// deployment hook.pool loss, so deriving the shell must not normalize the pool
// again or add job-level pool losses.
func hookRunner(pool *ast.Pool) ir.RunnerReq {
	if pool == nil || agentlessPool(pool) {
		return ir.RunnerReq{}
	}
	return ir.RunnerReq{Pool: scalarOr(pool.Name, ""), Image: scalarOr(pool.VMImage, "")}
}

func (c *conv) useRunner(runner ir.RunnerReq) func() {
	previous := c.activeRunner
	c.activeRunner = runner
	return func() { c.activeRunner = previous }
}
