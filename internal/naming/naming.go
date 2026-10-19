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

// encodedMarker protects one _UHH transliteration token while forbidden
// characters and repeated separators are processed. It is removed before the
// name is returned and can never come from source text, because every
// non-ASCII source rune is itself transliterated first.
const encodedMarker rune = '\uE000'

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
	seq     int
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
			seq:     i,
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
		if a.entry.SourceName != b.entry.SourceName {
			return a.entry.SourceName < b.entry.SourceName
		}
		if a.digest != b.digest {
			return string(a.digest[:]) < string(b.digest[:])
		}
		return a.seq < b.seq
	})

	decisions, err := resolveInventory(candidates, policy, tokenOf)
	if err != nil {
		return nil, err
	}
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
	if kind == KindWorkflowFile {
		caseName = strings.ToLower(caseName)
	}
	name = transliterateASCII(caseName)
	switch kind {
	case KindSecret:
		name = strings.ToUpper(name)
	case KindWorkflowFile:
		// The workflow charset is lower-case, including the _UHH encoding
		// tokens produced by transliteration.
		name = strings.ToLower(name)
	}
	name = stripEncodedMarkers(replaceForbidden(kind, name))
	name = stripEncodedMarkers(collapseSeparator(kind, name))
	if name == "" {
		return Decision{}, diagnosticError("TRL-NAME-0005", kind, stableID)
	}
	if illegalFirst(kind, name[0]) {
		name = "N_" + name
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
		if available <= 0 {
			return Decision{}, diagnosticError("TRL-NAME-0008", kind, stableID)
		}
	}
	if available > 0 && len(name) > available {
		suffix := truncationMarker(kind) + truncationHash(kind, stableID, nfcName)
		if available <= len(suffix) {
			return Decision{}, diagnosticError("TRL-NAME-0008", kind, stableID)
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
			result.WriteRune(encodedMarker)
			result.WriteString("_U")
			result.WriteString(strings.ToUpper(hex.EncodeToString([]byte{b})))
		}
	}
	return result.String()
}

// stripEncodedMarkers removes the private markers that protected encoding
// tokens during separator processing.
func stripEncodedMarkers(value string) string {
	return strings.ReplaceAll(value, string(encodedMarker), "")
}

func replaceForbidden(kind Kind, value string) string {
	separator := byte('_')
	if kind == KindWorkflowFile {
		separator = '-'
	}
	var result strings.Builder
	for i := 0; i < len(value); {
		if value[i] < utf8.RuneSelf {
			if allowed(kind, value[i]) {
				result.WriteByte(value[i])
				i++
				continue
			}
			result.WriteByte(separator)
			for i < len(value) && value[i] < utf8.RuneSelf && !allowed(kind, value[i]) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == encodedMarker {
			result.WriteRune(r)
			i += size
			end := i + 4
			if end > len(value) {
				end = len(value)
			}
			result.WriteString(value[i:end])
			i = end
			continue
		}
		result.WriteRune(r)
		i += size
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
	for i := 0; i < len(value); {
		if value[i] >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(value[i:])
			result.WriteRune(r)
			i += size
			if r == encodedMarker {
				end := i + 4
				if end > len(value) {
					end = len(value)
				}
				result.WriteString(value[i:end])
				i = end
			}
			previous = false
			continue
		}
		if value[i] == separator {
			if previous {
				i++
				continue
			}
			previous = true
		} else {
			previous = false
		}
		result.WriteByte(value[i])
		i++
	}
	if kind == KindWorkflowFile {
		return strings.Trim(result.String(), "-")
	}
	return result.String()
}

func illegalFirst(kind Kind, b byte) bool {
	if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' {
		return false
	}
	return kind != KindWorkflowFile
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
	hash := hex.EncodeToString(digest[:])[:10]
	if kind == KindSecret {
		return strings.ToUpper(hash)
	}
	return hash
}

func collisionToken(kind Kind, stableID string, length int) string {
	digest := sha256.Sum256([]byte(string(kind) + "\x00" + stableID))
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:]))
	if length > len(encoded) {
		length = len(encoded)
	}
	token := encoded[:length]
	if kind == KindSecret {
		return strings.ToUpper(token)
	}
	return token
}

