// Package openapi embeds the JSON API contract. It is served at
// /api/openapi.yaml and is the source of truth for /api/* routes.
package openapi

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
