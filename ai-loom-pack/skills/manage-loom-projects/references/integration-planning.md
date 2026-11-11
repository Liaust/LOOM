# Plan Integration Before Infrastructure

For a new repository/project, or a feature that introduces or changes runtime,
state, scheduling or shared infrastructure, design its fit with deployed LOOM
before implementing that infrastructure. Do not build a standalone deployment,
credential, task or sync system and only then discover how to retrofit it.

Keep a short integration section in the existing project/feature plan. For a
simple local library or research repository, one sentence saying which LOOM
surfaces it uses and that no managed runtime is needed can be enough. Small
fixes, ordinary source reads and unrelated edits need no new plan or approvals.

Register projects in their selected Box normally. `.loom` belongs to the
surrounding project workspace; member Git repos/client deliverables stay
portable and need no LOOM SDK, managed declaration or optional development
pack for ordinary coding/builds/tests. Main may be the test host rather than
the client target: do not let a missing LOOM adapter dictate the deliverable's
language or architecture. Only requested managed resources use adapters.
Resolve work policy with `loom box context --json`; account identity and work
Box are separate. Follow that Box's Basecamp rules throughout this guide:
Company reporting, even an existing-task update/completion or remote draft,
requires the operator's explicit approval. Prepare locally; never mirror Company
content into Personal task/knowledge systems. Missing auth holds only that tool.

## Record The Relevant Fit

- **Ownership and filesystem:** which node owns execution/source; the actual
  project and repository paths; where authoritative data lives. Use the existing
  project layout and normal ecosystem files. No fake Git membership, duplicate
  database, hidden second workspace or copy of generated views.
- **Existing owners:** reuse LOOM's relevant project, runtime, job, schedule,
  credential, file/knowledge and protection primitives. Basecamp owns task state;
  Provenance owns qualified durable meaning. Do not clone their infrastructure
  into an application. State which surfaces are unnecessary instead of activating
  everything by default.
- **Supported build and run path:** verify the current command/contract and
  actual producer/adapter support before choosing service wrappers or deployment
  architecture. Read known source/reference directly; use a relevant supported
  check when needed. Planned is not deployed, and declaration acceptance is not
  proof of working execution. See [managed applications](managed-applications.md).
- **Bindings and lifetime:** use genuine settings and approved credential/data
  references, portable application code and normal package manifests. Choose the
  real runtime owner; persistent Main work must not accidentally depend on a Mac
  client or its harness remaining online. Local-only/Mac-native work can stay
  local when that is the intended product. Declare only requested managed effects.
- **Integration and acceptance:** identify the small bridge/code change, relevant
  interfaces and actual-use check, preserving current data/services. Publication,
  website login/contact and protection retain their own real authority/readiness.
  Neither a test nor a project declaration implies missing credentials are valid.

## Handle A Real Gap At Its Owner

If LOOM lacks a required build adapter or primitive, name that precise gap before
committing the application to an incompatible architecture. Prefer a reusable
backend integration in its actual owning source over repeated project-specific
wrappers, handwritten artifact hashes, copied validators or mandatory HTTP health
services for workers that do not need HTTP.

MINA and coding agents may implement substantial code and the integration
themselves when the accepted scope covers that source/effect. A separate
integrator is not automatically required. If the gap is outside the task's
scope, identify that bounded dependency and continue independent work; do not
invent support, bypass policy or force a language change merely to fit a missing
producer. A future Rust/Cargo plan, for example, is not evidence that the current
deployment can package Rust.

This planning step does not authorize deployment, credentials, public exposure,
spending or destructive changes. It also does not require registering every
script, adopting an optional repo-development pack, creating another document,
or obtaining the operator's approval for each ordinary implementation step. Pass the
relevant integration decisions to delegated workers without private agent memory.

## Report Substantial Friction

When LOOM substantially blocks a workflow, forces repeated rework/duplicate
infrastructure, or appears unable to support a required feature, write a concise
report in the owning project's existing issue/handoff area and add it to Basecamp.
This applies during planning or implementation, even when the agent fixes the
problem in the same task. Routine tool errors need no separate report.

Investigate enough to distinguish a confirmed unsupported capability from a
bug, stale guidance, configuration, missing permission/credential, or an untested
hypothesis. Do not call a feature impossible merely because a command failed.
Include:

- Intended outcome and affected project/workflow.
- Expected versus observed behavior, relevant deployed version/source, bounded
  reproduction or command/error evidence, and what was actually checked.
- Analysis of the limitation, confidence and remaining unknowns; explain why the
  current supported LOOM path cannot meet the need when that is the conclusion.
- User impact, attempted alternatives and their tradeoffs, smallest proposed
  platform/protocol fix, and the exact dependency or decision still needed.
- Current status: blocked, mitigated or fixed, with verification if fixed.

Search the relevant approved Basecamp destination first. Update an existing
matching task or create one actionable follow-up, with a self-contained summary
and the report attached or included. A private filesystem path alone is not a
reader-accessible report; use a native Doc for longer material under the current
Basecamp protocol. Link any separately required LOOM-owned fix rather than
duplicating task state or routing company work into an unapproved account.
Keep credentials and unrelated private data out of the report.

Continue independent authorized work. Reporting is not a compulsory integrator
handoff, unlimited investigation, permission to bypass LOOM or authorization for
an unrequested platform change. If Basecamp access is unavailable, preserve the
report locally and explicitly hand off the outstanding Basecamp capture.
