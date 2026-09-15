---
title: "Notes Knowledge Index"
description: "Explains how notes roots become knowledge objects, extracted text, chunks, search documents, embeddings, and a read-only projection."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - concepts
  - notes
status: draft
verified_at: "2026-08-18"
source_scope:
  - "internal/knowledge/models.go"
  - "internal/knowledge/source_roots.go"
  - "internal/knowledge/objects.go"
  - "internal/knowledge/extractor.go"
  - "internal/knowledge/pipeline_definition.go"
  - "internal/knowledge/pipeline_coordinator.go"
  - "internal/knowledge/heavy_executor.go"
  - "internal/knowledge/search_bridge.go"
  - "internal/knowledge/absolute_time.go"
  - "internal/search/recency.go"
  - "internal/notesprojection/service.go"
  - "go run ./cmd/loom --json notes overview"
related:
  - "[[Notes And Search]]"
  - "[[Object Store Indexes And Search]]"
  - "[[File Type Support]]"
  - "[[Notes Index Maintenance]]"
aliases:
  - "Knowledge Notes"
  - "LOOM Notes Index"
---
# Notes Knowledge Index

## What This Page Covers

This page explains the unified LOOM Notes pipeline:

```text
notes root -> storage catalog entry -> knowledge object -> one versioned pipeline
-> derived artifacts -> chunks -> lexical index -> optional embeddings -> projection
```

The most important user-facing point is that LOOM separates canonical source
files from generated read/search views.

## Source Roots

A notes source root is a canonical input point for notes.

Current root kinds:

- `box_notes`: a node's LOOM Box Notes root;
- `project_notes`: a project's `notes/` facet.

Source roots carry:

- root kind;
- node identity;
- project identity when applicable;
- backend root key;
- source path;
- root relative path;
- active/stale/disabled/deleted/blocked status;
- authorization metadata.

The authorization metadata matters because a generated projection is not the
same thing as write access to the original source.

## One Pipeline Per File Revision

Each current file revision follows one immutable plan selected from its file
family and the current OCR, image-description, and embedding policy. The
coordinator advances lightweight stages; the globally serialized heavy
executor handles page OCR, standalone-image descriptions, and embeddings.

The normal trajectory is:

1. metadata;
2. native text, PDF page analysis, or standalone-image description as relevant;
3. page-selective PDF OCR when policy and page analysis require it;
4. deterministic text consolidation;
5. chunking and atomic lexical publication;
6. optional embedding;
7. finalize.

Stages record dependencies, attempts, progress, resource observations, and
bounded failures. Content-bearing derived artifacts remain internal; normal
status, portal, and support output exposes only operational metadata.

## Knowledge Objects

A knowledge object is the indexed representation of one file or directory under
a notes source root.

Important fields include:

- `knowledge_object_id`;
- source root ID;
- source node key;
- project ID when the source is project notes;
- source path;
- relative path;
- title;
- file class;
- MIME type;
- size;
- source hash and revision;
- processing state;
- pipeline key and version;
- extraction metadata.

Directories can be knowledge objects, but they are represented as metadata,
not copied into the projection as document content.

## Processing States

The current processing state vocabulary is:

| State | Meaning |
|---|---|
| `metadata_only` | LOOM has object metadata but no body chunks. |
| `text_extracted` | Text extraction has run. |
| `chunked` | Extracted text has been split into chunks. |
| `indexed` | Chunks or metadata have search documents. |
| `embedded` | Current chunks have active embeddings. |
| `failed` | Processing failed. |
| `stale` | Current processing should be refreshed. |
| `deleted` | Source object is deleted from active view. |

The portal summarizes these states under "Processing And Search Health".

## Extraction States

The extractor layer records more detail than the broad processing state.

Important extraction states:

| State | Meaning |
|---|---|
| `extracted` | Body text was extracted. |
| `metadata_only` | Only metadata text was indexed. |
| `too_large` | Body extraction was skipped due to size or page limits. |
| `source_unavailable` | The source path could not be read. |
| `unsupported_body_extraction` | The file is tracked but no body extractor supports it. |
| `password_required` | The file appears encrypted or password-protected. |
| `no_embedded_text` | The file had no embedded text. |
| `ocr_deferred` | OCR is disabled, unavailable, or deferred for the selected PDF pages. |
| `failed` | Extraction failed. |

## Text Sources

Search results expose where text came from:

