#!/bin/sh
set -eu

go install \
  ./cmd/agent-workflow-codex \
  ./cmd/agent-workflow-claude-code \
  ./cmd/agent-workflow-pi \
  ./cmd/agent-workflow-openclaw \
  ./cmd/agent-workflow-hermes
