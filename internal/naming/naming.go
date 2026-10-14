// Package naming provides pure, deterministic target-name normalization.
// All collision state is owned by a Resolver or Namer value; the package has
// no mutable package-level state and performs no I/O.
package naming

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"golang.org/x/text/unicode/norm"
)

// PolicyVersion identifies the implemented naming algorithm.
const PolicyVersion = "trasloco-name-v1"

// workflowScopeKey is the inventory scope of the single workflow file of one
// pipeline: the file lives in the repository tree, not in a stage or a job.
const workflowScopeKey = "workflow"

// Kind identifies a supported target-name kind.
type Kind string

const (
	// KindWorkflowFile is a GitHub workflow filename.
	KindWorkflowFile Kind = "workflow"
	// KindJobID is a GitHub Actions job identifier.
	KindJobID Kind = "github-job"
	// KindStepID is a GitHub Actions step identifier. The naming specification
	// does not define separate lexical rules, so it uses the job-ID rules.
	KindStepID Kind = "github-step"
	// KindSecret is a GitHub Actions secret name.
	KindSecret Kind = "github-secret"
)

// Policy supplies endpoint limits. Limits are byte lengths and a non-positive
// limit means unbounded. Every ASCII-only kind normalizes to ASCII, so a byte
// limit is also a code-point limit for them.
type Policy struct {
	// WorkflowFileLimit bounds the whole workflow filename, including ".yml".
	WorkflowFileLimit int
	// JobIDLimit bounds a GitHub job identifier.
	JobIDLimit int
	// StepIDLimit bounds a GitHub step identifier.
	StepIDLimit int
	// SecretLimit bounds a GitHub Actions secret name.
	SecretLimit int
}

// DefaultPolicy returns the portable policy used when endpoint probes do not
// provide tighter limits.
func DefaultPolicy() Policy {
	return Policy{WorkflowFileLimit: 255}
}

// Entry is one source name in a collision inventory. Observed entries reserve
// an existing target name and never receive a generated assignment.
type Entry struct {
	Kind       Kind
	Platform   string
	StableID   string
	SourceName string
	ScopeKey   string
	Observed   bool
}

// Decision is the deterministic result for one source name. DiagnosticCodes
// are registry codes, in a fixed order, that the emitter reports for it.
type Decision struct {
	Entry           Entry
	TargetName      string
	DiagnosticCodes []string
}

// Resolver owns the collision decisions of one complete inventory. All state
// lives in the value: nothing is shared between pipelines or goroutines.
type Resolver struct {
	decisions []Decision
}

// tokenFunc produces the disambiguation token of one source entry. The
// production value is collisionToken; a test substitutes a colliding function
// to exercise the escalation of collision handling step 4.
type tokenFunc func(kind Kind, stableID string, length int) string

// candidate is one normalized entry with its comparison key, content digest and
// decision codes.
type candidate struct {
	entry   Entry
	name    string
	compare string
	digest  [sha256.Size]byte
	codes   []string
}

// newCandidates normalizes every entry of one inventory.
func newCandidates(entries []Entry, policy Policy) ([]candidate, error) {
	candidates := make([]candidate, len(entries))
	for i, entry := range entries {
		decision, err := normalize(entry.Kind, entry.StableID, entry.SourceName, limitFor(policy, entry.Kind))
		if err != nil {
			return nil, err
		}
		candidates[i] = candidate{
			entry: entry, name: decision.TargetName,
			compare: comparisonKey(entry.Kind, decision.TargetName),
			digest:  sha256.Sum256([]byte(norm.NFC.String(entry.SourceName))),
			codes:   append([]string(nil), decision.DiagnosticCodes...),
		}
	}
	return candidates, nil
}

// NewResolver normalizes a complete inventory and resolves collisions without
// depending on its input order.
func NewResolver(entries []Entry, policy Policy) (*Resolver, error) {
	return newResolver(entries, policy, collisionToken)
}

