// This file runs the test-vector table of docs/spec/naming.md.
//
// The vectors are the specification's, not this implementation's: every input
// and every expectation is read from the normative table at run time. This file
// only decides how a row is executed and which shape of assertion applies.
package naming

import (
	"bufio"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/google/go-cmp/cmp"
	"golang.org/x/text/unicode/norm"
)

const specVectorsPath = "../../docs/spec/naming.md"

// specVector is one row of the naming.md test-vector table. Source is the
// required provenance of CONVENTIONS.md: the normative table that defines it.
type specVector struct {
	id       string
	kindCell string
	expected string
	source   string
}

// namingVector classifies one spec row: it is either executed by run or
// deliberately out of this package's scope, with a reason.
type namingVector struct {
	id            string
	run           func(t *testing.T, v specVector, all map[string]specVector)
	notApplicable string
}

// namingVectors is the complete classification of the spec table. A new row in
// docs/spec/naming.md fails TestNamingVectorsArePinned until it is classified
// here.
var namingVectors = []namingVector{
	{id: "NAME-001", run: runSimpleVector},
	{id: "NAME-002", run: runSimpleVector},
	{id: "NAME-003", run: runSimpleVector},
	{id: "NAME-004", run: runSimpleVector},
	{id: "NAME-005", notApplicable: "the GitHub Actions variable kind is outside the T7a naming surface (workflow files, job IDs, step IDs, secrets)"},
	{id: "NAME-006", run: runSimpleVector},
	{id: "NAME-007", run: runSimpleVector},
	{id: "NAME-008", run: runSimpleVector},
	{id: "NAME-009", notApplicable: "the GitHub environment kind is not part of the T7a naming surface"},
	{id: "NAME-010", notApplicable: "the GitHub environment kind is not part of the T7a naming surface"},
	{id: "NAME-011", run: runSimpleVector},
	{id: "NAME-012", run: runSimpleVector},
	{id: "NAME-013", run: runSimpleVector},
	{id: "NAME-014", run: runSimpleVector},
	{id: "NAME-015", run: runSimpleVector},
	{id: "NAME-016", run: runSimpleVector},
	{id: "NAME-017", run: runSimpleVector},
	{id: "NAME-018", notApplicable: "the GitHub repository kind belongs to the capability probe, not to this package"},
	{id: "NAME-019", notApplicable: "the GitHub repository kind belongs to the capability probe, not to this package"},
	{id: "NAME-020", notApplicable: "the ADO variable kind is not a target name produced by the emitter"},
	{id: "NAME-021", notApplicable: "the ADO variable kind is not a target name produced by the emitter"},
	{id: "NAME-022", notApplicable: "the ADO variable-group kind is not a target name produced by the emitter"},
	{id: "NAME-023", notApplicable: "the GitHub custom-property kind is not a target name produced by the emitter"},
	{id: "NAME-024", run: runCollisionVector},
	{id: "NAME-025", run: runObservedTargetVector},
	{id: "NAME-026", notApplicable: "the GitHub repository kind belongs to the capability probe, not to this package"},
	{id: "NAME-027", notApplicable: "the GitHub environment kind is not part of the T7a naming surface"},
	{id: "NAME-028", run: runWorkflowCollisionVector},
	{id: "NAME-029", notApplicable: "merging identical duplicates is the variable pipeline's step, not name generation"},
	{id: "NAME-030", run: runCollisionOrderVector},
	{id: "NAME-031", run: runCollisionOrderVector},
	{id: "NAME-032", notApplicable: "explicit mapping validation belongs to the mapping file, not to this package"},
	{id: "NAME-033", run: runTruncationVector},
	{id: "NAME-034", run: runTruncationHashVector},
	{id: "NAME-035", run: runUnicodeEquivalenceVector},
	{id: "NAME-036", notApplicable: "precondition drift is a plan-lifecycle rule, not name generation"},
	{id: "NAME-037", notApplicable: "adoption of an existing target name is a plan-level mapping decision"},
	{id: "NAME-038", run: runTokenEscalationVector},
	{id: "NAME-039", notApplicable: "the GitHub repository kind belongs to the capability probe, not to this package"},
	{id: "NAME-040", run: runSimpleVector},
	{id: "NAME-041", run: runSimpleVector},
	{id: "NAME-042", run: runCollisionVector},
	{id: "NAME-043", run: runThreeCollisionVector},
	{id: "NAME-044", run: runSmallLimitVector},
	{id: "NAME-045", run: runSimpleVector},
	{id: "NAME-046", run: runRepositoryNamespaceCollisionVector},
}

