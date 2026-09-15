---
title: "Concepts"
description: "Plain-language explanations of LOOM's architecture and mental model."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - concepts
status: draft
verified_at: "2026-07-07"
source_scope:
  - "AGENTS.md"
related:
  - "[[LOOM Documentation]]"
  - "[[User Guide]]"
  - "[[Developer Documentation]]"
aliases:
  - "LOOM Concepts"
---
# Concepts

Concept docs explain why LOOM behaves the way it does. They are the bridge
between task-oriented user docs and exact reference material.

## Start Here

- [[LOOM Architecture]] explains the system-level shape.
- [[Nodes Providers Capabilities]] explains how work is exposed and routed.
- [[Projects Facets And Contracts]] explains project scope and project-owned
  functionality.
- [[Box Storage And Lane]] explains user-visible files, storage views, and
  transfer surfaces.
- [[Notes Knowledge Index]] explains notes roots, extracted text, chunks,
  search, embeddings, and projection.

## Core Model

- [[Object Store Indexes And Search]] explains indexed objects, text extraction,
  and search surfaces.
- [[Automation Jobs And Workers]] explains direct events, schedules, jobs,
  runners, workers, artifacts, and invocations.
- [[Events Communication And Realtime]] explains durable events, communication
  messages, realtime primitives, and why events are not just logs.
- [[Backup Sync Retention And Cloud]] explains backup coverage, retention,
  cloud snapshots, sync, and safe-delete reasoning.
- [[Security Authorization And Policy]] explains actor scope, authorization
  levels, approvals, grants, policy decisions, and audit surfaces.
- [[Modules Connectors And Agents]] explains the boundary between core,
  modules, connectors, and agent-facing tool access.

## Related Docs

- [[Reference]]
- [[CLI Reference]]
- [[API Reference]]
- [[Developer Documentation]]