// newResolver resolves an inventory with an explicit disambiguation token
// function so that a test can exercise token escalation. It is the whole
// implementation of NewResolver.
func newResolver(entries []Entry, policy Policy, tokenOf tokenFunc) (*Resolver, error) {
	candidates, err := newCandidates(entries, policy)
	if err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.entry.Kind != b.entry.Kind {
			return a.entry.Kind < b.entry.Kind
		}
		if a.entry.ScopeKey != b.entry.ScopeKey {
			return a.entry.ScopeKey < b.entry.ScopeKey
		}
		if a.compare != b.compare {
			return a.compare < b.compare
		}
		if a.entry.Observed != b.entry.Observed {
			return a.entry.Observed
		}
		if a.entry.Platform != b.entry.Platform {
			return a.entry.Platform < b.entry.Platform
		}
		if a.entry.StableID != b.entry.StableID {
			return a.entry.StableID < b.entry.StableID
		}
		return string(a.digest[:]) < string(b.digest[:])
	})

	decisions := make([]Decision, 0, len(candidates))
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && sameCollisionGroup(candidates[start], candidates[end]) {
			end++
		}
		group, err := resolveGroup(candidates[start:end], policy, tokenOf)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, group...)
		start = end
	}
	sort.Slice(decisions, func(i, j int) bool { return decisionKey(decisions[i].Entry) < decisionKey(decisions[j].Entry) })
	return &Resolver{decisions: decisions}, nil
}

// Lookup returns the decision for a generated source entry.
func (r *Resolver) Lookup(kind Kind, scopeKey, stableID string) (Decision, bool) {
	if r == nil {
		return Decision{}, false
	}
	key := decisionKey(Entry{Kind: kind, ScopeKey: scopeKey, StableID: stableID})
	i := sort.Search(len(r.decisions), func(i int) bool { return decisionKey(r.decisions[i].Entry) >= key })
	if i >= len(r.decisions) || decisionKey(r.decisions[i].Entry) != key {
		return Decision{}, false
	}
	decision := r.decisions[i]
	decision.DiagnosticCodes = append([]string(nil), decision.DiagnosticCodes...)
	return decision, true
}

// NormalizeWorkflowFile normalizes one workflow filename without collision
// inventory. maxLength includes the canonical .yml extension.
func NormalizeWorkflowFile(stableID, name string, maxLength int) (Decision, error) {
	return normalize(KindWorkflowFile, stableID, name, maxLength)
}

// NormalizeJobID normalizes one job identifier without collision inventory.
func NormalizeJobID(stableID, name string, maxLength int) (Decision, error) {
	return normalize(KindJobID, stableID, name, maxLength)
}

// NormalizeStepID normalizes one step identifier without collision inventory.
func NormalizeStepID(stableID, name string, maxLength int) (Decision, error) {
	return normalize(KindStepID, stableID, name, maxLength)
}

// NormalizeSecretName normalizes one GitHub Actions secret name without
// collision inventory.
func NormalizeSecretName(stableID, name string, maxLength int) (Decision, error) {
	return normalize(KindSecret, stableID, name, maxLength)
}

