package normalize

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// loggingCommand matches https://learn.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands
// Matching is case-insensitive on purpose (VERIFY: agent behaviour): a secret
// hidden behind `##VSO[` must never slip through.
var loggingCommand = regexp.MustCompile(`(?i)##vso\[([^] \t\n]+)(?:[ \t]+([^]\n]*))?]`)

// syntheticMetadata marks a node the normalizer invented (ADR 0003: no origin).
func (c *conv) syntheticMetadata(reason string) ir.Metadata {
	a := c.newAcc(diag.Source{})
	a.keep("synthetic", reason)
	m := a.metadata()
	m.Origin = nil
	return m
}

// selfCheckoutRef is the Azure spelling of the triggering commit that a self
// checkout selects by default. ir.md section 6 keeps the original macro in
// extensions; its portable meaning is CheckoutStep.refSource: sourceVersion,
// and ref itself is ignored.
const selfCheckoutRef = "$(Build.SourceVersion)"

// selfCheckout is the portable form of `checkout: self`: the triggering
// commit, expressed with refSource instead of the Azure macro literal.
func selfCheckout() *ir.CheckoutStep {
	return &ir.CheckoutStep{Repository: "self", Ref: stringExpr(""), RefSource: ir.CheckoutRefSourceVersion}
}

func (c *conv) steps(list *ast.List[ast.Step], deployment bool, gate ir.Function) []ir.Step {
	var out []ir.Step
	hasCheckout := false
	if list != nil {
		for _, item := range list.Items {
			if _, ok := item.Value.(*ast.CheckoutStep); ok {
				hasCheckout = true
			}
		}
	}
	if !deployment && !hasCheckout {
		out = append(out, ir.Step{
			Metadata: c.syntheticMetadata("implicit checkout of the self repository"), ID: "implicit-checkout", Name: "Checkout self", Kind: ir.StepCheckout,
			If: ir.SuccessCondition(ir.PhaseRuntime), Checkout: selfCheckout(),
		})
	}
	if list == nil {
		return out
	}
	for _, item := range list.Items {
		if c.cancelled() {
			return out
		}
		if item.Value == nil {
			continue
		}
		if checkout, ok := item.Value.(*ast.CheckoutStep); ok && checkout.Checkout != nil && strings.EqualFold(checkout.Checkout.Value, "none") {
			continue
		}
		if download, ok := item.Value.(*ast.DownloadStep); ok && download.Download != nil && strings.EqualFold(download.Download.Value, "none") {
			continue
		}
		step := c.step(item.Value, len(out)+1, gate)
		if step.ID != "" {
			out = append(out, step)
		}
	}
	return out
}

// dedupeStepIDs makes step IDs unique within a job by suffixing later
// duplicates with the first free "-N".
func dedupeStepIDs(steps []ir.Step, counters ...*int) {
	bump := func() {
		if len(counters) > 0 && counters[0] != nil {
			*counters[0]++
		}
	}
	all := make(map[string]bool, len(steps))
	for i := range steps {
		bump()
		all[steps[i].ID] = true
	}
	seen := make(map[string]bool, len(steps))
	next := make(map[string]int, len(steps))
	for i := range steps {
		bump()
		id := steps[i].ID
		if seen[id] {
			n := next[id]
			if n < 2 {
				n = 2
			}
			for ; ; n++ {
				bump()
				candidate := id + "-" + strconv.Itoa(n)
				if !all[candidate] && !seen[candidate] {
					steps[i].ID = candidate
					all[candidate] = true
					next[id] = n + 1
					break
				}
			}
		}
		seen[steps[i].ID] = true
	}
}

// scalarBool reads a boolean-valued scalar. Non-literal text records a loss.
func (c *conv) scalarBool(a *acc, value *ast.Scalar, construct, property string) bool {
	if value == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value.Value)) {
	case "true":
		return true
	case "false":
		return false
	}
	a.keep(property, safeText(value.Value))
	a.lose(codeExt, construct, property, value.Source, "boolean option is not a literal and cannot be evaluated statically")
	return false
}

