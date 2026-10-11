package vars

import (
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/ast"
)

const setVariablePrefix = "##vso[task.setvariable "

type mutation struct {
	candidate  Candidate
	output     bool
	outputName string
	stepName   string
	// bareName is the name as written in the logging command, without the
	// "<step>." output prefix; settableVariables is checked against it.
	bareName string
	// unprovable marks a command the script text contains but does not provably
	// emit (a comment, branch, loop, function, heredoc, assignment or any other
	// non-top-level occurrence). It is recorded as an Unknown mutation.
	unprovable bool
	// stepWarning marks the step-level warning for macro values that may inject
	// logging commands; it carries no variable.
	stepWarning bool
	// diagnostic requests TRL-VAR-0016 without discarding a conservative
	// SecretRef, as for secret-dependent values and duplicate properties.
	diagnostic bool
}

// mutations extracts the setvariable logging commands a step would emit, from
// the static text of its script. Values are bound against the step's own table
// (macros expand before the shell runs); values only a shell can produce are
// marked Unknown rather than guessed.
func mutations(step ast.Step, table *Table, stage, job, leg string, stepIndex int, agentOS string) []mutation {
	return mutationsWithExpander(step, newMacroExpander(table), stage, job, leg, stepIndex, agentOS)
}

func mutationsWithExpander(step ast.Step, expander *macroExpander, stage, job, leg string, stepIndex int, agentOS string) []mutation {
	return mutationsWithExpanderWork(step, expander, stage, job, leg, stepIndex, agentOS, nil)
}

func mutationsWithExpanderWork(step ast.Step, expander *macroExpander, stage, job, leg string, stepIndex int, agentOS string, work *evaluationWork) []mutation {
	props := stepProps(step)
	if props.Enabled != nil && props.Enabled.Known && !props.Enabled.Val {
		return nil
	}
	text := scriptText(step)
	if text == "" {
		return nil
	}
	stepName := scalar(props.Name)
	lower := asciiLower(text)
	shell := stepShell(step, agentOS)
	emitted, warn := provableEmissions(text, shell)
	// An unknown shell, a condition or an `enabled:` that is not a literal
	// boolean means the command may not run.
	if shell == shellUnknown || !stepRunsUnconditionally(props) || props.Enabled != nil && !props.Enabled.Known {
		emitted = nil
	}
	var result []mutation
	lineEnd := physicalLineEnd(text, 0, work)
	for offset := 0; ; {
		start := strings.Index(lower[offset:], setVariablePrefix)
		if start < 0 {
			work.scan(len(lower) - offset)
			break
		}
		work.scan(start + len(setVariablePrefix))
		start += offset
		end := strings.IndexByte(text[start:], ']')
		if end < 0 {
			work.scan(len(text) - start)
			break
		}
		work.scan(end + 1)
		end += start
		header := text[start+len(setVariablePrefix) : end]
		for lineEnd < end {
			lineEnd = physicalLineEnd(text, lineEnd+1, work)
		}
		offset = end + 1
		var quote byte
		if start > 0 && (text[start-1] == '\'' || text[start-1] == '"') {
			quote = text[start-1]
		}
		raw := strings.TrimRight(text[end+1:lineEnd], "\r")
		valueQuote := quote
		if shell == shellCmd && quote != '"' {
			valueQuote = 0
		}
		if quote != 0 && shell != shellCmd {
			if closing := strings.IndexByte(raw, quote); closing >= 0 {
				raw = raw[:closing]
				work.scan(closing + 1)
			} else {
				work.scan(len(raw))
			}
		}
		properties := parseProperties(header)
		name := property(properties, "variable")
		// A header value with a macro is not expanded here: treat the command as an
		// output and a secret rather than guess (it is Unknown, see the grammar).
		headerMacro := strings.Contains(header, "$(")
		if headerMacro && (name == "" || hasMacroRef(name)) {
			// The variable name is not statically known: nothing can be recorded.
			warn = true
			continue
		}
		if !validLoggedName(name) {
			continue
		}
		output := propertyAnyTrue(properties, "isoutput") || headerMacro && propertyHasMacro(properties, "isoutput")
		if output && stepName == "" {
			// An unnamed step has no addressable output key.
			continue
		}
		secret := propertyAnyTrue(properties, "issecret") || headerMacro && (propertyHasMacro(properties, "issecret") || propertyNameHasMacro(properties))
		// Task output variables are always read-only (secin#variables).
		readonly := propertyAnyTrue(properties, "isreadonly") || output
		candidateName := name
		if output {
			candidateName = stepName + "." + name
		}
		candidate := loggingCandidate(candidateName, job, stepName, stage, leg, stepIndex, readonly, step.Position())
		provable := emitted[start] && !headerMacro
		if secret {
			candidate.Secret = true
			candidate.SecretRef = &SecretRef{Provider: "logging-command", ID: candidate.SourceID + "/" + name}
		}
		switch {
		case !provable:
			candidate.Unknown = true
		case secret:
		default:
			bindLoggedValue(&candidate, raw, valueQuote, shell)
		}
		result = append(result, mutation{candidate: candidate, output: output, outputName: name, stepName: stepName, bareName: name, unprovable: !provable || candidate.Unknown, diagnostic: duplicateProperties(properties)})
	}
	// Macro values can inject logging commands whose names no analysis can
	// guess; this is a documented limit, reported once per step.
	if warn || macroWarning(text, expander) {
		result = append(result, mutation{stepWarning: true, candidate: Candidate{Origin: step.Position()}})
	}
	return result
}