func TestNamingVectorsArePinned(t *testing.T) {
	vectors := loadSpecVectors(t)
	classified := make(map[string]bool, len(namingVectors))
	for _, vector := range namingVectors {
		if classified[vector.id] {
			t.Errorf("%s is classified twice", vector.id)
		}
		classified[vector.id] = true
		if (vector.run == nil) == (vector.notApplicable == "") {
			t.Errorf("%s must have exactly one of run and notApplicable", vector.id)
		}
		row, ok := vectors[vector.id]
		if !ok {
			t.Errorf("%s is classified but missing from %s", vector.id, specVectorsPath)
			continue
		}
		if row.source == "" {
			t.Errorf("%s has no vector source (CONVENTIONS.md)", vector.id)
		}
	}
	var unclassified []string
	for id := range vectors { //trasloco:unordered-ok the result is sorted before it is reported
		if !classified[id] {
			unclassified = append(unclassified, id)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("spec vectors neither implemented nor pinned as not applicable: %v", unclassified)
	}
}

func TestNamingSpecVectors(t *testing.T) {
	vectors := loadSpecVectors(t)
	for _, vector := range namingVectors {
		if vector.run == nil {
			continue
		}
		row := vectors[vector.id]
		t.Run(vector.id, func(t *testing.T) { vector.run(t, row, vectors) })
	}
}

// loadSpecVectors parses the test-vector table of the normative spec.
func loadSpecVectors(tb testing.TB) map[string]specVector {
	tb.Helper()
	file, err := os.Open(filepath.Join("..", "..", "docs", "spec", "naming.md"))
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	vectors := make(map[string]specVector)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if !strings.HasPrefix(line, "| NAME-") {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), " | ")
		if len(fields) != 3 {
			tb.Fatalf("%s row %q has %d cells, want 3", specVectorsPath, line, len(fields))
		}
		id := strings.TrimSpace(fields[0])
		vectors[id] = specVector{
			id:       id,
			kindCell: strings.TrimSpace(fields[1]),
			expected: strings.TrimSpace(fields[2]),
			source:   fmt.Sprintf("%s:%d", specVectorsPath, lineNumber),
		}
	}
	if err := scanner.Err(); err != nil {
		tb.Fatal(err)
	}
	if len(vectors) == 0 {
		tb.Fatalf("no test vectors found in %s", specVectorsPath)
	}
	return vectors
}

// quoted returns the backquoted spans of a table cell in table order.
func quoted(cell string) []string {
	spans := []string{}
	for rest := cell; ; {
		start := strings.IndexByte(rest, '`')
		if start < 0 {
			return spans
		}
		end := strings.IndexByte(rest[start+1:], '`')
		if end < 0 {
			return spans
		}
		spans = append(spans, rest[start+1:start+1+end])
		rest = rest[start+1+end+1:]
	}
}

// expectedName is the expected target name of an Expected cell: its first
// backquoted span, which is the only part that is a name.
func expectedName(t *testing.T, v specVector) string {
	t.Helper()
	spans := quoted(v.expected)
	if len(spans) == 0 {
		t.Fatalf("%s: expected cell %q has no backquoted name", v.id, v.expected)
	}
	return spans[0]
}

// expectedCodes are the diagnostic codes a row names in its Expected cell.
func expectedCodes(cell string) []string {
	return codePattern.FindAllString(cell, -1)
}

var codePattern = regexp.MustCompile(`TRL-[A-Z]+-[0-9]{4}`)

// kindOf maps the kind label of the table to an implemented kind.
func kindOf(label string) (Kind, bool) {
	switch label {
	case "GitHub secret":
		return KindSecret, true
	case "workflow":
		return KindWorkflowFile, true
	case "job ID":
		return KindJobID, true
	default:
		return "", false
	}
}

