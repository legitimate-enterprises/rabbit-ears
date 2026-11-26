// Package api holds the OpenAPI spec (openapi.yaml, the source of truth) and
// the server types and interfaces generated from it.
package api

//go:generate go tool oapi-codegen -config oapi-codegen.yaml openapi.yaml
