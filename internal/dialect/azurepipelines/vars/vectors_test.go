package vars

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/parse"
)

// vectorCase is one row of ado-variable-vectors.md transcribed into a typed
// fixture plus checks. Expected is the Markdown cell copied verbatim; the
// checks are cross-checked against it so an expectation cannot be bent to fit
// the implementation without the table test noticing.
type vectorCase struct {
	ID            string         `json:"id"`
	Phase         string         `json:"phase"`
	Input         string         `json:"input"`
	Consumer      string         `json:"consumer"`
	Expected      string         `json:"expected"`
	Confidence    string         `json:"confidence"`
	Source        string         `json:"source"`
	Fixture       *vectorFixture `json:"fixture"`
	Checks        []vectorCheck  `json:"checks"`
	NotApplicable string         `json:"notApplicable"`
	Disagreement  string         `json:"disagreement"`
	// DisagreementCode is the diagnostic the evaluator rejects the fixture with;
	// the disagreement only holds while exactly that rejection is observed.
	DisagreementCode string `json:"disagreementCode"`
	// DisagreementObserved is the exact observed outcome of an accepted pipeline.
	DisagreementObserved string `json:"disagreementObserved"`
	Reason               string `json:"reason"`
}

type vectorFixture struct {
	YAML     string          `json:"yaml"`
	YAMLText bool            `json:"yamlText"`
	Ctx      json.RawMessage `json:"ctx"`
}

type vectorScope struct {
	Level string `json:"level"`
	Stage string `json:"stage"`
	Job   string `json:"job"`
	Step  int    `json:"step"`
}

func (s vectorScope) scope() Scope {
	return Scope{Level: s.Level, Stage: s.Stage, Job: s.Job, Step: s.Step}
}

type vectorCheck struct {
	Kind    string      `json:"kind"`
	Scope   vectorScope `json:"scope"`
	Text    string      `json:"text"`
	Name    string      `json:"name"`
	Phase   string      `json:"phase"`
	Want    string      `json:"want"`
	Code    string      `json:"code"`
	Finding string      `json:"finding"`
	Null    bool        `json:"null"`
	Fails   bool        `json:"fails"`
	Prose   bool        `json:"prose"`
}

type vectorVariable struct {
	Value    string `json:"value"`
	Secret   bool   `json:"secret"`
	Settable bool   `json:"settable"`
}

type vectorSettings struct {
	Limit bool `json:"limitQueueTimeVariables"`
}

// vectorRun is a fixture after template preparation, parsing and evaluation.
type vectorRun struct {
	graph    *Graph
	findings []diag.Diagnostic
	err      error
}

func TestVARVDocs(t *testing.T)     { runVectorClass(t, "docs") }
func TestVARVInferred(t *testing.T) { runVectorClass(t, "inferred") }

// TestVARVUncertain records outcomes for later live conformance; it never fails
// on an outcome, only on a harness or table defect.
func TestVARVUncertain(t *testing.T) { runVectorClass(t, "uncertain") }

func runVectorClass(t *testing.T, class string) {
	t.Helper()
	tally := map[string]int{}
	total := 0
	for _, tc := range loadVectorCases(t) {
		if tc.Confidence != class {
			continue
		}
		total++
		t.Run(tc.ID, func(t *testing.T) {
			tally[runVector(t, tc)]++
		})
	}
	if total == 0 {
		t.Fatalf("no %s vectors", class)
	}
	t.Logf("class=%s total=%d passed=%d disagreement=%d not-applicable=%d logged=%d", class, total, tally["passed"], tally["disagreement"], tally["not-applicable"], tally["logged"])
}

