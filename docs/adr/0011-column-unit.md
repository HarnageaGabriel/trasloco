# ADR 0011: Column unit

Status: Accepted — 2026-10-07. Clarifies ADR 0003.

## Context

YAML nodes used Unicode code-point columns while scalar mappings and invalid
UTF-8 diagnostics counted bytes. Non-ASCII input therefore produced inconsistent
locations and incorrect editor highlights.

## Decision

- `diag.Source` lines and columns are one-based. Columns count Unicode code
  points, not UTF-8 bytes, UTF-16 code units, grapheme clusters or display cells.
  A tab counts as one code point. CRLF is one line break.
- `Offset` and `EndOffset` remain zero-based byte offsets in the original file,
  including any initial UTF-8 BOM. The BOM does not contribute a column.
- End positions are exclusive. ScalarMapper inputs are byte offsets in the
  decoded scalar; its results use the same source-coordinate rules.
- Parsed documents retain a line-start index so repeated mappings do not scan
  the entire file. Patches and scalar mappings share lexical scalar ranges.
- Block-scalar byte ranges include their final newline and trailing blank
  lines. Replacements must supply the line endings needed before the next node.

## Consequences

All diagnostic consumers can interpret one column unit. Editors requiring
UTF-16 or display-cell columns must convert at their boundary. Byte patches
remain independent of character width.
