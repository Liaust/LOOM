# Slice Protocol

Slices are scope, validation, review, and checkpoint boundaries. A committed
plan must name the goal, expected files, forbidden files, validation commands,
risk, auto-continue policy, and stop conditions.

## Preflight

- Verify clean Git state, active branch, base commit, and worktree owner.
- Read repository guidance and every file in the assigned feature folder.
- Confirm the feature is `ready` and the next slice is unambiguous.
- Change the feature to `in_progress` before implementation.

## Slice Loop

Implement only the current slice, run its exact gates, inspect the diff against
the file boundary, update progress, and create one checkpoint commit.

Continue only when `Auto-continue: yes`, every gate passes, no unexpected or
forbidden file changed, no TODO or unplanned dependency was introduced, and the
next slice is clear. Otherwise stop with evidence. A risk label alone is not a
stop condition when the accepted plan is explicit.

## Finalization

Run the declared final checks and review protocol, update progress, write the
handoff, change the feature to `review`, commit, and stop without merging.