// simpleVector splits the "Kind / sourceStableId / input" cell of a row that
// follows the table's regular shape. The input is taken verbatim from the table.
func simpleVector(t *testing.T, v specVector) (Kind, string, string) {
	t.Helper()
	parts := strings.Split(v.kindCell, " / ")
	if len(parts) != 3 {
		t.Fatalf("%s: kind cell %q is not 'Kind / stableId / input'", v.id, v.kindCell)
	}
	kind, ok := kindOf(parts[0])
	if !ok {
		t.Fatalf("%s: kind %q is not implemented by this package", v.id, parts[0])
	}
	stableID := strings.TrimSpace(parts[1])
	if stableID == "" {
		t.Fatalf("%s: empty stable ID", v.id)
	}
	return kind, stableID, strings.Trim(strings.TrimSpace(parts[2]), "`")
}

// normalizeFor calls the exported normalizer of one kind.
func normalizeFor(kind Kind, stableID, name string, limit int) (Decision, error) {
	switch kind {
	case KindWorkflowFile:
		return NormalizeWorkflowFile(stableID, name, limit)
	case KindJobID:
		return NormalizeJobID(stableID, name, limit)
	case KindStepID:
		return NormalizeStepID(stableID, name, limit)
	case KindSecret:
		return NormalizeSecretName(stableID, name, limit)
	default:
		return Decision{}, errors.New("unsupported kind " + string(kind))
	}
}

// assertToken checks a production collision suffix against naming.md: the
// base, the kind's marker, and a base32 token of the requested length derived
// independently from SHA-256 over "kind NUL sourceStableId".
func assertToken(t *testing.T, id string, kind Kind, stableID, name, base string, length int) {
	t.Helper()
	marker := collisionSeparator(kind)
	token, ok := strings.CutPrefix(name, base+marker)
	if !ok {
		t.Errorf("%s: %q does not start with %q", id, name, base+marker)
		return
	}
	alphabet := "abcdefghijklmnopqrstuvwxyz234567"
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sha256Digest(string(kind) + "\x00" + stableID)))
	if kind == KindSecret {
		alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
		encoded = strings.ToUpper(encoded)
	}
	if len(token) != length {
		t.Errorf("%s: token %q has length %d, want %d", id, token, len(token), length)
	}
	if strings.Trim(token, alphabet) != "" {
		t.Errorf("%s: token %q is not %s base32", id, token, alphabetName(kind))
	}
	if length > len(encoded) {
		t.Fatalf("%s: independent token length %d exceeds the digest", id, length)
	}
	if want := encoded[:length]; token != want {
		t.Errorf("%s: token = %q, independently derived SHA-256 token is %q", id, token, want)
	}
}

// assertTokenShape checks a suffixed name produced by an injected token
// function, for which the SHA-256 derivation does not apply.
func assertTokenShape(t *testing.T, id, name, base, marker, alphabet string, length int) {
	t.Helper()
	token, ok := strings.CutPrefix(name, base+marker)
	if !ok {
		t.Errorf("%s: %q does not start with %q", id, name, base+marker)
		return
	}
	if len(token) != length {
		t.Errorf("%s: token %q has length %d, want %d", id, token, len(token), length)
	}
	if strings.Trim(token, alphabet) != "" {
		t.Errorf("%s: token %q is outside the expected alphabet %q", id, token, alphabet)
	}
}

