---
title: "Capabilities Policy Security And Routes API"
description: "API reference for providers, capabilities, runtime bindings, routes, capability calls, policy decisions, approvals, grants, actors, and security audit records."
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
  - "internal/loomcli/capabilities.go"
  - "internal/loomcli/routing.go"
  - "internal/loomcli/policy.go"
  - "internal/loomcli/approval_grant.go"
related:
  - "[[API Reference]]"
  - "[[Capabilities Security And Policy CLI Reference]]"
  - "[[Nodes Providers Capabilities]]"
  - "[[Security Authorization And Policy]]"
  - "[[Capability Addresses]]"
aliases:
  - "Capabilities API"
  - "Policy API"
  - "Routes API"
---
# Capabilities Policy Security And Routes API

## Envelope And Correlation

These endpoints use the normal LOOM response envelope:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_..."
  }
}
```

Errors use the same envelope with `ok=false`, `error.code`, `error.summary`,
`error.domain`, `error.target`, and `meta.correlation_id`. Preserve the
correlation id when debugging policy, routing, or capability-call failures.

## Providers

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/providers` | GET | read-only | `loom providers list` |
| `/v1/providers/{ref}` | GET | read-only | `loom provider inspect` |
| `/v1/providers/{ref}/health` | GET | read-only | `loom provider health` |

Provider list filters include `limit`, `node`, `scope`, `project`, `type`,
`status`, `health`, and `require_active_endpoint`.

Slice 11 verified the local client through live CLI calls. Provider list
returned five rows with provider address, status, health, availability, node,
scope, and metadata fields. Provider inspect returned provider, endpoints,
health, and usage document sections.

## Provider Advertisements

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/provider-advertisements` | GET | read-only | `loom provider-advertisements list` |
| `/v1/provider-advertisements/{ref}` | GET | read-only | `loom provider-advertisement inspect` |
| `/v1/provider-advertisements/{ref}/approve` | POST | operator-mutating | `loom provider-advertisement approve` |
| `/v1/provider-advertisements/{ref}/reject` | POST | operator-mutating | `loom provider-advertisement reject` |

Advertisement list filters include `limit`, `node`, `provider`, and `status`.
Review actions accept a reason payload from the CLI. Approval can activate
provider endpoint rows, so clients should never auto-approve advertisements
without operator review.

## Capabilities

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/capabilities` | GET | read-only | `loom capabilities list` |
| `/v1/capabilities/search` | POST | read-only search | `loom capabilities search` |
| `/v1/capabilities/{ref}` | GET | read-only | `loom capability inspect` |
| `/v1/capabilities/{ref}/usage-docs` | GET | read-only | `loom capability usage-docs` |

Capability list filters include `limit`, `provider`, `class`, `node`, `scope`,
`project`, `form`, `status`, `risk`, and `authorization_level`.

Capability search accepts a query plus filters. It returns ranked candidates
with match reasons, score, provider address, risk, form, and authorization
level.

Slice 11 verified list, search, inspect, and usage-doc retrieval.

## Runtime Bindings

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/capability-runtime-bindings` | GET | read-only | `loom capability runtime-bindings list` |
| `/v1/capability-runtime-bindings` | POST | operator-mutating | `loom capability runtime-binding register` |
| `/v1/capability-runtime-bindings/{ref}` | GET | read-only | `loom capability runtime-binding inspect` |
| `/v1/capability-runtime-bindings/{ref}/validate` | GET | read-only validation | `loom capability runtime-binding validate` |
| `/v1/capability-runtime-bindings/{ref}/test` | POST | diagnostic execution | `loom capability runtime-binding test` |

Runtime binding list filters include `limit`, `capability`,
`endpoint_version`, `provider`, `runtime_kind`, and `status`.

`test` executes a runtime binding directly and is not a casual read-only
endpoint. It should be used only with reviewed input and expected side effects.

## Capability Calls And Routes

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/capability-calls` | POST | operator-mutating or dry-run planning | `loom capability call` |
| `/v1/capability-calls` | GET | read-only | `loom capability-calls list` |
| `/v1/capability-calls/{ref}` | GET | read-only | `loom capability-call inspect` |
| `/v1/routes` | GET | read-only | `loom routes list` |
| `/v1/routes/{ref}` | GET | read-only | `loom route inspect` |

Capability-call input contains the target capability, actor, origin node,
scope, input JSON, approval request flags, dry-run flag, and metadata.

Route and call filters include status, actor, origin node, target node,
provider, capability, policy decision, approval, grant, job, correlation, and
idempotency where supported by the local client.

Slice 11 verified:

- route list and inspect;
- capability-call list and inspect;
- a dry-run capability call through the CLI/local-client path.

The dry-run path returned a completed outcome without dispatching the provider
adapter.

## Policy

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/policy/explain` | POST | policy-evaluating, sometimes mutating | `loom policy explain` |
| `/v1/policy/decisions` | GET | read-only | `loom policy decisions list` |
| `/v1/policy/decisions/{ref}` | GET | read-only | `loom policy decision inspect` |

`/v1/policy/explain` can create or reuse an approval request when the request
sets approval flags. Treat it as mutating when `create_approval_request` or the
CLI's `--request-approval` equivalent is used.

Decision list filters include actor, origin node, target node, operation,
decision, capability, approval, grant, and limit.

## Approvals

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/approvals` | GET | read-only | `loom approvals list` |
| `/v1/approvals/{ref}` | GET | read-only | `loom approval inspect` |
| `/v1/approvals/{ref}/decide` | POST | sensitive operator-mutating | `loom approval decide` |

Approvals can move to approved, denied, expired, cancelled, or superseded
states depending on policy lifecycle. A positive decision can issue a bounded
grant. Clients must include actor and reason metadata when surfacing approval
decisions to humans.

## Grants

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/grants` | GET | read-only | `loom grants list` |
| `/v1/grants/{ref}` | GET | read-only | `loom grant inspect` |
| `/v1/grants/{ref}/revoke` | POST | sensitive operator-mutating | `loom grant revoke` |

Grant list filters include status, grant type, granted-to actor, granted-by
actor, approval, target node, capability, and limit.

Revocation should be explicit and audited. Use a reason and actor reference
through the CLI path.

## Actors

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/actors/{ref}` | GET | read-only | `loom actor inspect` |

The current HTTP API exposes actor lookup by ref. It does not expose a general
actor list route through the CLI/API docs surface.

Slice 11 verified actor inspection with a live actor id from policy history.

## Security Audit

The CLI security audit path reads `/v1/events` through the events client and
filters policy, approval, and grant event families when no exact type is
provided.

Use:

```bash
loom security audit --limit 20
loom security audit --type policy.decision.created
```

The API event route itself is broader than security. Security docs should not
present it as a dedicated security-only endpoint.

## Error Notes

The CLI now validates two common user-correctable inputs before sending a
request:

- `agent worklog list` and `agent worklog write` require `--work-context` and
  return `agents.work_context_required` locally when it is missing.
- `realtime topic create` validates non-empty `--retention` and
  `--delivery-class` values locally before calling the realtime API.

## Related Docs

- [[Capabilities Security And Policy CLI Reference]]
- [[Security Authorization And Policy]]
- [[Nodes Providers Capabilities]]
- [[Capability Addresses]]
- [[Event Types]]
