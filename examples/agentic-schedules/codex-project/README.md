# Codex schedule trial

This project checks that a LOOM-owned timer calls a project capability which runs
Codex in this project's context and reports completion. It contains no real user
data. The native Hermes route is a different example and a different timer owner.

On Main, `loom-codex-exec` bridges the normal `loom` script runner to the existing
`agents` Codex login. The shell launcher derives the project root from its normal
scripts layout; it needs no runtime config or global Python. The call is ephemeral and
read-only. LOOM owns its timer, overlap policy, job status and answer artifact.

For an editing task, change the script's invocation to
`--sandbox workspace-write --timeout-seconds 1800` and supply its task prompt.
Set the script manifest's `execution.timeout_seconds` higher than the requested
Codex duration (for example 1830), so it can collect the result. The bridge accepts
1-3600 seconds. Do not use a synchronous capability wait: keep exposure mode
`enqueue_only`; inspect the returned job for actual completion.

`workspace-write` permits project work, subject to the agents account's existing
permissions and Codex sandbox. It is not root access or an unrestricted network/
integration profile. Jobs remain fresh ephemeral sessions, use the existing login,
and ignore user configuration. Read-only/90 seconds remain the compatible defaults.
The outer script only calls the bridge, so its read-only filesystem setting can
stay unchanged; Codex's own sandbox controls the project edits.
