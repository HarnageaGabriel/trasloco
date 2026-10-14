package normalize

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// minTaintBytes is the shortest secret literal that is replaced inside longer
// text. Every declared value is also tracked for whole-leaf replacement.
const minTaintBytes = 4

// minNumericSubstring is the shortest decimal secret replaced inside longer
// text (a PIN in `echo 123456`). Shorter decimals match whole leaves only.
const minNumericSubstring = 6

// addTaint records value as secret material when it is a literal under a
// secret-classified key.
func (c *conv) addTaint(key, value string) {
	if !secretName(key) {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	if shortSecretValue(value) {
		c.addShortSecretName(key)
	}
	// Whole-leaf tracking is unconditional for a declared secret: booleans,
	// decimals, exponent forms, short values and surrounding whitespace must
	// not escape through a typed or canonicalized carrier.
	c.taintWhole = append(c.taintWhole, trimmed)
	if value != trimmed {
		c.taintWhole = append(c.taintWhole, value)
	}
	if literalSecret(key, value) && len(value) >= minTaintBytes {
		c.taint = append(c.taint, value)
		if trimmed != value && len(trimmed) >= minTaintBytes {
			c.taint = append(c.taint, trimmed)
		}
	}
	// A decimal value is also tracked in the canonical spelling the expression
	// lexer produces (7421 for 007421, 1.5 for 1.50), so a number literal built
	// from it is recognized. Decimals of minNumericSubstring or more characters
	// are replaced inside longer text too (documented loss).
	forms := []string{trimmed}
	if canon, ok := canonicalDecimal(trimmed); ok {
		if canon != trimmed {
			forms = append(forms, canon)
			c.taintWhole = append(c.taintWhole, canon)
		}
		if canon == "0" {
			c.taintWhole = append(c.taintWhole, "-0")
		}
	}
	for _, form := range forms {
		if numericText(form) && len(form) >= minNumericSubstring {
			c.taint = append(c.taint, form)
		}
	}
}

// shortSecretValue reports a declared secret value whose occurrences inside
// longer text are not replaced: under minTaintBytes, a decimal under
// minNumericSubstring characters (in any tracked spelling), or a boolean-like
// word. References to such a variable by name are flagged for manual review.
func shortSecretValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if numericText(trimmed) {
		if len(trimmed) < minNumericSubstring {
			return true
		}
		canon, ok := canonicalDecimal(trimmed)
		return ok && len(canon) < minNumericSubstring
	}
	if nonSecretValue(trimmed) {
		return true
	}
	return len(trimmed) < minTaintBytes
}

// addShortSecretName records a secret key for the #13 reference scan, once.
func (c *conv) addShortSecretName(name string) {
	if name == "" {
		return
	}
	if c.shortSecretSet == nil {
		c.shortSecretSet = map[string]bool{}
	}
	if c.shortSecretSet[name] {
		return
	}
	c.shortSecretSet[name] = true
	c.shortSecretNames = append(c.shortSecretNames, name)
}

// addTaintAny records the literal leaves of a free-form default whose key (or
// an enclosing key) is secret-classified.
func (c *conv) addTaintAny(value *ast.Any, key string) {
	if value == nil {
		return
	}
	switch value.Kind {
	case ast.AnyScalar:
		if value.Scalar != nil && !value.Scalar.Null {
			c.addTaint(key, value.Scalar.Value)
		}
	case ast.AnySeq:
		for _, item := range value.Items {
			c.addTaintAny(item, key)
		}
	case ast.AnyMap:
		for _, entry := range value.Entries {
			if entry.Key == nil {
				continue
			}
			inner := entry.Key.Value
			if secretName(key) {
				inner = key
			}
			c.addTaintAny(entry.Value, inner)
		}
	}
}

