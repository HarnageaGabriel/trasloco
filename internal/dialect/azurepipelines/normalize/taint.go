package normalize

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// taintSet replaces secret literals in free text. Long literals (and their
// lower-case, upper-case and JSON-escaped forms) go into one strings.Replacer
// built once, so a leaf costs one pass regardless of how many literals exist.
// Short and all-digit literals are matched on whole-leaf equality only.
type taintSet struct {
	replacer *strings.Replacer
	whole    map[string]bool
	// buildWork is the number of bytes put into the replacer.
	buildWork int
	// replacerScans and appliedBytes are deterministic test counters.
	replacerScans int
	appliedBytes  int
	// overflow conservatively redacts every eligible leaf when declarations
	// exceed the construction bound.
	overflow bool
}

// maxTaintSourceBytes bounds the total bytes of tracked source literals. The
// derived lower, upper and JSON variants have a constant expansion factor.
const maxTaintSourceBytes = 48 << 10

// newTaintSet builds the set from long literals and whole-leaf literals.
func newTaintSet(values, whole []string) *taintSet {
	set := &taintSet{}
	total := 0
	for _, group := range [][]string{values, whole} {
		for _, value := range group {
			total += len(value)
			if total > maxTaintSourceBytes {
				set.overflow = true
				return set
			}
		}
	}
	if len(whole) != 0 {
		set.whole = make(map[string]bool, 5*len(whole))
		addWhole := func(value string) {
			if value != "" {
				set.whole[value] = true
			}
		}
		for _, value := range whole {
			addWhole(value)
			addWhole(strings.ToLower(value))
			addWhole(strings.ToUpper(value))
			addWhole(jsonEscaped(value, true))
			addWhole(jsonEscaped(value, false))
		}
	}
	seen := make(map[string]bool, 4*len(values))
	var variants []string
	add := func(variant string) {
		if variant != "" && !seen[variant] {
			seen[variant] = true
			variants = append(variants, variant)
		}
	}
	for _, value := range values {
		add(value)
		add(strings.ToLower(value))
		add(strings.ToUpper(value))
		add(jsonEscaped(value, true))
		add(jsonEscaped(value, false))
	}
	if len(variants) == 0 {
		return set
	}
	// Longest first so a value containing another is replaced whole.
	slices.SortFunc(variants, func(x, y string) int {
		if len(x) != len(y) {
			return len(y) - len(x)
		}
		return strings.Compare(x, y)
	})
	pairs := make([]string, 0, 2*len(variants))
	placeholder := redact.Replacement()
	for _, variant := range variants {
		pairs = append(pairs, variant, placeholder)
		set.buildWork += len(variant)
	}
	set.replacer = strings.NewReplacer(pairs...)
	return set
}

// jsonEscaped is the JSON string encoding of value without the quotes.
func jsonEscaped(value string, escapeHTML bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if enc.Encode(value) != nil {
		return ""
	}
	encoded := strings.TrimSuffix(buf.String(), "\n")
	if len(encoded) < 2 {
		return ""
	}
	return encoded[1 : len(encoded)-1]
}

// apply replaces every tainted occurrence in text. Replaced text is never
// scanned again and an existing placeholder is never split.
func (s *taintSet) apply(text string) string {
	if s == nil || text == "" {
		return text
	}
	if s.overflow {
		return redact.Replacement()
	}
	if s.whole[text] {
		return redact.Replacement()
	}
	if s.replacer == nil {
		return text
	}
	s.replacerScans++
	s.appliedBytes += len(text)
	placeholder := redact.Replacement()
	if !strings.Contains(text, placeholder) {
		return s.replacer.Replace(text)
	}
	parts := strings.Split(text, placeholder)
	for i := range parts {
		parts[i] = s.replacer.Replace(parts[i])
	}
	return strings.Join(parts, placeholder)
}

type idRefKind uint8

const (
	stageIDRef idRefKind = iota
	jobIDRef
	stepIDRef
)

type idReference struct {
	target   *string
	original string
	kind     idRefKind
	job      string
}

type outputReference struct {
	target   *ir.OutputRef
	original ir.OutputRef
}

// idPlan retains declaration and reference spellings from before tainting so
// references can follow the exact declaration they named after collisions are
// de-duplicated.
type idPlan struct {
	stages  []string
	jobs    []string
	steps   [][]string
	refs    []idReference
	outputs []outputReference
}