// runVector returns the outcome class: passed, disagreement, not-applicable or logged.
func runVector(t *testing.T, tc vectorCase) string {
	t.Helper()
	if tc.NotApplicable != "" {
		t.Logf("not applicable: %s", tc.NotApplicable)
		return "not-applicable"
	}
	if len(tc.Checks) == 0 || tc.Fixture == nil {
		t.Fatalf("%s has neither checks nor a not-applicable reason", tc.ID)
	}
	run, observed, failures := vectorOutcome(t, tc)
	switch {
	case tc.Confidence == "uncertain":
		t.Logf("expected=%s; observed=%s; mismatches=%v", tc.Expected, strings.Join(observed, "; "), failures)
		return "logged"
	case tc.Disagreement != "":
		if len(failures) == 0 {
			t.Fatalf("stale disagreement note, the implementation now agrees: %s", tc.Disagreement)
		}
		// The disagreement is pinned to its actual observed outcome, so an
		// unrelated failure cannot keep it alive.
		// A rejection is pinned by its code; an accepted pipeline is pinned by the
		// diagnostic it carries and by the exact observed outcome.
		var rejection *diag.Error
		switch {
		case tc.DisagreementCode == "" || tc.Reason == "":
			t.Fatalf("%s: a disagreement needs a code and a reason", tc.ID)
		case run.graph == nil:
			if !errors.As(run.err, &rejection) || rejection.Finding.Code != tc.DisagreementCode {
				t.Fatalf("disagreement is pinned to rejection with %q; observed graph=%t err=%v findings=%v", tc.DisagreementCode, run.graph != nil, run.err, codes(run.findings))
			}
		default:
			if !hasCode(run.findings, tc.DisagreementCode) || strings.Join(observed, "; ") != tc.DisagreementObserved {
				t.Fatalf("disagreement is pinned to %q and outcome %q; observed %q, findings=%v", tc.DisagreementCode, tc.DisagreementObserved, strings.Join(observed, "; "), codes(run.findings))
			}
		}
		t.Logf("documented disagreement: %s; observed %v", tc.Disagreement, failures)
		return "disagreement"
	case len(failures) > 0:
		t.Fatalf("expected %s: %s", tc.Expected, strings.Join(failures, "; "))
	}
	return "passed"
}