// collectTaint gathers the literal values of secret-classified root parameters
// and variables. The resolver substitutes them into every use site; the
// backstop later replaces every occurrence. Template-file and call-site
// parameter values are not root declarations and are a known limit.
func (c *conv) collectTaint(root *ast.Pipeline) {
	if p := root.Parameters; p != nil {
		if p.Map != nil {
			for _, entry := range p.Map.Entries {
				if entry.Cond == nil && entry.Key != nil {
					c.addTaintAny(entry.Value, entry.Key.Value)
				}
			}
		}
		if p.List != nil {
			for _, item := range p.List.Items {
				if item.Value != nil && item.Value.Name != nil {
					c.addTaintAny(item.Value.Default, item.Value.Name.Value)
				}
			}
		}
	}
	if v := root.Variables; v != nil {
		if v.Map != nil {
			for _, entry := range v.Map.Entries {
				if entry.Cond == nil && entry.Key != nil && entry.Value != nil {
					c.addTaint(entry.Key.Value, entry.Value.Value)
				}
			}
		}
		if v.List != nil {
			for _, item := range v.List.Items {
				if def, ok := item.Value.(*ast.VariableDef); ok && def.Name != nil && def.Value != nil {
					c.addTaint(def.Name.Value, def.Value.Value)
				}
			}
		}
	}
	c.ts = newTaintSet(c.taint, c.taintWhole)
}

func (c *conv) inputs(parameters *ast.Parameters) []ir.Input {
	if parameters == nil {
		return nil
	}
	var out []ir.Input
	if parameters.Form == ast.ParametersMap && parameters.Map != nil {
		for _, entry := range parameters.Map.Entries {
			if entry.Cond != nil || entry.Key == nil {
				continue
			}
			src := entry.Key.Source
			if entry.Value != nil {
				src = spanTo(src, entry.Value.Source)
			}
			a := c.newAcc(src).in(entry.Key.Chain)
			input := ir.Input{ID: entry.Key.Value, Name: entry.Key.Value, Type: ir.InputString}
			if entry.Value != nil && entry.Value.Kind == ast.AnyScalar && entry.Value.Scalar != nil {
				input.Type = inferInputType(entry.Value.Scalar)
			}
			input.Default = c.inputDefault(a, entry.Key.Value, entry.Value, input.Type, nil, src)
			input.Metadata = a.metadata()
			out = append(out, input)
		}
		return out
	}
	if parameters.List == nil {
		return out
	}
	for _, item := range parameters.List.Items {
		p := item.Value
		if p == nil || p.Name == nil {
			continue
		}
		a := c.newAcc(p.Source).in(p.Chain)
		typeName := "string"
		if p.Type != nil && p.Type.Canon != "" {
			typeName = p.Type.Canon
		}
		inputType := ir.InputString
		switch strings.ToLower(typeName) {
		case "boolean":
			inputType = ir.InputBool
		case "number":
			inputType = ir.InputNumber
		case "string":
		default:
			a.keep("type", typeName)
			a.lose(codeExt, "parameter-type", "type", p.Type.Source, "structured Azure parameter type is not representable as an IR input")
		}
		var choices []string
		if p.Values != nil {
			for _, choice := range p.Values.Items {
				if choice.Value != nil {
					choices = append(choices, choice.Value.Value)
				}
			}
		}
		if len(choices) > 0 {
			if inputType == ir.InputString && strings.EqualFold(typeName, "string") {
				inputType = ir.InputChoice
			} else {
				a.keep("values", choices)
				a.lose(codeExt, "parameter", "values", p.Values.Source, "allowed-values restriction is only representable for string parameters")
				choices = nil
			}
		}
		input := ir.Input{ID: p.Name.Value, Name: scalarOr(p.DisplayName, p.Name.Value), Type: inputType, Choices: choices}
		input.Default = c.inputDefault(a, p.Name.Value, p.Default, inputType, choices, p.Source)
		input.Required = input.Default == nil
		c.unknown(a, "parameter", p.Extra)
		input.Metadata = a.metadata()
		out = append(out, input)
	}
	return out
}

var decimalText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// inferInputType types a map-form parameter from its default value.
func inferInputType(value *ast.Scalar) ir.InputType {
	text := strings.TrimSpace(value.Value)
	switch {
	case value.Null:
		return ir.InputString
	case strings.EqualFold(text, "true"), strings.EqualFold(text, "false"):
		return ir.InputBool
	case decimalText.MatchString(text):
		return ir.InputNumber
	default:
		return ir.InputString
	}
}

// spanTo widens first so its byte range ends where last ends.
func spanTo(first, last diag.Source) diag.Source {
	if last.EndOffset > first.EndOffset {
		first.EndOffset = last.EndOffset
	}
	return first
}