var (
	outputRefType = reflect.TypeOf(ir.OutputRef{})
	variableType  = reflect.TypeOf(ir.Variable{})
	secretRefType = reflect.TypeOf(ir.SecretRef{})
	jobType       = reflect.TypeOf(ir.Job{})
	stageType     = reflect.TypeOf(ir.Stage{})
)

func captureIDPlan(p *ir.Pipeline) *idPlan {
	plan := &idPlan{stages: make([]string, len(p.Stages)), jobs: make([]string, len(p.Jobs)), steps: make([][]string, len(p.Jobs))}
	for i := range p.Stages {
		stage := &p.Stages[i]
		plan.stages[i] = stage.ID
		for j := range stage.DependsOn {
			plan.refs = append(plan.refs, idReference{target: &stage.DependsOn[j], original: stage.DependsOn[j], kind: stageIDRef})
		}
		for j := range stage.Jobs {
			plan.refs = append(plan.refs, idReference{target: &stage.Jobs[j], original: stage.Jobs[j], kind: jobIDRef})
		}
	}
	for i := range p.Jobs {
		job := &p.Jobs[i]
		plan.jobs[i] = job.ID
		plan.steps[i] = make([]string, len(job.Steps))
		for j := range job.Needs {
			plan.refs = append(plan.refs, idReference{target: &job.Needs[j], original: job.Needs[j], kind: jobIDRef})
		}
		for j := range job.Steps {
			plan.steps[i][j] = job.Steps[j].ID
		}
	}
	plan.collect(reflect.ValueOf(p).Elem(), "", "")
	return plan
}

func (p *idPlan) collect(v reflect.Value, ownerStage, ownerJob string) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Pointer {
		if !v.IsNil() {
			p.collect(v.Elem(), ownerStage, ownerJob)
		}
		return
	}
	if v.Kind() == reflect.Interface {
		if !v.IsNil() {
			p.collect(v.Elem(), ownerStage, ownerJob)
		}
		return
	}
	if v.Kind() == reflect.Struct {
		if v.Type() == outputRefType && v.CanAddr() {
			ref := v.Addr().Interface().(*ir.OutputRef)
			p.outputs = append(p.outputs, outputReference{target: ref, original: *ref})
			return
		}
		if v.Type() == stageType {
			ownerStage = v.FieldByName("ID").String()
		}
		if v.Type() == jobType {
			ownerJob = v.FieldByName("ID").String()
		}
		if v.Type() == variableType && v.CanAddr() {
			variable := v.Addr().Interface().(*ir.Variable)
			p.collectScopeID(&variable.ScopeID, variable.Scope, ownerStage, ownerJob)
		}
		if v.Type() == secretRefType && v.CanAddr() {
			secret := v.Addr().Interface().(*ir.SecretRef)
			p.collectScopeID(&secret.ScopeID, secret.Scope, ownerStage, ownerJob)
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				p.collect(v.Field(i), ownerStage, ownerJob)
			}
		}
		return
	}
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		for i := 0; i < v.Len(); i++ {
			p.collect(v.Index(i), ownerStage, ownerJob)
		}
	}
}

func (p *idPlan) collectScopeID(target *string, scope ir.VariableScope, ownerStage, ownerJob string) {
	if target == nil || *target == "" {
		return
	}
	ref := idReference{target: target, original: *target}
	switch scope {
	case ir.ScopeStage:
		ref.kind = stageIDRef
	case ir.ScopeJob:
		ref.kind = jobIDRef
	case ir.ScopeStep:
		ref.kind, ref.job = stepIDRef, ownerJob
	case ir.ScopeGroup:
		switch {
		case ownerJob != "":
			ref.kind = jobIDRef
		case ownerStage != "":
			ref.kind = stageIDRef
		default:
			return
		}
	default:
		return
	}
	p.refs = append(p.refs, ref)
}

// declarationMap maps each original declaration spelling to its final one. A
// spelling declared more than once is ambiguous: it is left out, so no
// reference is repointed to an arbitrary one of the declarations (fail closed).
func declarationMap(original, final []string) map[string]string {
	out := make(map[string]string, len(original))
	ambiguous := map[string]bool{}
	for i := range min(len(original), len(final)) {
		name := original[i]
		if ambiguous[name] {
			continue
		}
		if _, seen := out[name]; seen {
			delete(out, name)
			ambiguous[name] = true
			continue
		}
		out[name] = final[i]
	}
	return out
}