func sha256Digest(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func alphabetName(kind Kind) string {
	if kind == KindSecret {
		return "upper-case"
	}
	return "lower-case"
}

// runSimpleVector executes one regular "Kind / stableId / input" row.
func runSimpleVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	kind, stableID, input := simpleVector(t, v)
	want := expectedName(t, v)
	limit := limitFor(DefaultPolicy(), kind)
	decision, err := normalizeFor(kind, stableID, input, limit)
	if err != nil {
		t.Fatalf("%s: normalize %s %q: %v", v.id, kind, input, err)
	}
	if decision.TargetName != want {
		t.Errorf("%s: normalize %s %q = %q, spec expects %q", v.id, kind, input, decision.TargetName, want)
	}
	for _, code := range expectedCodes(v.expected) {
		if !slices.Contains(decision.DiagnosticCodes, code) {
			t.Errorf("%s: normalize %s %q codes = %v, spec expects %s", v.id, kind, input, decision.DiagnosticCodes, code)
		}
	}
	again, err := normalizeFor(kind, stableID, input, limit)
	if err != nil {
		t.Fatalf("%s: second normalize %s %q: %v", v.id, kind, input, err)
	}
	if diff := cmp.Diff(decision, again); diff != "" {
		t.Errorf("%s: normalization is not deterministic (-first +second):\n%s", v.id, diff)
	}
}

// runCollisionVector executes a row whose kind cell names two colliding sources
// in one scope, for example "same GitHub secret scope: `db.password` ID `a`,
// `db-password` ID `b`".
func runCollisionVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	spans := quoted(v.kindCell)
	if len(spans) != 4 {
		t.Fatalf("%s: want two names and two stable IDs, kind cell %q", v.id, v.kindCell)
	}
	first, firstID, second, secondID := spans[0], spans[1], spans[2], spans[3]
	want := expectedName(t, v)
	scope := "github:repo:1"
	entries := []Entry{
		{Kind: KindSecret, Platform: "ado", StableID: firstID, SourceName: first, ScopeKey: scope},
		{Kind: KindSecret, Platform: "ado", StableID: secondID, SourceName: second, ScopeKey: scope},
	}
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	decisions := map[string]Decision{}
	for _, entry := range entries {
		decision, ok := resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
		if !ok {
			t.Fatalf("%s: no decision for %s", v.id, entry.StableID)
		}
		if decision.TargetName != want && !strings.HasPrefix(decision.TargetName, want+collisionSeparator(KindSecret)) {
			t.Errorf("%s: %s normalized to %q, which is neither %q nor a suffixed %q",
				v.id, entry.StableID, decision.TargetName, want, want)
		}
		decisions[entry.StableID] = decision
	}
	if decisions[firstID].TargetName == decisions[secondID].TargetName {
		t.Fatalf("%s: both sources were assigned %q; the spec expects a detected collision",
			v.id, decisions[firstID].TargetName)
	}
	// Byte order of the stable IDs decides which source keeps the candidate.
	if decisions[firstID].TargetName != want {
		t.Errorf("%s: %s got %q, expected the unsuffixed %q", v.id, firstID, decisions[firstID].TargetName, want)
	}
	assertToken(t, v.id, KindSecret, secondID, decisions[secondID].TargetName, want, 8)
	if !slices.Contains(decisions[secondID].DiagnosticCodes, "TRL-NAME-0002") {
		t.Errorf("%s: disambiguated source codes = %v, expected TRL-NAME-0002",
			v.id, decisions[secondID].DiagnosticCodes)
	}
}

// runThreeCollisionVector executes the row that assigns one unsuffixed
// candidate and two colliding tokens, and requires escalation to extend every
// colliding token (naming.md collision step 4).
func runThreeCollisionVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	spans := quoted(v.kindCell)
	if len(spans) != 4 {
		t.Fatalf("%s: want three stable IDs and one input name, kind cell %q", v.id, v.kindCell)
	}
	ids := spans[:3]
	input := spans[3]
	want := expectedName(t, v)
	entries := make([]Entry, len(ids))
	for index, id := range ids {
		entries[index] = Entry{Kind: KindSecret, Platform: "ado", StableID: id, SourceName: input, ScopeKey: "github:repository"}
	}
	colliding := func(_ Kind, stableID string, length int) string {
		token := strings.Repeat("a", length)
		if length > 8 {
			token = token[:length-1] + stableID[:1]
		}
		return token
	}
	resolver, err := newResolver(entries, DefaultPolicy(), colliding)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	decisions := make(map[string]Decision, len(ids))
	for _, id := range ids {
		decision, ok := resolver.Lookup(KindSecret, "github:repository", id)
		if !ok {
			t.Fatalf("%s: no decision for %s", v.id, id)
		}
		decisions[id] = decision
	}
	if decisions[ids[0]].TargetName != want {
		t.Errorf("%s: %s got %q, expected the unsuffixed %q", v.id, ids[0], decisions[ids[0]].TargetName, want)
	}
	seen := map[string]bool{want: true}
	for _, id := range ids[1:] {
		decision := decisions[id]
		assertTokenShape(t, v.id, decision.TargetName, want, collisionSeparator(KindSecret), "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 10)
		if seen[decision.TargetName] {
			t.Errorf("%s: duplicate output %q", v.id, decision.TargetName)
		}
		seen[decision.TargetName] = true
		if !slices.Contains(decision.DiagnosticCodes, "TRL-NAME-0002") {
			t.Errorf("%s: %s codes = %v, expected TRL-NAME-0002", v.id, id, decision.DiagnosticCodes)
		}
	}
}

