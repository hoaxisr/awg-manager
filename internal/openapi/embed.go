package openapi

import _ "embed"

// RawSpec is the committed OpenAPI (Swagger 2) document; regenerate with:
//
//	go generate ./cmd/awg-manager
//
//go:embed swagger.yaml
var RawSpec []byte
