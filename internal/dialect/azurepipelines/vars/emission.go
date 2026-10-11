package vars

import (
	"strings"
	"unicode"

	"github.com/HarnageaGabriel/trasloco/internal/dialect/azurepipelines/expr"
)

// provableEmissions returns the offsets of the "##vso[task.setvariable "
// markers that the script provably emits. Provability is a minimal allowlist
// (see docs/spec/variable-precedence.md): a marker is proven only when every
// line of the script up to and including its emission line is one of
//
//   - bash: a blank line; a "#" comment not ending in a backslash; the exact
//     lowercase command echo with one quoted ASCII argument;
//   - pwsh: a blank line; a "#" comment other than "#requires"; Write-Host or
//     Write-Output with one quoted ASCII argument;
//   - cmd: a blank line; a REM comment not ending in "^"; "@echo off"; echo
//     with one unquoted ASCII argument free of % ! ^ & | < > and ".
//
// A quoted argument holds no quote character of either kind, no backtick and no
// "$". Macros are never expanded here: a "$(" anywhere on a line (comments
// included) ends provability for that line and every later line. A line longer
// than 8191 bytes ends provability on cmd, which truncates it and aborts.
//
// splitMarker reports a line that forms a logging-command prefix only once a
// "$(...)" macro is removed; the step is then warned about.
func provableEmissions(text string, shell shellKind) (proven map[int]bool, splitMarker bool) {
	proven = map[int]bool{} //trasloco:unordered-ok membership lookups only
	provable := shell != shellUnknown
	offset := 0
	for _, raw := range strings.SplitAfter(text, "\n") {
		lineStart := offset
		offset += len(raw)
		hasLF := strings.HasSuffix(raw, "\n")
		line := strings.TrimSuffix(raw, "\n")
		if hasLF && strings.HasSuffix(line, "\r") {
			line = line[:len(line)-1] // the CR of a CRLF line ending
		}
		if strings.Contains(line, "$(") {
			provable = false
			if !strings.Contains(asciiLower(line), setVariablePrefix) && strings.Contains(asciiLower(stripMacroRefs(line)), setVariablePrefix) {
				splitMarker = true
			}
		}
		if hasControl(line) || shell == shellCmd && len(line) > maxCmdLine {
			provable = false
		}
		if !provable {
			continue
		}
		trimmed := trimASCII(line)
		if trimmed == "" || isCommentLine(trimmed, shell) || shell == shellCmd && isEchoOff(trimmed) {
			continue
		}
		marker, emits := simpleEmission(line, 0, shell)
		if !emits {
			provable = false
			continue
		}
		if marker >= 0 {
			proven[lineStart+marker] = true
		}
	}
	return proven, splitMarker
}

// maxCmdLine is the longest command line cmd.exe runs; a longer line is
// truncated and aborted.
const maxCmdLine = 8191

// isEchoOff reports "@echo off" split on ASCII space and tab only.
func isEchoOff(trimmed string) bool {
	fields := asciiFields(trimmed)
	return len(fields) == 2 && strings.EqualFold(fields[0], "@echo") && strings.EqualFold(fields[1], "off")
}

// asciiFields splits on ASCII space and tab only (never on Unicode spaces).
func asciiFields(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' })
}

// macroWarning reports whether a script holds a "$(name)" macro whose value is
// unknown, secret, overflowed, or known to contain a logging-command marker.
// Such a value could inject logging commands that no static analysis sees.
func macroWarning(text string, expander *macroExpander) bool {
	if !strings.Contains(text, "$(") {
		return false
	}
	macros, _ := expr.ScanMacros(text)
	seen := map[string]bool{} //trasloco:unordered-ok membership only
	for _, m := range macros {
		literal := text[m.Span.Start:m.Span.End]
		if seen[literal] {
			continue
		}
		seen[literal] = true
		result := expander.expandText(literal)
		if result.Masked || overflowed(result.Diagnostics) || result.Text == literal || hasMacroRef(result.Text) || strings.Contains(asciiLower(result.Text), "##vso[") {
			return true
		}
	}
	return false
}

