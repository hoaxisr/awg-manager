// Package main — OpenAPI metadata for swag. Regenerate YAML before commit when API comments change:
//
//	go generate ./cmd/awg-manager
//
// In dev, Swagger UI: open the Svelte app at /dev/api-docs (Vite proxies /api to the daemon).
//
// Requires Go ≥ module version (see go.mod). Output: internal/openapi/swagger.yaml
//
//	@title						AWG Manager API
//	@version					1.0
//	@description				HTTP API for the AWG Manager daemon (tunnels, routing, system).
//	@BasePath					/api
//
//	@securityDefinitions.apikey	CookieAuth
//	@in							cookie
//	@name						awg_session
//	@description				Session cookie set by POST /auth/login. Omit for public routes.
package main

//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.4 init -g docs.go -d .,../../internal/api -o ../../internal/openapi --parseInternal --ot yaml
