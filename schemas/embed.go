// Package schemas embeds the JSON Schemas published by trasloco.
package schemas

import _ "embed" // required for go:embed

// ConfigV1 is the JSON Schema (draft 2020-12) of the version 1 configuration file.
//
//go:embed config/v1.schema.json
var ConfigV1 []byte
