# Repository Definition

## Purpose

<Explain the bounded responsibility of this repository.>

## Architecture Entry Points

- <repository-relative path and purpose>

## Validation

- Narrow: <focused repository command>
- Full: <full repository command>
- Static analysis: <lint, vet, or type-check command>

## Branches

- Stable: <stable branch if known>
- Integration: <integration branch if used>
- Worker pattern: <worker branch pattern if used>

## Sensitive And External State

- <paths or systems that require explicit authority>

## Stop Conditions

- Stop when accepted planning state is missing or stale.
- Stop before unplanned production, credential, destructive, or external-state
  work.
- Stop when implementation would cross the current slice's file boundary.