// scalarInt reads an integer-valued scalar. Non-literal text records a loss.
func (c *conv) scalarInt(a *acc, value *ast.Scalar, construct, property string) int {
	n, _ := c.scalarIntSet(a, value, construct, property)
	return n
}

// scalarIntSet is scalarInt plus whether the scalar was a statically known,
// non-negative literal. It backs CheckoutStep.fetchDepthSet: a redacted or
// non-literal value stays unspecified instead of claiming 0.
func (c *conv) scalarIntSet(a *acc, value *ast.Scalar, construct, property string) (int, bool) {
	if value == nil {
		return 0, false
	}
	n, secret, err := c.checkedAtoi(a, value.Value, value.Source, property, construct, property)
	if secret {
		return 0, false
	}
	if err != nil || n < 0 {
		a.keep(property, safeText(value.Value))
		a.lose(codeExt, construct, property, value.Source, "integer option is not a literal and cannot be evaluated statically")
		return 0, false
	}
	return n, true
}

// timeout maps an explicitly declared timeoutInMinutes. Job defaults are
// materialized after runner resolution by jobTimeout; steps have no default.
func (c *conv) timeout(a *acc, value *ast.Scalar, construct string) *ir.Duration {
	if value == nil {
		return nil
	}
	n, secret, err := c.checkedAtoi(a, value.Value, value.Source, "timeoutInMinutes", construct, "timeoutInMinutes")
	if secret {
		return nil
	}
	switch {
	case err != nil || n < 0:
		a.keep("timeoutInMinutes", safeText(value.Value))
		a.lose(codeExt, construct, "timeoutInMinutes", value.Source, "timeout is not a literal non-negative integer")
		return nil
	case n == 0:
		a.keep("timeoutInMinutes", 0)
		a.lose(codeExt, construct, "timeoutInMinutes", value.Source, "timeoutInMinutes 0 means the maximum allowed by the agent pool; no IR duration expresses that")
		return nil
	}
	return &ir.Duration{Duration: time.Duration(n) * time.Minute}
}

