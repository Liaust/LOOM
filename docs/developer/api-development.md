---
title: "API Development"
description: "How to add and change LOOM daemon API handlers, local-client wrappers, envelopes, and endpoint tests."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - api
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/localclient/client.go"
  - "internal/response"
  - "internal/errors"
related:
  - "[[API Reference]]"
  - "[[CLI Development]]"
  - "[[Database Schemas]]"
  - "[[Testing]]"
aliases:
  - "Developing API Endpoints"
---
# API Development

## What This Page Covers

This page explains how LOOM daemon API routes are added and changed. Most users
reach these routes through the CLI or portal, but the route contract still
matters because it is the boundary between the daemon, local client, node
agent, and future integrations.

## Route Registration

Routes are registered in `internal/httpapi/server.go`:

```go
mux.HandleFunc("/v1/health", s.handleHealth)
```

Handlers are methods on `httpapi.Server` and use services from
`httpapi.Services`. If the feature needs a new domain service, wire it in
`internal/loomdapp/root.go` before exposing the handler.

## Endpoint Pattern

A normal endpoint change should add or update:

1. domain input/result types in `internal/<domain>`;
2. domain service method;
3. HTTP handler in `internal/httpapi`;
4. local-client method in `internal/localclient`;
5. CLI command or portal action if user-facing;
6. route/local-client/domain tests;
7. docs and API audit rows when documentation is in scope.

## Response Envelopes

Use LOOM response envelopes for normal JSON responses. Successful responses
should have consistent `ok`, `data`, and `meta` semantics. Error responses
should carry specific codes, domains, and targets.

Prefer domain-specific error codes over generic failures. If the server knows a
request failed because a ref was missing, an enum was unsupported, or a config
precondition was absent, expose that clearly.

## Request Context

Handlers should preserve:

- correlation id;
- actor and node context where relevant;
- idempotency key for retryable creates/mutations;
- redaction rules for secrets;
- authorization and policy checks before sensitive actions.

The API should not trust CLI-only validation for safety. Server-side validation
is the durable boundary.

## Local Client

`internal/localclient.Client` is the supported client for CLI/portal calls. It
uses the daemon Unix socket by default and can also be constructed with an HTTP
base URL.

Do not duplicate route construction across CLI files. Add a local-client method
for new routes so command code stays readable and testable.

## Tests

For handler changes:

```bash
go test ./internal/httpapi ./internal/localclient ./internal/<domain>
```

For a new command path over the API:

```bash
go test ./internal/loomcli ./internal/localclient ./internal/httpapi ./internal/<domain>
go run ./cmd/loom --json <safe-command>
```

If the endpoint is mutating, test with a local fixture, dry-run path, or
handler-level test instead of a production-like live mutation.

## Route Documentation

When documenting an endpoint, include:

- method and path;
- whether it is read-only, dry-run, fixture-mutating, or operator-mutating;
- request body or query parameters;
- response shape;
- authorization or actor assumptions;
- idempotency behavior;
- safe example or source-only caveat.

Do not document raw HTTP request bodies from memory. Inspect route handlers and
local-client method types first.

## Related Docs

- [[API Reference]]
- [[API Route Index]]
- [[Storage Box And File Transfers API]]
- [[CLI Development]]
- [[Testing]]