// runSmallLimitVector executes the row that rejects a limit too small to hold
// a valid truncation marker with the dedicated diagnostic.
func runSmallLimitVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	_, stableID, input := simpleVector(t, v)
	_, err := NormalizeSecretName(stableID, input, 3)
	var finding *diag.Error
	if !errors.As(err, &finding) || finding.Finding.Code != "TRL-NAME-0008" {
		t.Fatalf("%s: error = %v, want TRL-NAME-0008", v.id, err)
	}
}

// runRepositoryNamespaceCollisionVector executes the row that keeps every IR
// secret scope in one repository namespace except environment scope.
func runRepositoryNamespaceCollisionVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	spans := quoted(v.kindCell)
	if len(spans) != 4 {
		t.Fatalf("%s: want two names and two scope IDs, kind cell %q", v.id, v.kindCell)
	}
	firstRef := ir.SecretRef{Name: spans[0], Scope: ir.ScopeStage, ScopeID: spans[1]}
	secondRef := ir.SecretRef{Name: spans[2], Scope: ir.ScopeJob, ScopeID: spans[3]}
	entries := []Entry{secretEntry(firstRef), secretEntry(secondRef)}
	if entries[0].ScopeKey != "github:repository" || entries[1].ScopeKey != "github:repository" {
		t.Fatalf("%s: stage and job secrets resolved to scopes %q and %q, want one repository namespace",
			v.id, entries[0].ScopeKey, entries[1].ScopeKey)
	}
	want := expectedName(t, v)
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	decisions := make([]Decision, len(entries))
	for index, entry := range entries {
		decision, ok := resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID)
		if !ok {
			t.Fatalf("%s: no decision for %s", v.id, entry.StableID)
		}
		decisions[index] = decision
	}
	if decisions[0].TargetName == decisions[1].TargetName {
		t.Fatalf("%s: both source scopes were assigned %q; the repository namespace must collide",
			v.id, decisions[0].TargetName)
	}
	unsuffixed, suffixed := 0, 1
	if decisions[1].TargetName == want {
		unsuffixed, suffixed = 1, 0
	}
	if decisions[unsuffixed].TargetName != want {
		t.Errorf("%s: neither decision kept the unsuffixed %q", v.id, want)
	}
	assertToken(t, v.id, KindSecret, decisions[suffixed].Entry.StableID, decisions[suffixed].TargetName, want, 8)
	if !slices.Contains(decisions[suffixed].DiagnosticCodes, "TRL-NAME-0002") {
		t.Errorf("%s: disambiguated source codes = %v, expected TRL-NAME-0002",
			v.id, decisions[suffixed].DiagnosticCodes)
	}
}