func physicalLineEnd(text string, start int, work *evaluationWork) int {
	if start >= len(text) {
		return len(text)
	}
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		if work != nil {
			work.lineBytes += len(text) - start
		}
		return len(text)
	}
	if work != nil {
		work.lineBytes += end + 1
	}
	return start + end
}

func loggingCandidate(name, job, stepName, stage, leg string, stepIndex int, readonly bool, origin diag.Source) Candidate {
	return Candidate{Name: name, ComparisonKey: key(name), Source: SourceLogging, SourceID: job + "/" + stepName, DeclarationIndex: stepIndex, Phase: PhaseMacro, Scope: Scope{Level: "job", Stage: stage, Job: job, Step: stepIndex + 1, Leg: leg}, Readonly: readonly, Available: !ReservedName(name), Origin: origin}
}

// bindLoggedValue binds the written value text of a provably emitted command
// (which holds no macro). A value the shell would still compute is Unknown.
func bindLoggedValue(candidate *Candidate, raw string, quote byte, shell shellKind) {
	if dynamicShellValue(raw, quote, shell) {
		candidate.Unknown = true
		return
	}
	candidate.Value = unescapeAgent(raw)
}

// dynamicShellValue reports whether a shell would compute part of the value.
// Single quotes make every character literal; elsewhere expansion and command
// substitution characters mean the value is not statically known.
//
// On cmd only %NAME% and !NAME! are expansions; "$", backtick, backslash and
// the glob characters are literal there.
func dynamicShellValue(text string, quote byte, shell shellKind) bool {
	if shell == shellCmd {
		return windowsVariable(text) || bangVariable(text)
	}
	if quote == '\'' {
		return false
	}
	if strings.ContainsAny(text, "$`") {
		return true
	}
	return quote == 0 && strings.ContainsAny(text, ";&|<>*?\\~")
}

// windowsVariable matches cmd-style %NAME% references.
func windowsVariable(text string) bool { return delimitedName(text, '%') }

// bangVariable matches cmd delayed-expansion !NAME! references.
func bangVariable(text string) bool { return delimitedName(text, '!') }