// vectorOutcome runs every check of a vector and returns the printable outcomes
// and the mismatches, without failing the test on a mismatch.
func vectorOutcome(t *testing.T, tc vectorCase) (run *vectorRun, observed, failures []string) {
	t.Helper()
	run = executeVector(t, tc)
	for _, check := range tc.Checks {
		if reason := guardExpected(check, tc.Expected); reason != "" {
			t.Fatalf("check does not follow the verbatim expectation %s: %s", tc.Expected, reason)
		}
		outcome, err := run.evaluate(check)
		observed = append(observed, outcome)
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	return run, observed, failures
}

func executeVector(t *testing.T, tc vectorCase) *vectorRun {
	t.Helper()
	ctx, files := decodeVectorContext(t, tc.Fixture.Ctx)
	yaml := tc.Fixture.YAML
	if !tc.Fixture.YAMLText {
		yaml = expandFixture(t, yaml, files, ctx.Predefined)
	}
	collector := diag.NewCollector()
	pipeline := parse.Source(tc.ID+".yml", []byte(yaml), collector)
	graph, err := Evaluate(pipeline, ctx, collector)
	return &vectorRun{graph: graph, findings: collector.List(), err: err}
}

func (r *vectorRun) hasFinding(code string) bool {
	for _, finding := range r.findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

// evaluate runs one check against the production Graph and returns a printable
// outcome and a mismatch error. Every semantic decision (precedence, phases,
// output visibility, runtime and compile expressions, secrecy) is made by the
// Graph; this function only routes a check to the matching API and compares.
func (r *vectorRun) evaluate(check vectorCheck) (string, error) {
	if check.Kind == "reject" || check.Kind == "finding" {
		if check.Code == "" {
			return "", fmt.Errorf("%s check must name the expected diagnostic code", check.Kind)
		}
		if check.Kind == "reject" && r.graph != nil {
			return "graph produced", errors.New("rejected input still produced a binding graph")
		}
		if !r.hasFinding(check.Code) {
			return "no finding", fmt.Errorf("missing %s; got %v", check.Code, codes(r.findings))
		}
		if check.Kind == "finding" {
			return "found " + check.Code, nil
		}
		return "rejected " + check.Code, nil
	}
	if check.Kind == "observe" {
		if r.graph == nil {
			return "evaluation rejected", nil
		}
		return fmt.Sprintf("evaluated %d tables, %d outputs", len(r.graph.Tables), len(r.graph.Outputs)), nil
	}
	if r.graph == nil {
		return "no graph", fmt.Errorf("evaluation failed: %w (findings %v)", r.err, codes(r.findings))
	}
	if check.Finding != "" && !r.hasFinding(check.Finding) {
		return "no finding", fmt.Errorf("missing %s; got %v", check.Finding, codes(r.findings))
	}
	scope := check.Scope.scope()
	if _, ok := r.graph.Table(scope); !ok {
		return "no table", fmt.Errorf("no consumer table for %+v", scope)
	}
	switch check.Kind {
	case "macro":
		expansion := r.graph.Expand(scope, check.Text)
		outcome := strconv.Quote(expansion.Text)
		if err := compare(expansion.Text, check.Want); err != nil {
			return outcome, err
		}
		if check.Null {
			return outcome, r.expectNull(scope, check.Text)
		}
		if check.Fails {
			return outcome, r.expectRuntimeError(scope, check.Text)
		}
		return outcome, nil
	case "orderDependent":
		expansion := r.graph.Expand(scope, check.Text)
		if len(expansion.Diagnostics) == 0 || expansion.Diagnostics[0].Code != check.Code {
			return strconv.Quote(expansion.Text), fmt.Errorf("want diagnostic %s, got %d diagnostics", check.Code, len(expansion.Diagnostics))
		}
		return strconv.Quote(expansion.Text) + " flagged " + check.Code, compare(expansion.Text, check.Want)
	case "compile":
		result, err := r.graph.EvalCompile(scope, check.Text)
		if err != nil {
			return "error", err
		}
		return strconv.Quote(result.Text), compare(result.Text, check.Want)
	case "masked":
		expansion := r.graph.Expand(scope, check.Text)
		outcome := fmt.Sprintf("masked=%t %q", expansion.Masked, expansion.Text)
		if !expansion.Masked || len(expansion.SecretRefs) == 0 {
			return outcome, errors.New("expected a masked expansion carrying secret references")
		}
		if check.Want != "" {
			return outcome, compare(expansion.Text, check.Want)
		}
		return outcome, nil
	case "value", "absent":
		candidate, found := r.graph.Resolve(scope, check.Name, phaseOf(check.Phase))
		if found && candidate.Secret && candidate.Value != "" {
			return "secret value in graph", fmt.Errorf("%s: secret candidate carries a value", check.Name)
		}
		compile := ""
		if check.Phase == "compile" {
			result, err := r.graph.EvalCompile(scope, "variables['"+check.Name+"']")
			if err != nil {
				return "compile error", err
			}
			compile = result.Text
			if found != !result.Null {
				return "compile mismatch", fmt.Errorf("Resolve found=%t but EvalCompile null=%t", found, result.Null)
			}
		}
		if check.Kind == "absent" {
			if found {
				return strconv.Quote(candidate.Value), fmt.Errorf("%s resolved (%q), want absent", check.Name, candidate.Value)
			}
			return "absent", nil
		}
		if !found || candidate.Secret {
			return "absent", fmt.Errorf("%s did not resolve to a plain value", check.Name)
		}
		if check.Phase == "compile" && compile != candidate.Value {
			return strconv.Quote(compile), fmt.Errorf("EvalCompile = %q, Resolve = %q", compile, candidate.Value)
		}
		return strconv.Quote(candidate.Value), compare(candidate.Value, check.Want)
	case "env", "envAbsent", "envMasked":
		entry, found := environmentEntry(r.graph.EnvironmentEntries(scope), check.Name)
		if found && entry.Masked && entry.Value != "" {
			return "secret value in env", fmt.Errorf("environment %s is masked but carries %q", check.Name, entry.Value)
		}
		switch check.Kind {
		case "envAbsent":
			if found {
				return strconv.Quote(entry.Value), fmt.Errorf("environment %s=%q, want absent", check.Name, entry.Value)
			}
			return "env-absent", nil
		case "envMasked":
			if !found || !entry.Masked || len(entry.SecretRefs) == 0 {
				return "unmasked", fmt.Errorf("environment %s found=%t masked=%t refs=%d, want masked with secret references", check.Name, found, entry.Masked, len(entry.SecretRefs))
			}
			return "masked with references", nil
		}
		if !found {
			return "env-absent", fmt.Errorf("environment %s absent, want %q", check.Name, check.Want)
		}
		return strconv.Quote(entry.Value), compare(entry.Value, check.Want)
	case "condition":
		runs, err := r.graph.EvalCondition(scope, check.Text)
		if err != nil {
			return "error", err
		}
		got := "skipped"
		if runs {
			got = "runs"
		}
		return got, compare(got, check.Want)
	}
	return "", fmt.Errorf("unknown check kind %q", check.Kind)
}

// expectNull asserts that the runtime expression a $(name) macro stands for
// evaluates successfully to null: an error must not pass for "empty".
func (r *vectorRun) expectNull(scope Scope, macro string) error {
	name := strings.TrimSuffix(strings.TrimPrefix(macro, "$("), ")")
	candidate, found := r.graph.Resolve(scope, name, PhaseRuntime)
	if !found || !isRuntimeExpression(candidate.Value) {
		return fmt.Errorf("%s is not a runtime-expression variable", name)
	}
	result, err := r.graph.EvalRuntime(scope, candidate.Value)
	if err != nil {
		return fmt.Errorf("%s: runtime expression failed instead of evaluating to null: %w", name, err)
	}
	if !result.Null {
		return fmt.Errorf("%s evaluated to %q, want null", name, result.Text)
	}
	return nil
}

// expectRuntimeError asserts the runtime expression behind a macro is reported
// as an evaluation error (documented as "unavailable"), not silently emptied.
func (r *vectorRun) expectRuntimeError(scope Scope, macro string) error {
	name := strings.TrimSuffix(strings.TrimPrefix(macro, "$("), ")")
	candidate, found := r.graph.Resolve(scope, name, PhaseRuntime)
	if !found || !isRuntimeExpression(candidate.Value) {
		return fmt.Errorf("%s is not a runtime-expression variable", name)
	}
	if _, err := r.graph.EvalRuntime(scope, candidate.Value); err == nil {
		return fmt.Errorf("%s: expected an evaluation error", name)
	}
	return nil
}

func compare(got, want string) error {
	if got != want {
		return fmt.Errorf("got %q, want %q", got, want)
	}
	return nil
}

func codes(findings []diag.Diagnostic) []string {
	result := make([]string, 0, len(findings))
	for _, finding := range findings {
		result = append(result, finding.Code)
	}
	return result
}

func phaseOf(name string) Phase {
	switch name {
	case "compile":
		return PhaseCompile
	case "runtime":
		return PhaseRuntime
	}
	return PhaseMacro
}

func environmentEntry(entries []EnvEntry, name string) (EnvEntry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if sameName(entries[i].Name, name) {
			return entries[i], true
		}
	}
	return EnvEntry{}, false
}

// guardExpected ties a check to the verbatim Markdown expectation.
func guardExpected(check vectorCheck, expected string) string {
	if check.Prose {
		return ""
	}
	contains := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(expected, word) {
				return true
			}
		}
		return false
	}
	switch check.Kind {
	case "macro", "value", "env":
		switch {
		case check.Want == check.Text && check.Kind == "macro" && check.Text != "":
			if !contains("literal", "unavailable") {
				return "reference left unchanged requires literal/unavailable"
			}
		case contains("`literal`") && strings.Contains(expected, check.Want):
			// the unchanged text is quoted in backticks inside the expectation
		case check.Want == "":
			if !contains("empty", "unavailable", "env-absent") {
				return "empty result requires empty/unavailable"
			}
		case !strings.Contains(expected, strconv.Quote(check.Want)):
			return "wanted value " + strconv.Quote(check.Want) + " is not quoted in the expectation"
		}
	case "absent":
		if !contains("literal", "empty", "unavailable") {
			return "absence requires literal/empty/unavailable"
		}
	case "envAbsent":
		if !contains("env-absent", "unavailable") {
			return "absence requires env-absent/unavailable"
		}
	case "masked", "envMasked":
		if !contains("masked") {
			return "masking requires `masked`"
		}
	case "reject", "finding":
		if !contains("reject:", "naming-rule violation") {
			return "rejection requires reject:"
		}
	case "condition":
		if !contains(check.Want) {
			return "outcome " + check.Want + " not in the expectation"
		}
	}
	return ""
}