// runObservedTargetVector executes the row that reserves an existing target name.
func runObservedTargetVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	spans := quoted(v.kindCell)
	if len(spans) != 2 {
		t.Fatalf("%s: want the existing and the source name, kind cell %q", v.id, v.kindCell)
	}
	existing, source := spans[0], spans[1]
	scope := "github:repo:1"
	resolver, err := NewResolver([]Entry{
		{Kind: KindSecret, Platform: "ado", StableID: "existing", SourceName: existing, ScopeKey: scope, Observed: true},
		{Kind: KindSecret, Platform: "ado", StableID: "source", SourceName: source, ScopeKey: scope},
	}, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	decision, ok := resolver.Lookup(KindSecret, scope, "source")
	if !ok {
		t.Fatalf("%s: no decision for the source", v.id)
	}
	if decision.TargetName == source {
		t.Errorf("%s: the source kept %q although the observed target already owns it case-insensitively", v.id, source)
	}
	assertToken(t, v.id, KindSecret, "source", decision.TargetName, source, 8)
	if !slices.Contains(decision.DiagnosticCodes, "TRL-NAME-0002") {
		t.Errorf("%s: source codes = %v, expected TRL-NAME-0002", v.id, decision.DiagnosticCodes)
	}
	if observed, ok := resolver.Lookup(KindSecret, scope, "existing"); ok {
		t.Errorf("%s: the observed target received the decision %+v; it never receives an assignment", v.id, observed)
	}
}

// runWorkflowCollisionVector executes the row that collides two workflow
// basenames under the portable comparison.
func runWorkflowCollisionVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	spans := quoted(v.kindCell)
	if len(spans) != 2 {
		t.Fatalf("%s: want the two workflow names, kind cell %q", v.id, v.kindCell)
	}
	scope := "repo:1"
	limit := limitFor(DefaultPolicy(), KindWorkflowFile)
	resolver, err := NewResolver([]Entry{
		{Kind: KindWorkflowFile, Platform: "ado", StableID: "s1", SourceName: spans[0], ScopeKey: scope},
		{Kind: KindWorkflowFile, Platform: "ado", StableID: "s2", SourceName: spans[1], ScopeKey: scope},
	}, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	first, ok := resolver.Lookup(KindWorkflowFile, scope, "s1")
	if !ok {
		t.Fatalf("%s: no decision for s1", v.id)
	}
	second, ok := resolver.Lookup(KindWorkflowFile, scope, "s2")
	if !ok {
		t.Fatalf("%s: no decision for s2", v.id)
	}
	if first.TargetName == second.TargetName {
		t.Fatalf("%s: both workflows were assigned %q; the spec expects a collision", v.id, first.TargetName)
	}
	candidate, err := NormalizeWorkflowFile("s1", spans[0], limit)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	suffixed := first.TargetName
	suffixedID := "s1"
	if suffixed == candidate.TargetName {
		suffixed = second.TargetName
		suffixedID = "s2"
	}
	if first.TargetName != candidate.TargetName && second.TargetName != candidate.TargetName {
		t.Errorf("%s: neither %q nor %q is the unsuffixed candidate %q", v.id, first.TargetName, second.TargetName, candidate.TargetName)
	}
	base := strings.TrimSuffix(candidate.TargetName, ".yml")
	if !strings.HasSuffix(suffixed, ".yml") {
		t.Errorf("%s: %q lost the canonical extension", v.id, suffixed)
	}
	assertToken(t, v.id, KindWorkflowFile, suffixedID, strings.TrimSuffix(suffixed, ".yml"), base, 8)
}

// collisionAssignments resolves one collision inventory in the given API order
// and returns the stable-ID to target-name assignment.
func collisionAssignments(t *testing.T, v specVector, kind Kind, input, scope string, ids []string) map[string]string {
	t.Helper()
	entries := make([]Entry, len(ids))
	for i, id := range ids {
		entries[i] = Entry{Kind: kind, Platform: "ado", StableID: id, SourceName: input, ScopeKey: scope}
	}
	resolver, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	assignments := make(map[string]string, len(ids))
	for _, id := range ids {
		decision, ok := resolver.Lookup(kind, scope, id)
		if !ok {
			t.Fatalf("%s: no decision for %s", v.id, id)
		}
		assignments[id] = decision.TargetName
	}
	return assignments
}