func delimitedName(text string, delimiter byte) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != delimiter {
			continue
		}
		j := i + 1
		for j < len(text) && (text[j] == '_' || text[j] >= '0' && text[j] <= '9' || text[j] >= 'A' && text[j] <= 'Z' || text[j] >= 'a' && text[j] <= 'z') {
			j++
		}
		if j > i+1 && j < len(text) && text[j] == delimiter {
			return true
		}
	}
	return false
}

func validLoggedName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// scriptText returns the static script text of a step that can emit logging
// commands: script steps and the inline-script inputs of the common script
// tasks. File-based scripts are not visible to the evaluator.
func scriptText(step ast.Step) string {
	switch value := step.(type) {
	case *ast.ScriptStep:
		return scalar(value.Script)
	case *ast.TaskStep:
		return taskInlineScript(value)
	}
	return ""
}

// shellKind is the shell a script step runs in, as far as it decides how an
// echo argument starting with "#" and its quotes are treated.
type shellKind uint8

const (
	// shellBash is bash: "#" starts a comment and quotes are removed before the
	// text is printed.
	shellBash shellKind = iota
	// shellPwsh is PowerShell: the same comment and quote rules as bash.
	shellPwsh
	// shellCmd is cmd.exe: an unquoted "##vso[" is printed and quotes are printed too.
	shellCmd
	// shellUnknown is a step whose shell depends on an agent OS that is not known.
	shellUnknown
)

// stepShell picks the shell. `script:` and CmdLine@2 run cmd.exe on Windows and
// bash on Linux and macOS; every other script kind does not depend on the OS.
func stepShell(step ast.Step, agentOS string) shellKind {
	switch value := step.(type) {
	case *ast.ScriptStep:
		switch value.Kind {
		case ast.ScriptBash:
			return shellBash
		case ast.ScriptPwsh, ast.ScriptPowerShell:
			return shellPwsh
		}
		return osShell(agentOS)
	case *ast.TaskStep:
		name, _, _ := strings.Cut(scalar(value.Task), "@")
		switch fold(name) {
		case "CMDLINE":
			return osShell(agentOS)
		case "BASH":
			return shellBash
		case "POWERSHELL", "AZUREPOWERSHELL":
			return shellPwsh
		}
	}
	return shellUnknown
}

func osShell(agentOS string) shellKind {
	switch agentOS {
	case AgentOSWindows:
		return shellCmd
	case AgentOSLinux, AgentOSMacOS:
		return shellBash
	}
	return shellUnknown
}

// stepRunsUnconditionally reports a step whose condition is absent or one of
// the defaults that do not depend on any variable.
func stepRunsUnconditionally(props ast.StepProps) bool {
	if props.Condition == nil {
		return true
	}
	condition := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(props.Condition.Value), " ", ""))
	return condition == "" || condition == "succeeded()" || condition == "always()"
}

func taskInlineScript(task *ast.TaskStep) string {
	name, _, _ := strings.Cut(scalar(task.Task), "@")
	input := func(want string) string {
		if task.Inputs == nil {
			return ""
		}
		for _, entry := range task.Inputs.Entries {
			if entry.Cond == nil && sameName(scalar(entry.Key), want) {
				return scalar(entry.Value)
			}
		}
		return ""
	}
	switch fold(name) {
	case "CMDLINE":
		return input("script")
	case "POWERSHELL", "BASH":
		if strings.EqualFold(input("targetType"), "inline") {
			return input("script")
		}
	case "AZURECLI":
		if strings.EqualFold(input("scriptLocation"), "inlineScript") {
			return input("inlineScript")
		}
	case "AZUREPOWERSHELL":
		if strings.EqualFold(input("ScriptType"), "InlineScript") {
			return input("Inline")
		}
	}
	return ""
}

func parseProperties(text string) [][2]string {
	parts := strings.Split(text, ";")
	result := make([][2]string, 0, len(parts))
	for _, part := range parts {
		name, value, ok := strings.Cut(part, "=")
		if ok {
			result = append(result, [2]string{strings.TrimSpace(name), unescapeAgent(value)})
		}
	}
	return result
}