func normalize(kind Kind, stableID, sourceName string, maxLength int) (Decision, error) {
	entry := Entry{Kind: kind, StableID: stableID, SourceName: sourceName}
	if !utf8.ValidString(sourceName) {
		return Decision{}, fmt.Errorf("invalid UTF-8 in %s source name", kind)
	}
	nfcName := norm.NFC.String(sourceName)
	name := strings.TrimSpace(nfcName)
	if kind == KindWorkflowFile {
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".yaml") {
			name = name[:len(name)-len(".yaml")]
		} else if strings.HasSuffix(lower, ".yml") {
			name = name[:len(name)-len(".yml")]
		}
	}
	if name == "" {
		return Decision{}, diagnosticError("TRL-NAME-0005", kind, stableID)
	}

	caseName := name
	switch kind {
	case KindWorkflowFile:
		caseName = strings.ToLower(caseName)
	case KindSecret:
		caseName = strings.ToUpper(caseName)
	}
	name = transliterateASCII(caseName)
	if kind == KindWorkflowFile {
		name = strings.ToLower(name)
	}
	name = replaceForbidden(kind, name)
	name = collapseSeparator(kind, name)
	if name == "" {
		return Decision{}, diagnosticError("TRL-NAME-0005", kind, stableID)
	}
	if illegalFirst(kind, name[0]) {
		if kind == KindWorkflowFile {
			name = "n-" + name
		} else {
			name = "N_" + name
		}
	}
	if reserved(kind, name) {
		if kind == KindWorkflowFile {
			name = "t-" + name
		} else {
			name = "T_" + name
		}
	}

	codes := normalizedCodes(sourceName, name, kind)
	extension := ""
	if kind == KindWorkflowFile {
		extension = ".yml"
	}
	available := maxLength
	if available > 0 {
		available -= len(extension)
	}
	if available > 0 && len(name) > available {
		suffix := "--h" + truncationHash(kind, stableID, nfcName)
		if available < len(suffix) {
			return Decision{}, diagnosticError("TRL-NAME-0003", kind, stableID)
		}
		name = truncateUTF8(name, available-len(suffix)) + suffix
		codes = appendCode(codes, "TRL-NAME-0006")
	}
	name += extension
	return Decision{Entry: entry, TargetName: name, DiagnosticCodes: codes}, nil
}

func transliterateASCII(value string) string {
	decomposed := norm.NFD.String(value)
	var result strings.Builder
	for _, r := range decomposed {
		if r < utf8.RuneSelf {
			result.WriteRune(r)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		encoded := []byte(string(r))
		for _, b := range encoded {
			result.WriteString("_U")
			result.WriteString(strings.ToUpper(hex.EncodeToString([]byte{b})))
		}
	}
	return result.String()
}

func replaceForbidden(kind Kind, value string) string {
	separator := byte('_')
	if kind == KindWorkflowFile {
		separator = '-'
	}
	var result strings.Builder
	for i := 0; i < len(value); {
		if allowed(kind, value[i]) {
			result.WriteByte(value[i])
			i++
			continue
		}
		result.WriteByte(separator)
		for i < len(value) && !allowed(kind, value[i]) {
			i++
		}
	}
	return result.String()
}

func allowed(kind Kind, b byte) bool {
	alnum := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
	if alnum || b == '_' {
		return true
	}
	return (kind == KindWorkflowFile || kind == KindJobID || kind == KindStepID) && b == '-'
}

func collapseSeparator(kind Kind, value string) string {
	separator := byte('_')
	if kind == KindWorkflowFile {
		separator = '-'
	}
	var result strings.Builder
	previous := false
	for i := 0; i < len(value); i++ {
		if value[i] == separator {
			if previous {
				continue
			}
			previous = true
		} else {
			previous = false
		}
		result.WriteByte(value[i])
	}
	if kind == KindWorkflowFile {
		return strings.Trim(result.String(), "-")
	}
	return result.String()
}

func illegalFirst(kind Kind, b byte) bool {
	if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
		return false
	}
	return kind != KindJobID && kind != KindStepID && kind != KindSecret || b != '_'
}

func reserved(kind Kind, name string) bool {
	upper := strings.ToUpper(name)
	if kind == KindSecret {
		return strings.HasPrefix(upper, "GITHUB_")
	}
	if kind != KindWorkflowFile {
		return false
	}
	if upper == "README" || upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" {
		return true
	}
	for i := 1; i <= 9; i++ {
		if upper == fmt.Sprintf("COM%d", i) || upper == fmt.Sprintf("LPT%d", i) {
			return true
		}
	}
	return false
}

func normalizedCodes(source, normalized string, kind Kind) []string {
	expected := normalized
	if kind == KindWorkflowFile {
		expected += ".yml"
	}
	if source == expected {
		return nil
	}
	return []string{"TRL-NAME-0001"}
}

func comparisonKey(kind Kind, name string) string {
	return strings.ToLower(strings.TrimSuffix(name, workflowExtension(kind)))
}

