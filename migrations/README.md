# LOOM Migrations

Migration `00060` adds the unified LOOM Notes file-pipeline trajectory:
pipeline runs, ordered stage runs, resumable page units, and normalized derived
artifacts with source chronology. It is additive and leaves the historical
extraction and embedding queue tables intact for rollback.

Migration `00061` adds generic durable worker resource capacities and fenced
slot leases, seeding the Notes-only `knowledge_heavy` resource at capacity one.
It serializes OCR, standalone-image description, and embedding work without
claiming admission control over unrelated backup or system workers.

Migration `00057` adds protected-folder control-plane projection state. Main's
Box YAML remains the canonical editable contract; PostgreSQL stores bounded
preflight delivery/results plus desired/applied revision, config-hash, message,
and node-ack evidence. Rolling back removes only that delivery/projection state
and does not rewrite contract YAML or protected source data.

Database migrations begin in Slice 2.

Slice 1 only proved the daemon, CLI, local socket transport, config loading, PostgreSQL health check, storage health check, and response contracts.

Slice 2 wires this directory to the selected migration runner:

```text
goose
```

The first migration creates the durable core schemas for:

- `identity`
- `nodes`
- `scopes`
- `events`

The second migration creates the first project/object/file storage contract for Slice 3:

- `projects`
- `objects`
- `files`

Slice 3 migration scope is intentionally schema-only. Project creation, object-store writes, local file ingestion, API routes, CLI commands, and smoke verification are implemented in later Slice 3 parts.

The third migration creates the first search/indexing contract for Slice 4:

- `search.extracted_text`
- `search.document_chunks`
- `search.search_documents`
- `search.index_status`

Slice 4 Part 1 is schema-only. Text extraction, chunking, indexing, search API routes, CLI commands, and smoke verification are implemented in later Slice 4 parts.

The fourth migration creates the first jobs/scripts/artifacts contract for Slice 5:

- `packages.scripts`
- `packages.script_versions`
- `jobs.runners`
- `jobs.jobs`
- `jobs.job_attempts`
- `jobs.job_logs`
- `jobs.job_outputs`
- `jobs.artifacts`

Slice 5 Part 1 is schema and contract only. Script registration, local runner execution, artifact capture, CLI commands, and smoke verification are implemented in later Slice 5 parts.

The fifth migration creates the first CLI operability support contract for Slice 6:

- `interface.idempotency_keys`
- `admin.operations`

Slice 6 Part 2 uses `interface.idempotency_keys` for retry-safe effectful CLI/API requests and adds only the first `admin.operations` record shape. It does not expose a full admin command catalog yet.

The sixth migration creates the local provider and capability registry contract for Slice 7:

- `capabilities.providers`
- `capabilities.provider_health`
- `capabilities.capability_classes`
- `capabilities.capability_endpoints`
- `capabilities.endpoint_versions`
- `capabilities.usage_documents`

Slice 7 Part 1 is schema-only. Provider registration services, bootstrap seeding, HTTP routes, CLI inspection commands, and capability search are implemented in later Slice 7 parts.

The seventh migration creates the first policy, approval, and grant contract for Slice 8:

- `policy.decisions`
- `policy.approvals`
- `policy.grants`

Slice 8 Part 1 is schema and domain-model only. Policy evaluation, approval decision handling, grant coverage, HTTP routes, CLI commands, and smoke verification are implemented in later Slice 8 parts.

The eighth migration creates the first local routing and capability-call contract for Slice 9:

- `routing.routes`
- `routing.capability_calls`

Slice 9 Part 1 is schema and domain-model only. Route planning, provider adapter dispatch, grant consumption during real execution, HTTP routes, CLI commands, and smoke verification are implemented in later Slice 9 parts.

The ninth migration creates the first node-agent and durable communication spine for Slice 10:

- node presence fields on `nodes.nodes`
- `nodes.authority_profiles`
- `nodes.runtime_profiles`
- `nodes.node_profile_assignments`
- `nodes.heartbeats`
- `nodes.node_status_history`
- `security.node_enrollment_tokens`
- `security.node_enrollment_requests`
- `security.node_auth_credentials`
- `communication.messages`
- `communication.message_acks`

Slice 10 Part 1 is main-side only. It supports owner-created enrollment tokens, pending enrollment requests, approval-issued node credentials, node health reads, and durable main-to-node message enqueue/list/inspect contracts. The actual node-agent loop, remote polling, acking, and second-VM deployment are intentionally deferred until a second UTM VM exists.

The tenth migration creates the remote provider advertisement review contract for Slice 11:

- `capabilities.provider_advertisements`

Slice 11 Part 1 lets enrolled nodes advertise provider/capability metadata to main, records the advertisement as a node-to-main communication message, and keeps newly advertised remote capabilities non-callable until owner approval.