func (c *conv) step(source ast.Step, index int, gate ir.Function) ir.Step {
	a := c.newAcc(source.Position()).in(stepChain(source))
	props := stepProperties(source)
	id := "step-" + strconv.Itoa(index)
	name := id
	if props != nil {
		// Scrubbed before dedupeStepIDs composes "<id>-N" suffixes from it.
		id = scalarOr(props.Name, id)
		if props.Name != nil {
			id = c.part(a, "step", id, props.Name.Source)
		}
		name = scalarOr(props.DisplayName, id)
	}
	step := ir.Step{Metadata: a.metadata(), ID: id, Name: name, If: ir.SuccessCondition(ir.PhaseRuntime)}
	if props != nil {
		step.If = c.condition(props.Condition, a)
		disabled := false
		if props.Enabled != nil {
			if !props.Enabled.Known {
				c.flag(a, props.Enabled, "step", "enabled")
			} else if !props.Enabled.Val {
				disabled = true
				falseExpr := ir.Expr{Kind: ir.ExprLiteral, Literal: &ir.Literal{Type: ir.LiteralBool}}
				step.If = &ir.Cond{Phase: ir.PhaseRuntime, Expr: &falseExpr}
			}
		}
		step.If = c.gateCondition(step.If, hasCondition(props.Condition), disabled, gate, a, props.Condition)
		step.ContinueOnError = c.flag(a, props.ContinueOnError, "step", "continueOnError")
		step.Timeout = c.timeout(a, props.TimeoutInMinutes, "step")
		step.Retry = c.scalarInt(a, props.RetryCountOnTaskFailure, "step", "retryCountOnTaskFailure")
		step.Env = c.scalarMap(props.Env, a, "step-env")
		if props.Target != nil {
			target := ir.OrderedMap[any]{}
			if props.Target.Container != nil {
				target.Set("container", props.Target.Container.Value)
			}
			if props.Target.Commands != nil {
				target.Set("commands", props.Target.Commands.Value)
			}
			if sv := props.Target.SettableVariables; sv != nil {
				if sv.None {
					target.Set("settableVariables", "none")
				} else if sv.List != nil {
					var names []any
					for _, item := range sv.List.Items {
						if item.Value != nil {
							names = append(names, item.Value.Value)
						}
					}
					target.Set("settableVariables", names)
				}
			}
			a.keep("target", target)
			a.lose(codeExt, "step", "target", props.Target.Source, "step target restrictions have no IR field")
		}
	}
	switch value := source.(type) {
	case *ast.ScriptStep:
		step.Kind = ir.StepRun
		shell := mapShell(value.Kind)
		if value.Kind == ast.ScriptCmd {
			a.lose(codeExt, "script", "defaultShell", value.Source, "Azure script shell depends on agent OS; retained as the Azure default shell")
		}
		step.Run = &ir.RunStep{Script: scalarOr(value.Script, ""), Shell: ir.Shell(shell), WorkingDirectory: scalarOr(value.WorkingDirectory, "")}
		step.Run.Operations, step.Run.Script = c.protocolOperations(step.Run.Script, a, value.Source, shell)
		step.Run.Script = a.scrub("script", "script", value.Source, step.Run.Script)
		if value.ErrorActionPreference != nil || value.FailOnStderr != nil || value.IgnoreLASTEXITCODE != nil {
			opts := ir.OrderedMap[any]{}
			for _, pair := range []struct {
				key string
				v   *ast.Scalar
			}{{"errorActionPreference", value.ErrorActionPreference}, {"failOnStderr", value.FailOnStderr}, {"ignoreLASTEXITCODE", value.IgnoreLASTEXITCODE}} {
				if pair.v != nil {
					opts.Set(pair.key, safeText(pair.v.Value))
				}
			}
			a.keep("scriptOptions", opts)
			a.lose(codeExt, "script", "options", value.Source, "script host options have no portable run fields")
		}
		c.unknown(a, "script", value.Extra)
	case *ast.TaskStep:
		step.Kind = ir.StepAction
		refName, refVersion := value.Ref.Name, value.Ref.Version
		if refName == "" {
			refName = scalarOr(value.Task, "unknown")
		}
		if refVersion == "" {
			refVersion = "unknown"
			a.lose(codeExt, "task", "version", value.Source, "task reference has no version; IR records \"unknown\"")
		}
		action := &ir.ActionStep{Provider: Platform, Name: refName, Version: refVersion}
		if value.Inputs != nil {
			var inputs ir.OrderedMapBuilder[ir.Expr]
			for _, entry := range value.Inputs.Entries {
				if entry.Key != nil && entry.Value != nil {
					input := *entry.Value
					input.Value = taskInputText(a, input.Value, input.Source)
					inputs.Set(entry.Key.Value, c.keyedExpr(a, "task-input", entry.Key.Value, &input))
					c.work++
					deferred, work := deferredTextWork(entry.Value.Value)
					c.work += work
					if deferred {
						a.lose(codeDeferred, "task-input", entry.Key.Value, entry.Value.Source, "Azure variable expression is preserved for the later precedence pass")
					}
				}
			}
			if inputs.Len() > 0 {
				action.With = inputs.Build()
			}
		}
		step.Action = action
		c.unknown(a, "task", value.Extra)
	case *ast.CheckoutStep:
		step.Kind = ir.StepCheckout
		repo := scalarOr(value.Checkout, "self")
		checkout := selfCheckout()
		if repo != "self" {
			checkout.Repository = repo
			checkout.Ref, checkout.RefSource = stringExpr("HEAD"), ir.CheckoutRefLiteral
			c.work++
			resource := c.repoByAlias[repo]
			found := resource != nil && resource.Ref != nil
			if found {
				checkout.Ref = stringExpr(resource.Ref.Value)
			}
			if !found {
				a.lose(codeExt, "checkout", "ref", value.Source, "repository resource has no ref; Azure checks out its default branch, recorded as HEAD")
			}
		}
		if repo == "self" {
			a.keep("ref", selfCheckoutRef)
		}
		checkout.Path = scalarOr(value.Path, "")
		checkout.FetchDepth, checkout.FetchDepthSet = c.scalarIntSet(a, value.FetchDepth, "checkout", "fetchDepth")
		checkout.LFS = c.scalarBool(a, value.Lfs, "checkout", "lfs")
		checkout.PersistCredentials = c.scalarBool(a, value.PersistCredentials, "checkout", "persistCredentials")
		step.Checkout = checkout
		if value.Submodules != nil {
			if strings.EqualFold(strings.TrimSpace(value.Submodules.Value), "recursive") {
				step.Checkout.Submodules = true
				a.keep("submodules", "recursive")
				a.lose(codeExt, "checkout", "submodules", value.Submodules.Source, "recursive submodules cannot be distinguished from top-level submodules in IR")
			} else {
				step.Checkout.Submodules = c.scalarBool(a, value.Submodules, "checkout", "submodules")
			}
		}
		if value.Clean != nil || value.FetchFilter != nil || value.FetchTags != nil || value.SparseCheckoutDirectories != nil || value.SparseCheckoutPatterns != nil || value.WorkspaceRepo != nil {
			opts := ir.OrderedMap[any]{}
			if value.Clean != nil {
				opts.Set("clean", value.Clean.Value)
			}
			for _, pair := range []struct {
				key string
				v   *ast.Scalar
			}{{"fetchFilter", value.FetchFilter}, {"fetchTags", value.FetchTags}, {"sparseCheckoutDirectories", value.SparseCheckoutDirectories}, {"sparseCheckoutPatterns", value.SparseCheckoutPatterns}} {
				if pair.v != nil {
					opts.Set(pair.key, safeText(pair.v.Value))
				}
			}
			if value.WorkspaceRepo != nil {
				opts.Set("workspaceRepo", value.WorkspaceRepo.Value)
			}
			a.keep("checkoutOptions", opts)
			a.lose(codeExt, "checkout", "options", value.Source, "checkout clean, filters, tags, sparse mode, or workspaceRepo are not represented in IR")
		}
		c.unknown(a, "checkout", value.Extra)
	case *ast.DownloadStep:
		step.Kind = ir.StepArtifactRestore
		step.ArtifactRestore = &ir.ArtifactRestoreStep{Name: scalarOr(value.Artifact, ""), Path: stringExpr("$(Pipeline.Workspace)")}
		if value.Download != nil && !strings.EqualFold(value.Download.Value, "current") {
			a.keep("downloadSource", value.Download.Value)
			a.lose(codeExt, "download", "source", value.Download.Source, "download source pipeline resource alias has no IR field")
		}
		if value.Artifact == nil {
			a.lose(codeExt, "download", "artifact", value.Source, "download without artifact fetches all artifacts; IR records an empty artifact name")
		}
		if value.Patterns != nil {
			a.keep("patterns", value.Patterns.Value)
			a.lose(codeExt, "download", "patterns", value.Patterns.Source, "artifact include patterns have no IR field")
		}
		c.unknown(a, "download", value.Extra)
	case *ast.DownloadBuildStep:
		step.Kind = ir.StepArtifactRestore
		step.ArtifactRestore = &ir.ArtifactRestoreStep{Name: scalarOr(value.Artifact, ""), Path: stringExpr(scalarOr(value.Path, "$(Pipeline.Workspace)"))}
		if value.DownloadBuild != nil {
			a.keep("downloadSource", value.DownloadBuild.Value)
			a.lose(codeExt, "downloadBuild", "source", value.DownloadBuild.Source, "build resource alias has no IR field")
		}
		if value.Artifact == nil {
			a.lose(codeExt, "downloadBuild", "artifact", value.Source, "download without artifact fetches all artifacts; IR records an empty artifact name")
		}
		if value.Patterns != nil {
			a.keep("patterns", value.Patterns.Value)
			a.lose(codeExt, "downloadBuild", "patterns", value.Patterns.Source, "artifact include patterns have no IR field")
		}
		c.unknown(a, "downloadBuild", value.Extra)
	case *ast.GetPackageStep:
		step.Kind = ir.StepRaw
		payload := a.scrubRetained("getPackage", value.Source, orderedScalars("package", value.GetPackage, "path", value.Path))
		step.Raw = &ir.Raw{Provider: Platform, Kind: "getPackage", Format: "json", JSON: rawJSON(payload), Origin: c.origin(value.Source, value.Chain)}
		a.lose(codeExt, "getPackage", "step", value.Source, "package acquisition has no portable IR step")
		c.unknown(a, "getPackage", value.Extra)
	case *ast.PublishStep:
		step.Kind = ir.StepArtifactPublish
		publish := value.Publish
		if publish == nil {
			publish = value.Upload
		}
		step.ArtifactPublish = &ir.ArtifactPublishStep{Name: scalarOr(value.Artifact, "artifact"), Path: stringExpr(scalarOr(publish, ""))}
		c.unknown(a, "publish", value.Extra)
	case *ast.ReviewAppStep:
		step.Kind = ir.StepRaw
		payload := a.scrubRetained("reviewApp", value.Source, orderedScalars("route", value.ReviewApp))
		step.Raw = &ir.Raw{Provider: Platform, Kind: "reviewApp", Format: "json", JSON: rawJSON(payload), Origin: c.origin(value.Source, value.Chain)}
		a.lose(codeExt, "reviewApp", "step", value.Source, "review app routing has no portable IR step")
		c.unknown(a, "reviewApp", value.Extra)
	default:
		return ir.Step{}
	}
	step.Metadata = a.metadata()
	return step
}

