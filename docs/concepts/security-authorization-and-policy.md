---
title: "Security Authorization And Policy"
description: "Explains LOOM actors, authorization levels, policy decisions, approvals, grants, and safe capability execution."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - concepts
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "AGENTS.md"
  - "internal/policy/models.go"
  - "internal/policy/schema.go"
  - "internal/capabilities/models.go"
  - "internal/capabilities/seed.go"
  - "internal/httpapi/server.go"
  - "go run ./cmd/loom policy --help"
related:
  - "[[LOOM Architecture]]"
  - "[[Nodes Providers Capabilities]]"
  - "[[Command Safety]]"
  - "[[Capabilities Security And Policy CLI Reference]]"
  - "[[Capabilities Policy Security And Routes API]]"
  - "[[Authentication Envelope And Errors]]"
aliases:
  - "Policy"
  - "Authorization"
  - "Approvals And Grants"
---
# Security Authorization And Policy

## What This Page Covers

This page explains how LOOM thinks about safe execution: actors, nodes,
capabilities, risk levels, authorization levels, policy decisions, approvals,
and grants.

It is a concept page, not a command reference. Use the CLI and API docs for
exact commands and request examples once those pages are written and verified.

## Security Model

LOOM should be treated as zero-trust distributed infrastructure. A node is not
trusted just because it is on the network. An agent is not trusted just because
it is useful. A capability call is not safe just because the action is
available.

Every meaningful action should be authenticated, authorized, logged, and
revocable. Higher-risk actions can require confirmation or a temporary grant.

Core security concepts include:

- actor identity;
- node identity;
- provider identity;
- scoped authorization;
- risk metadata;
- policy decisions;
- approvals;
- grants;
- credential brokering;
- revocation and audit records.

## Actors And Authorization Levels

Actors are the working identities inside LOOM. They may represent humans,
agents, services, nodes, or modules. The owner remains the policy root, but LOOM
does not model a higher real-human identity above actors for current
implementation purposes.

`AGENTS.md` states that capability execution authorization levels are fixed
from 1 to 5, and actor authorization is scoped by actor plus node. The current
capability and policy models store execution authorization level, actor
authorization level, risk level, target node, scope, actor, approval, and grant
references.

The practical rule is:

```text
The actor must be authorized for the action, the target, and the context.
```

## Risk Levels

The current source defines risk levels:

- low;
- medium;
- high;
- critical.

Risk is separate from authorization level. A low-risk read command and a
high-risk script execution may both be capabilities, but they should not require
the same policy treatment.

Seeded capabilities show this difference clearly. Reading status is low risk
and low authorization. Running a script or deciding an approval is high risk and
requires a higher authorization level.

## Policy Decisions

Policy answers whether an operation is:

- allowed;
- denied;
- approval required.

Slice 3 verified this command surface:

```bash
loom policy --help
```

The current command group includes:

- `loom policy explain`
- `loom policy decisions`
- `loom policy decision`

The current HTTP API exposes `/v1/policy/explain`, `/v1/policy/decisions`, and
the individual decision route. Exact examples belong in
[[Capabilities Security And Policy CLI Reference]] and
[[Capabilities Policy Security And Routes API]].

## Approvals

Approvals represent owner or authorized-actor review of an operation that policy
does not allow immediately. An approval can be pending, approved, denied,
expired, cancelled, or superseded.

Approving an operation should not silently execute the original action. The
current policy model treats approval as a state change that can produce a
bounded grant. Execution still needs to occur through the proper capability or
job path.

## Grants

Grants are temporary or bounded authorization records. Current grant types
include one-shot, elevation, job, workflow, project, route, transfer,
credential-use, and break-glass categories.

A grant can constrain:

- risk level;
- authorization level;
- scope;
- node;
- capability;
- credential;
- object;
- egress;
- maximum uses;
- expiration.

This is what makes safe escalation possible. A grant should be narrow enough
that it solves the workflow without turning into broad permanent access.

## Credentials

LOOM's rule is that credentials should be brokered, not exposed. An agent,
script, or module should request a capability or credential-brokered action
instead of receiving raw secrets. Public docs must not publish real credentials,
tokens, passwords, private keys, or host-specific secret material.

If a workflow needs credentials, the docs should explain the brokered or
configured path and mark manual secret handling as operator-sensitive.

## Capability Calls And Auditability

A capability call should carry enough context for LOOM to answer:

- who requested the action;
- where the request originated;
- what node, provider, and capability were targeted;
- what scope or project was involved;
- what policy decided;
- whether an approval or grant was used;
- what job, route, event, or result followed.

If the system cannot answer those questions for a meaningful action, the action
is probably bypassing the intended LOOM model.

## What To Do When Policy Blocks Work

When a workflow is blocked by policy:

1. Use read-only inspection first.
2. Check the policy explanation or decision record.
3. Identify whether the problem is missing actor authorization, missing node
   scope, missing approval, expired grant, disabled capability, or wrong target.
4. Request a narrow approval or grant only when that is the intended user
   action.
5. Record unexpected behavior as a bug during docs acceptance work.

Do not work around policy with raw shell access in user-facing docs.

## Future Or Deferred Behavior

The architecture expects stronger security over time: signed messages, richer
node posture, quarantine flows, egress policy, module signing, and broader
credential brokering. Current docs should mark those as future or deferred
unless a specific CLI/API/portal workflow has been verified.

## Related Docs

- [[Nodes Providers Capabilities]]
- [[Command Safety]]
- [[Capabilities Security And Policy CLI Reference]]
- [[Capabilities Policy Security And Routes API]]
- [[Authentication Envelope And Errors]]
- [[Diagnostics And Support]]
