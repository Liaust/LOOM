# Code Review Protocol

Review the exact base-to-head diff and report findings before summary.

Check, in order:

1. Scope and expected-file boundary.
2. Correctness, failure paths, data integrity, authorization, and concurrency.
3. Tests against the stated behavior and regression risk.
4. Portable-state hygiene: no credentials, absolute paths, session IDs,
   private memory, caches, generated runtime state, or semantic-truth claims.
5. Checkpoint, progress, handoff, and lifecycle consistency.

Classify actionable findings by severity and cite the narrow path and line.
Blocking findings must be fixed within scope or returned to the worker with a
bounded repair request. Do not weaken the accepted plan to make a diff pass.
When no findings remain, state the tests reviewed, residual risks, and exact
commit proposed for integration.
