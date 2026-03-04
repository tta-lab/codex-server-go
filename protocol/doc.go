// Package protocol provides typed Go bindings for the Codex app-server
// WebSocket JSON-RPC protocol.
//
// All types in *_gen.go files are generated from the JSON Schema at
// schema/codex_app_server_protocol.schemas.json. Do not edit them by hand.
//
//go:generate python3 ../cmd/codegen/main.py
package protocol