// inputDefault builds the default literal from the declared input type. A
// default that cannot be expressed in that type is dropped with a recorded loss
// instead of producing an IR that fails validation.
func (c *conv) inputDefault(a *acc, name string, value *ast.Any, kind ir.InputType, choices []string, src diag.Source) *ir.Literal {
	if value == nil {
		return nil
	}
	if value.Kind != ast.AnyScalar || value.Scalar == nil {
		retained, removed := anyValueKey(value, name)
		a.keep("default", retained)
		a.lose(codeExt, "parameter", "default", src, "structured parameter default has no IR literal")
		if removed {
			a.needsManual()
		}
		return nil
	}
	scalar := value.Scalar
	text := scalar.Value
	if scalar.Null {
		text = ""
	}
	if c.sourceTextTainted(text) || literalSecret(name, text) {
		a.keep("default", redact.Replacement())
		a.lose(codeExt, "parameter", "default", scalar.Source, "secret-looking parameter default was not copied into the IR")
		a.needsManual()
		return nil
	}
	bad := func(reason string) *ir.Literal {
		a.keep("default", safeText(text))
		a.lose(codeExt, "parameter", "default", scalar.Source, reason)
		return nil
	}
	switch kind {
	case ir.InputBool:
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true":
			return &ir.Literal{Type: ir.LiteralBool, Bool: true}
		case "false":
			return &ir.Literal{Type: ir.LiteralBool}
		}
		return bad("parameter default is not a boolean")
	case ir.InputNumber:
		trimmed := strings.TrimSpace(text)
		if !decimalText.MatchString(trimmed) {
			return bad("parameter default is not a canonical decimal number")
		}
		return &ir.Literal{Type: ir.LiteralNumber, Number: trimmed}
	case ir.InputChoice:
		for _, choice := range choices {
			if choice == text {
				return &ir.Literal{Type: ir.LiteralString, String: safeText(text)}
			}
		}
		return bad("parameter default is not one of the allowed values")
	default:
		return &ir.Literal{Type: ir.LiteralString, String: safeText(text)}
	}
}

func (c *conv) triggers(root *ast.Pipeline) []ir.Trigger {
	var out []ir.Trigger
	if root.Trigger == nil {
		// Azure runs CI on every branch when `trigger` is absent. The default is
		// not written in source, so it is synthesized (ir.md section 4).
		out = append(out, c.defaultTrigger(root, "ci", ir.TriggerPush, diag.FidelityExact,
			"default CI trigger: Azure triggers on every branch when trigger is omitted"))
	} else if root.Trigger.Form != ast.TriggerNone {
		out = append(out, c.trigger("ci", ir.TriggerPush, root.Trigger, root.Trigger.Pos))
	}
	switch {
	case root.PR != nil && root.PR.Form != ast.TriggerNone:
		out = append(out, c.trigger("pr", ir.TriggerPR, root.PR, root.PR.Pos))
	case root.PR == nil:
		if t, ok := c.defaultPRTrigger(root); ok {
			out = append(out, t)
		}
	}
	if root.Schedules != nil {
		for i, item := range root.Schedules.Items {
			schedule := item.Value
			if schedule == nil || schedule.Cron == nil {
				continue
			}
			a := c.newAcc(schedule.Source).in(schedule.Chain)
			if schedule.DisplayName != nil {
				a.keep("displayName", schedule.DisplayName.Value)
				a.lose(codeExt, "schedule", "displayName", schedule.DisplayName.Source, "schedule display name has no IR field")
			}
			if schedule.Batch != nil {
				a.keep("batch", schedule.Batch.Value)
				a.lose(codeExt, "schedule", "batch", schedule.Batch.Source, "schedule batch behavior has no IR field")
			}
			always := c.flag(a, schedule.Always, "schedule", "always")
			c.unknown(a, "schedule", schedule.Extra)
			branches := c.filterGlobs(a, schedule.Branches)
			trigger := ir.Trigger{Metadata: a.metadata(), ID: fmt.Sprintf("schedule-%d", i+1), Kind: ir.TriggerSchedule, Binding: ir.TriggerBinding{InYAML: true}}
			trigger.Filters.Branches = branches
			trigger.Schedule = &ir.Schedule{Cron: schedule.Cron.Value, Timezone: "Etc/UTC", Original: schedule.Cron.Value, OnlyIfChanged: !always}
			out = append(out, trigger)
		}
	}
	out = append(out, c.resourceTriggers(root)...)
	return out
}