func (p *idPlan) rewrite(after *ir.Pipeline) {
	stageFinal := make([]string, len(after.Stages))
	for i := range after.Stages {
		stageFinal[i] = after.Stages[i].ID
	}
	jobFinal := make([]string, len(after.Jobs))
	stepMaps := make(map[string]map[string]string, len(after.Jobs))
	for i := range after.Jobs {
		jobFinal[i] = after.Jobs[i].ID
		final := make([]string, len(after.Jobs[i].Steps))
		for j := range after.Jobs[i].Steps {
			final[j] = after.Jobs[i].Steps[j].ID
		}
		if i < len(p.jobs) && i < len(p.steps) {
			stepMaps[p.jobs[i]] = declarationMap(p.steps[i], final)
		}
	}
	stageMap := declarationMap(p.stages, stageFinal)
	jobMap := declarationMap(p.jobs, jobFinal)
	for _, ref := range p.refs {
		var final string
		var ok bool
		switch ref.kind {
		case stageIDRef:
			final, ok = stageMap[ref.original]
		case jobIDRef:
			final, ok = jobMap[ref.original]
		case stepIDRef:
			final, ok = stepMaps[ref.job][ref.original]
		}
		if ok {
			*ref.target = final
		}
	}
	for _, item := range p.outputs {
		if final, ok := stageMap[item.original.Stage]; ok {
			item.target.Stage = final
		}
		if final, ok := jobMap[item.original.Job]; ok {
			item.target.Job = final
		}
		if final, ok := stepMaps[item.original.Job][item.original.Step]; ok {
			item.target.Step = final
		}
	}
}

// sanitize makes user-derived text safe for findings and losses: tainted
// literals and credential patterns are replaced.
func (c *conv) sanitize(text string) string {
	if text == "" || text == redact.Replacement() {
		return text
	}
	return safeText(c.ts.apply(text))
}

// allocateUnique makes a post-scrub namespace unique. All original final names
// are reserved before allocation, so a duplicate gets the first unused "-N".
func allocateUnique(names []string) bool {
	reserved := make(map[string]bool, len(names))
	for _, name := range names {
		reserved[name] = true
	}
	seen := make(map[string]bool, len(names))
	next := make(map[string]int, len(names))
	changed := false
	for i, name := range names {
		if seen[name] {
			n := max(next[name], 2)
			for ; ; n++ {
				candidate := name + "-" + strconv.Itoa(n)
				if !reserved[candidate] {
					names[i] = candidate
					reserved[candidate] = true
					next[name] = n + 1
					changed = true
					break
				}
			}
		}
		seen[names[i]] = true
	}
	return changed
}

func dedupeIDList(ids []string) {
	allocateUnique(ids)
}

// dedupeEntryKeys makes every ordered-map key unique after tainting, including
// partial replacements such as "prefix[REDACTED]". It reports a change.
func dedupeEntryKeys(v reflect.Value) bool {
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Struct ||
		!strings.HasPrefix(v.Type().Elem().Name(), "OrderedEntry") {
		return false
	}
	field, ok := v.Type().Elem().FieldByName("Key")
	if !ok || field.Type != reflect.TypeOf("") {
		return false
	}
	keys := make([]string, v.Len())
	for i := 0; i < v.Len(); i++ {
		keys[i] = v.Index(i).FieldByIndex(field.Index).String()
	}
	if !allocateUnique(keys) {
		return false
	}
	for i, key := range keys {
		if key != v.Index(i).FieldByIndex(field.Index).String() {
			v.Index(i).FieldByIndex(field.Index).SetString(key)
		}
	}
	return true
}

// dedupeStructIDs re-runs ID deduplication over a slice of structs with a
// string ID field after the taint pass collapsed distinct IDs.
func dedupeStructIDs(v reflect.Value) {
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Struct {
		return
	}
	field, ok := v.Type().Elem().FieldByName("ID")
	if !ok || field.Type != reflect.TypeOf("") {
		return
	}
	ids := make([]string, v.Len())
	for i := range ids {
		ids[i] = v.Index(i).FieldByIndex(field.Index).String()
	}
	before := slices.Clone(ids)
	dedupeIDList(ids)
	for i := range ids {
		if ids[i] != before[i] {
			v.Index(i).FieldByIndex(field.Index).SetString(ids[i])
		}
	}
}
