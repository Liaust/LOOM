# Notes Pipeline Declaration

Declare a project-relative knowledge folder in `.loom/project.yaml`, then use
project plan/apply/status. Application-data sources use the same controls; see
[application-data Notes](application-data-notes.md).

Defaults: 1 GiB/file, 250 PDF pages, minute polling; smaller limits remain.
Apply updates existing roots. Uploads stream above 32 MiB; views converge
incrementally. Optional knowledge fields:

```yaml
include: ['**/*.[pP][dD][fF]']
exclude: ['**/partial/**', '**/*.part.*']
refresh: {quiet_for: 10m, max_wait: 30m}
processing: {ocr: auto, embeddings: true, image_descriptions: false}
```

Filters are source-relative watcher globs, including application-data sources.
Omitted/empty filters default to include `**/*`, exclude nothing. Shared protection
roots inherit policy filters; explicit filters must match. Application-data
protection stays on its application declaration.

Refresh waits once before processing, capped by max_wait; unchanged polling
does not reset it. Whole seconds only: quiet 0s..24h; maximum positive, >=quiet,
<=24h. Omission retains prompt native work / ten-minute heavy quiet time.
OCR: auto/off. Processing omission inherits Main; false opts out, true cannot
override host disablement. Models/sync remain host/application-owned.

Use `loom notes pipelines inspect <ref>` for policy/deadline/skip reasons;
selected revisions finish; newer edits coalesce. Search labels last-published
content during refresh. Use `--require-current` to exclude lag; preserve citations.
Sync delivery, source admission, lexical publication and embedding completion
are separate steps, not proof of each other.
