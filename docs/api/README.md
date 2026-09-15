---
title: "API Reference"
description: "Reference for LOOM HTTP and local-client API behavior."
audience:
  - developer
  - operator
tags:
  - loom
  - api
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
related:
  - "[[API Route Index]]"
  - "[[Developer Documentation]]"
  - "[[CLI Reference]]"
aliases:
  - "LOOM API"
---
# API Reference

The API docs describe the private LOOM HTTP surface and local-client behavior.
They should explain request shape, response envelope, idempotency and
correlation expectations, error shape, safety level, and the CLI or portal
workflow that normally drives the same behavior.

## Start Here

- [[Authentication Envelope And Errors]] explains how requests and responses are
  shaped.
- [[Health Status And Bootstrap API]] covers health, status, and bootstrap
  status routes.
- [[API Route Index]] lists the current route groups discovered from
  `internal/httpapi/server.go`.

## Domain References

- [[Projects Scopes And Contracts API]]
- [[Notes Knowledge And Search API]]
- [[Storage Box And File Transfers API]]
- [[Backup Cloud And Maintenance API]]
- [[Automation Jobs Scripts And Artifacts API]]
- [[Nodes Sync And Watched Roots API]]
- [[Capabilities Policy Security And Routes API]]
- [[Modules Agents And Realtime API]]

## Verification Rule

The archived v0.9.6 endpoint audit remains the original verification evidence.
If an endpoint now fails or its shape differs from the docs, capture a bounded
bugfix through `.project/` and defer the affected docs section until the runtime
fix is integrated.

## Related Docs

- [[Developer Documentation]]
- [[CLI Reference]]
- [[Portal Guide]]
- [[Reference]]
