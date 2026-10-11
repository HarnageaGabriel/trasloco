package normalize

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// scrubber removes built-in credential shapes (tokens, URL credentials, private
// keys) from every free-form string that reaches the IR or its extensions.
var scrubber = redact.New()

// secretWords are whole words (after camelCase / separator splitting) that mark
// a key as holding secret material.
var secretWords = map[string]bool{
	"password": true, "passwd": true, "passphrase": true, "secret": true, "secrets": true,
	"pwd": true, "token": true, "tokens": true, "credential": true, "credentials": true, "apikey": true, "accesskey": true,
	"accountkey": true, "privatekey": true, "secretkey": true, "sharedkey": true,
	"signingkey": true, "connectionstring": true, "pat": true, "sas": true, "sig": true,
	"sastoken": true,
}

// keyQualifiers make a trailing "key" word secret ("account key", "API_KEY").
var keyQualifiers = map[string]bool{
	"account": true, "access": true, "api": true, "storage": true, "private": true,
	"secret": true, "signing": true, "shared": true, "encryption": true, "master": true,
	"primary": true, "secondary": true, "subscription": true, "ssh": true, "auth": true,
}

// nonSecretSuffixes mark keys that name, locate or describe a secret rather than
// hold it (secretName, tokenEndpoint, SecretsFilter, ...).
var nonSecretSuffixes = map[string]bool{
	"name": true, "names": true, "type": true, "filter": true, "prefix": true, "suffix": true,
	"endpoint": true, "url": true, "uri": true, "path": true, "file": true,
}

// nonSecretExactNames are case-sensitive environment conventions. Lowercase
// pwd remains password-like.
var nonSecretExactNames = map[string]bool{
	"PWD": true, // Conventional uppercase current-directory environment key.
}

// nonSecretTaskInputs are exact task inputs whose values are references or
// names, not credential values. The context check is essential: a variable
// with one of these names remains secret-classified.
var nonSecretTaskInputs = map[string]bool{
	"externalFeedCredentials": true, // NuGet feed service-connection name(s).
	"publishFeedCredentials":  true, // NuGet publishing service-connection name.
	"imagePullSecret":         true, // Kubernetes secret name, not its value.
	"imagePullSecrets":        true,
}

// maxPredicateBytes bounds the value text the secret-literal predicates
// inspect. A longer value of a secret-classified key is treated as a secret
// without further tests (conservative; see docs/spec/normalize-ado.md).
const maxPredicateBytes = 4096

var macroOnly = regexp.MustCompile(`^\$\([A-Za-z0-9_.\-]+\)$`)

// nameWords splits a key into lowercase words on separators and camelCase.
func nameWords(name string) []string {
	var words []string
	current := make([]rune, 0, len(name))
	flush := func() {
		if len(current) != 0 {
			words = append(words, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && len(current) != 0:
			prevLower := unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if prevLower || (unicode.IsUpper(runes[i-1]) && nextLower) {
				flush()
			}
		}
		current = append(current, r)
	}
	flush()
	return words
}

// secretName reports whether a key looks like it holds secret material.
func secretName(name string) bool {
	if nonSecretExactNames[name] {
		return false
	}
	words := nameWords(name)
	if len(words) == 0 {
		return false
	}
	if nonSecretSuffixes[words[len(words)-1]] {
		return false
	}
	for i, word := range words {
		switch {
		case secretWords[word]:
			return true
		case word == "key" && i > 0 && keyQualifiers[words[i-1]]:
			return true
		case word == "string" && i > 0 && words[i-1] == "connection":
			return true
		}
	}
	return false
}

// isReference reports whether value is only a macro or runtime expression, i.e.
// a reference to a value rather than the value itself.
func isReference(value string) bool {
	trimmed := strings.TrimSpace(value)
	return macroOnly.MatchString(trimmed) || (strings.HasPrefix(trimmed, "$[") && strings.HasSuffix(trimmed, "]"))
}

// safeText scrubs credential patterns from free-form text.
func safeText(value string) string {
	if !mayHoldCredential(value) {
		return value
	}
	return scrubber.Redact(value)
}

// credentialTriggers is a superset of the literal substrings every redact
// built-in pattern needs (lower case; see internal/redact builtinPatterns and
// the "?" query scrubber). Text without any of them cannot change.
var credentialTriggers = []string{
	"-----begin", "azdo", "github_pat_", "gho_", "ghp_", "ghu_", "ghs_", "ghr_",
	"glpat-", "glptt-", "gldt-", "xoxb-", "eyj", "://", "?", "authorization", "cookie",
}

func mayHoldCredential(value string) bool {
	lower := strings.ToLower(value)
	for _, trigger := range credentialTriggers {
		if strings.Contains(lower, trigger) {
			return true
		}
	}
	return false
}

// scrub scrubs credential patterns and records a loss when the text changed.
func (a *acc) scrub(construct, property string, source diag.Source, value string) string {
	out := safeText(value)
	if out != value {
		a.lose(codeExt, construct, property, source, "credential pattern in text was replaced by the redaction placeholder")
	}
	return out
}

// needsManual raises the fidelity to needs-manual.
func (a *acc) needsManual() { a.f = maxFidelity(a.f, diag.FidelityNeedsManual) }

// secretRefExpr references a secret by name instead of embedding its value.
func secretRefExpr(name string) ir.Expr {
	return ir.Expr{Kind: ir.ExprRef, Ref: &ir.RefExpr{Context: ir.RefSecrets, Path: []string{name}}}
}

// nonSecretValue reports values that are never credentials: booleans, numbers
// and null.
func nonSecretValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > maxNonSecretBytes {
		return false
	}
	trimmed = strings.ToLower(trimmed)
	return trimmed == "true" || trimmed == "false" || trimmed == "null" || trimmed == "~" || decimalText.MatchString(trimmed)
}

