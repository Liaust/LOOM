# Word Count Usage

Use this script when a workflow needs to inspect a text object, count its words, and create a derived report artifact. It is useful as a small executable capability for validating the LOOM job runner, stdout/stderr log capture, output records, artifact ingestion, and artifact indexing.

In larger workflows, call this script after ingesting a note, markdown file, transcript, or generated text object. The returned artifact can then be searched by normal LOOM text search and used by later workflow steps.