type plannedEntry struct {
	candidate candidate
	suffixed  bool
	tokenLen  int
	token     string
	name      string
}

type collisionGroup struct {
	members     []candidate
	items       []*plannedEntry
	byKey       map[string]*plannedEntry
	hasObserved bool
}

type positionedDecision struct {
	decision Decision
	seq      int
}

// resolveInventory assigns every generated candidate a name that is unique in
// its target namespace. Collision groups are only a starting point: a suffixed
// form must not equal any literal candidate or any assigned name of the
// namespace, including candidates of other groups.
func resolveInventory(candidates []candidate, policy Policy, tokenOf tokenFunc) ([]Decision, error) {
	literals := make(map[string]map[string]bool)
	for _, item := range candidates {
		namespace := targetNamespace(item.entry)
		if literals[namespace] == nil {
			literals[namespace] = make(map[string]bool)
		}
		literals[namespace][comparisonKey(item.entry.Kind, item.name)] = true
	}

	groups := make([]*collisionGroup, 0, len(candidates))
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && sameCollisionGroup(candidates[start], candidates[end]) {
			end++
		}
		group := &collisionGroup{byKey: make(map[string]*plannedEntry)}
		for _, item := range candidates[start:end] {
			group.members = append(group.members, item)
			if item.entry.Observed {
				group.hasObserved = true
				continue
			}
			identity := item.entry.StableID + "\x00" + string(item.digest[:])
			if _, exists := group.byKey[identity]; exists {
				continue
			}
			planned := &plannedEntry{candidate: item}
			group.byKey[identity] = planned
			group.items = append(group.items, planned)
		}
		for index, item := range group.items {
			item.suffixed = group.hasObserved || index > 0
			if item.suffixed {
				item.tokenLen = 8
			}
		}
		groups = append(groups, group)
		start = end
	}

	for {
		changed := false
		tokenGroups := make(map[string][]*plannedEntry)
		nameGroups := make(map[string][]*plannedEntry)
		for _, group := range groups {
			for _, item := range group.items {
				if !item.suffixed {
					continue
				}
				kind := item.candidate.entry.Kind
				item.token = tokenOf(kind, item.candidate.entry.StableID, item.tokenLen)
				if kind == KindSecret {
					item.token = strings.ToUpper(item.token)
				}
				name, err := withSuffix(item.candidate.name, collisionSeparator(kind)+item.token, limitFor(policy, kind), item.candidate.entry)
				if err != nil {
					return nil, err
				}
				item.name = name
				namespace := targetNamespace(item.candidate.entry)
				tokenKey := namespace + "\x00" + item.candidate.compare + "\x00" + item.token
				tokenGroups[tokenKey] = append(tokenGroups[tokenKey], item)
				nameKey := namespace + "\x00" + comparisonKey(kind, name)
				nameGroups[nameKey] = append(nameGroups[nameKey], item)
			}
		}
		// Collect every entry that must grow exactly once in this iteration.
		// Extending the same entry twice would skip a valid token length (for
		// example 8 -> 10 -> 12) when its token and its name both collide.
		marked := make(map[*plannedEntry]bool)
		for _, colliding := range tokenGroups { //trasloco:unordered-ok only marks entries; extension follows group order
			if len(colliding) < 2 {
				continue
			}
			for _, item := range colliding {
				marked[item] = true
			}
		}
		for _, colliding := range nameGroups { //trasloco:unordered-ok only marks entries; extension follows group order
			if len(colliding) < 2 {
				continue
			}
			for _, item := range colliding {
				marked[item] = true
			}
		}
		for _, group := range groups {
			for _, item := range group.items {
				if !item.suffixed {
					continue
				}
				if literals[targetNamespace(item.candidate.entry)][comparisonKey(item.candidate.entry.Kind, item.name)] {
					marked[item] = true
				}
			}
		}
		for _, group := range groups {
			for _, item := range group.items {
				if !marked[item] {
					continue
				}
				if err := extendToken(item); err != nil {
					return nil, err
				}
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	for _, group := range groups {
		for _, item := range group.items {
			if !item.suffixed {
				item.name = item.candidate.name
			}
		}
	}

	var positioned []positionedDecision
	seen := make(map[string]bool)
	for _, group := range groups {
		for _, member := range group.members {
			if member.entry.Observed {
				continue
			}
			key := decisionKey(member.entry)
			if seen[key] {
				continue
			}
			seen[key] = true
			identity := member.entry.StableID + "\x00" + string(member.digest[:])
			planned := group.byKey[identity]
			if planned == nil {
				return nil, fmt.Errorf("naming: no plan for %s", key)
			}
			codes := append([]string(nil), planned.candidate.codes...)
			if planned.suffixed {
				codes = appendCode(codes, "TRL-NAME-0002")
			}
			positioned = append(positioned, positionedDecision{
				decision: Decision{Entry: member.entry, TargetName: planned.name, DiagnosticCodes: codes},
				seq:      len(positioned),
			})
		}
	}
	sort.Slice(positioned, func(i, j int) bool {
		a, b := positioned[i].decision, positioned[j].decision
		keyA, keyB := decisionKey(a.Entry), decisionKey(b.Entry)
		if keyA != keyB {
			return keyA < keyB
		}
		if a.TargetName != b.TargetName {
			return a.TargetName < b.TargetName
		}
		return positioned[i].seq < positioned[j].seq
	})
	decisions := make([]Decision, len(positioned))
	for index, item := range positioned {
		decisions[index] = item.decision
	}
	return decisions, nil
}

// extendToken applies naming.md collision step 4 to one planned entry.
func extendToken(item *plannedEntry) error {
	if item.tokenLen >= maxCollisionTokenLength {
		return diagnosticError("TRL-NAME-0003", item.candidate.entry.Kind, item.candidate.entry.StableID)
	}
	item.tokenLen += 2
	if item.tokenLen > maxCollisionTokenLength {
		item.tokenLen = maxCollisionTokenLength
	}
	return nil
}

func withSuffix(name, suffix string, limit int, entry Entry) (string, error) {
	extension := workflowExtension(entry.Kind)
	base := strings.TrimSuffix(name, extension)
	if limit <= 0 {
		result := base + suffix + extension
		if reserved(entry.Kind, strings.TrimSuffix(result, extension)) {
			result = "T_" + result
		}
		return result, nil
	}
	available := limit - len(extension) - len(suffix)
	if available < 1 {
		return "", diagnosticError("TRL-NAME-0008", entry.Kind, entry.StableID)
	}
	result := truncateUTF8(base, available) + suffix + extension
	plain := strings.TrimSuffix(result, extension)
	if !reserved(entry.Kind, plain) {
		return result, nil
	}
	// A collision suffix can itself create a reserved name, for example
	// GITHUB plus the secret "__" marker. Apply step 8 to the suffixed form and
	// reserve room for the prefix.
	const prefix = "T_"
	available = limit - len(extension) - len(suffix) - len(prefix)
	if available < 1 {
		return "", diagnosticError("TRL-NAME-0008", entry.Kind, entry.StableID)
	}
	return prefix + truncateUTF8(base, available) + suffix + extension, nil
}

// collisionSeparator is the marker that separates a base name from a collision
// token. Secret and variable names admit only [A-Z0-9_], so they use an
// underscore marker; file and identifier kinds keep the specification's dash.
func collisionSeparator(kind Kind) string {
	if kind == KindSecret {
		return "__"
	}
	return "--"
}

// truncationMarker is the marker of normalization step 10 before the hash.
func truncationMarker(kind Kind) string {
	if kind == KindSecret {
		return "__H"
	}
	return "--h"
}

// maxCollisionTokenLength is the length of the unpadded lower-case base32
// encoding of a SHA-256 digest (52 characters).
const maxCollisionTokenLength = 52

// targetNamespace is the namespace in which generated names must be unique.
// GitHub secrets are repository-level except for environment secrets.
func targetNamespace(entry Entry) string {
	return string(entry.Kind) + "\x00" + entry.ScopeKey
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
	entries := make([]Entry, 0, 8)
	// emit-gha.md 5.1: an empty Pipeline.name is valid and omits the workflow
	// name. Naming.md forbids inventing a filename from IDs, so no workflow-file
	// decision exists until the emitter chooses its documented fallback.
	if pipeline.Name != "" {
		entries = append(entries, Entry{Kind: KindWorkflowFile, Platform: "ado", StableID: pipeline.ID, SourceName: pipeline.Name, ScopeKey: workflowScopeKey})
	}
	for _, job := range pipeline.Jobs {
		entries = append(entries, Entry{Kind: KindJobID, Platform: "ado", StableID: job.ID, SourceName: job.ID, ScopeKey: pipeline.ID})
		for _, step := range job.Steps {
			entries = append(entries, Entry{Kind: KindStepID, Platform: "ado", StableID: step.ID, SourceName: step.ID, ScopeKey: job.ID})
		}
	}

	declared := declaredSecretRefs(pipeline)
	for _, secret := range declared {
		entries = append(entries, secretEntry(secret))
	}
	for _, secret := range expressionSecretRefs(pipeline, declared) {
		entries = append(entries, secretEntry(secret))
	}

	resolver, err := NewResolver(entries, policy)
	if err != nil {
		return nil, err
	}
	return &Namer{pipelineID: pipeline.ID, resolver: resolver}, nil
}

// declaredSecretRefs collects every SecretRef that the IR stores directly:
// variables at pipeline, stage and job scope, job bindings, reusable-workflow
// secret bindings, and container/service passwords. Variables with ScopeGroup
// are included because they are variables too.
func declaredSecretRefs(pipeline *ir.Pipeline) []ir.SecretRef {
	var refs []ir.SecretRef
	for _, variable := range pipeline.Variables {
		if variable.SecretRef != nil {
			refs = append(refs, *variable.SecretRef)
		}
	}
	for _, stage := range pipeline.Stages {
		for _, variable := range stage.Variables {
			if variable.SecretRef != nil {
				refs = append(refs, *variable.SecretRef)
			}
		}
	}
	for _, job := range pipeline.Jobs {
		for _, variable := range job.Variables {
			if variable.SecretRef != nil {
				refs = append(refs, *variable.SecretRef)
			}
		}
		refs = append(refs, job.Secrets...)
		if job.Uses != nil {
			for _, secret := range job.Uses.Secrets.All() {
				refs = append(refs, secret)
			}
		}
		refs = appendContainerPasswords(refs, job.Container)
		for _, service := range job.Services {
			refs = appendContainerPasswords(refs, &service.Container)
		}
	}
	return refs
}

func appendContainerPasswords(refs []ir.SecretRef, container *ir.Container) []ir.SecretRef {
	if container == nil || container.Credentials == nil || container.Credentials.Password == nil {
		return refs
	}
	return append(refs, *container.Credentials.Password)
}

// expressionSecretRefs collects secret references that are stored as Expr
// values: container and service usernames and env, step env, and step with.
func expressionSecretRefs(pipeline *ir.Pipeline, declared []ir.SecretRef) []ir.SecretRef {
	var refs []ir.SecretRef
	appendExpr := func(expr ir.Expr, ownerStep string) {
		appendExprSecretRefs(&refs, expr, declared, pipeline.ID, ownerStep)
	}
	for _, job := range pipeline.Jobs {
		appendContainerExpressions(&refs, job.Container, declared, pipeline.ID)
		for _, service := range job.Services {
			appendContainerExpressions(&refs, &service.Container, declared, pipeline.ID)
		}
		for _, step := range job.Steps {
			for _, entry := range step.Env {
				appendExpr(entry.Value, step.ID)
			}
			if step.Action != nil {
				for _, entry := range step.Action.With {
					appendExpr(entry.Value, step.ID)
				}
			}
		}
	}
	return refs
}

func appendContainerExpressions(refs *[]ir.SecretRef, container *ir.Container, declared []ir.SecretRef, pipelineID string) {
	if container == nil {
		return
	}
	if container.Credentials != nil && container.Credentials.Username != nil {
		appendExprSecretRefs(refs, *container.Credentials.Username, declared, pipelineID, "")
	}
	for _, entry := range container.Env {
		appendExprSecretRefs(refs, entry.Value, declared, pipelineID, "")
	}
}

func appendExprSecretRefs(refs *[]ir.SecretRef, expr ir.Expr, declared []ir.SecretRef, pipelineID, ownerStep string) {
	switch expr.Kind {
	case ir.ExprRef:
		if expr.Ref == nil || expr.Ref.Context != ir.RefSecrets || len(expr.Ref.Path) == 0 {
			return
		}
		name := strings.Join(expr.Ref.Path, ".")
		*refs = append(*refs, resolveSecretRef(name, declared, pipelineID, ownerStep))
	case ir.ExprMacro:
		if expr.Macro == nil || !expr.Macro.Secret || expr.Macro.Name == "" {
			return
		}
		*refs = append(*refs, resolveSecretRef(expr.Macro.Name, declared, pipelineID, ownerStep))
	case ir.ExprCall:
		if expr.Call == nil {
			return
		}
		for _, argument := range expr.Call.Args {
			appendExprSecretRefs(refs, argument, declared, pipelineID, ownerStep)
		}
	}
}

// resolveSecretRef links a macro or secrets expression to its declaration when
// exactly one declaration has that name. Ambiguous or undeclared names use one
// stable synthetic identity so several uses still share one target name.
func resolveSecretRef(name string, declared []ir.SecretRef, pipelineID, _ string) ir.SecretRef {
	var found ir.SecretRef
	matches := 0
	for _, candidate := range declared {
		if strings.EqualFold(candidate.Name, name) {
			found = candidate
			matches++
		}
	}
	if matches == 1 {
		return found
	}
	return ir.SecretRef{Name: name, Scope: ir.ScopePipeline, ScopeID: pipelineID}
}

// WorkflowFile returns the precomputed workflow filename of the pipeline. A name
// that is not the normalized pipeline name is an error: the emitter must not ask
// for a decision the inventory never made.
func (n *Namer) WorkflowFile(name string) (string, error) {
	if n == nil {
		return "", fmt.Errorf("nil namer")
	}
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
	if n == nil {
		return "", fmt.Errorf("nil namer")
	}
	if decision, ok := n.resolver.Lookup(KindJobID, n.pipelineID, jobID); ok {
		return decision.TargetName, nil
	}
	return "", fmt.Errorf("job %q/%q is not in the naming inventory", stageID, jobID)
}

// StepID returns the precomputed GitHub step ID within its owning job.
func (n *Namer) StepID(jobID, stepID string) (string, error) {
	if n == nil {
		return "", fmt.Errorf("nil namer")
	}
	if decision, ok := n.resolver.Lookup(KindStepID, jobID, stepID); ok {
		return decision.TargetName, nil
	}
	return "", fmt.Errorf("step %q/%q is not in the naming inventory", jobID, stepID)
}

// SecretName returns the precomputed GitHub secret name.
func (n *Namer) SecretName(secret ir.SecretRef) (string, error) {
	if n == nil {
		return "", fmt.Errorf("nil namer")
	}
	entry := secretEntry(secret)
	if decision, ok := n.resolver.Lookup(entry.Kind, entry.ScopeKey, entry.StableID); ok {
		return decision.TargetName, nil
	}
	return "", fmt.Errorf("secret %q is not in the naming inventory", secret.Name)
}

func secretEntry(secret ir.SecretRef) Entry {
	sourceScope := string(secret.Scope) + ":" + secret.ScopeID
	namespace := "github:repository"
	if secret.Scope == ir.ScopeEnvironment {
		namespace = "github:environment:" + secret.ScopeID
	}
	return Entry{
		Kind: KindSecret, Platform: "ado", StableID: sourceScope + "\x00" + secret.Name,
		SourceName: secret.Name, ScopeKey: namespace,
	}
}