func property(properties [][2]string, name string) string {
	for _, p := range properties {
		if strings.EqualFold(p[0], name) {
			return p[1]
		}
	}
	return ""
}

func propertyAnyTrue(properties [][2]string, name string) bool {
	for _, p := range properties {
		if strings.EqualFold(p[0], name) && strings.EqualFold(p[1], "true") {
			return true
		}
	}
	return false
}

func propertyHasMacro(properties [][2]string, name string) bool {
	for _, p := range properties {
		if strings.EqualFold(p[0], name) && hasMacroRef(p[1]) {
			return true
		}
	}
	return false
}

// propertyNameHasMacro reports a macro in a property name: the property could
// be any of them, including issecret.
func propertyNameHasMacro(properties [][2]string) bool {
	for _, p := range properties {
		if hasMacroRef(p[0]) {
			return true
		}
	}
	return false
}

func duplicateProperties(properties [][2]string) bool {
	seen := map[string]bool{} //trasloco:unordered-ok membership only
	for _, p := range properties {
		name := fold(p[0])
		if seen[name] {
			return true
		}
		seen[name] = true
	}
	return false
}

// unescapeAgent decodes exactly the escapes the agent decodes: %3B, %0D, %0A
// and %5D, then %AZP25. Other percent sequences are data (CommandStringConvertor.Unescape
// in microsoft/azure-pipelines-agent).
func unescapeAgent(value string) string {
	for _, pair := range [][2]string{{"%3B", ";"}, {"%0D", "\r"}, {"%0A", "\n"}, {"%5D", "]"}, {"%AZP25", "%"}} {
		value = strings.ReplaceAll(value, pair[0], pair[1])
	}
	return value
}

func applyMutation(table *Table, event mutation, props ast.StepProps, findings *diag.Collector) bool {
	return applyMutationWork(table, event, props, findings, nil)
}

func applyMutationWork(table *Table, event mutation, props ast.StepProps, findings *diag.Collector, work *evaluationWork) bool {
	if !mutationAllowed(event.bareName, props.Target) {
		findings.Add(diag.MustNew("TRL-VAR-0012", diag.WithSource(event.candidate.Origin), diag.WithArgs(map[string]any{"name": displayName(event.bareName)})))
		return false
	}
	if current, ok := lookupWork(table, event.candidate.Name, PhaseMacro, work); ok && current.Readonly {
		findings.Add(diag.MustNew("TRL-VAR-0013", diag.WithSource(event.candidate.Origin), diag.WithArgs(map[string]any{"name": displayName(event.candidate.Name)})))
		return false
	}
	return ingestWork(table, event.candidate, findings, work, event.bareName)
}

// displayName keeps diagnostic arguments printable.
func displayName(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
}

func mutationAllowed(name string, target *ast.Target) bool {
	if target == nil || target.SettableVariables == nil {
		return true
	}
	settable := target.SettableVariables
	if settable.None {
		return false
	}
	if settable.List == nil {
		return true
	}
	for _, item := range settable.List.Items {
		if item.Value != nil && sameName(item.Value.Value, name) {
			return true
		}
	}
	return false
}

func stepProps(step ast.Step) ast.StepProps {
	switch value := step.(type) {
	case *ast.ScriptStep:
		return value.StepProps
	case *ast.TaskStep:
		return value.StepProps
	case *ast.CheckoutStep:
		return value.StepProps
	case *ast.DownloadStep:
		return value.StepProps
	case *ast.DownloadBuildStep:
		return value.StepProps
	case *ast.GetPackageStep:
		return value.StepProps
	case *ast.PublishStep:
		return value.StepProps
	case *ast.ReviewAppStep:
		return value.StepProps
	default:
		return ast.StepProps{}
	}
}