// flag reads a boolean property. A value that is not a literal boolean (for
// example a macro) cannot be evaluated here: false is used and a loss recorded.
func (c *conv) flag(a *acc, value *ast.Bool, construct, property string) bool {
	if value == nil {
		return false
	}
	if !value.Known {
		a.keep(property, safeText(value.Value))
		a.lose(codeExt, construct, property, value.Source, "boolean property is not a literal and cannot be evaluated statically")
		return false
	}
	return value.Val
}

func (c *conv) trigger(id string, kind ir.TriggerKind, source *ast.Trigger, pos ast.Pos) ir.Trigger {
	a := c.newAcc(pos.Source).in(pos.Chain)
	out := ir.Trigger{ID: id, Kind: kind, Binding: ir.TriggerBinding{InYAML: true}}
	if source != nil {
		out.Filters.Branches = c.filterGlobs(a, source.Branches)
		out.Filters.Paths = c.filterGlobs(a, source.Paths)
		out.Filters.Tags = c.filterGlobs(a, source.Tags)
		if source.Batch != nil {
			a.keep("batch", source.Batch.Value)
			a.lose(codeExt, "trigger", "batch", source.Batch.Source, "batched trigger behavior has no IR field")
		}
		if source.AutoCancel != nil {
			a.keep("autoCancel", source.AutoCancel.Value)
			a.lose(codeExt, "trigger", "autoCancel", source.AutoCancel.Source, "PR auto-cancel has no IR field")
		}
		if source.Drafts != nil {
			a.keep("drafts", source.Drafts.Value)
			a.lose(codeExt, "trigger", "drafts", source.Drafts.Source, "draft PR inclusion has no IR field")
		}
		c.unknown(a, "trigger", source.Extra)
	}
	out.Metadata = a.metadata()
	return out
}

// defaultTrigger builds a provider-default trigger the source omits. The trigger
// is not written in source YAML, so its binding records the synthesis (ir.md
// section 4) and the reader records TRL-CONV-0004.
func (c *conv) defaultTrigger(root *ast.Pipeline, id string, kind ir.TriggerKind, fidelity diag.Fidelity, note string) ir.Trigger {
	a := c.newAcc(root.Source).in(root.Chain).withFidelity(fidelity)
	a.keep("synthesized", note)
	a.note(codeSynth, "trigger", string(kind), root.Source, note)
	return ir.Trigger{
		Metadata: a.metadata(),
		ID:       id,
		Kind:     kind,
		Binding:  ir.TriggerBinding{InYAML: false, Synthetic: true},
	}
}

// defaultPRTrigger synthesizes the repository-type-dependent default PR trigger
// Azure Pipelines applies when `pr` is omitted (normalize-ado.md).
func (c *conv) defaultPRTrigger(root *ast.Pipeline) (ir.Trigger, bool) {
	repo := c.opts.RepositoryType
	if repo == "" {
		repo = RepositoryUnknown // the documented zero value
	}
	switch repo {
	case RepositoryAzureReposGit:
		// Azure Repos Git owns PR validation with branch policies, so no YAML pr
		// trigger is synthesized; record why with TRL-CONV-0004.
		c.newAcc(root.Source).in(root.Chain).note(codeSynth, "trigger", "pr", root.Source,
			"Azure Repos Git uses branch policies for pull request validation; no pr trigger is synthesized")
		return ir.Trigger{}, false
	case RepositoryGitHub, RepositoryBitbucket:
		// Only these two hosting types have an exact unfiltered default PR
		// trigger; every other value is unconfirmed.
		return c.defaultTrigger(root, "pr", ir.TriggerPR, diag.FidelityExact,
			"default PR trigger: Azure validates pull requests to all branches when pr is omitted"), true
	default:
		// other, unknown and unrecognized spellings: the provider default is
		// unconfirmed here, so fire more often, never less, and mark the
		// synthesized trigger approximated.
		return c.defaultTrigger(root, "pr", ir.TriggerPR, diag.FidelityApproximated,
			"default PR trigger: pull request validation for all branches (repository type unknown)"), true
	}
}