The eleventh migration creates the first workspace sync and backup contract for Slice 12:

- `sync.local_events`
- `sync.local_outbox`
- `sync.local_cursors`
- `sync.local_conflicts`
- `sync.batches`
- `sync.batch_items`
- `sync.ingested_events`
- `sync.cursors`
- `sync.conflicts`
- `sync.replicas`
- `sync.private_backup_operations`
- `sync.deletion_requests`

Slice 12 Part 1 establishes the event batch/cursor/conflict spine. Full object metadata sync, object blob backup, private folder backup, deletion queues, module backup channels, and local PostgreSQL-backed workspace queues are implemented in later Slice 12 parts.

The twelfth migration creates the first minimal real-time primitive contract for Slice 15:

- `realtime.topics`
- `realtime.topic_publications`
- `realtime.subscriptions`
- `realtime.presence`
- `realtime.notifications`
- `realtime.notification_deliveries`
- `realtime.progress_feeds`
- `realtime.progress_updates`
- `realtime.leases`

Slice 15 Part 1 is schema and domain-model only. Topic publication/polling, heartbeat-to-presence projection, approval notifications, progress feed updates, lease conflict checks, expiry workers, API routes, CLI commands, and smoke verification are implemented in later Slice 15 parts.

The thirteenth migration creates the first agent-facing tool visibility contract for Slice 16:

- `agents.agent_access_sessions`
- `agents.agent_work_contexts`
- `agents.tool_views`
- `agents.tool_view_entries`
- `agents.tool_calls`
- `agents.worklog_entries`

Slice 16 Part 1 records LOOM-owned access/work context around self-contained agent actors. It does not create an agent runtime, agent memory substrate, model connection layer, or internal planner.

The fourteenth migration creates the first native module registry contract for Slice 17:

- `modules.packages`
- `modules.module_versions`
- `modules.runtime_requirements`
- `modules.object_type_declarations`
- `modules.provider_declarations`
- `modules.capability_declarations`
- `modules.usage_document_declarations`
- `modules.backup_hook_declarations`

Slice 17 Part 1 registers validated native module manifests and declaration metadata only. It does not install modules, reserve namespaces, create provider records, expose capabilities, create module databases, or start module runtimes.

The fifteenth migration creates the first native module installation lifecycle contract for Slice 17:

- `modules.installations`
- `modules.namespaces`
- `modules.installation_providers`
- `modules.installation_capabilities`
- `modules.health_snapshots`

Slice 17 Part 2 installs registered native modules on the main node only, reserves module namespaces, creates disabled provider/capability registry rows, imports manifest usage documents as pending review, and records derived module health. Enabling a module can activate the installed provider row, but it deliberately does not expose capability endpoints or start a module runtime.

The sixteenth migration creates the first native module exposure and backup-export contract for Slice 17:

- `modules.backup_exports`

Slice 17 Part 3 keeps module exposure explicit: a module installation can be enabled while all endpoints remain disabled, and each capability must be exposed before it enters normal capability search, routing, policy, and agent tool visibility. The first backup export stores a LOOM-owned manifest payload as a database record; full module database/file backup hooks are deferred.

The twenty-sixth migration creates the v0.3 capability runtime binding contract:

- `capabilities.endpoint_runtime_bindings`

v0.3 Slice 02 stores endpoint-version-scoped runtime metadata only. It does not create project capabilities, scaffold project folders, register scripts, activate schedules or direct events, or add concrete runtime executors. Existing explicit provider adapters remain valid and continue to be the first execution path.

The twenty-seventh migration creates the v0.3 project contract registration lifecycle contract:

- `projects.project_contract_registrations`
- `projects.project_contract_facets`

v0.3 Slice 04 stores the current registered project-folder contract snapshot,
validation report, registration plan, and facet inventory. It does not activate
scripts, schedules, direct events, capabilities, modules, watched roots, sync,
backup, or workers.

The twenty-eighth migration creates the v0.3 project script exposure mapping
contract:

- `projects.project_script_exposures`

v0.3 Slice 05 records which project script packages have been activated into
script registry rows, project-owned providers, capability endpoints, endpoint
versions, and script runtime bindings. The mapping is deliberately additive:
removed or disabled script folders can be marked stale/disabled without deleting
their historical capability records.

The twenty-ninth migration creates the v0.3 project schedule registration
mapping contract:

- `projects.project_schedule_registrations`

v0.3 Slice 06 records which project-authored schedule contracts have been
activated into normal automation scheduler records. Project schedules are
registered as paused by default; resuming or manually firing them continues to
use the existing `automation.schedules`, `automation.schedule_fires`, and
`automation.invocations` runtime path.

