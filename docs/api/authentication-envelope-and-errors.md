---
title: "Authentication Envelope And Errors"
description: "Reference for LOOM API request context, response envelopes, correlation ids, idempotency, and structured errors."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/localclient/client.go"
  - "internal/response"
  - "internal/errors"
related:
  - "[[API Reference]]"
  - "[[API Route Index]]"
  - "[[Security Authorization And Policy]]"
  - "[[API Development]]"
aliases:
  - "Response Envelopes"
  - "API Errors"
---
# Authentication Envelope And Errors

## What This Page Covers

This page explains the common API shape used by LOOM's daemon routes and local
client. LOOM's public user entry point is usually the CLI or portal, but those
surfaces still depend on predictable API envelopes and errors.

## Transport

The local CLI normally talks to `loomd` through a Unix socket using
`internal/localclient.Client`. The client sets a synthetic base URL of
`http://loom` for socket-backed requests.

HTTP-backed clients can be created with `localclient.NewHTTP` when a caller
explicitly needs a private HTTP base URL. Do not assume an endpoint is public
internet-facing just because it is implemented as HTTP.

## Request Context

Handlers should preserve and validate:

- correlation id;
- actor identity when the route is actor-scoped;
- origin and target node context when relevant;
- idempotency key for retryable creates or mutations;
- authorization and policy decisions for sensitive actions;
- redaction boundaries for credentials and secret material.

Server-side validation is required. CLI validation helps users, but it is not
the durable security boundary.

## Success Envelope

Most daemon routes return:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_example"
  }
}
```

The exact `data` shape belongs to the domain endpoint. API docs should describe
the result shape when they give a concrete endpoint example.

## Error Envelope

Errors should use:

```json
{
  "ok": false,
  "error": {
    "code": "domain.specific_code",
    "summary": "Human-readable summary.",
    "domain": "domain",
    "target": "target-ref"
  },
  "meta": {
    "correlation_id": "corr_example"
  }
}
```

Good errors identify the precondition or failed target. Avoid routing known
states through a generic runtime failure.

## Idempotency

Create, enqueue, route, capability, realtime, and other retryable mutation
paths should preserve idempotency behavior where supported. The caller should
provide an idempotency key when retrying a request could otherwise duplicate
records or actions.

## Authentication And Authorization

LOOM authorization is actor, node, scope, provider, and capability aware.
Sensitive routes should not rely on "local caller" trust. They should run
through policy, approval, grant, credential, or node-auth checks appropriate to
the route.

See [[Security Authorization And Policy]] for the concept model.

## Documentation Rule

When adding an API doc example:

- inspect the handler and local-client method;
- verify the route or use source-only status when live mutation is unsafe;
- record the audit row in the active API endpoint audit;
- include the safety class;
- note whether the example is raw HTTP, local-client behavior, or CLI-backed
  verification.

## Related Docs

- [[API Reference]]
- [[API Route Index]]
- [[API Development]]
- [[Security Authorization And Policy]]
- [[Command Safety]]
