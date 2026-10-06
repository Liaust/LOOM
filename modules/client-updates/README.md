# Reviewed client packages

The reusable contract is `loom.client-release.v1`. The first installation
adapter is the Obsidian LOOM Notes overlay, not a universal device installer.
`release.json` lists five code files with exact lengths and SHA-256 digests.
The embedded `loom-release.json` identifies the compiled bundle without a
self-referential file hash. The upstream license is a separate distribution
asset. Settings, credentials, journals, databases and vault files are never
release inputs. Checksums detect corruption, not a compromised publisher.

Build with an explicit component version, sequence and source commit, then run:

```sh
python3 modules/client-updates/package_release.py /owned/build /new/package
python3 -m unittest discover -s modules/client-updates -p 'test_*.py'
```

Normal development builds have sequence zero and cannot be distributed through
the stable updater. Monotonic sequence numbers are component-specific; neither
upstream LiveSync 1.0.32 nor the LOOM server version identifies an overlay build.
Release inputs must come from reviewed, audited public source before promotion.
Packaging is local only: it never uploads, creates a release or installs code.

The feed is `Liaust/LOOM`, explicit `client-notes-*` stable releases.
Promotion requires separate approval; never publish private integration history
or relabel a private source commit as an audited public commit. The updater
uses public HTTPS without device tokens and installs only allowlisted files.
Other adapters, updater self-update and data migrations are deferred.
