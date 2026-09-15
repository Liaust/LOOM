# D0 v0.5 contract fixtures

These expectations were written before declaration_types.go and its test helper.
They freeze future DTO shapes, not a shipped loader, compiler or executor.
Fixture format: declaration.fixture.v0.5. invalid.json contains complete JSON
source documents plus stable expected structural error codes. YAML positives
use project.contract.v0.5. JSON and YAML represent the same source DTO.

Required negative cases: unknown root/nested fields, unknown schema/kind,
wrong scalar types and fractional integer capacity,
missing identity/resources, malformed mechanical IDs, invalid local keys,
multiple/mismatched union payloads, absolute/traversal/noncanonical paths,
missing/wrong-kind references, overlapping repositories/knowledge roots,
multiple primary repositories, duplicate repository IDs, conflicting data
locations, invalid capacity/exposure, duplicate credential references and inline
secrets. Unknown actions and incomplete operation envelopes must also refuse.

Protected identity expectations: established project ID, resource kind, repository
ID and persistent data binding cannot be silently replaced by an ordinary edit.
Resource key changes are removal/addition; labels/path changes do not mint IDs.
Same-path knowledge/protection intent is explicit composition, not duplicate
enrollment. Coverage never means payload verification or safe deletion.

Plan identity includes transitive document hashes, exact owner/location, existing
identity bindings, relevant state revisions, requested effects and ordered typed
actions. A time-only display change preserves identity; dependency, policy,
authorization, target or action changes invalidate it. No payload scan is implied.

Test-only shape/identity/hash helpers are reference checks, deliberately not
exported validation or runtime implementations. They cannot validate symlink or
mount custody, current authority, owner availability, endpoint/provider support,
artifact compatibility, real capacity, durable replay or semantic truth.
E1 owns application manifest internals; webdav-application.fixture.txt is opaque
synthetic transitive content, not a proposed executable application manifest.

## First integrator correction

The four independent source-only assertions are incorporated unchanged except
for test names; the original overlay remains separately runnable. Additional
invalid-requests.json expectations cover missing/empty envelopes, unknown/empty/
null/duplicate effects and invalid required apply references. JSON keys are
unique recursively after escape decoding; paths reject every Unicode control.

Protection policy_ref denotes a safe project-relative source file, never a
backupcontracts named lookup. backup.yaml is actual backup.policy.v0.3 source;
its exact bytes/schema bind the plan. Source-only selection tests require one
root matching each explicitly attached/direct path, reject missing/ancestor/
conflicting roots, preserve disabled intent, and leave other roots unenrolled.
D1/D2 own implementation. The WebDAV data path is explicit. External snapshots
represent only future E1/E2 endpoint/credential prerequisites, not current
resolvers. Allocation resolution and application internals remain planned E work.