func TestVARVMarkdownAndCasesStayInSync(t *testing.T) {
	cases := loadVectorCases(t)
	byID := make(map[string]vectorCase, len(cases))
	for _, tc := range cases {
		if _, exists := byID[tc.ID]; exists {
			t.Fatalf("duplicate structured case %s", tc.ID)
		}
		byID[tc.ID] = tc
		if (tc.NotApplicable == "") == (len(tc.Checks) == 0) {
			t.Errorf("%s must have exactly one of checks or a not-applicable reason", tc.ID)
		}
		if tc.Disagreement != "" && tc.Confidence == "uncertain" {
			t.Errorf("%s: uncertain vectors are logged, not disagreements", tc.ID)
		}
		if (tc.Disagreement == "") != (tc.DisagreementCode == "") || (tc.Disagreement == "") != (tc.Reason == "") {
			t.Errorf("%s: a disagreement, its pinned diagnostic code and its reason go together", tc.ID)
		}
	}
	markdown := loadMarkdownVectors(t)
	if len(markdown) != 146 {
		t.Errorf("Markdown table has %d vectors, want 146", len(markdown))
	}
	for id, row := range markdown { //trasloco:unordered-ok failures do not expose iteration order
		tc, ok := byID[id]
		if !ok {
			t.Errorf("Markdown vector %s has no structured case", id)
			continue
		}
		pairs := [][3]string{
			{"phase", tc.Phase, row.phase},
			{"input", tc.Input, row.input},
			{"consumer", tc.Consumer, row.consumer},
			{"expected", tc.Expected, row.expected},
			{"confidence", tc.Confidence, row.confidence},
			{"source", tc.Source, row.source},
		}
		for _, pair := range pairs {
			if pair[1] != pair[2] {
				t.Errorf("%s %s changed: structured=%q Markdown=%q", id, pair[0], pair[1], pair[2])
			}
		}
	}
	for id := range byID { //trasloco:unordered-ok failures do not expose iteration order
		if _, ok := markdown[id]; !ok {
			t.Errorf("structured case %s references a nonexistent Markdown vector", id)
		}
	}
}