func (c *conv) filterGlobs(a *acc, filter *ast.Filter) []ir.Glob {
	if filter == nil {
		return nil
	}
	var includes, excludes []ir.Glob
	appendList := func(list *ast.List[*ast.Scalar], exclude bool, out *[]ir.Glob) {
		if list == nil {
			return
		}
		for _, item := range list.Items {
			if item.Value == nil {
				continue
			}
			// The component is scrubbed before the "!" prefix is composed.
			pattern := c.part(a, "glob", item.Value.Value, item.Value.Source)
			if normalized, widened := translateGlob(pattern); widened {
				a.lose(codeApprox, "glob", "pattern", item.Value.Source,
					"Azure `*` matches across `/`; normalized to the IR `**` (VERIFY-01)")
				pattern = normalized
			}
			if exclude {
				pattern = "!" + pattern
			}
			*out = append(*out, ir.Glob{Pattern: pattern, Original: c.sanitize(item.Value.Value)})
		}
	}
	appendList(filter.Include, false, &includes)
	appendList(filter.Exclude, true, &excludes)
	var out []ir.Glob
	if len(includes) == 0 && len(excludes) != 0 {
		// A list with no positive pattern matches every value: materialize the
		// implicit `**` include the IR requires (ir.md section 4).
		a.note(codeSynth, "glob", "include", filter.Source,
			"filter list contains no positive pattern; materialized an implicit `**` include")
		out = append(out, ir.Glob{Pattern: "**", Original: "**"})
	}
	out = append(out, includes...)
	out = append(out, excludes...)
	return out
}

// translateGlob maps Azure Pipelines wildcard semantics onto the IR glob
// dialect. Azure `*` crosses `/`, so every unescaped star run outside a
// character class becomes `**` (idempotent: `*`, `**` and `***` all
// normalize to `**`); `?`, character classes and `\` escapes are preserved.
// Inside an unescaped `[...]` class a `*` is a literal member, so it is copied
// verbatim: it neither widens nor marks the pattern changed. It reports whether
// the pattern changed.
func translateGlob(pattern string) (string, bool) {
	var b strings.Builder
	b.Grow(len(pattern))
	changed := false
	inClass := false
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch {
		case ch == '\\':
			b.WriteByte(ch)
			if i+1 < len(pattern) {
				i++
				b.WriteByte(pattern[i])
			}
		case ch == '[' && !inClass:
			inClass = true
			b.WriteByte(ch)
		case ch == ']' && inClass:
			inClass = false
			b.WriteByte(ch)
		case ch == '*' && !inClass:
			run := 1
			for i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				run++
			}
			b.WriteString("**")
			if run != 2 {
				changed = true
			}
		default:
			b.WriteByte(ch)
		}
	}
	return b.String(), changed
}