func stepProperties(step ast.Step) *ast.StepProps {
	switch value := step.(type) {
	case *ast.ScriptStep:
		return &value.StepProps
	case *ast.TaskStep:
		return &value.StepProps
	case *ast.CheckoutStep:
		return &value.StepProps
	case *ast.DownloadStep:
		return &value.StepProps
	case *ast.DownloadBuildStep:
		return &value.StepProps
	case *ast.GetPackageStep:
		return &value.StepProps
	case *ast.PublishStep:
		return &value.StepProps
	case *ast.ReviewAppStep:
		return &value.StepProps
	default:
		return nil
	}
}

// keyedExpr converts a literal under a key: secret-looking literals become a
// secret reference (IR rule 7) and credential patterns are scrubbed.
func (c *conv) keyedExpr(a *acc, construct, key string, value *ast.Scalar) ir.Expr {
	if construct == "task-input" && nonSecretTaskInputs[key] {
		c.work += len(value.Value)
		return ir.StringLiteral(a.scrub(construct, key, value.Source, value.Value))
	}
	secret, work := literalSecretWork(key, value.Value)
	c.work += work
	if secret {
		a.lose(codeExt, construct, key, value.Source, "secret-looking literal value was not copied into the IR; the referenced secret must be created manually")
		a.needsManual()
		return secretRefExpr(key)
	}
	return ir.StringLiteral(a.scrub(construct, key, value.Source, value.Value))
}