// maxNonSecretBytes bounds the values nonSecretValue lower-cases (longer
// "numbers" are treated as ordinary values).
const maxNonSecretBytes = 256

// literalSecret reports whether a keyed literal must not be stored verbatim.
func literalSecret(name, value string) bool {
	secret, _ := literalSecretWork(name, value)
	return secret
}

// literalSecretWork is literalSecret with a deterministic count of the bytes
// examined by secretName, TrimSpace, nonSecretValue, isReference and
// deferredText. Normalization scaling tests include this otherwise invisible
// predicate work.
func literalSecretWork(name, value string) (bool, int) {
	work := len(name)
	if !secretName(name) {
		return false, work
	}
	trimmed := strings.TrimSpace(value)
	work += len(value)
	if trimmed == "" {
		return false, work
	}
	if len(value) > maxPredicateBytes {
		return true, work
	}
	work += 2 * len(value)
	if nonSecretValue(value) {
		return false, work
	}
	work += 2 * len(value)
	if isReference(value) {
		return false, work
	}
	deferred, deferredWork := deferredTextWork(value)
	return !deferred, work + deferredWork
}

// literalValue reports whether a value is a literal rather than empty, a
// boolean/number, a reference or deferred text. A value longer than
// maxPredicateBytes skips those tests (it is a literal unless blank), so the
// predicates never run over unbounded input.
func literalValue(value string, deferred func(string) bool) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	if len(value) > maxPredicateBytes {
		return true
	}
	return !nonSecretValue(value) && !isReference(value) && !deferred(value)
}

// scrubRetainedValue recursively removes keyed secret literals and free-form
// credential patterns from provider-extension and raw values.
func scrubRetainedValue(value any, key string) (any, bool) {
	switch value := value.(type) {
	case string:
		if literalSecret(key, value) {
			return redact.Replacement(), true
		}
		clean := safeText(value)
		return clean, clean != value
	case []any:
		out := make([]any, 0, len(value))
		removed := false
		for _, item := range value {
			clean, changed := scrubRetainedValue(item, key)
			out = append(out, clean)
			removed = removed || changed
		}
		return out, removed
	case ir.OrderedMap[any]:
		var out ir.OrderedMapBuilder[any]
		removed := false
		for _, entry := range value {
			clean, changed := scrubRetainedValue(entry.Value, entry.Key)
			out.Set(entry.Key, clean)
			removed = removed || changed
		}
		return out.Build(), removed
	case map[string]any:
		out := make(map[string]any, len(value))
		removed := false
		for itemKey, item := range value { //trasloco:unordered-ok result is a map and removed is an order-independent OR
			clean, changed := scrubRetainedValue(item, itemKey)
			out[itemKey] = clean
			removed = removed || changed
		}
		return out, removed
	default:
		return value, false
	}
}

func (a *acc) scrubRetained(construct string, source diag.Source, value any) any {
	clean, removed := scrubRetainedValue(value, "")
	if removed {
		a.lose(codeExt, construct, "secretMaterial", source, "secret material in retained Azure data was replaced by the redaction placeholder")
		a.needsManual()
	}
	return clean
}
