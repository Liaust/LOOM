---
title: "Modules Connectors And Agents"
description: "Separate LOOM services from external harnesses, connectors, instruction packages and personas."
audience: [user, operator, developer, agent]
tags: [loom, concepts]
status: draft
verified_at: "2026-09-15"
source_scope: ["internal/modules", "internal/agents", "internal/workers/runtimes/provenance_archivist.go", "ai-loom-pack/manifest.yaml", "nix/modules/loom-morathustra.nix"]
related: ["[[LOOM Architecture]]", "[[Nodes Providers Capabilities]]", "[[Security Authorization And Policy]]", "[[Modules Agents And Realtime CLI Reference]]", "[[Modules Agents And Realtime API]]", "[[External Agents And AI LOOM Pack]]", "[[Main-To-Mac Computer Use]]", "[[Automation Jobs And Workers]]"]
aliases: ["Modules", "Connectors", "Agents"]
---

# Modules Connectors And Agents

## Ownership Is The Important Distinction

| Part | Owns | Does not imply |
| --- | --- | --- |
| LOOM core | Identity, routes, policy, events, jobs, shared storage and discovery primitives | Every application or unrestricted shell access |
| LOOM module | A native application's behavior and domain data | Ownership of unrelated systems |
| Connector | An adapter to an external service, app, device or protocol | A credential or grant for that external system |
| Agent harness | Model execution, sessions, native memory and tools | LOOM registration or permission for every tool action |
| Skill | Instructions for a harness | An executable capability or policy grant |
| Persona | Voice, temperament and role context | Extra authority or a separate system database |

Nodes host providers; providers expose capabilities. LOOM calls are typed,
permissioned, routed and logged. Module-specific databases are not automatically
core schemas. Connectors preserve the ownership of the system they adapt.

## Hermes, MINA, ORCA And Coding Agents

Hermes is an independently developed agent harness. Its sessions, conversational
memory, native skills, model configuration and messaging gateway belong to
Hermes. LOOM's Nix integration pins and starts it and exposes selected LOOM
instructions/tools. This does not make its native cron jobs LOOM schedules.

MINA is an optional persona/protocol package used with Hermes. Morathustra is
the earlier identity retained for compatibility, not a prerequisite for MINA.
ORCA provides the surrounding workspace and terminal/coding-task experience.
Codex or another coding harness works in the selected project/repository under
that project's instructions; it does not inherit MINA's personality or accounts.

The system is usable through its CLI/API without any of those agent interfaces.
Installing their binaries or templates does not perform model OAuth, external
account login, messaging setup, desktop pairing or permission consent.

## From A Conversation To A System Operation

1. A person talks to an agent through its harness: for example Hermes TUI in an
   ORCA terminal, a Hermes messaging gateway, or a separate coding-agent session.
2. The harness loads the applicable workspace/project instructions and skills.
   These help the model select an interface; they do not run services themselves.
3. For LOOM work, the agent calls the CLI or a configured, authorized API tool.
   The CLI's typed client reaches `loomd` through the configured connection.
4. LOOM dispatches to the relevant owner: project operations, search services,
   capability execution or a background job. Authorization and durable operation
   state belong to those owners. Results return to the agent to interpret.

Not all agent activity goes through step 3. Ordinary file edits, Git commands,
Hermes conversational memory and native tools remain harness/workspace activity.
They do not automatically become LOOM events, capabilities or accepted Provenance
records. An integration must explicitly connect the action when that is needed.
There is no requirement for every harness to use one universal MCP endpoint.

## Knowledge Is Not Conversational Memory

Objects represent technical observations. Notes provide source material with
citations. Provenance carries source-backed interpretations, lifecycle and
reconciliation. Hermes memory supports conversational continuity.
These roles can cooperate without sharing one unqualified truth store.

A candidate may be searchable while still pending. A source's current contents,
a historical citation, an accepted decision and a runtime policy are different
things. None independently grants authority for an external action.

The optional Archivist has two layers: a reasoning/review workspace and the
bounded deterministic `main.provenance_archivist` worker in `loomd`.
The worker's selection, leases, checkpoints and replay are not a persistent
LLM lifecycle. Unified model-driven automation remains deferred.

## Device Access Is Explicit

Main Chromium is a Main-owned browser. ORCA's shared browser is an ORCA surface.
Paired Mac computer use targets the user's actual desktop through a bridge and
Mac-side driver. General SSH is a separate shell route. Do not substitute one
for another when the requested device is unavailable.

Mac computer-use integration is implemented and has real terminal/gateway
acceptance on the development system. Other installations must establish their
own pairing, permissions and connectivity. See [[Main-To-Mac Computer Use]].

## Extending LOOM

Prefer the existing provider/capability and module interfaces to hidden shell
conventions. State what component owns persistent data, authorization, retries,
backup, health and scheduling. A new integration should remove a real workflow
gap, not create a second registry for something an existing owner already tracks.

Broader native application UI, additional connectors, complete LOOM-owned
persistent agent lifecycle and recurring model-driven execution are not implied
by the narrower primitives shipped here.