func (c *conv) scalarMap(value *ast.Map[*ast.Scalar], a *acc, construct string) ir.OrderedMap[ir.Expr] {
	var out ir.OrderedMapBuilder[ir.Expr]
	if value == nil {
		return out.Build()
	}
	for _, entry := range value.Entries {
		if entry.Key != nil && entry.Value != nil {
			out.Set(entry.Key.Value, c.keyedExpr(a, construct, entry.Key.Value, entry.Value))
			c.work++
			deferred, work := deferredTextWork(entry.Value.Value)
			c.work += work
			if deferred {
				a.lose(codeDeferred, construct, entry.Key.Value, entry.Value.Source, "Azure variable expression is preserved for the later precedence pass")
			}
		}
	}
	return out.Build()
}

func deferredText(value string) bool {
	deferred, _ := deferredTextWork(value)
	return deferred
}

func deferredTextWork(value string) (bool, int) {
	trimmed := strings.TrimSpace(value)
	return strings.Contains(value, "$(") || strings.HasPrefix(trimmed, "${{") || strings.HasPrefix(trimmed, "$["), 2 * len(value)
}

func mapShell(kind ast.ScriptKind) string {
	switch kind {
	case ast.ScriptBash:
		return "bash"
	case ast.ScriptPwsh:
		return "pwsh"
	case ast.ScriptPowerShell:
		return "powershell"
	default:
		return "azure-default"
	}
}