// hasControl rejects controls inside a physical line. A terminating CRLF is
// removed before this check; tab is the sole control admitted by the grammar.
func hasControl(text string) bool {
	for _, r := range text {
		if r != '\t' && unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func trimASCII(text string) string { return strings.Trim(text, " \t\r\n") }

func isCommentLine(trimmed string, shell shellKind) bool {
	switch shell {
	case shellCmd:
		lower := strings.ToLower(trimmed)
		if lower == "rem" {
			return true
		}
		if !strings.HasPrefix(lower, "rem ") {
			return false
		}
		for _, c := range trimmed[len("REM "):] {
			if c != ' ' && c != '.' && c != ',' && c != '_' && c != '-' && (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
				return false
			}
		}
		return true
	case shellBash:
		return strings.HasPrefix(trimmed, "#") && !strings.HasSuffix(trimmed, "\\")
	case shellPwsh:
		return strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(strings.ToLower(trimmed), "#requires")
	}
	return false
}

// simpleEmission reports whether the line is the allowed print command with one
// literal argument. The offset is that of a task.setvariable marker starting the
// argument, or -1 when the argument is another literal.
func simpleEmission(line string, lineStart int, shell shellKind) (int, bool) {
	left := strings.TrimLeft(line, " \t")
	word, rest := left, ""
	if end := strings.IndexAny(left, " \t"); end >= 0 {
		word, rest = left[:end], left[end:]
	}
	if shell == shellCmd {
		if !strings.EqualFold(word, "echo") || len(rest) < 2 || rest[0] != ' ' {
			return 0, false
		}
		argument := rest[1:]
		return cmdEmission(argument, lineStart+len(line)-len(argument))
	}
	argument := strings.TrimLeft(rest, " \t")
	if argument == "" {
		return 0, false
	}
	switch shell {
	case shellBash:
		if word != "echo" {
			return 0, false
		}
		if bareWord(argument) {
			return -1, true
		}
	case shellPwsh:
		if lower := strings.ToLower(word); lower != "write-host" && lower != "write-output" {
			return 0, false
		}
	default:
		return 0, false
	}
	return quotedArgument(argument, lineStart+len(line)-len(argument), shell)
}

// bareWord reports one unquoted word of [A-Za-z0-9._/:=-].
func bareWord(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '.' && c != '_' && c != '/' && c != ':' && c != '=' && c != '-' && (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// plainASCII reports printable ASCII only: no control characters, no
// non-ASCII (smart quotes included).
func plainASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < 0x20 || text[i] > 0x7e {
			return false
		}
	}
	return true
}

// quotedArgument accepts exactly one quoted string: the first and the last
// character are the same quote and the body contains no quote of either kind.
func quotedArgument(argument string, position int, shell shellKind) (int, bool) {
	if len(argument) < 2 {
		return 0, false
	}
	quote := argument[0]
	if quote != '\'' && quote != '"' || argument[len(argument)-1] != quote {
		return 0, false
	}
	body := argument[1 : len(argument)-1]
	if !plainASCII(body) || strings.ContainsAny(body, "'\"`") {
		return 0, false
	}
	if shell == shellBash && strings.ContainsAny(body, "\\!") {
		return 0, false
	}
	if strings.Contains(body, "$") {
		return 0, false
	}
	if strings.HasPrefix(asciiLower(body), setVariablePrefix) {
		return position + 1, true
	}
	return -1, true
}

// stripMacroRefs removes $(name) references.
func stripMacroRefs(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '$' && i+1 < len(text) && text[i+1] == '(' {
			j := i + 2
			for j < len(text) && (text[j] == '_' || text[j] == '.' || text[j] == '-' || text[j] >= '0' && text[j] <= '9' || text[j] >= 'A' && text[j] <= 'Z' || text[j] >= 'a' && text[j] <= 'z') {
				j++
			}
			if j > i+2 && j < len(text) && text[j] == ')' {
				i = j
				continue
			}
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// hasMacroRef reports an Azure $(name) reference in text.
func hasMacroRef(text string) bool { return stripMacroRefs(text) != text }

// cmdEmission accepts the rest of an unquoted cmd echo line. cmd prints it
// verbatim, so nothing in it may be special to cmd.
func cmdEmission(argument string, position int) (int, bool) {
	if argument == "" || argument[0] == ' ' || argument[0] == '\t' || !plainASCII(argument) {
		return 0, false
	}
	switch strings.ToLower(trimASCII(argument)) {
	case "on", "off":
		return 0, false
	}
	if strings.Contains(argument, "$(") || strings.ContainsAny(argument, "%!^&|<>\"") {
		return 0, false
	}
	if strings.HasPrefix(asciiLower(argument), setVariablePrefix) {
		return position, true
	}
	return -1, true
}
