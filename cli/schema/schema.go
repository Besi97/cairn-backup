// Package schema embeds the published configuration contract for offline checks.
package schema

import _ "embed"

// V1 is the JSON Schema used by editors and Cairn's runtime loader.
//
//go:embed config-v1.schema.json
var V1 []byte
