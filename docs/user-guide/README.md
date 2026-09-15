---
title: "User Guide"
description: "Task-oriented guides for using LOOM as a working system."
audience:
  - user
tags:
  - loom
  - user-guide
status: draft
verified_at: "2026-07-07"
source_scope:
related:
  - "[[LOOM Documentation]]"
  - "[[Portal Guide]]"
  - "[[CLI Reference]]"
aliases:
  - "LOOM User Guide"
---
# User Guide

The user guide explains how to use LOOM for normal work. It starts from user
goals rather than implementation details: checking system health, creating and
managing projects, searching notes, moving files, inspecting backups, running
automation, and collecting support evidence.

## Start Here

- [[Getting Started]] introduces the first safe checks and the basic shape of a
  Mac/main LOOM system.
- [[Daily Use]] explains the normal status, health, portal, notes, projects,
  files, and automation loop.
- [[Portal First Tour]] gives a guided path through the terminal portal.
- [[Diagnostics And Support]] explains how to collect evidence when something
  looks wrong.

## Main Workflows

- [[Projects]] covers project creation, facets, contract registration, archive
  behavior, and project-specific actions.
- [[Notes And Search]] covers notes roots, object reconciliation, indexing,
  search, embeddings status, and projection.
- [[Files Storage And Lane]] covers LOOM Box, Lane transfers, storage views,
  retention, safe-delete checks, and file-transfer status.
- [[Backups Cloud And Restore]] covers backup coverage, backup dry-runs,
  restore-drill dry-runs, cloud doctor, and snapshot list.
- [[Automation]] covers schedules, direct events, scripts, jobs, workers,
  invocations, and archived-project suppression.
- [[External Agents And AI LOOM Pack]] covers the release-matched instruction
  pack, harness-native skill deployment, and non-destructive Morathustra
  workspace scaffolding.

## Safety Model

User docs should prefer read-only checks and dry-runs. When a workflow changes
state, the page should say whether it is fixture-safe, project-scoped,
main-owned, Mac-local, cross-node, or operator-only.

## Related Docs

- [[Concepts]]
- [[Portal Guide]]
- [[CLI Reference]]
- [[Operations]]
