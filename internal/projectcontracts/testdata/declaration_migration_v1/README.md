# D4a conversion fixtures

Frozen before production implementation on exact ready base
4467d25715abc8a651837dbf98f0b9ff26af8ebb. These files are an additive corpus;
existing projectcontracts fixtures and expectations are not edited.

The positive cases use pre-existing typed identities, Unicode names/paths and
an integer above 2^53. `cases.json` freezes the positive/refusal matrix and stable
causes. Tests build mutations from these inputs and must execute every case.
Expected YAML is a semantic oracle independent of output formatting; repeated
candidate bytes must also be stable. No partial candidate may escape refusal.

Supplied owner snapshots in tests come from the ordinary legacy analyzer in
separate temporary fixture roots. Candidate parity uses the ordinary v0.5
validator in another fixture. Both source trees include byte/mode sentinels;
the library receives no path or filesystem interface. Every source hash covers
exact bytes. Facts have explicit complete-empty families, one project/node,
one source generation and owner revisions. Refusal fixtures are not invalid
production files to install, and no fixture is a live snapshot or apply plan.

*Written by LOOM's Codex*
