// Package catalogschema embeds the JSON Schema of trasloco catalog files.
package catalogschema

import _ "embed" // required for go:embed

// V1 is the JSON Schema (draft 2020-12) of version 1 catalog files.
//
//go:embed v1.schema.json
var V1 []byte
