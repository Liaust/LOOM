---
title: "Operations"
description: "Runbooks for operating LOOM safely across Mac, main, storage, backups, cloud, and maintenance."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
status: draft
verified_at: "2026-09-05"
source_scope:
  - "AGENTS.md"
related:
  - "[[User Guide]]"
  - "[[CLI Reference]]"
  - "[[Reference]]"
aliases:
  - "LOOM Operations"
---
# Operations

Operations docs are for maintaining LOOM safely. They can describe
production-like actions, but they must be explicit about dry-runs, backups,
confirmations, user approval, and which node owns the operation.

## Start Here

- [[Main And Mac Operations]] explains the production-like Mac/main topology and
  locality rules.
- [[Updating And Rebuilding]] covers update planning, release staging, rebuilds,
  rollback boundaries, and safe validation.
- [[Troubleshooting]] provides a first-pass diagnostic path.
- [[Support Bundles]] explains how to collect bounded support evidence.

## Runbooks

- [[Backup Restore And Drills]]
- [[Cloud Storage]]
- [[Database Maintenance]]
- [[Provenance Runtime Operations]]
- [[Storage Retention And Safe Delete]]
- [[Notes Index Maintenance]]
- [[Node Enrollment And Watched Roots]]
- [[Digital Estate Project Migration Pilot]]

## Safety Rule

Operations examples should default to read-only status, dry-run, or explicit
operator confirmation. They must not expose credentials or encourage writes into
generated backup views.

## Related Docs

- [[Command Safety]]
- [[Backup Sync Retention And Cloud]]
- [[Security Authorization And Policy]]
- [[Developer Documentation]]