func workflowExtension(kind Kind) string {
	if kind == KindWorkflowFile {
		return ".yml"
	}
	return ""
}

func truncationHash(kind Kind, stableID, nfcName string) string {
	digest := sha256.Sum256([]byte(string(kind) + "\x00" + stableID + "\x00" + nfcName))
	return hex.EncodeToString(digest[:])[:10]
}

func collisionToken(kind Kind, stableID string, length int) string {
	digest := sha256.Sum256([]byte(string(kind) + "\x00" + stableID))
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:]))
	if length > len(encoded) {
		length = len(encoded)
	}
	return encoded[:length]
}

func resolveGroup(group []candidate, policy Policy, tokenOf tokenFunc) ([]Decision, error) {
	generated := make([]candidate, 0, len(group))
	hasObserved := false
	for _, item := range group {
		if item.entry.Observed {
			hasObserved = true
			continue
		}
		generated = append(generated, item)
	}
	if len(generated) == 0 {
		return nil, nil
	}

	results := make([]Decision, 0, len(generated))
	for i, item := range generated {
		duplicate := -1
		for j := 0; j < i; j++ {
			if item.entry.StableID == generated[j].entry.StableID && item.digest == generated[j].digest {
				duplicate = j
				break
			}
		}
		if duplicate >= 0 {
			results = append(results, Decision{Entry: item.entry, TargetName: results[duplicate].TargetName, DiagnosticCodes: append([]string(nil), results[duplicate].DiagnosticCodes...)})
			continue
		}
		name := item.name
		codes := append([]string(nil), item.codes...)
		if hasObserved || i > 0 {
			var err error
			name, err = appendCollisionSuffix(item, policy, generated[:i], tokenOf)
			if err != nil {
				return nil, err
			}
			codes = appendCode(codes, "TRL-NAME-0002")
		}
		results = append(results, Decision{Entry: item.entry, TargetName: name, DiagnosticCodes: codes})
	}
	return results, nil
}

func appendCollisionSuffix(item candidate, policy Policy, preceding []candidate, tokenOf tokenFunc) (string, error) {
	tokenLength := 8
	for {
		token := tokenOf(item.entry.Kind, item.entry.StableID, tokenLength)
		unique := true
		for _, other := range preceding {
			if tokenOf(other.entry.Kind, other.entry.StableID, tokenLength) == token {
				unique = false
				break
			}
		}
		if unique {
			return withSuffix(item.name, "--"+token, limitFor(policy, item.entry.Kind), item.entry.Kind), nil
		}
		if tokenLength >= 52 {
			return "", diagnosticError("TRL-NAME-0003", item.entry.Kind, item.entry.StableID)
		}
		tokenLength += 2
	}
}

func withSuffix(name, suffix string, limit int, kind Kind) string {
	extension := workflowExtension(kind)
	base := strings.TrimSuffix(name, extension)
	if limit > 0 {
		base = truncateUTF8(base, limit-len(extension)-len(suffix))
	}
	return base + suffix + extension
}

func truncateUTF8(value string, length int) string {
	if length <= 0 {
		return ""
	}
	if len(value) <= length {
		return value
	}
	for length > 0 && !utf8.RuneStart(value[length]) {
		length--
	}
	return value[:length]
}

func sameCollisionGroup(a, b candidate) bool {
	return a.entry.Kind == b.entry.Kind && a.entry.ScopeKey == b.entry.ScopeKey && a.compare == b.compare
}

func decisionKey(entry Entry) string {
	return string(entry.Kind) + "\x00" + entry.ScopeKey + "\x00" + entry.StableID
}

func limitFor(policy Policy, kind Kind) int {
	switch kind {
	case KindWorkflowFile:
		return policy.WorkflowFileLimit
	case KindJobID:
		return policy.JobIDLimit
	case KindStepID:
		return policy.StepIDLimit
	case KindSecret:
		return policy.SecretLimit
	default:
		return 0
	}
}

