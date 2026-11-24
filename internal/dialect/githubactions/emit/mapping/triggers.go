package mapping

import (
	"strings"

	goyaml "go.yaml.in/yaml/v3"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/githubactions/emit/model"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
)

// Diagnostic codes of the trigger concern (emit-gha.md sections 5.1-5.3).
const (
	codeManualConversion     = "TRL-CONV-0003"
	codeSyntheticDefault     = "TRL-CONV-0004"
	codeTriggerWithoutTarget = "TRL-CONV-0005"
	codeEmitterAdjustment    = "TRL-EMIT-0001"
	codePatternWidened       = "TRL-EMIT-0007"
)

// triggerMapper converts IR triggers to the workflow on mapping
// (emit-gha.md sections 5.2 and 5.3). It stays opt-in: callers inject it
// into Run; the default Emit dispatch registers no mapper yet.
type triggerMapper struct{}

func (triggerMapper) Concern() Concern { return Triggers }

func (triggerMapper) Map(pipeline *ir.Pipeline, claimed func(string) bool) Result {
	var result Result
	var entries []Entry
	for index := range pipeline.Triggers {
		trigger := &pipeline.Triggers[index]
		id := QualifyID("trigger", trigger.ID)
		if claimed(id) {
			continue
		}
		outcome, entry := triggerOutcome(pipeline, trigger, id)
		result.Outcomes = append(result.Outcomes, outcome)
		if entry != nil {
			entries = append(entries, *entry)
		}
	}
	if len(entries) > 0 {
		result.Fragment.Workflow = []WorkflowFragment{{On: entries}}
	}
	return result
}

// triggerOutcome maps one trigger node. push and pr become on entries; every
// other kind is dropped with its master-table disposition (5.1). When no
// push/pr event remains the emitter renders the workflow_dispatch fallback
// itself (5.2 rule 2), so the mapper contributes no entry for it.
func triggerOutcome(pipeline *ir.Pipeline, trigger *ir.Trigger, id string) (Outcome, *Entry) {
	switch trigger.Kind {
	case ir.TriggerPush, ir.TriggerPR:
		return emittedTrigger(pipeline, trigger, id)
	case ir.TriggerSchedule, ir.TriggerManual:
		return droppedTrigger(trigger, id, codeManualConversion, diag.FidelityNeedsManual), nil
	default:
		return droppedTrigger(trigger, id, codeTriggerWithoutTarget, diag.FidelityUnsupported), nil
	}
}

func emittedTrigger(pipeline *ir.Pipeline, trigger *ir.Trigger, id string) (Outcome, *Entry) {
	builder := eventBuilder{trigger: trigger, id: id}
	fidelity := trigger.Fidelity
	if !validFidelity(fidelity) {
		fidelity = diag.FidelityExact
	}
	// Binding contract (5.2 rule 1): a synthetic trigger carries the reader's
	// TRL-CONV-0004 without duplicating it; an external trigger materialized
	// in YAML records TRL-EMIT-0001.
	if trigger.Binding.Synthetic {
		if !synthesisRecorded(pipeline, trigger.Kind) {
			builder.finding(codeSyntheticDefault)
		}
	} else if !trigger.Binding.InYAML {
		builder.finding(codeEmitterAdjustment)
		fidelity = worstFidelity(fidelity, diag.FidelityApproximated)
	}
	var value *goyaml.Node
	key := "push"
	if trigger.Kind == ir.TriggerPR {
		key = "pull_request"
		value = builder.pullRequest()
	} else {
		value = builder.push()
	}
	if builder.lost {
		fidelity = worstFidelity(fidelity, diag.FidelityApproximated)
	}
	outcome := Outcome{
		Disposition: Disposition{ID: id, Kind: "emitted", Fidelity: fidelity},
		Diagnostics: builder.diagnostics,
	}
	return outcome, &Entry{Node: id, Key: key, Value: value}
}

func droppedTrigger(trigger *ir.Trigger, id, code string, fidelity diag.Fidelity) Outcome {
	builder := eventBuilder{trigger: trigger, id: id}
	builder.finding(code)
	return Outcome{
		Disposition: Disposition{ID: id, Kind: "dropped", Fidelity: fidelity},
		Diagnostics: builder.diagnostics,
	}
}

