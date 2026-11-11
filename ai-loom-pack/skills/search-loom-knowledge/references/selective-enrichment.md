# Selective File Enrichment

Load this only when asked to extract more text, describe an image or create
embeddings for admitted Notes files. Ordinary reads and searches do not queue
processing. This one-version request does not change a source's automatic
opt-out; future revisions use its defaults. Host policy and file limits apply.

## Single File

Preview the requested work and bind its exact version when applying it:

```sh
loom --json notes enrich <knowledge-object-id> --ocr
loom --json notes enrich <knowledge-object-id> --ocr --source-revision <revision> --source-hash <hash> --yes
loom --json notes enrich <knowledge-object-id> --embeddings --source-revision <revision> --source-hash <hash> --yes
```

Use the preview's actual revision/hash, not placeholders. Select flags
independently. `--vision` describes images, not PDF pages; OCR uses the existing
PDF sparse-page or image engine. Embeddings-only reuses compatible text/chunks
without enabling OCR or vision. Missing text is not permission to enable all
enrichment or rewrite automatic defaults.

## Folder

Use `--folder` and explicitly add `--recursive` for descendants. Save the
`--json` preview and apply it with `--preview-file <file> --yes`. Each preview
covers at most 100 admitted entries; follow `next_after` with `--after` for
another page. This is not a disk scan. Later arrivals are excluded and changed
versions fail rather than receiving work approved for older bytes.

## Completion And Failures

Use `loom --json notes pipelines inspect <object-id>` when completion matters.
An intent receipt is not successful OCR, vision or embedding publication.
Repeated intent returns existing work. Use `notes pipelines retry` for a failed
run rather than reprocess, which resets to automatic defaults. Preserve failed,
unavailable and policy-refused results; do not change limits, enable a global
scan or reset the queue to make a request succeed.
