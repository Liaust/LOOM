---
title: "Capability Addresses"
description: "Reference for LOOM provider and capability address syntax, validation rules, and examples."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - reference
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/capabilities/address.go"
  - "internal/capabilities/models.go"
  - "/tmp/loomdocs-v096 --json capabilities list --limit 5"
related:
  - "[[Reference]]"
  - "[[Nodes Providers Capabilities]]"
  - "[[Capabilities Security And Policy CLI Reference]]"
  - "[[Capabilities Policy Security And Routes API]]"
aliases:
  - "Capability Address"
  - "Provider Address"
---
# Capability Addresses

## What This Page Covers

Capability addresses are compact strings that identify a callable endpoint in
LOOM. Provider addresses identify the provider that owns endpoint rows.

These addresses are used by CLI commands, portal action details, policy
decisions, routes, capability calls, usage docs, and agent tool views.

## Provider Address Shape

Provider addresses have this shape:

```text
scope/path@provider-key
```

Examples:

```text
main@system
main@object-store
workspace/macbook@system
```

`internal/capabilities/address.go` validates provider addresses with these
rules:

- exactly one `@`;
- non-empty scope path;
- scope path is slash-separated;
- scope path segments are lowercase slug segments;
- provider key is lowercase, starts with a letter, contains letters, numbers,
  underscores, or hyphens, and does not contain dots.

## Capability Address Shape

Capability addresses have this shape:

```text
scope/path@provider-key.capability.name
```

Examples from the current system include:

```text
main@system.status.read
main@object-store.object.inspect
workspace/macbook@system.echo
```

Validation rules:

- exactly one `@`;
- a provider key before the first dot after `@`;
- a capability name after that first dot;
- capability name uses non-empty dot-separated segments;
- capability segments are lowercase snake-case identifiers;
- provider key does not contain dots.

## Case And Normalization

The parser lowercases and trims input before validation. Docs and command
examples should still write canonical lowercase addresses. Do not rely on
normalization to make unclear examples readable.

## Scope Path

The scope path identifies where the provider is mounted or registered. Common
scope paths include:

| Scope Path | Meaning |
|---|---|
| `main` | Main node/system scope in current examples. |
| `workspace/macbook` | Mac workspace scope in current examples. |
| project or nested scope paths | Project or future nested scopes when registered. |

Scope paths are not filesystem paths. They are LOOM routing identifiers.

## Provider Key

Provider keys identify provider records inside a scope. Examples include:

| Provider Key | Meaning |
|---|---|
| `system` | Built-in system provider. |
| `object-store` | Object-store provider. |
| `script-runner` | Script runner provider. |
| `loom-project-cockpit` | Module provider from the minimal module fixture after installation/exposure. |

Provider type is stored separately from provider key. A provider key is part of
the address; provider type is registry metadata.

## Capability Name

Capability names identify endpoint actions inside a provider. They are
dot-separated. Examples:

| Capability Name | Meaning |
|---|---|
| `status.read` | Read status. |
| `health.read` | Read health. |
| `object.inspect` | Inspect an object. |
| `object.ingest` | Ingest an object. |
| `approval.decide` | Decide an approval. |

Capability names are not command strings. The endpoint's runtime binding
decides how the capability is executed.

## Related Identifiers

Do not confuse address strings with database ids:

| Identifier | Example Shape | Use |
|---|---|---|
| Provider address | `main@system` | Human/agent target ref for a provider. |
| Capability address | `main@system.status.read` | Human/agent target ref for a capability. |
| Provider id | `provider_...` | Stable database row id. |
| Capability endpoint id | `capability_endpoint_...` | Stable database row id. |
| Endpoint version id | `capability_endpoint_version_...` | Versioned implementation row id. |
| Runtime binding id | `runtime_binding_...` | Runtime binding row id. |
| Route id | `route_...` | Routing decision row id. |
| Capability call id | `capability_call_...` | Call audit row id. |

Most inspect commands accept either row ids or unambiguous compact addresses.
List and search commands are the safest way to find the right ref.

## Address Use By Surface

| Surface | Address Use |
|---|---|
| CLI | `loom capability inspect <address>`, `loom capability call <address>`, provider filters. |
| API | Capability refs in route paths and request bodies. |
| Portal | Capability Explorer rows and raw action details. |
| Policy | Operation strings such as `capability:<address>`. |
| Agent tools | Tool entries can expose capability-backed refs and usage docs. |

## Related Docs

- [[Nodes Providers Capabilities]]
- [[Capabilities Security And Policy CLI Reference]]
- [[Capabilities Policy Security And Routes API]]
- [[Nodes And Capabilities Portal]]
