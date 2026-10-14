package normalize

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/HarnageaGabriel/trasloco/internal/diag"
	"github.com/HarnageaGabriel/trasloco/internal/ir"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

// keyEquals finds the start of a KEY= pair such as `--env DB_PASSWORD=value`.
var keyEquals = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.\-]*)=`)

// scrubPairs replaces the value of every secret-classified KEY=value pair in
// command-line text. A value is delimited by whitespace and honours quotes
// (KEY="a b", "KEY=a b"); it is never cut at , ] } or ; because those can be
// part of a password. The value of a non-secret key is scanned again so that
// `--env=DB_PASSWORD=x` is caught.
func scrubPairs(text string) string {
	out, _ := scrubPairsCount(text)
	return out
}

// maxScanBytes bounds the free-form leaf the pair scanners accept. A larger
// leaf is replaced as a whole by the placeholder (needs-manual).
const maxScanBytes = 1 << 20

// wsCache remembers that [from, to) holds no whitespace so repeated value
// searches inside one token stay linear.
type wsCache struct {
	from, to int
	ok       bool
}

func (w *wsCache) nextSpace(view string, from int) (end, steps int) {
	if w.ok && from >= w.from && from <= w.to {
		return w.to, 1
	}
	end = from
	for end < len(view) && !strings.ContainsRune(" \t\r\n", rune(view[end])) {
		end++
	}
	w.from, w.to, w.ok = from, end, true
	return end, end - from + 1
}

// scrubPairsCount is the iterative worklist behind scrubPairs: one left-to-right
// pass with an explicit stack of quote limits instead of recursion, so the work
// is linear in the input even for `a=a=a=...=KEY=value`. steps counts the bytes
// the lexer examined (the test asserts a linear bound on it).
func scrubPairsCount(text string) (string, int) {
	out, steps, _ := scrubPairsWork(text)
	return out, steps
}

// workFactor and workSlack set the per-leaf work budget of the pair scanner:
// workFactor*len(text)+workSlack steps. Every byte examined by the lexer, the
// whitespace search and the value predicates is counted. When the budget is
// exceeded the whole leaf is replaced by the redaction placeholder.
const (
	workFactor = 16
	workSlack  = 256
)

// deferCache remembers the last "$(" inside the value region [from, end) of the
// current token, so the deferred-text predicate costs O(1) for every further
// secret key inside the same token instead of a rescan of the remaining token.
type deferCache struct {
	from, end, last int
	ok              bool
}

// deferred is deferredText for text[from:end] using the cache; cost is the
// number of bytes examined.
func (d *deferCache) deferred(text string, from, end int) (isDeferred bool, cost int) {
	trimmed := strings.TrimSpace(text[from:end])
	if strings.HasPrefix(trimmed, "${{") || strings.HasPrefix(trimmed, "$[") {
		return true, 1
	}
	cost = 1
	if !d.ok || d.end != end || from < d.from {
		d.from, d.end, d.ok = from, end, true
		d.last = strings.LastIndex(text[from:end], "$(")
		if d.last >= 0 {
			d.last += from
		}
		cost += end - from
	}
	return d.last >= from, cost
}

// scrubPairsWork is scrubPairsCount that also reports a budget overflow, in
// which case the result is the redaction placeholder for the whole text.
func scrubPairsWork(text string) (result string, steps int, overflow bool) {
	if !strings.Contains(text, "=") {
		return text, 0, false
	}
	budget := workFactor*len(text) + workSlack
	var out strings.Builder
	limits := []int{len(text)}
	var cache wsCache
	var dc deferCache
	pos, last := 0, 0
	for {
		if steps > budget {
			return redact.Replacement(), steps, true
		}
		limit := limits[len(limits)-1]
		if pos >= limit {
			if len(limits) == 1 {
				break
			}
			limits, cache.ok = limits[:len(limits)-1], false
			continue
		}
		loc := keyEquals.FindStringSubmatchIndex(text[pos:limit])
		if loc == nil {
			steps += limit - pos
			pos = limit
			continue
		}
		steps += loc[1] + 1
		view := text[:limit]
		keyStart, keyEnd, valueStart := pos+loc[2], pos+loc[3], pos+loc[1]
		key := text[keyStart:keyEnd]
		secret := secretName(key)
		end, skip, quoted, scanned := pairValue(view, keyStart, valueStart, secret, &cache)
		steps += scanned
		if !quoted && end < 0 {
			pos = valueStart // non-secret unquoted value: keep scanning inside it
			continue
		}
		isLiteral := false
		if secret {
			// The predicates run once per key on a bounded value; the
			// "$(" search is shared by every key of the same token.
			value := view[valueStart+skip : end]
			steps += 1 + len(value) - len(strings.TrimSpace(value))
			isLiteral = literalValue(value, func(string) bool {
				isDeferred, cost := dc.deferred(text, valueStart+skip, end)
				steps += cost
				return isDeferred
			})
		}
		if isLiteral {
			out.WriteString(text[last : valueStart+skip])
			out.WriteString(redact.Replacement())
			last, pos = end, end
			continue
		}
		pos = valueStart + skip
		if quoted && end < limit {
			limits, cache.ok = append(limits, end), false
		}
	}
	if last == 0 {
		return text, steps, false
	}
	out.WriteString(text[last:])
	return out.String(), steps, false
}

// pairValue locates the value of the pair whose `=` ends at valueStart. end is
// -1 when the value is an unquoted one of a non-secret key (its end is never
// needed: scanning continues inside it). skip is the number of opening-quote
// bytes before the value text.
func pairValue(view string, keyStart, valueStart int, secret bool, cache *wsCache) (end, skip int, quoted bool, steps int) {
	if valueStart < len(view) && (view[valueStart] == '"' || view[valueStart] == '\'') {
		if closeIndex := strings.IndexByte(view[valueStart+1:], view[valueStart]); closeIndex >= 0 {
			return valueStart + 1 + closeIndex, 1, true, closeIndex + 1
		}
		return len(view), 1, true, len(view) - valueStart
	}
	if keyStart > 0 && (view[keyStart-1] == '"' || view[keyStart-1] == '\'') {
		// "KEY=a b": the value runs to the enclosing quote.
		if closeIndex := strings.IndexByte(view[valueStart:], view[keyStart-1]); closeIndex >= 0 {
			return valueStart + closeIndex, 0, true, closeIndex + 1
		}
	}
	if !secret {
		return -1, 0, false, 0
	}
	end, steps = cache.nextSpace(view, valueStart)
	return end, 0, false, steps
}

// flagName finds `--flag ` / `-Flag ` followed by whitespace.
var flagName = regexp.MustCompile(`(?:^|[ \t\r\n])(--?[A-Za-z][A-Za-z0-9_\-]*)[ \t]+`)

// scrubFlags replaces the value of whitespace-separated command-line flags with
// secret-classified names (`--client-secret X`, `-Password X`). It is a linear
// single pass. Used for task inputs and container options, never for scripts.
func scrubFlags(text string) string {
	if !strings.Contains(text, "-") {
		return text
	}
	var out strings.Builder
	pos, last := 0, 0
	for pos < len(text) {
		loc := flagName.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		name := text[pos+loc[2] : pos+loc[3]]
		valueStart := pos + loc[1]
		pos = valueStart - 1
		if valueStart >= len(text) || !secretName(strings.TrimLeft(name, "-")) {
			continue
		}
		end, skip := valueStart, 0
		if q := text[valueStart]; q == '"' || q == '\'' {
			skip = 1
			end = len(text)
			if closeIndex := strings.IndexByte(text[valueStart+1:], q); closeIndex >= 0 {
				end = valueStart + 1 + closeIndex
			}
		} else {
			for end < len(text) && !strings.ContainsRune(" \t\r\n", rune(text[end])) {
				end++
			}
		}
		value := text[valueStart+skip : end]
		if strings.HasPrefix(value, "-") || !literalSecret(strings.TrimLeft(name, "-"), value) {
			continue
		}
		out.WriteString(text[last : valueStart+skip])
		out.WriteString(redact.Replacement())
		last, pos = end, end
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

// demandEquals matches an Azure pool demand `NAME -equals VALUE`.
var demandEquals = regexp.MustCompile(`^(\s*[A-Za-z_][A-Za-z0-9_.\-]*\s+-equals\s+)(\S[^\r\n]*)$`)

// scrubDemand replaces the value of a `NAME -equals VALUE` demand when NAME is
// secret-classified.
func scrubDemand(text string) string {
	if !strings.Contains(text, "-equals") {
		return text
	}
	loc := demandEquals.FindStringSubmatchIndex(text)
	if loc == nil {
		return text
	}
	name := strings.Fields(text[loc[2]:loc[3]])[0]
	if !literalSecret(name, text[loc[4]:loc[5]]) {
		return text
	}
	return text[:loc[4]] + redact.Replacement()
}

var (
	metadataType   = reflect.TypeOf(ir.Metadata{})
	rawMessageType = reflect.TypeOf(json.RawMessage{})
	originPtrType  = reflect.TypeOf((*ir.Origin)(nil))
	stringType     = reflect.TypeOf("")
	literalType    = reflect.TypeOf(ir.Literal{})
	inputType      = reflect.TypeOf(ir.Input{})
)

// optionsField reports names whose string content is a command-line fragment
// (container options and arguments) where KEY=value pairs are scrubbed.
func optionsField(name string) bool {
	switch strings.ToLower(name) {
	case "options", "args", "arguments":
		return true
	}
	return false
}

// backstop walks every string leaf of the IR after normalization, applies the
// credential scrubber and, for container options and arguments, the
// secret-classified KEY=value scrubber. Any change is recorded as a
// needs-manual loss on the nearest node with metadata. It exists so that no
// individual conversion site has to be perfect.
type backstop struct {
	stack  []*ir.Metadata
	hit    []*ir.Metadata
	hitSet map[*ir.Metadata]bool
	raise  map[*ir.Metadata]bool
	// flags enables whitespace CLI flag scanning (task inputs, options).
	flags bool
	// shell is the resolved shell of the RunStep being walked ("" elsewhere).
	shell string
	// work counts the bytes examined by the leaf scanners (tests assert it).
	work int
	// ts replaces the secret literals declared at the pipeline root in every
	// string leaf.
	ts *taintSet
	// taintHits counts leaves changed by ts (ID deduplication re-runs on change).
	taintHits int
	// leafChanges counts string leaves rewritten for any reason (ID
	// deduplication re-runs when a slice's leaves changed).
	leafChanges int
	// noTaint suspends ts for the structural leaf being walked.
	noTaint bool
	// scanRefs is true while walking a free-text leaf (a script, an opaque
	// value or a string literal) whose references to renamed IDs (#12) and to
	// short/numeric/boolean-like secret variables (#13) are recorded.
	scanRefs bool
	// renameNeedles and shortNames are the free-text spellings to search for.
	renameNeedles []nameNeedle
	shortNames    []nameNeedle
	// refSeen de-duplicates a (node, kind) hit; refOrder keeps the walk order.
	refSeen  map[*ir.Metadata]map[refKind]bool
	refOrder []refHit
}

func (c *conv) finalScrub(p *ir.Pipeline) {
	b := &backstop{hitSet: map[*ir.Metadata]bool{}, raise: map[*ir.Metadata]bool{}, ts: c.ts, refSeen: map[*ir.Metadata]map[refKind]bool{}}
	ids := captureIDPlan(p)
	b.renameNeedles, b.shortNames = c.referenceNeedles(ids)
	if c.ts != nil {
		b.work += c.ts.buildWork
	}
	// Every free-form leaf gets the KEY=value scan; script bodies are the
	// only exception (they follow the whole-text policy, see walkStruct).
	b.walk(reflect.ValueOf(p).Elem(), true)
	ids.rewrite(p)
	c.work += b.work
	for _, node := range b.hit {
		c.recordBackstop(node, p)
	}
	for _, hit := range b.refOrder {
		c.emitReferenceLoss(hit, p)
	}
	for node := range b.raise { //trasloco:unordered-ok idempotent per-node fidelity raise
		node.Fidelity = maxFidelity(node.Fidelity, diag.FidelityNeedsManual)
	}
}

// mark attributes a change to the nearest node with metadata and raises every
// enclosing node so fidelity roll-ups stay consistent.
func (b *backstop) mark() {
	if len(b.stack) == 0 {
		return
	}
	nearest := b.stack[len(b.stack)-1]
	if !b.hitSet[nearest] {
		b.hitSet[nearest] = true
		b.hit = append(b.hit, nearest)
	}
	for _, node := range b.stack {
		b.raise[node] = true
	}
}

func (b *backstop) text(value string, pairs bool) string {
	return b.textTaint(value, pairs, true)
}

// textTaint is text; taint false skips the secret-literal replacement for
// structural fields (enum-typed values).
func (b *backstop) textTaint(value string, pairs, taint bool) string {
	if value == "" || value == redact.Replacement() {
		return value
	}
	out := value
	b.work += len(value)
	if taint && !b.noTaint {
		b.work += len(value) // one replacer pass
		out = b.ts.apply(value)
	}
	if out != value {
		b.taintHits++
		b.mark()
		if out == redact.Replacement() {
			return out
		}
	}
	value = out
	if secretLoggingLiteralFor(value, b.shell) || (pairs && len(value) > maxScanBytes) {
		// Script-like text outside steps (parameter defaults, template
		// context, unknown keys) or a leaf above the scan bound: replace the
		// whole leaf, as for scripts.
		out = redact.Replacement()
	} else {
		out = safeText(out)
		if pairs {
			scrubbed, work, overflow := scrubPairsWork(out)
			b.work += work
			if overflow {
				// Work budget exceeded: replace the whole leaf (needs-manual).
				b.mark()
				return redact.Replacement()
			}
			out = scrubDemand(scrubbed)
			if b.flags {
				out = scrubFlags(out)
			}
		}
	}
	if out != value {
		b.mark()
	}
	return out
}

// rawJSON scrubs the string leaves of a raw JSON value. The document is decoded
// and re-encoded token by token (key order is preserved) so that escapes such
// as & never split a value. Unchanged documents are returned as they are.
func (b *backstop) rawJSON(raw []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var buf bytes.Buffer
	changed := false
	err := b.jsonValue(dec, &buf, &changed, jsonRoot)
	if err == nil {
		if _, extra := dec.Token(); !errors.Is(extra, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		// Not JSON: keep the prior text as a scrubbed string, never wipe it.
		text := string(raw)
		out := b.text(text, true)
		encoded, _ := json.Marshal(out)
		return encoded
	}
	if !changed {
		return raw
	}
	b.mark()
	return buf.Bytes()
}

func (b *backstop) jsonString(buf *bytes.Buffer, value string, changed *bool) {
	out := b.text(value, true)
	if out != value {
		*changed = true
	}
	encoded, _ := json.Marshal(out)
	buf.Write(encoded)
}

type jsonContext uint8

const (
	jsonNormal jsonContext = iota
	jsonRoot
	jsonLossArray
	jsonLossObject
)

func (b *backstop) jsonValue(dec *json.Decoder, buf *bytes.Buffer, changed *bool, context jsonContext) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok := tok.(type) {
	case json.Delim:
		switch tok {
		case '{':
			type member struct {
				key   string
				value []byte
			}
			var members []member
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyTok.(string)
				if !ok {
					return fmt.Errorf("unexpected JSON object key %T", keyTok)
				}
				child := jsonNormal
				if context == jsonRoot && key == "losses" {
					child = jsonLossArray
				}
				var value bytes.Buffer
				if err := b.jsonValue(dec, &value, changed, child); err != nil {
					return err
				}
				members = append(members, member{key: key, value: value.Bytes()})
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			keys := make([]string, len(members))
			for i, item := range members {
				key := item.key
				if context != jsonLossObject || !lossJSONKey(key) {
					key = b.text(key, true)
				}
				if key != item.key {
					*changed = true
				}
				keys[i] = key
			}
			if allocateUnique(keys) {
				*changed = true
			}
			buf.WriteByte('{')
			for i, item := range members {
				if i != 0 {
					buf.WriteByte(',')
				}
				encoded, _ := json.Marshal(keys[i])
				buf.Write(encoded)
				buf.WriteByte(':')
				buf.Write(item.value)
			}
			buf.WriteByte('}')
		case '[':
			buf.WriteByte('[')
			for first := true; dec.More(); first = false {
				if !first {
					buf.WriteByte(',')
				}
				child := jsonNormal
				if context == jsonLossArray {
					child = jsonLossObject
				}
				if err := b.jsonValue(dec, buf, changed, child); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			buf.WriteByte(']')
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", tok)
		}
	case string:
		b.jsonString(buf, tok, changed)
	case json.Number:
		text := tok.String()
		if b.ts.apply(text) != text {
			encoded, _ := json.Marshal(redact.Replacement())
			buf.Write(encoded)
			*changed = true
			b.taintHits++
			b.mark()
		} else {
			buf.WriteString(text)
		}
	case bool:
		buf.WriteString(strconv.FormatBool(tok))
	case nil:
		buf.WriteString("null")
	}
	return nil
}

func lossJSONKey(key string) bool {
	return key == "construct" || key == "property" || key == "detail"
}

func (b *backstop) walk(v reflect.Value, pairs bool) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			if b.scanRefs {
				b.noteReferences(v.String())
			}
			// Enum-typed (named string) fields are structural: never tainted.
			if out := b.textTaint(v.String(), pairs, v.Type() == stringType); out != v.String() {
				v.SetString(out)
				b.leafChanges++
			}
		}
	case reflect.Pointer:
		if !v.IsNil() && v.Type() != originPtrType {
			b.walk(v.Elem(), pairs)
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		inner := v.Elem()
		cp := reflect.New(inner.Type()).Elem()
		cp.Set(inner)
		b.walk(cp, pairs)
		if v.CanSet() {
			v.Set(cp)
		}
	case reflect.Struct:
		b.walkStruct(v, pairs)
	case reflect.Slice:
		if v.Type() == rawMessageType {
			if v.CanSet() && !v.IsNil() {
				v.SetBytes(b.rawJSON(v.Bytes()))
			}
			return
		}
		changes := b.leafChanges
		for i := 0; i < v.Len(); i++ {
			b.walk(v.Index(i), pairs)
		}
		// Any leaf change (declared secret or built-in credential scrubber)
		// can collapse distinct IDs.
		if b.leafChanges != changes {
			dedupeStructIDs(v)
		}
		if dedupeEntryKeys(v) {
			b.mark()
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			b.walk(v.Index(i), pairs)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		keys := v.MapKeys()
		slices.SortFunc(keys, func(x, y reflect.Value) int {
			return strings.Compare(fmt.Sprint(x.Interface()), fmt.Sprint(y.Interface()))
		})
		for _, key := range keys {
			cp := reflect.New(v.Type().Elem()).Elem()
			cp.Set(v.MapIndex(key))
			saved := b.flags
			if key.Kind() == reflect.String && optionsField(key.String()) {
				b.flags = true
			}
			b.walk(cp, pairs || b.flags)
			b.flags = saved
			v.SetMapIndex(key, cp)
		}
	}
}

// structuralString reports the plain-string IR fields that select behaviour
// (number text, deployment strategy and hook, runner OS and architecture,
// annotation level, action and raw provider tags). Replacing them with the
// placeholder would corrupt the IR, so a declared secret never taints them.
func structuralString(typeName, field string) bool {
	switch typeName {
	case "Literal":
		return field == "Number"
	case "DeploymentIntent":
		return field == "Strategy" || field == "Hook"
	case "RunnerReq":
		return field == "OS" || field == "Arch"
	case "ProtocolOperation":
		return field == "Level"
	case "ActionStep":
		return field == "Provider"
	case "Raw":
		return field == "Provider" || field == "Kind" || field == "Format"
	}
	return false
}

// normalizerConstant reports a field currently holding a value the normalizer
// itself writes (the implicit `self` checkout repository). A user-written
// repository alias is not a constant and is still tainted.
func normalizerConstant(v reflect.Value, typeName, field string) bool {
	if typeName != "CheckoutStep" || field != "Repository" {
		return false
	}
	switch v.FieldByName(field).String() {
	case "self", "none":
		return true
	}
	return false
}

func (b *backstop) walkStruct(v reflect.Value, pairs bool) {
	if v.Type() == metadataType {
		if v.CanAddr() {
			b.metadata(v.Addr().Interface().(*ir.Metadata))
		}
		return
	}
	if v.Type() == literalType && v.CanAddr() {
		// A number literal is atomic: a tracked secret matching its text (whole
		// or in part) turns the whole literal into the placeholder string.
		lit := v.Addr().Interface().(*ir.Literal)
		if lit.Type == ir.LiteralNumber && b.ts.apply(lit.Number) != lit.Number {
			*lit = ir.Literal{Type: ir.LiteralString, String: redact.Replacement()}
			b.taintHits++
			b.mark()
			return
		}
	}
	savedFlags, savedShell := b.flags, b.shell
	defer func() { b.flags, b.shell = savedFlags, savedShell }()
	if v.Type().Name() == "RunStep" {
		if shell := v.FieldByName("Shell"); shell.Kind() == reflect.String {
			b.shell = shell.String()
		}
	}
	if strings.HasPrefix(v.Type().Name(), "OrderedEntry") {
		// Task inputs and environments are OrderedMap entries: the entry key
		// (for example Docker@2 `arguments`) decides whether its value is a
		// command line.
		if key := v.FieldByName("Key"); key.Kind() == reflect.String && optionsField(key.String()) {
			pairs, b.flags = true, true
		}
	}
	pushed := false
	if f, ok := v.Type().FieldByName("Metadata"); ok && f.Type == metadataType && v.CanAddr() {
		b.stack = append(b.stack, v.FieldByIndex(f.Index).Addr().Interface().(*ir.Metadata))
		pushed = true
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" || field.Type == originPtrType {
			continue
		}
		if t.Name() == "Schedule" && field.Name == "Cron" {
			cron := v.Field(i)
			if out := b.ts.apply(cron.String()); out != cron.String() {
				// Cron is validated as five fields. Use a valid opaque schedule
				// instead of leaking the source or making the IR unserializable.
				cron.SetString("0 0 1 1 *")
				b.taintHits++
				b.mark()
				continue
			}
		}
		fieldPairs := pairs
		inner := b.flags
		switch {
		case t.Name() == "RunStep" && field.Name == "Script":
			// Script bodies follow the whole-text policy (guardScript).
			fieldPairs = false
		case optionsField(field.Name) || t.Name() == "ActionStep" && field.Name == "With":
			fieldPairs, b.flags = true, true
		}
		// A number literal's text is structural: replacing it would break the
		// decimal-string contract.
		savedNoTaint, savedScan := b.noTaint, b.scanRefs
		b.noTaint = b.noTaint || structuralString(t.Name(), field.Name) || normalizerConstant(v, t.Name(), field.Name)
		b.scanRefs = referenceField(t.Name(), field.Name)
		b.walk(v.Field(i), fieldPairs)
		b.noTaint, b.scanRefs = savedNoTaint, savedScan
		b.flags = inner
	}
	if t == inputType && v.CanAddr() {
		// A redacted number default is a string; keep the input valid.
		in := v.Addr().Interface().(*ir.Input)
		if in.Type == ir.InputNumber && in.Default != nil && in.Default.Type == ir.LiteralString {
			in.Type = ir.InputString
		}
	}
	if pushed {
		b.stack = b.stack[:len(b.stack)-1]
	}
}

// metadata scrubs the provider extensions of a node. Origin, fidelity and
// provenance carry no free text.
func (b *backstop) metadata(m *ir.Metadata) {
	for _, key := range slices.Sorted(maps.Keys(m.Extensions)) {
		m.Extensions[key] = b.rawJSON(m.Extensions[key])
	}
}

// recordBackstop appends a needs-manual loss to a node's provider extension and
// emits the matching finding.
func (c *conv) recordBackstop(node *ir.Metadata, p *ir.Pipeline) {
	src := p.Origin.Source
	if node.Origin != nil {
		src = node.Origin.Source
	}
	chain := p.Origin.Chain
	if node.Origin != nil {
		chain = node.Origin.Chain
	}
	c.addSecretLoss(node, src, chain)
}

// addSecretLoss appends a secretMaterial loss to a node's provider extension
// and emits the matching finding.
func (c *conv) addSecretLoss(node *ir.Metadata, src diag.Source, chain []diag.Source) {
	c.addNodeLoss(node, "ir", "secretMaterial", secretDetail, src, chain)
}

// addNodeLoss appends a loss to a node's provider extension and emits the
// matching finding. A node that already carries the same (construct, property)
// loss is left alone. A node without an origin uses src. The detail is scrubbed
// before it is stored, so a diagnostic never carries secret material.
func (c *conv) addNodeLoss(node *ir.Metadata, construct, property, detail string, src diag.Source, chain []diag.Source) {
	detail = c.sanitize(detail)
	if node.Origin != nil {
		src = node.Origin.Source
	}
	var bag struct {
		Values   json.RawMessage `json:"values,omitempty"`
		Losses   []loss          `json:"losses,omitempty"`
		Deferred []loss          `json:"deferred,omitempty"`
	}
	if raw, ok := node.Extensions[ExtensionKey]; ok {
		_ = json.Unmarshal(raw, &bag)
	}
	for _, existing := range bag.Losses {
		if existing.Construct == construct && existing.Property == property {
			return
		}
	}
	a := c.newAcc(src)
	a.chain = chain
	a.lose(codeExt, construct, property, src, detail)
	bag.Losses = append(bag.Losses, loss{Construct: construct, Property: property, Detail: detail})
	encoded, err := json.Marshal(bag)
	if err != nil {
		c.err = err
		return
	}
	if node.Extensions == nil {
		node.Extensions = ir.Extensions{}
	}
	node.Extensions[ExtensionKey] = encoded
}