// commentLine reports whether a script line is a comment in bash, PowerShell or cmd.
func commentLine(trimmed string) bool {
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "::") || lower == "rem" || strings.HasPrefix(lower, "rem ")
}

const backslash = byte(92)

// quoteScanner tracks the enclosing quote character (0 when none) and whether
// the scan position sits behind an unquoted `#` comment marker. A backslash
// escapes the next character inside double quotes. It is carried forward
// between logging commands: the state at each command is reached by scanning only the bytes
// since the previous one, so a line is scanned once.
type quoteScanner struct {
	pos       int
	quote     byte
	commented bool
}

// advance scans line[s.pos:to] and returns the number of bytes examined.
func (s *quoteScanner) advance(line string, to int) int {
	start := s.pos
	for ; s.pos < to && !s.commented; s.pos++ {
		i, ch := s.pos, line[s.pos]
		switch {
		case s.quote == '"' && ch == backslash:
			s.pos++
		case s.quote != 0:
			if ch == s.quote {
				s.quote = 0
			}
		case ch == '"' || ch == '\'':
			s.quote = ch
		case ch == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			s.quote, s.commented = 0, true
		}
	}
	if s.pos < start {
		return 0
	}
	return s.pos - start
}

// unescapedIndex finds the first quote not preceded by a backslash.
func unescapedIndex(text string, quote byte) int {
	for i := 0; i < len(text); i++ {
		if text[i] == backslash && quote == '"' {
			i++
			continue
		}
		if text[i] == quote {
			return i
		}
	}
	return -1
}

// loggingUnescape decodes the logging-command escapes
// (https://learn.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands).
var loggingUnescape = strings.NewReplacer("%0D", "\r", "%0d", "\r", "%0A", "\n", "%0a", "\n",
	"%3B", ";", "%3b", ";", "%5D", "]", "%5d", "]", "%AZP25", "%")

var shellReference = regexp.MustCompile(`^(\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Za-z_][A-Za-z0-9_]*|%[A-Za-z_][A-Za-z0-9_]*%|\$env:[A-Za-z_][A-Za-z0-9_]*)$`)

// valueReference reports a payload that is provably only a single reference:
// one $(macro), $VAR, ${VAR}, $env:VAR or %VAR%. Runtime expressions are not
// accepted: `$[a] literal [b]` would otherwise pass.
func valueReference(text string) bool {
	trimmed := strings.TrimSpace(text)
	return macroOnly.MatchString(trimmed) || shellReference.MatchString(trimmed)
}

type loggingSpan struct {
	command, props string
	// text is the best-effort command payload.
	text string
}

// unquotedPayloadEnd returns the first shell control operator outside quotes
// (best effort, used only to model non-secret operations).
func unquotedPayloadEnd(text string) int {
	var quote byte
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case quote == '"' && ch == backslash:
			i++
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == ';' || ch == '|' || ch == '&':
			return i
		}
	}
	return len(text)
}