// synthesisRecorded reports whether the reader already raised TRL-CONV-0004
// for the synthesized default of this trigger kind, so the emitter never
// duplicates it (5.2 rule 1).
func synthesisRecorded(pipeline *ir.Pipeline, kind ir.TriggerKind) bool {
	for _, finding := range pipeline.Diagnostics {
		if finding.Code == codeSyntheticDefault &&
			finding.Params["construct"] == "trigger" &&
			finding.Params["property"] == string(kind) {
			return true
		}
	}
	return false
}

// eventBuilder accumulates the on entry and findings of one push/pr trigger.
type eventBuilder struct {
	trigger     *ir.Trigger
	id          string
	diagnostics []diag.Diagnostic
	// lost reports a widened or dropped pattern, or a dropped pr tag filter.
	lost bool
}

func (b *eventBuilder) finding(code string, opts ...diag.Option) {
	options := []diag.Option{diag.WithResource(diag.Resource{Kind: "trigger", Key: b.id})}
	if b.trigger.Origin != nil {
		options = append(options, diag.WithSource(b.trigger.Origin.Source))
	}
	options = append(options, opts...)
	b.diagnostics = append(b.diagnostics, diag.MustNew(code, options...))
}

// push builds the push event value. The branch guard (5.2 rules 3 and 6)
// always writes branches: an event without a branch filter would also fire
// for tag pushes on GitHub, which Azure does not.
func (b *eventBuilder) push() *goyaml.Node {
	branches, movedTags := splitRefPrefixes(b.trigger.Filters.Branches)
	tags := append(append([]ir.Glob(nil), b.trigger.Filters.Tags...), movedTags...)
	branchPatterns := b.patterns(branches, "branch")
	if len(branchPatterns) == 0 {
		branchPatterns = []string{"**"}
	}
	pairs := []model.Pair{{Key: "branches", Value: patternSeq(branchPatterns)}}
	if tagPatterns := b.patterns(tags, "tag"); len(tagPatterns) > 0 {
		pairs = append(pairs, model.Pair{Key: "tags", Value: patternSeq(tagPatterns)})
	}
	if pathPatterns := b.patterns(b.trigger.Filters.Paths, "path"); len(pathPatterns) > 0 {
		pairs = append(pairs, model.Pair{Key: "paths", Value: patternSeq(pathPatterns)})
	}
	return model.Map(pairs...)
}

// pullRequest builds the pull_request event value. Filters match the base
// branch; there is no tag filter and no branch guard (5.2 rule 7, EMITV-002).
func (b *eventBuilder) pullRequest() *goyaml.Node {
	branches, movedTags := splitRefPrefixes(b.trigger.Filters.Branches)
	if len(b.trigger.Filters.Tags) > 0 || len(movedTags) > 0 {
		b.lost = true
		b.finding(codeTriggerWithoutTarget)
	}
	var pairs []model.Pair
	if branchPatterns := b.patterns(branches, "branch"); len(branchPatterns) > 0 {
		pairs = append(pairs, model.Pair{Key: "branches", Value: patternSeq(branchPatterns)})
	}
	if pathPatterns := b.patterns(b.trigger.Filters.Paths, "path"); len(pathPatterns) > 0 {
		pairs = append(pairs, model.Pair{Key: "paths", Value: patternSeq(pathPatterns)})
	}
	if len(pairs) == 0 {
		return model.Null()
	}
	return model.Map(pairs...)
}