// runCollisionOrderVector executes the rows that fix which source keeps the
// candidate under a collision, and that require the assignment to be
// independent of the inventory order. Neither row restates its input, so both
// take it from the colliding row of the same table.
func runCollisionOrderVector(t *testing.T, v specVector, all map[string]specVector) {
	t.Helper()
	ids := quoted(all["NAME-030"].kindCell)
	seed := quoted(all["NAME-024"].kindCell)
	if len(ids) != 2 || len(seed) == 0 {
		t.Fatalf("%s: NAME-030 or NAME-024 is no longer usable as the inventory source", v.id)
	}
	input, scope := seed[0], "github:repo:1"
	candidate, err := NormalizeSecretName("1", input, limitFor(DefaultPolicy(), KindSecret))
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	forward := collisionAssignments(t, v, KindSecret, input, scope, ids)
	reversedIDs := []string{ids[1], ids[0]}
	reversed := collisionAssignments(t, v, KindSecret, input, scope, reversedIDs)
	if diff := cmp.Diff(forward, reversed); diff != "" {
		t.Errorf("%s: assignments depend on inventory order (-forward +reversed):\n%s", v.id, diff)
	}
	lowest := slices.Min(ids)
	if forward[lowest] != candidate.TargetName {
		t.Errorf("%s: stable ID %q got %q, expected the unsuffixed %q from byte order",
			v.id, lowest, forward[lowest], candidate.TargetName)
	}
	for _, id := range ids {
		if id != lowest {
			assertToken(t, v.id, KindSecret, id, forward[id], candidate.TargetName, 8)
		}
	}
}

// runTruncationVector executes the row that fixes the shape of an over-limit
// name. The row fixes the behavior, not the input: it states only that the input
// exceeds the endpoint limit, so the fixture derives one from the table.
func runTruncationVector(t *testing.T, v specVector, all map[string]specVector) {
	t.Helper()
	seed := quoted(all["NAME-024"].kindCell)
	if len(seed) == 0 {
		t.Fatalf("%s: NAME-024 is no longer usable as the input source", v.id)
	}
	limit := 32
	input := strings.Repeat(seed[0], limit)
	decision, err := NormalizeSecretName("1", input, limit)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	if len(decision.TargetName) > limit {
		t.Errorf("%s: %q has length %d, over the limit %d", v.id, decision.TargetName, len(decision.TargetName), limit)
	}
	if !utf8.ValidString(decision.TargetName) {
		t.Errorf("%s: %q is not valid UTF-8", v.id, decision.TargetName)
	}
	if !slices.Contains(decision.DiagnosticCodes, "TRL-NAME-0006") {
		t.Errorf("%s: codes = %v, expected TRL-NAME-0006", v.id, decision.DiagnosticCodes)
	}
	marker := strings.Index(decision.TargetName, "__H")
	if marker < 0 {
		t.Fatalf("%s: %q carries no truncation hash", v.id, decision.TargetName)
	}
	hash := decision.TargetName[marker+len("__H"):]
	if len(hash) != 10 || strings.Trim(hash, "0123456789ABCDEF") != "" {
		t.Errorf("%s: %q does not end with __H and 10 upper-case hex characters", v.id, decision.TargetName)
	}
	if strings.HasPrefix(decision.TargetName, "-") {
		t.Errorf("%s: %q starts with a dash", v.id, decision.TargetName)
	}
}

// runTruncationHashVector executes the row that requires the truncation hash of
// one name and stable ID to be identical across runs, and checks it against the
// derivation of normalization step 10.
func runTruncationHashVector(t *testing.T, v specVector, all map[string]specVector) {
	t.Helper()
	seed := quoted(all["NAME-024"].kindCell)
	if len(seed) == 0 {
		t.Fatalf("%s: NAME-024 is no longer usable as the input source", v.id)
	}
	limit := 32
	stableID := "1"
	input := strings.Repeat(seed[0], limit)
	first, err := NormalizeSecretName(stableID, input, limit)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	second, err := NormalizeSecretName(stableID, input, limit)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	if first.TargetName != second.TargetName {
		t.Errorf("%s: two runs produced %q then %q", v.id, first.TargetName, second.TargetName)
	}
	digest := sha256.Sum256([]byte(string(KindSecret) + "\x00" + stableID + "\x00" + norm.NFC.String(input)))
	want := "__H" + strings.ToUpper(hex.EncodeToString(digest[:])[:10])
	if !strings.HasSuffix(first.TargetName, want) {
		t.Errorf("%s: %q does not end with the step-10 hash %q", v.id, first.TargetName, want)
	}
}

