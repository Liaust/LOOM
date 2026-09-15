---
title: "Database Schemas"
description: "Reference map of LOOM PostgreSQL schemas, migration ownership, and major table groups."
audience:
  - developer
  - operator
tags:
  - loom
  - reference
  - database
status: draft
verified_at: "2026-07-07"
source_scope:
  - "migrations"
  - "internal/migrations/migrations.go"
  - "rg 'CREATE TABLE IF NOT EXISTS' migrations"
related:
  - "[[Migrations]]"
  - "[[Architecture]]"
  - "[[Database Maintenance]]"
  - "[[API Route Index]]"
aliases:
  - "Schemas"
  - "PostgreSQL Schemas"
---
# Database Schemas

## What This Page Covers

This page maps the current LOOM PostgreSQL schema families. It is a developer
reference, not a replacement for SQL migrations. The source of truth remains
the ordered files under `migrations/`.

## Migration Ownership

Schema changes must be introduced through a new migration file. The migration
wrapper in `internal/migrations` uses Goose with the PostgreSQL dialect.

Use:

```bash
loomd migrate status
loomd migrate up
```

`status` is read-only. `up` mutates the database and should normally run
inside planned daemon startup or update flow.

## Schema Families

| Schema | Purpose | Representative Tables |
|---|---|---|
| `identity` | Actors and actor-node authorization. | `actors`, `actor_node_authorizations` |
| `nodes` | Node registry, profiles, runtime profiles, heartbeats, status history. | `nodes`, `runtime_profiles`, `authority_profiles`, `heartbeats` |
| `scopes` | Scope types and scope records. | `scope_types`, `scopes` |
| `events` | Durable event truth. | `events` |
| `projects` | Project records, memberships, contract registration, facet registration. | `projects`, `workspace_views`, `project_contract_registrations`, `project_script_exposures`, `project_watched_root_registrations` |
| `objects` | Logical objects and versions. | `objects`, `object_versions`, `object_scope_links` |
| `files` | Blob and file metadata/location records. | `blobs`, `file_metadata`, `object_locations` |
| `search` | Extracted text, chunks, search documents, lexical search, and index status. | `extracted_text`, `document_chunks`, `search_documents`, `lexical_documents`, `lexical_terms`, `index_status` |
| `knowledge` | Notes roots, knowledge objects, chunks, pipeline statuses, embeddings. | `notes_source_roots`, `knowledge_objects`, `knowledge_object_versions`, `knowledge_chunks`, `pipeline_statuses`, `chunk_embeddings` |
| `packages` | Script and workflow packages. | `scripts`, `script_versions`, `workflows`, `workflow_versions` |
| `jobs` | Jobs, runners, attempts, logs, outputs, artifacts. | `jobs`, `runners`, `job_attempts`, `job_logs`, `job_outputs`, `artifacts` |
| `automation` | Automations, integrations, schedules, direct events, invocations. | `automations`, `integrations`, `integration_auth_profiles`, `schedules`, `direct_event_endpoints`, `direct_events`, `invocations` |
| `capabilities` | Provider registry, endpoint versions, usage docs, runtime bindings, advertisements, health. | `providers`, `capability_endpoints`, `endpoint_versions`, `endpoint_runtime_bindings`, `provider_advertisements`, `provider_health` |
| `routing` | Capability calls and routes. | `capability_calls`, `routes` |
| `policy` | Authorization decisions, approvals, and grants. | `decisions`, `approvals`, `grants` |
| `security` | Node enrollment and auth credentials. | `node_enrollment_tokens`, `node_enrollment_requests`, `node_auth_credentials` |
| `agents` | Agent sessions, work contexts, tool views, tool calls, and worklog entries. | `agent_access_sessions`, `agent_work_contexts`, `tool_views`, `tool_view_entries`, `tool_calls`, `worklog_entries` |
| `modules` | Native module packages, versions, declarations, installations, namespaces, exposure, health, backup exports. | `packages`, `module_versions`, `provider_declarations`, `capability_declarations`, `installations`, `installation_providers`, `installation_capabilities`, `backup_exports` |
| `realtime` | Topics, publications, subscriptions, presence, notifications, progress, leases. | `topics`, `topic_publications`, `subscriptions`, `presence`, `notifications`, `progress_updates`, `leases` |
| `communication` | Inter-node messages and acknowledgements. | `messages`, `message_acks` |
| `sync` | Workspace sync, replicas, batches, conflicts, deletion requests, local outbox. | `cursors`, `replicas`, `batches`, `batch_items`, `conflicts`, `deletion_requests`, `local_outbox` |
| `watched_roots` | Watched root inventory, findings, backup batches/items. | `roots`, `findings`, `backup_batches`, `backup_items` |
| `storage` | Storage catalog, versions, physical refs, export views, retention, tombstones, file transfers, archives, fidelity. | `storage_entries`, `storage_entry_versions`, `storage_physical_refs`, `storage_view_entries`, `retention_entries`, `file_transfers`, `archive_manifests`, `storage_fidelity_findings` |
| `box` | Box watched root registrations. | `watch_root_registrations` |
| `workers` | Worker kinds, instances, runs, leases, checkpoints, controls, heartbeats, health. | `worker_kinds`, `worker_instances`, `worker_runs`, `worker_leases`, `worker_controls`, `worker_health` |
| `maintenance` | Maintenance operations, findings, artifacts, database rollups. | `operations`, `findings`, `artifacts`, `database_rollups` |
| `interface` | CLI/API idempotency records. | `idempotency_keys` |
| `admin` | Administrative operation records. | `operations` |

## Reading The Schema

To inspect current schema source:

```bash
rg "CREATE TABLE IF NOT EXISTS" migrations
rg "CREATE INDEX IF NOT EXISTS" migrations
```

To inspect applied database state, prefer LOOM commands before direct SQL:

```bash
loom database status
loom database doctor
loomd migrate status
```

Use direct SQL only for planned developer investigation or operator-approved
maintenance.

## Adding A Schema Change

For a schema change:

1. Add the next ordered migration under `migrations/`.
2. Update domain models and services.
3. Update API/local-client/CLI behavior when exposed.
4. Add package tests for both new behavior and migration assumptions.
5. Update this reference if a new schema family or major table group appears.
6. Include migration impact in release notes or handoff.

## Related Docs

- [[Migrations]]
- [[Database Maintenance]]
- [[Architecture]]
- [[API Development]]