// loggingSpans finds every non-commented `##vso[...]` command in a line
// (best effort; secret handling never relies on it).
// The quote state is carried forward between commands (one pass over the
// line); work counts every byte examined and is bounded by the caller.
func loggingSpansWork(line string) ([]loggingSpan, int) {
	all := loggingCommand.FindAllStringSubmatchIndex(line, -1)
	var out []loggingSpan
	work := len(line)
	var scan quoteScanner
	for i, loc := range all {
		work += scan.advance(line, loc[0]) + 1
		quote, commented := scan.quote, scan.commented
		if commented {
			break
		}
		start, end := loc[1], len(line)
		if i+1 < len(all) {
			end = all[i+1][0]
		}
		work += end - start // payload scan below
		if quote != 0 {
			if cut := unescapedIndex(line[start:end], quote); cut >= 0 {
				end = start + cut
			}
		} else {
			end = start + unquotedPayloadEnd(line[start:end])
		}
		span := loggingSpan{command: strings.ToLower(line[loc[2]:loc[3]]), text: strings.TrimSpace(line[start:end])}
		if loc[4] >= 0 {
			span.props = line[loc[4]:loc[5]]
		}
		out = append(out, span)
	}
	return out, work
}

// secretLoggingLiteral reports whether any logging command anywhere in the
// text, including comment-looking lines and heredocs, sets a secret whose
// payload is not provably only a reference. Matching is deliberately
// conservative: the payload is the rest of the physical line up to the next
// command, and nothing is parsed as shell.
func secretLoggingLiteral(text string) bool { return secretLoggingLiteralFor(text, "") }

// secretLoggingLiteralFor is secretLoggingLiteral for a script run by the given
// resolved shell. A shell joins a line continuation before the agent sees the
// output, so the command name or payload may be split across physical lines:
// bash joins a backslash, cmd a caret, PowerShell a backtick. Only the
// continuation of the resolved shell is joined; when the shell is unknown
// (empty, azure-default, task inputs, non-script leaves) all three are joined,
// a documented conservative false positive.
func secretLoggingLiteralFor(text, shell string) bool {
	if !strings.Contains(text, "#") {
		return false
	}
	if loggingLiteral(text) {
		return true
	}
	joined := continuationFor(shell).ReplaceAllString(text, "")
	return joined != text && loggingLiteral(joined)
}

var (
	lineContinuation = regexp.MustCompile("(?:\\\\|\\^|`)\r?\n")
	bashContinuation = regexp.MustCompile("\\\\\r?\n")
	cmdContinuation  = regexp.MustCompile("\\^\r?\n")
	pwshContinuation = regexp.MustCompile("`\r?\n")
)

func continuationFor(shell string) *regexp.Regexp {
	switch shell {
	case "bash", "sh":
		return bashContinuation
	case "cmd":
		return cmdContinuation
	case "pwsh", "powershell":
		return pwshContinuation
	}
	return lineContinuation
}

func loggingLiteral(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		all := loggingCommand.FindAllStringSubmatchIndex(line, -1)
		for i, loc := range all {
			end := len(line)
			if i+1 < len(all) {
				end = all[i+1][0]
			}
			payload := strings.TrimSpace(line[loc[1]:end])
			payload = strings.TrimSpace(strings.TrimRight(payload, `"'`))
			payload = loggingUnescape.Replace(payload)
			if payload == "" || valueReference(payload) {
				continue
			}
			props := ""
			if loc[4] >= 0 {
				props = line[loc[4]:loc[5]]
			}
			switch strings.ToLower(line[loc[2]:loc[3]]) {
			case "task.setsecret":
				return true
			case "task.setvariable":
				properties := commandProperties(props)
				if strings.EqualFold(properties["issecret"], "true") || literalSecret(properties["variable"], payload) {
					return true
				}
			}
		}
	}
	return false
}

const scriptSecretDetail = "secret literal in script: supply it from a secret store and restore the script manually"

// guardScript replaces the whole text by the redaction placeholder when it
// carries a secret logging-command literal, recording a needs-manual loss.
func guardScript(a *acc, text string, source diag.Source, shell string) (string, bool) {
	if !secretLoggingLiteralFor(text, shell) {
		return text, false
	}
	a.lose(codeExt, "logging-command", "secretLiteral", source, scriptSecretDetail)
	a.needsManual()
	return redact.Replacement(), true
}

