---
title: "Capabilities Security And Policy CLI Reference"
description: "CLI reference for inspecting providers, capabilities, routes, policy decisions, approvals, grants, actors, and agent-facing access."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - cli
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 providers --help"
  - "/tmp/loomdocs-v096 capabilities --help"
  - "/tmp/loomdocs-v096 capability call --help"
  - "/tmp/loomdocs-v096 policy --help"
  - "/tmp/loomdocs-v096 security audit --help"
  - "/tmp/loomdocs-v096 agent --help"
  - "go run ./cmd/loom --json agent worklog list --limit 5"
  - "/tmp/loomdocs-v096 --json providers list --limit 5"
  - "/tmp/loomdocs-v096 --json capability call <capability> --dry-run --input '{}'"
related:
  - "[[CLI Reference]]"
  - "[[Nodes Providers Capabilities]]"
  - "[[Security Authorization And Policy]]"
  - "[[Capabilities Policy Security And Routes API]]"
  - "[[Capability Addresses]]"
aliases:
  - "Capabilities CLI"
  - "Security CLI"
  - "Policy CLI"
---
# Capabilities Security And Policy CLI Reference

## What This Page Covers

This page covers the CLI surfaces that inspect and operate LOOM's control
plane:

- providers and provider advertisements;
- capabilities and runtime bindings;
- routed capability calls;
- policy decisions;
- approvals and grants;
- security audit events;
- actor inspection;
- agent-facing access sessions, work contexts, tools, tool calls, and worklogs.

Use this page when you need to answer "what can LOOM call?", "why was this
allowed or blocked?", or "what did an agent see and call?"

## Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom providers list`, `loom provider inspect`, `loom provider health` | read-only | Provider registry and health state. |
| `loom provider-advertisements list`, `loom provider-advertisement inspect` | read-only | Remote advertisement review state. |
| `loom provider-advertisement approve/reject` | operator-mutating | Accepts or rejects advertised provider surfaces. |
| `loom capabilities list/search`, `loom capability inspect`, `loom capability usage-docs` | read-only | Capability discovery and documentation. |
| `loom capability call --dry-run` | dry-run | Plans and authorizes without dispatching the provider adapter. It can still create audit/control records. |
| `loom capability call` without `--dry-run` | operator-mutating | Dispatches a real capability call through policy and routing. |
| `loom capability runtime-bindings list/inspect/validate` | read-only | Runtime binding inventory and validation. |
| `loom capability runtime-binding register/test` | operator-mutating or diagnostic execution | Registers a binding or executes a binding directly. |
| `loom routes list`, `loom route inspect` | read-only | Capability route records. |
| `loom capability-calls list`, `loom capability-call inspect` | read-only | Capability call audit records. |
| `loom approvals list`, `loom approval inspect` | read-only | Approval queue and approval detail. |
| `loom approval decide` | sensitive operator-mutating | Approves or denies an approval and can issue a grant. |
| `loom grants list`, `loom grant inspect` | read-only | Grant inventory and detail. |
| `loom grant revoke` | sensitive operator-mutating | Revokes authorization. |
| `loom policy explain` | policy-evaluating | Explains a policy decision. With `--request-approval`, it can create an approval request. |
| `loom policy decisions list`, `loom policy decision inspect` | read-only | Persisted policy decision audit. |
| `loom security audit` | read-only | Filters policy, approval, and grant events. |
| `loom actor inspect` | read-only | Actor lookup by actor id or actor key. |
| `loom agent access-sessions`, `loom agent work-contexts` | read-only | Agent access/work context inventory. |
| `loom agent access-session create`, `loom agent work-context create`, `loom agent call`, `loom agent worklog write` | agent-context-mutating | Creates agent access records, calls a tool, or writes worklog state. |

## Providers

Provider commands answer which runtime components are registered and healthy:

```bash
loom providers list --limit 20
loom providers list --node main --status active
loom provider inspect <provider-ref>
loom provider health <provider-ref>
```

Slice 11 verified live JSON for `providers list`, `provider inspect`, and
`provider health`. The verified system returned active provider rows for main
and workspace nodes, with provider status, health, availability, node, scope,
and compact address fields.