// runUnicodeEquivalenceVector executes the row that gives the composed and the
// decomposed spelling of one name the same candidate.
func runUnicodeEquivalenceVector(t *testing.T, v specVector, _ map[string]specVector) {
	t.Helper()
	spans := quoted(v.kindCell)
	if len(spans) != 2 {
		t.Fatalf("%s: want the composed spelling and the base letter, kind cell %q", v.id, v.kindCell)
	}
	composed := spans[0]
	// The row spells the decomposed form as the base letter plus a combining
	// acute, so the mark is the only part this fixture adds.
	decomposed := spans[1] + "\u0301"
	scope := "github:repo:1"
	first, err := NormalizeSecretName("1", composed, 0)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	second, err := NormalizeSecretName("1", decomposed, 0)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	if first.TargetName != second.TargetName {
		t.Errorf("%s: composed %q normalized to %q but decomposed %q to %q",
			v.id, composed, first.TargetName, decomposed, second.TargetName)
	}
	resolver, err := NewResolver([]Entry{
		{Kind: KindSecret, Platform: "ado", StableID: "1", SourceName: composed, ScopeKey: scope},
		{Kind: KindSecret, Platform: "ado", StableID: "2", SourceName: decomposed, ScopeKey: scope},
	}, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	firstDecision, ok := resolver.Lookup(KindSecret, scope, "1")
	if !ok {
		t.Fatalf("%s: no decision for the first spelling", v.id)
	}
	secondDecision, ok := resolver.Lookup(KindSecret, scope, "2")
	if !ok {
		t.Fatalf("%s: no decision for the second spelling", v.id)
	}
	if firstDecision.TargetName == secondDecision.TargetName {
		t.Fatalf("%s: identical candidates with distinct stable IDs were not disambiguated", v.id)
	}
	assertToken(t, v.id, KindSecret, "2", secondDecision.TargetName, firstDecision.TargetName, 8)
}

// runTokenEscalationVector executes the row that extends colliding tokens to
// 10 characters. A real 8-character base32 collision cannot be constructed from
// SHA-256, so the fixture substitutes the token function of the production loop;
// every other rule, including the ordering, stays production code. Two suffixed
// entries are required to exercise collision step 4.
func runTokenEscalationVector(t *testing.T, v specVector, all map[string]specVector) {
	t.Helper()
	spans := quoted(all["NAME-043"].kindCell)
	if len(spans) != 4 {
		t.Fatalf("%s: NAME-043 is no longer usable as the three-entry inventory source", v.id)
	}
	ids, input := spans[:3], spans[3]
	base := expectedName(t, all["NAME-043"])
	scope := "github:repository"
	colliding := func(_ Kind, stableID string, length int) string {
		token := strings.Repeat("a", length)
		if length > 8 {
			token = token[:length-1] + stableID[:1]
		}
		return token
	}
	entries := make([]Entry, len(ids))
	for index, id := range ids {
		entries[index] = Entry{Kind: KindSecret, Platform: "ado", StableID: id, SourceName: input, ScopeKey: scope}
	}
	resolver, err := newResolver(entries, DefaultPolicy(), colliding)
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	if first, ok := resolver.Lookup(KindSecret, scope, ids[0]); !ok || first.TargetName != base {
		t.Errorf("%s: %s got %q, expected the unsuffixed %q", v.id, ids[0], first.TargetName, base)
	}
	for _, id := range ids[1:] {
		extended, ok := resolver.Lookup(KindSecret, scope, id)
		if !ok {
			t.Fatalf("%s: no decision for %s", v.id, id)
		}
		assertTokenShape(t, v.id, extended.TargetName, base, collisionSeparator(KindSecret), "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 10)
	}
	production, err := NewResolver(entries, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s: %v", v.id, err)
	}
	for _, id := range ids[1:] {
		normal, ok := production.Lookup(KindSecret, scope, id)
		if !ok {
			t.Fatalf("%s: no decision for %s", v.id, id)
		}
		assertToken(t, v.id, KindSecret, id, normal.TargetName, base, 8)
	}
}