| Text Source | Meaning |
|---|---|
| `embedded_text` | Text came from the file body, such as Markdown, text, PDF embedded text, DOCX text, or code. |
| `structured_text` | Text came from parsed structured data such as JSON, YAML, XML, CSV, or TSV. |
| `metadata_text` | Text came from object metadata, extraction metadata, path, title, or classification fields. |
| `ocr_text` | Text came from page-selective Tesseract OCR for a PDF page. |
| `vision_description` | Text is a bounded factual description of a standalone image from the configured local model. |
| `consolidated_text` | Deterministic combination of the relevant channels used for chunking. |

This distinction matters because metadata-only results are useful but should
not be confused with full document body search.

## Chunking

Extracted text is split into knowledge chunks. Chunks preserve:

- object ID;
- object version ID;
- chunk index;
- chunk hash;
- structural path when known;
- offsets;
- estimated token count;
- chunker version;
- extraction metadata.

Markdown and DOCX headings provide useful structural paths. Search results can
show those paths in citations and portal rows.

## Links

Markdown extraction records:

- normal Markdown links;
- wikilinks;
- URLs;
- file-like links.

DOCX extraction records hyperlinks from the document body, footnotes,
endnotes, and comments when present.

Links are stored so later LOOM features can reason about note graph structure.

## Search Documents

Search documents are generated from chunks and from selected metadata-only
objects. Metadata search is intentional for non-body formats, large files, and
password-protected files because users still need to find the record.

Examples of metadata-only searchable text include:

- title;
- path;
- source node;
- file class;
- MIME type;
- extraction status;
- useful parsed metadata such as PDF author or Google Docs pointer URL.

## Absolute Time And Provenance

LOOM keeps source chronology separate from database and index activity:

| Time | Meaning | Used For Source Recency |
|---|---|---|
| source created | Best supported creation time from source metadata or embedded file metadata. | Only when the resolver selects it. |
| source modified | Best supported modification time from origin filesystem, source-object metadata, frontmatter, or embedded metadata. | Preferred. |
| observation | When LOOM observed the source revision. | Fallback only, labelled `observed_at_fallback`. |
| index time | When a search document was indexed. | Never. |

The resolver stores all recognized candidates, raw invalid values, warnings,
the selected `recency_at`, and its `recency_basis`. Origin filesystem mtime has
the strongest default modification authority. Exact Markdown `updated_at` and
`created_at`, PDF `ModDate` and `CreationDate`, and DOCX core modified/created
properties can refine chronology when stronger source time is unavailable.
A generic Markdown `date` field is not interpreted as file chronology.

Each immutable knowledge-object version preserves the chronology selected for
that source revision. Reindexing can change `indexed_at`; it cannot relabel the
file's source time.

## Recency-Aware Retrieval

Notes search first discovers relevant lexical or semantic candidates and only
then adds a bounded recency ranked list. The initial policy uses:

- relevance rank weight `1.0`;
- recency rank weight `0.15`;
- recency half-life `180 days`.

This lets equally relevant recent material win a tie while keeping a strongly
relevant older result above a weak recent one. Lexical, semantic, and hybrid
search use the same policy. `sort:newest` and `sort:oldest` reorder the already
relevant candidates by `recency_at`; they do not perform recency-only discovery.

## Embeddings

Embeddings are the optional final heavy stage of the same file pipeline. They
are not written by a separate specialized queue.

Current default settings:

- settings ID: `notes_embeddings`;
- runtime: `ollama`;
- model: `mxbai-embed-large`;
- dimensions: `1024`;
- distance metric: `cosine`;
- default enabled state: disabled.

When embeddings are disabled, hybrid search falls back to lexical search and
reports `fallback_reason=embeddings_disabled`.

## Projection

The notes projection materializes readable copies under a configured projection
root. At verification time it was:

```text
/var/lib/loom/loom-notes
```

Projection properties:

- generated from knowledge objects;
- read-only;
- raw writes unsupported;
- includes materialized files when source content is available;
- skips directories as content;
- writes a manifest under `.loom/manifest.json`;
- can be rebuilt with a dry-run or apply command.

The projection is useful for browsing. It is not the write protocol for
changing original notes.

## Healthy State

A healthy notes system usually has:

- expected active roots;
- objects under every active root with files;
- processing states dominated by `chunked` or expected `metadata_only`;
- zero failed pipeline rows;
- search document count greater than zero when notes exist;
- projection exists and is read-only;
- embedding status clear, either intentionally disabled or enabled with active
  vectors.

## Related Docs

- [[Notes And Search]]
- [[Object Store Indexes And Search]]
- [[Notes Search And Indexes CLI Reference]]
- [[Notes Knowledge And Search API]]
- [[LOOM Notes Portal]]
- [[File Type Support]]