Provider refs can be provider ids, provider keys, or compact provider
addresses. A provider compact address has this shape:

```text
scope/path@provider-key
```

See [[Capability Addresses]] for exact parsing rules.

## Provider Advertisements

Remote provider advertisements are review records from node agents:

```bash
loom provider-advertisements list --limit 20
loom provider-advertisements list --node macbook --status pending_review
loom provider-advertisement inspect <advertisement-ref>
```

Approving or rejecting an advertisement changes the registry:

```bash
loom provider-advertisement approve <advertisement-ref> --reason "reviewed"
loom provider-advertisement reject <advertisement-ref> --reason "not approved"
```

Use those commands only after reviewing the advertised provider, endpoint
metadata, source node, and expected project or system scope.

## Capabilities

Capability commands are the normal discovery path:

```bash
loom capabilities list --limit 20
loom capabilities list --node main --status active
loom capabilities list --provider main@system
loom capabilities search status --limit 10
loom capability inspect main@system.status.read
loom capability usage-docs main@system.status.read
```

Slice 11 verified:

- `capabilities list` returned active endpoint rows;
- `capabilities search system --limit 5` returned ranked candidates;
- `capability inspect` returned endpoint, provider, class, active version,
  provider health, and usage documents;
- `capability usage-docs` returned usage document rows for a tested
  capability.

Useful filters include `--provider`, `--class`, `--node`, `--scope`,
`--project`, `--form`, `--status`, `--risk`, and `--authorization-level`.

## Capability Calls

Use dry-run first:

```bash
loom capability call main@system.status.read --dry-run --input '{}'
```

Slice 11 verified a dry-run capability call against a low-risk active
capability. It returned `ok=true` and `status=completed`. The dry-run flag
plans and authorizes without dispatching the provider adapter, but the system
can still persist route, call, or policy records so the action is auditable.

A real call uses the same target and input without `--dry-run`:

```bash
loom capability call <capability-address> --input '{"key":"value"}'
loom capability call <capability-address> --input-file ./input.json --wait
```

Important flags:

| Flag | Meaning |
|---|---|
| `--actor` | Actor id or key for the call. |
| `--origin-node` | Origin node id or key. |
| `--scope` | Scope id, key, or slug. |
| `--request-approval` | Create or reuse an approval when policy requires one. |
| `--approval-reason` | Store an operator-readable reason on the approval request. |
| `--idempotency-key` | Retry key for a repeatable request. |
| `--wait` | Wait for terminal state. |
| `--timeout-seconds` | Wait timeout when `--wait` is used. |

Do not use real capability calls as casual examples. They can run scripts,
write objects, request remote node work, or trigger jobs depending on the
target capability.

## Routes And Calls

Routes are routing decisions. Capability calls are the call audit records:

```bash
loom routes list --limit 20
loom routes list --status completed --target-node main
loom route inspect <route-ref>

loom capability-calls list --limit 20
loom capability-calls list --status failed
loom capability-call inspect <capability-call-ref>
```

Slice 11 verified live list and inspect behavior for both routes and capability
calls. Route and call rows include actor, origin node, target node, provider,
capability endpoint, status, policy decision, approval, grant, job, correlation,
and idempotency fields where available.

## Runtime Bindings

Runtime bindings connect an endpoint version to an executable runtime:

```bash
loom capability runtime-bindings list --limit 20
loom capability runtime-bindings list --runtime-kind command
loom capability runtime-binding inspect <binding-ref>
loom capability runtime-binding validate <binding-ref>
```

Registering or testing a binding is an operator/developer action:

```bash
loom capability runtime-binding register <endpoint-version-ref> \
  --kind command \
  --config-json '{}'

loom capability runtime-binding test <binding-ref> --input-file ./input.json
```

`test` executes the binding directly for diagnostics. Do not run it against
production-like bindings unless the input and side effects are understood.

## Policy Decisions

Policy explains whether an operation is allowed, denied, or approval-required:

```bash
loom policy explain capability:main@system.status.read
loom policy decisions list --limit 20
loom policy decisions list --decision deny
loom policy decision inspect <decision-ref>
```

