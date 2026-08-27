// Package contracts exposes the canonical public JSON Schema.
package contracts

import _ "embed"

// AgentWorkflowV1 is the source of truth for public v1 wire types.
//
//go:embed agent-workflow.v1.schema.json
var AgentWorkflowV1 []byte