type markdownRow struct{ phase, input, consumer, expected, confidence, source string }

func loadVectorCases(t *testing.T) []vectorCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "vars", "ado-variable-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []vectorCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func loadMarkdownVectors(t *testing.T) map[string]markdownRow {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "..", "..", "testdata", "azurepipelines", "vars", "ado-variable-vectors.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	rows := make(map[string]markdownRow)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "| VARV-") {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), " | ")
		if len(fields) == 7 {
			rows[strings.TrimSpace(fields[0])] = markdownRow{
				phase:      strings.TrimSpace(fields[1]),
				input:      strings.TrimSpace(fields[2]),
				consumer:   strings.TrimSpace(fields[3]),
				expected:   strings.TrimSpace(fields[4]),
				confidence: strings.TrimSpace(fields[5]),
				source:     strings.TrimSpace(fields[6]),
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// decodeVectorContext builds the non-YAML inputs in fixture order.
func decodeVectorContext(t *testing.T, raw json.RawMessage) (Context, map[string]*onode) {
	t.Helper()
	files := map[string]*onode{}
	ctx := Context{}
	if len(raw) == 0 {
		return ctx, files
	}
	root, err := parseOrdered(raw)
	if err != nil {
		t.Fatalf("decode fixture context: %v", err)
	}
	decode := func(n *onode, into any) {
		if err := json.Unmarshal([]byte(n.JSON()), into); err != nil {
			t.Fatalf("decode fixture value %s: %v", n.JSON(), err)
		}
	}
	for i, key := range root.keys {
		section := root.vals[i]
		switch key {
		case "ui":
			for j, name := range section.keys {
				var value vectorVariable
				decode(section.vals[j], &value)
				if value.Secret {
					ctx.UISecrets = append(ctx.UISecrets, SecretVariable{Name: name, ID: "fixture/" + name, Provider: "vector", Settable: value.Settable})
				} else {
					ctx.UIPlain = append(ctx.UIPlain, PlainVariable{Name: name, Value: value.Value, ID: "fixture/" + name, Settable: value.Settable})
				}
			}
		case "run":
			for j, name := range section.keys {
				var value string
				decode(section.vals[j], &value)
				ctx.Predefined = append(ctx.Predefined, PlainVariable{Name: name, Value: value, ID: "run/" + name, CompileVisible: CompileVisibleSystem(name)})
			}
		case "queue":
			for j, name := range section.keys {
				var value string
				decode(section.vals[j], &value)
				ctx.Queue = append(ctx.Queue, QueueAssignment{Name: name, Value: value})
			}
		case "settings":
			var settings vectorSettings
			decode(section, &settings)
			ctx.LimitQueueTimeVariables = settings.Limit
		case "fork":
			decode(section, &ctx.ForkPullRequest)
		case "groups":
			for j, groupName := range section.keys {
				ctx.Groups = append(ctx.Groups, decodeGroup(t, groupName, section.vals[j]))
			}
		case "files":
			for j, name := range section.keys {
				files[name] = section.vals[j]
			}
		}
	}
	return ctx, files
}

func decodeGroup(t *testing.T, name string, members *onode) VariableGroup {
	t.Helper()
	group := VariableGroup{Name: name, Authorized: true, OrderProven: true}
	for i, member := range members.keys {
		encoded := members.vals[i].JSON()
		if member == "authorized" {
			if err := json.Unmarshal([]byte(encoded), &group.Authorized); err != nil {
				t.Fatalf("decode group authorization: %v", err)
			}
			continue
		}
		var value vectorVariable
		if err := json.Unmarshal([]byte(encoded), &value); err != nil {
			t.Fatalf("decode group member %s: %v", member, err)
		}
		if value.Secret {
			group.Secrets = append(group.Secrets, SecretVariable{Name: member, ID: "group/" + name + "/" + member, Provider: "vector"})
		} else {
			group.Plain = append(group.Plain, PlainVariable{Name: member, Value: value.Value})
		}
	}
	return group
}

// Pinned allowlists: growing any of them must be a deliberate, reviewed edit.
var (
	notApplicableAllowlist = []string{"VARV-033", "VARV-050", "VARV-054", "VARV-055", "VARV-056", "VARV-074", "VARV-113", "VARV-114", "VARV-131", "VARV-132", "VARV-134", "VARV-135", "VARV-136", "VARV-146"}
	disagreementAllowlist  = []string{"VARV-060", "VARV-075", "VARV-085"}
	proseAllowlist         = []string{"VARV-089", "VARV-095", "VARV-129"}
)

func TestVARVAllowlistsArePinned(t *testing.T) {
	var na, dis, prose []string
	for _, tc := range loadVectorCases(t) {
		if tc.NotApplicable != "" {
			na = append(na, tc.ID)
		}
		if tc.Disagreement != "" {
			dis = append(dis, tc.ID)
		}
		for _, c := range tc.Checks {
			if c.Prose {
				prose = append(prose, tc.ID)
				break
			}
		}
	}
	for name, pair := range map[string][2][]string{"not-applicable": {na, notApplicableAllowlist}, "disagreement": {dis, disagreementAllowlist}, "prose": {prose, proseAllowlist}} { //trasloco:unordered-ok failures do not expose iteration order
		if strings.Join(pair[0], ",") != strings.Join(pair[1], ",") {
			t.Errorf("%s set changed: got %v, pinned %v", name, pair[0], pair[1])
		}
	}
	if len(na) != 14 {
		t.Errorf("not-applicable count = %d, pinned 14", len(na))
	}
}