`policy explain` can be mutating if you add approval flags:

```bash
loom policy explain capability:<address> \
  --actor <actor-ref> \
  --request-approval \
  --approval-reason "operator reviewed"
```

Slice 11 verified read-only policy decision listing. It did not create an
approval request with `policy explain`.

## Approvals And Grants

Approval inspection:

```bash
loom approvals list --limit 20
loom approvals list --status pending
loom approval inspect <approval-ref>
```

Approval decisions:

```bash
loom approval decide <approval-ref> --approve --actor <actor-ref> --grant-ttl 15m
loom approval decide <approval-ref> --deny --actor <actor-ref> --reason "not needed"
```

Grant inspection:

```bash
loom grants list --limit 20
loom grants list --status active
loom grant inspect <grant-ref>
```

Grant revocation:

```bash
loom grant revoke <grant-ref> --actor <actor-ref> --reason "completed"
```

Approvals and grants are sensitive. They alter who may do what. Use them from
an operator-reviewed terminal, preserve the correlation id, and verify that the
target node, scope, capability, risk, and expiration match the intended action.

## Security Audit

Security audit events filter policy, approval, and grant event families:

```bash
loom security audit --limit 20
loom security audit --type policy.decision.created
loom security audit --actor <actor-id>
loom security audit --target-kind approval
```

Slice 11 verified `security audit --limit 5`. The current clean state returned
zero matching security rows for the default policy/approval/grant filter.

## Actors

Actor inspection is ref-based:

```bash
loom actor inspect <actor-id-or-key>
```

Slice 11 verified actor inspection by selecting an actor id from policy
decision history. The tested actor row returned key `owner`, kind `human`, and
status `active`.

There is no current `loom actor list` command. Do not document one until the
CLI exposes it.

## Agent-Facing Access

Agent commands are for scoped agent access and tool visibility:

```bash
loom agent access-sessions --limit 20
loom agent access-session inspect <access-session-ref>
loom agent work-contexts --limit 20
loom agent work-context inspect <work-context-ref>
loom agent tool-view inspect <tool-view-ref>
```

Creation requires an actor whose `actor_kind` is `agent`:

```bash
loom agent access-session create --actor <agent-actor-ref> --runtime-node main
loom agent work-context create --access-session <session-ref> --objective "..."
```

Tool search and calls are work-context-bound:

```bash
loom agent tools search --work-context <work-context-ref> status --limit 10
loom agent tool inspect --work-context <work-context-ref> <tool-ref>
loom agent call --work-context <work-context-ref> <tool-ref> --input '{}'
loom agent worklog write --work-context <work-context-ref> --summary "checked status"
loom agent worklog list --work-context <work-context-ref> --limit 20
loom agent tool-call inspect <agent-tool-call-id>
```

Slice 11 verified `agent access-sessions` and `agent work-contexts` empty-list
behavior. The live system did not have an active `agent` actor available for a
safe full lifecycle fixture.

`agent worklog list` and `agent worklog write` require `--work-context`. If the
flag is omitted, the CLI returns `agents.work_context_required` locally before
building a backend worklog route.

## Troubleshooting

| Symptom | Check |
|---|---|
| Capability is not visible | Check provider status, provider health, endpoint status, scope/project filters, and archived project context. |
| Capability call fails authorization | Inspect the policy decision, actor authorization, approval, grant, and target node. |
| Approval is pending | Inspect the approval, confirm requested actor/scope/capability, then decide or deny with an explicit reason. |
| Grant is not used | Check grant status, expiration, use count, actor, node, scope, and capability constraints. |
| Agent tool search returns nothing | Confirm the work context exists, the actor is an agent actor, and there are active contextual capabilities. |
| Security audit looks empty | The default audit filter only returns policy, approval, and grant event families. Use `--type` for exact event inspection. |

## Related Docs

- [[Nodes Providers Capabilities]]
- [[Security Authorization And Policy]]
- [[Capability Addresses]]
- [[Capabilities Policy Security And Routes API]]
- [[Nodes And Capabilities Portal]]