The thirtieth migration creates the v0.3 project direct-event registration
mapping contract:

- `projects.project_direct_event_registrations`

v0.3 Slice 07 records which project-authored direct-event contracts have been
activated into normal automation integrations, auth profiles, and direct-event
endpoints. Project direct events are registered as paused by default; resuming
or test-ingesting them continues to use the existing `automation.integrations`,
`automation.integration_auth_profiles`, `automation.direct_event_endpoints`,
`automation.direct_events`, and `automation.invocations` runtime path.

The thirty-first migration creates the v0.3 project watched-root registration
mapping contract:

- `projects.project_watched_root_registrations`

v0.3 Slice 08 records the watched-root desired state compiled from project
notes, repo, sync, and backup policy contracts. The table links project-owned
intent to actual `watched_roots.roots` reports by node and backend root key, so
main can track pending node-agent application, reported roots, config drift,
stale roots, and project-level sync/backup status without mutating workspace
node config directly.

The thirty-second migration creates the v0.3 project connector registration
mapping contract:

- `projects.project_connector_registrations`

v0.3 Slice 09 records which project-authored connector contracts have been
activated into normal capability providers, endpoint versions, script runtime
bindings, and usage-document records. Connector registration rows give project
status a stable place to show provider address, runtime kind, endpoint counts,
and stale/disabled connector state without inventing a parallel capability
registry.

The thirty-third migration creates the v0.3 project module registration mapping
contract:

- `projects.project_module_registrations`

v0.3 Slice 11 records which project-authored module packages have been
registered through the existing native module substrate. The table links a
project contract revision to module package/version IDs and stores explicit
install/exposure intent without implicitly installing, enabling, or exposing
module capabilities.

The thirty-fourth migration creates the v0.3.1 executable workflow package
contract:

- `packages.workflows`
- `packages.workflow_versions`
- workflow columns on `jobs.jobs`
- `projects.project_workflow_registrations`

v0.3.1 stores executable workflow packages using the same package/version shape
as scripts, allows `workflow_run` jobs in the durable job queue, and links
project workflow facet activation to normal capability endpoints and runtime
bindings through `projects.project_workflow_registrations`. Project activation
owns the provider/endpoint/runtime-binding rows; the job runner still executes
the workflow package as one opaque process.

The fiftieth migration creates the v0.8.4 notes hybrid search lexical contract:

- `search.lexical_documents`
- `search.lexical_terms`
- `knowledge_object_metadata` search documents

v0.8.4 stores BM25-ready field lengths and term frequencies for notes search
documents, keyed to `search.search_documents` with cascading cleanup. It also
allows metadata-only notes objects such as PDFs and images to have searchable
filename/path/class metadata without adding PDF extraction, OCR, or Office
document extraction.

The fifty-first migration creates the v0.8.5 notes file extraction foundation:

- `office_document` file-class constraint support for storage and knowledge
  objects

v0.8.5 widens existing storage and knowledge file-class constraints so notes
can classify Office and Google Docs pointer files without treating them as
generic binaries.

The fifty-second migration creates the v0.8.6 local notes embeddings foundation:

- PostgreSQL `pgvector` extension support
- `knowledge.embedding_runtime_models`
- `knowledge.embedding_settings`
- `knowledge.embedding_object_states`
- `knowledge.chunk_embeddings`
- `knowledge.embedding_work_items`

v0.8.6 Slice 1 is schema and runtime configuration only. Ollama client calls,
embedding generation, semantic search ranking, CLI controls, and portal controls
are implemented in later v0.8.6 slices. Embeddings remain disabled by default.

The fifty-third migration repairs the v0.9.2 Dropzone custody processing-state
model:

- updates available `dropzone_custody` rows in `source_area='dropzone'` from
  `metadata_only` to `backup_only`

Dropzone custody rows retain payload bytes through Dropzone physical refs, so
they should not be modeled as metadata-only entries. The migration is scoped to
Dropzone custody rows and does not rewrite payload bytes or refresh storage
exports.

The fifty-fourth migration adds the v0.9.2 failed-job attention lifecycle:

- `jobs.jobs.failure_attention_status`
- `jobs.jobs.failure_attention_updated_at`
- `jobs.jobs.failure_attention_updated_by_actor_id`
- `jobs.jobs.failure_attention_note`

This preserves terminal job status while allowing operators to acknowledge or
archive stale failed-job attention.

The fifty-sixth migration adds v0.9.9 Box backup-contract registration keys:

- extends `box.watch_root_registrations.area_key` to accept
  `backup_<contract-key>` alongside Notes/Documents/Launchpad

This lets multiple standalone Box backup contracts coexist as watched-root
registrations without changing the existing Box sync-scope semantics.
