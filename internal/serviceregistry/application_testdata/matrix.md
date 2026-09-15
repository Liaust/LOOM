# E1 independent expectations — written before implementation

Source: immutable Wave 2 E1 matrix at base c1252024d27fa719ca5ed12b86fe6ec47eb9b59b.
The D0 WebDAV and backup fixtures remain byte-for-byte frozen. E1 supplies a
separate synthetic typed manifest because D0's opaque manifest is not deployable.

| Dimension | Positive | Negative |
|---|---|---|
| Composition | D0 private WebDAV source plus supplied owned facts yields ordered artifact/data/credential/process/health/endpoint/protection stages | Missing/wrong-kind repository, cross-project/node ownership, missing target or revision |
| Source | Exact root/manifest/policy bytes and hashes retained | Missing policy, hash mismatch, duplicate/unknown fields, alias/null/trailing docs, absent/wrong exact policy root |
| Artifact/process | Immutable digest, matching platform/review, systemd dedicated user | Missing/unreviewed artifact, platform mismatch, root user, unsupported manager/config/privilege |
| Data | Stable names, explicit permanent path, checked custody and capacity | Transient/unsafe/overlapping path, cross-app custody, changed existing binding, unresolved allocation, insufficient combined capacity |
| Capacity | Planned bytes, monitoring threshold and enforced quota distinct | Quota or monitor unavailable, quota below planned capacity, no claim of reserved capacity |
| Endpoint | Default loopback, private explicit binding, exact approved app HTTPS edge | Occupied port, missing/private-invalid endpoint, public edge missing approval, LOOM/ORCA purpose, changed backend |
| Credential | Only reference metadata and provider support | Missing reference, unsupported provider/delivery, wrong scope, raw value/env/args fields |
| Authority/effects | Actor+node policy and action level bound; reconcile supported | Missing/revoked/insufficient authority, unknown effect, empty/duplicate effects |
| Identity | Time independent, map insertion independent, binds every effective source/fact/config/action | Every effective input mutation changes identity or refuses readiness; uint64 precision preserved |
| Evidence/purity | Input unchanged, no IO API, no runtime satisfied facts | No secret reads, allocation, processes, network, writes or fabricated E2 adapters |
| Lifecycle | Rollback only executable/config; retirement retains data | No persistent-data deletion or data rollback action |

Tests are worker-authored implementations of these frozen expectations, not
independent integrator acceptance. E2/E3 and runtime remain unstarted.

## Corrective independent outcomes (before corrective product code)

The integrator's First Review Findings supersede worker-chosen one-library,
one-credential and WebDAV-only restrictions. The positive WebDAV intent remains.

| Case | Required outcome |
|---|---|
| Existing logical WebDAV | Ready pure plan with original data/credential/private-edge/protection semantics |
| Credential-free service, HTTP GET /health | Ready; no credential/data/protection effects invented |
| Two explicitly named data bindings in one pool | Ready when combined planned bytes fit |
| Shared-pool aggregate exhaustion | Refuse even when each binding fits individually |
| Multiple required credentials | Ready only with exact declared, scoped, supported references |
| Wrong artifact configuration schema | Refuse with configuration-schema prerequisite |
| Unsupported configuration/credential delivery | Refuse with explicit E2 prerequisite |
| Generic manager health without listener | Ready without inventing port/HTTP facts |

Existing malformed-source, unknown-effect, authority, path, persistence, source
byte, identity and raw-secret regressions remain mandatory. Only obsolete
protocol/cardinality assertions and unreleased E1 fixture shape may change.