func appendCode(codes []string, code string) []string {
	for _, existing := range codes {
		if existing == code {
			return codes
		}
	}
	return append(codes, code)
}

func diagnosticError(code string, kind Kind, stableID string) error {
	return &diag.Error{Finding: diag.MustNew(code, diag.WithResource(diag.Resource{Kind: string(kind), Key: stableID}))}
}

// Namer is a naming policy precomputed for one IR pipeline.
type Namer struct {
	pipelineID string
	resolver   *Resolver
}

// New creates a per-pipeline namer with a complete collision inventory.
func New(pipeline *ir.Pipeline, policy Policy) (*Namer, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("nil pipeline")
	}
	entries := []Entry{{Kind: KindWorkflowFile, Platform: "ado", StableID: pipeline.ID, SourceName: pipeline.Name, ScopeKey: workflowScopeKey}}
	for _, job := range pipeline.Jobs {
		entries = append(entries, Entry{Kind: KindJobID, Platform: "ado", StableID: job.ID, SourceName: job.ID, ScopeKey: pipeline.ID})
		for _, step := range job.Steps {
			entries = append(entries, Entry{Kind: KindStepID, Platform: "ado", StableID: step.ID, SourceName: step.ID, ScopeKey: job.ID})
		}
		for _, secret := range job.Secrets {
			entries = append(entries, secretEntry(secret))
		}
		for _, variable := range job.Variables {
			if variable.SecretRef != nil {
				entries = append(entries, secretEntry(*variable.SecretRef))
			}
		}
	}
	for _, variable := range pipeline.Variables {
		if variable.SecretRef != nil {
			entries = append(entries, secretEntry(*variable.SecretRef))
		}
	}
	resolver, err := NewResolver(entries, policy)
	if err != nil {
		return nil, err
	}
	return &Namer{pipelineID: pipeline.ID, resolver: resolver}, nil
}

// WorkflowFile returns the precomputed workflow filename of the pipeline. A name
// that is not the normalized pipeline name is an error: the emitter must not ask
// for a decision the inventory never made.
func (n *Namer) WorkflowFile(name string) (string, error) {
	decision, ok := n.resolver.Lookup(KindWorkflowFile, workflowScopeKey, n.pipelineID)
	if !ok {
		return "", fmt.Errorf("workflow name %q is not in the naming inventory", name)
	}
	if name != decision.Entry.SourceName {
		return "", fmt.Errorf("workflow name %q is not the pipeline name %q", name, decision.Entry.SourceName)
	}
	return decision.TargetName, nil
}

// JobID returns the precomputed GitHub job ID. stageID is accepted for the
// emitter interface; IR job IDs are already pipeline-qualified.
func (n *Namer) JobID(stageID, jobID string) (string, error) {
	if decision, ok := n.resolver.Lookup(KindJobID, n.pipelineID, jobID); ok {
		return decision.TargetName, nil
	}
	return "", fmt.Errorf("job %q/%q is not in the naming inventory", stageID, jobID)
}

// StepID returns the precomputed GitHub step ID within its owning job.
func (n *Namer) StepID(jobID, stepID string) (string, error) {
	if decision, ok := n.resolver.Lookup(KindStepID, jobID, stepID); ok {
		return decision.TargetName, nil
	}
	return "", fmt.Errorf("step %q/%q is not in the naming inventory", jobID, stepID)
}

// SecretName returns the precomputed GitHub secret name.
func (n *Namer) SecretName(secret ir.SecretRef) (string, error) {
	entry := secretEntry(secret)
	if decision, ok := n.resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID); ok {
		return decision.TargetName, nil
	}
	return "", fmt.Errorf("secret %q is not in the naming inventory", secret.Name)
}

func secretEntry(secret ir.SecretRef) Entry {
	scope := string(secret.Scope) + ":" + secret.ScopeID
	return Entry{
		Kind: KindSecret, Platform: "ado", StableID: scope + "\x00" + secret.Name,
		SourceName: secret.Name, ScopeKey: scope,
	}
}