// taskInputText applies guardScript to a task input value.
func taskInputText(a *acc, text string, source diag.Source) string {
	out, _ := guardScript(a, text, source, "")
	return out
}

// protocolOperations statically recognizes `##vso[...]` logging commands
// (https://learn.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands).
// Secret values are never copied (IR rule 7): when a command sets a secret
// literal (see secretLoggingLiteral) the whole script is replaced by the
// redaction placeholder with a needs-manual loss and the operation carries a
// SecretRef. Every unmodelled command is recorded as a loss.
func (c *conv) protocolOperations(script string, a *acc, source diag.Source, shell string) ([]ir.ProtocolOperation, string) {
	var out []ir.ProtocolOperation
	work := len(script)
	defer func() { c.work += work }()
	for _, raw := range strings.Split(script, "\n") {
		line := strings.TrimRight(raw, "\r")
		if commentLine(strings.TrimSpace(line)) || !strings.Contains(strings.ToLower(line), "##vso[") {
			continue
		}
		spans, lineWork := loggingSpansWork(line)
		work += lineWork
		for _, span := range spans {
			properties := commandProperties(span.props)
			text := loggingUnescape.Replace(span.text)
			switch span.command {
			case "task.setvariable":
				name := properties["variable"]
				secret := strings.EqualFold(properties["issecret"], "true") || literalSecret(name, text)
				if name == "" {
					a.lose(codeExt, "logging-command", "task.setvariable", source, "setvariable without a variable name was not modelled")
					break
				}
				kind := ir.StepSetPipelineVar
				if strings.EqualFold(properties["isoutput"], "true") {
					kind = ir.StepSetOutput
				}
				if strings.EqualFold(properties["isreadonly"], "true") {
					a.lose(codeExt, "logging-command", "isReadOnly", source, "read-only variable intent has no IR field")
				}
				var value ir.Expr
				switch {
				case secret && !valueReference(text) && text != "":
					value = secretRefExpr(name)
				case secret:
					value = stringExpr(text)
				default:
					value = ir.StringLiteral(a.scrub("logging-command", span.command, source, text))
				}
				out = append(out, ir.ProtocolOperation{Kind: kind, Name: name, Value: &value, Secret: secret})
			case "task.prependpath":
				value := ir.StringLiteral(a.scrub("logging-command", span.command, source, text))
				out = append(out, ir.ProtocolOperation{Kind: ir.StepAddPath, Value: &value})
			case "task.setsecret":
				var value ir.Expr
				if valueReference(text) {
					value = stringExpr(text)
				} else {
					value = secretRefExpr("task.setsecret")
				}
				out = append(out, ir.ProtocolOperation{Kind: ir.StepMask, Value: &value, Secret: true})
			case "task.logissue":
				level := strings.ToLower(properties["type"])
				if level != "error" {
					level = "warning"
				}
				message := ir.StringLiteral(a.scrub("logging-command", span.command, source, text))
				op := ir.ProtocolOperation{Kind: ir.StepAnnotate, Level: level, Message: &message}
				if file := loggingUnescape.Replace(properties["sourcepath"]); file != "" {
					ln, _, _ := c.checkedAtoi(a, properties["linenumber"], source, "linenumber", "logging-command", "linenumber")
					col, _, _ := c.checkedAtoi(a, properties["columnnumber"], source, "columnnumber", "logging-command", "columnnumber")
					op.Location = &ir.Location{File: file, Line: ln, Col: col}
				}
				out = append(out, op)
			default:
				a.lose(codeExt, "logging-command", span.command, source, "logging command has no IR protocol operation and was not modelled")
			}
		}
	}
	script, _ = guardScript(a, script, source, shell)
	return out, script
}

func commandProperties(text string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(text, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			out[strings.ToLower(strings.TrimSpace(key))] = loggingUnescape.Replace(strings.TrimSpace(value))
		}
	}
	return out
}
