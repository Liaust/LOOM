---
title: "File Type Support"
description: "Reference for file classes accepted by LOOM Notes and the current body extraction support for each family."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - reference
  - notes
status: draft
verified_at: "2026-08-18"
source_scope:
  - "internal/storagecatalog/models.go"
  - "internal/storagecatalog/classify.go"
  - "internal/knowledge/extractor.go"
  - "internal/knowledge/extractor_pdf.go"
  - "internal/knowledge/pipeline_definition.go"
  - "internal/knowledge/pipeline_stage_pdf.go"
  - "internal/knowledge/pipeline_stage_image.go"
  - "internal/knowledge/extractor_docx.go"
  - "internal/knowledge/extractor_office.go"
  - "internal/knowledge/extractor_structured.go"
  - "internal/knowledge/extractor_code.go"
  - "migrations/00051_notes_file_extraction_foundation.sql"
related:
  - "[[Reference]]"
  - "[[Notes And Search]]"
  - "[[Notes Knowledge Index]]"
  - "[[Notes Index Maintenance]]"
aliases:
  - "Supported File Types"
  - "Notes File Types"
---
# File Type Support

## What This Page Covers

This page explains which file classes LOOM accepts and how much text extraction
exists today.

There are two separate questions:

1. Can LOOM track the file as a knowledge object?
2. Can LOOM extract body text from it?

The answer to the first question is broad. The answer to the second depends on
the file family.

## File Classes

Current valid file classes:

| File Class | Meaning |
|---|---|
| `markdown` | Markdown note files. |
| `text` | Plain text and text-like files. |
| `pdf` | PDF files. |
| `image` | Image files. |
| `video` | Video files. |
| `audio` | Audio files. |
| `archive` | Archive files such as tar or zip. |
| `code` | Source code and code-like configuration files. |
| `office_document` | DOCX, legacy Office, ODT, RTF, and Google Docs pointer files. |
| `directory` | Directory entries. |
| `package` | Package directories such as `.pages` bundles. |
| `generated_metadata` | Generated sidecars and dependency/runtime metadata. |
| `binary` | Binary files not classified elsewhere. |
| `unknown` | Unknown or unsupported files. |

## Body Extraction Summary

| Family | File Class | Body Extraction | Notes |
|---|---|---|---|
| Markdown | `markdown` | yes | Extracts body, frontmatter, headings, Markdown links, and wikilinks. |
| Plain text | `text` | yes | Normalizes text and chunks it. |
| Code | `code` | yes when queued | Extracts source text and regex-based symbols. |
| Structured data | extension-based | yes when queued | Parses JSON, YAML, XML, CSV, TSV; TOML falls back to bounded plain text with a deferred parse warning. |
| PDF | `pdf` | embedded text plus selected-page OCR | Preserves page boundaries, keeps useful embedded text, and uses Poppler plus Tesseract only for pages that need OCR. PDF pages are never sent to vision. |
| DOCX | `office_document` | yes for `.docx` | Extracts body text, headings, links, footnotes, endnotes, comments, core properties, and embedded media count. |
| Google Docs pointer | `office_document` | metadata only | Reads `.gdoc` pointer metadata such as URL and document ID. |
| Legacy Office | `office_document` | metadata only | `.doc`, `.odt`, and `.rtf` are tracked but body extraction is unsupported. |
| Images | `image` | metadata plus optional description | A configured local vision model can produce one bounded factual description for a standalone image. This is disabled by default. |
| Video/audio/archive/package/binary/unknown | mixed | metadata only | Tracked for metadata and future extractors. |
| Directory | `directory` | metadata only | Represents folders and counts, not body text. |
| Generated metadata | `generated_metadata` | ignored or metadata only | Generated sidecars should not become rich note content. |

## Markdown

Markdown extraction records:

- YAML frontmatter when present;
- invalid frontmatter warnings;
- headings and structural paths;
- Markdown links;
- wikilinks;
- body text without frontmatter;
- chunks with embedded-text provenance.

This is the richest current note format.

## Text

Plain text extraction normalizes line endings and chunks body text. It does not
try to infer headings unless the file is Markdown-like and classified as
Markdown.

## Code

Code extraction records:

- full normalized source text;
- line count;
- source format from extension;
- simple regex-based symbols such as functions and classes.

The extractor is intentionally lightweight. It is not a full language parser.

## Structured Data

Structured data extraction is extension-based.

Supported extensions:

- `.json`;
- `.yaml`;
- `.yml`;
- `.xml`;
- `.csv`;
- `.tsv`;
- `.toml`.

JSON and YAML are flattened into searchable key paths. XML extracts element
text with element paths. CSV and TSV use headers where available.

TOML currently records `toml_parse_deferred` and falls back to bounded plain
text.

## PDF

PDF extraction and OCR use local command-line tools:

- `pdfinfo` for metadata and encryption/page count;
- `pdftotext -layout -enc UTF-8` for embedded text.
- `pdftoppm` for bounded rendering of selected pages;
- `tesseract` for English OCR of those rendered pages.

Important limits and states:

- maximum default page count is 250;
- encrypted/password-protected PDFs become `password_required`;
- PDFs over the page limit become `too_large`;
- each page is evaluated independently using versioned usefulness rules;
- useful embedded-text pages are not OCRed;
- selected OCR pages are checkpointed and resume without repeating committed pages;
- OCR is policy-controlled and needs Poppler and Tesseract on the heavy executor;
- PDF pages are explicitly excluded from the standalone-image vision path.

## Standalone Images

Standalone image descriptions use a configured local Ollama-compatible vision
runtime. Inputs are bounded by decoded bytes and pixels, and outputs are
normalized and capped. The artifact records runtime, model, and prompt version.
The policy can be enabled before the model is installed, but work will remain
unavailable until hardware provisioning and model validation are complete.

## Office Documents

DOCX body extraction supports:

- core properties;
- body paragraphs;
- heading styles;
- tables;
- hyperlinks;
- footnotes;
- endnotes;
- comments;
- embedded media count.

Other office documents are tracked differently:

- `.gdoc` pointer files are metadata-only and can expose URL/doc ID metadata;
- `.doc`, `.odt`, and `.rtf` are metadata-only with unsupported body
  extraction status.

## Metadata-Only Still Matters

Metadata-only objects are still searchable by metadata. This helps users find:

- file names;
- paths;
- nodes;
- project roots;
- file classes;
- extraction states;
- useful parsed metadata such as PDF author or Google Docs URL.

The search result will expose `text_source=metadata_text` or
`metadata_only=true` when body text was not extracted.

## Size Limits

Current defaults from the notes knowledge indexer:

- source text read limit per object: 5 MiB;
- extracted text limit per object: 10 MiB;
- maximum chunks per object: 1000;
- default objects per run: 50.

The general text indexer has a stricter default body limit for object-store text
indexing:

- text bytes per object: 1 MiB;
- text bytes per run: 10 MiB.

Files above a relevant limit should become metadata-only or `too_large` rather
than causing uncontrolled indexing work.

## Classification Notes

Catalog classification is conservative:

- Markdown, text, and code are text-index candidates by class.
- PDFs and office documents may enter catalog state as metadata-only and later
  need notes reprocessing to run richer extractors.
- Images, video, audio, archives, packages, binaries, and unknown files are
  tracked for metadata unless a later extractor changes behavior.

This distinction explains why a file can be accepted by LOOM Notes but still
show as metadata-only.

## Related Docs

- [[Notes And Search]]
- [[Notes Knowledge Index]]
- [[Notes Search And Indexes CLI Reference]]
- [[Notes Index Maintenance]]