func (c *conv) variables(values *ast.Variables, scope ir.VariableScope, scopeID string) []ir.Variable {
	if values == nil {
		return nil
	}
	c.pushMacroScope(values)
	defer c.popMacroScope()
	var out []ir.Variable
	add := func(name, value string, source diag.Source, chain []diag.Source, readonly bool) {
		a := c.newAcc(source).in(chain)
		if readonly {
			a.keep("readonly", true)
			a.lose(codeExt, "variable", "readonly", source, "readonly variable intent has no IR field")
		}
		if c.sourceTextTainted(value) || literalSecret(name, value) {
			// IR rule 7: a secret-looking literal is never copied; the variable
			// becomes a SecretRef.
			a.lose(codeExt, "variable", "value", source, "secret-looking literal value was not copied into the IR; the referenced secret must be created manually")
			a.needsManual()
			out = append(out, ir.Variable{
				Metadata: a.metadata(), Name: name, Scope: scope, ScopeID: scopeID, Secret: true,
				SecretRef: &ir.SecretRef{Name: name, Scope: scope, ScopeID: scopeID},
			})
			return
		}
		if expr, ok := c.wholeMacro(a, "variable-expression", "value", source, value); ok {
			out = append(out, ir.Variable{Metadata: a.metadata(), Name: name, Scope: scope, ScopeID: scopeID, Value: &expr})
			return
		}
		if strings.Contains(value, "$(") || strings.HasPrefix(strings.TrimSpace(value), "$[") {
			a.lose(codeDeferred, "variable-expression", "value", source, "Azure variable expression is preserved for the later precedence pass")
		}
		literal := ir.StringLiteral(a.scrub("variable", "value", source, value))
		out = append(out, ir.Variable{Metadata: a.metadata(), Name: name, Scope: scope, ScopeID: scopeID, Value: &literal})
	}
	if values.Map != nil {
		for _, entry := range values.Map.Entries {
			if entry.Cond == nil && entry.Key != nil && entry.Value != nil {
				add(entry.Key.Value, entry.Value.Value, spanTo(entry.Key.Source, entry.Value.Source), entry.Key.Chain, false)
			}
		}
	}
	if values.List != nil {
		for _, item := range values.List.Items {
			switch value := item.Value.(type) {
			case *ast.VariableDef:
				if value.Name != nil && value.Value != nil {
					add(value.Name.Value, value.Value.Value, value.Source, value.Chain, boolValue(value.Readonly))
				}
			case *ast.VariableGroup:
				if value.Group != nil {
					expr := stringExpr(value.Group.Value)
					a := c.newAcc(value.Source).in(value.Chain)
					a.keep("groupReference", value.Group.Value)
					a.lose(codeExt, "variable-group", "reference", value.Source, "IR has no dedicated variable-group import node")
					c.unknown(a, "variable-group", value.Extra)
					out = append(out, ir.Variable{Metadata: a.metadata(), Name: value.Group.Value, Scope: ir.ScopeGroup, ScopeID: scopeID, Value: &expr})
				}
			}
		}
	}
	return out
}

func scalarOr(value *ast.Scalar, fallback string) string {
	if value != nil && value.Value != "" {
		return value.Value
	}
	return fallback
}

func boolValue(value *ast.Bool) bool { return value != nil && value.Known && value.Val }

func (c *conv) unknown(a *acc, construct string, extras []*ast.Extra) {
	// Key components are scrubbed before the "unknown." prefix is composed;
	// keys that collapse to the same text get collision-free suffixes.
	var valid []*ast.Extra
	var names []string
	for _, extra := range extras {
		if extra != nil && extra.Key != nil {
			valid = append(valid, extra)
			scrubbed := c.ts.apply(extra.Key.Value)
			if scrubbed != extra.Key.Value {
				a.lose(codeExt, "ir", "secretMaterial", extra.Key.Source, secretDetail)
				a.needsManual()
			}
			names = append(names, "unknown."+scrubbed)
		}
	}
	allocateUnique(names)
	for i, extra := range valid {
		retained, removed := anyValueKey(extra.Value, extra.Key.Value)
		a.keep(names[i], retained)
		a.lose(codeExt, construct, extra.Key.Value, extra.Key.Source, "unknown Azure Pipelines property preserved in provider extension")
		if removed {
			a.needsManual()
		}
	}
}

// anyValue converts a free-form subtree for a provider extension. Credential
// patterns and values under secret-looking keys never survive (IR rule 7).
func anyValue(value *ast.Any) any {
	retained, _ := anyValueKey(value, "")
	return retained
}

func anyValueKey(value *ast.Any, key string) (any, bool) {
	if value == nil {
		return nil, false
	}
	switch value.Kind {
	case ast.AnyScalar:
		if value.Scalar != nil {
			if literalSecret(key, value.Scalar.Value) {
				return redact.Replacement(), true
			}
			clean := safeText(value.Scalar.Value)
			return clean, clean != value.Scalar.Value
		}
	case ast.AnySeq:
		out := make([]any, 0, len(value.Items))
		removed := false
		for _, item := range value.Items {
			retained, changed := anyValueKey(item, key)
			out = append(out, retained)
			removed = removed || changed
		}
		return out, removed
	case ast.AnyMap:
		var out ir.OrderedMapBuilder[any]
		removed := false
		for _, entry := range value.Entries {
			if entry.Key != nil {
				retained, changed := anyValueKey(entry.Value, entry.Key.Value)
				out.Set(entry.Key.Value, retained)
				removed = removed || changed
			}
		}
		return out.Build(), removed
	}
	return nil, false
}