// patterns translates one filter kind and applies the sign handling of 5.2
// rule 4 in the shape of EMITV-006/008: a list with a positive pattern keeps
// the positive key and IR order; an exclude-only list means "everything
// except" (AZ-TRG), which GitHub expresses as a leading '**' followed by the
// negations under the positive key.
func (b *eventBuilder) patterns(globs []ir.Glob, kind string) []string {
	var patterns []string
	for _, glob := range globs {
		sign, body := splitSign(glob.Pattern)
		if body == "" {
			// An empty pattern is dropped without widening (5.3).
			continue
		}
		translated, expressible := translatePattern(body)
		if !expressible {
			b.lost = true
			action := "widened"
			if sign == "!" {
				action = "dropped"
			}
			b.finding(codePatternWidened, diag.WithParams(map[string]any{
				"pattern": glob.Pattern, "kind": kind, "action": action,
			}))
			if sign == "!" {
				continue
			}
		}
		patterns = append(patterns, sign+translated)
	}
	if len(patterns) == 0 {
		return nil
	}
	for _, pattern := range patterns {
		if !strings.HasPrefix(pattern, "!") {
			return patterns
		}
	}
	return append([]string{"**"}, patterns...)
}

// splitSign separates the leading negation marker from the pattern body.
func splitSign(pattern string) (sign, body string) {
	if strings.HasPrefix(pattern, "!") {
		return "!", pattern[1:]
	}
	return "", pattern
}

// splitRefPrefixes moves refs/tags/<p> branch entries to the tag list and
// strips refs/heads/<p>, keeping the sign (5.2 rule 5, VERIFY-02).
func splitRefPrefixes(globs []ir.Glob) (branches, tags []ir.Glob) {
	for _, glob := range globs {
		sign, body := splitSign(glob.Pattern)
		switch {
		case strings.HasPrefix(body, "refs/tags/"):
			tags = append(tags, ir.Glob{Pattern: sign + strings.TrimPrefix(body, "refs/tags/"), Original: glob.Original})
		case strings.HasPrefix(body, "refs/heads/"):
			branches = append(branches, ir.Glob{Pattern: sign + strings.TrimPrefix(body, "refs/heads/"), Original: glob.Original})
		default:
			branches = append(branches, glob)
		}
	}
	return branches, tags
}

func patternSeq(patterns []string) *goyaml.Node {
	items := make([]*goyaml.Node, len(patterns))
	for index, pattern := range patterns {
		items[index] = model.String(pattern)
	}
	return model.Seq(items...)
}

// translatePattern converts one sign-free IR glob to the GitHub filter
// dialect (emit-gha.md 5.3). expressible reports whether GitHub can express
// the pattern; when it cannot, the returned pattern replaces every
// inexpressible token with '*' so the trigger fires more often, never less.
func translatePattern(pattern string) (translated string, expressible bool) {
	runes := []rune(pattern)
	var out strings.Builder
	expressible = true
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; {
		case c == '\\' && i+1 < len(runes):
			out.WriteRune('\\')
			i++
			out.WriteRune(runes[i])
		case c == '?':
			out.WriteRune('*')
			expressible = false
		case c == '+':
			out.WriteString(`\+`)
		case c == '[':
			end := classEnd(runes, i)
			if end < 0 {
				out.WriteRune(c)
				continue
			}
			if classExpressible(runes[i+1 : end]) {
				out.WriteString(string(runes[i : end+1]))
			} else {
				out.WriteRune('*')
				expressible = false
			}
			i = end
		default:
			out.WriteRune(c)
		}
	}
	return out.String(), expressible
}

// classEnd returns the index of the ']' closing the class opened at start,
// or -1 when the class is unclosed. Escaped characters do not close it.
func classEnd(runes []rune, start int) int {
	for i := start + 1; i < len(runes); i++ {
		if runes[i] == '\\' {
			i++
			continue
		}
		if runes[i] == ']' {
			return i
		}
	}
	return -1
}

// classExpressible reports whether a class uses only the a-z, A-Z and 0-9
// ranges GitHub covers (5.3). Negated classes and any other member are not
// expressible.
func classExpressible(class []rune) bool {
	if len(class) == 0 || class[0] == '!' {
		return false
	}
	for _, c := range class {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// worstFidelity returns the less faithful of two grades.
func worstFidelity(left, right diag.Fidelity) diag.Fidelity {
	rank := func(fidelity diag.Fidelity) int {
		switch fidelity {
		case diag.FidelityApproximated:
			return 1
		case diag.FidelityNeedsManual:
			return 2
		case diag.FidelityUnsupported:
			return 3
		default:
			return 0
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}
