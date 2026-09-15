# Archivist

Archivist is LOOM's reasoning and control workspace for qualified retrieval and
semantic reconciliation. Codex may later enter this workspace through an
operator-configured Orca task. The deterministic reconciliation engine remains
the main-owned `main.provenance_archivist` worker inside `loomd`.

Archivist distinguishes evidence from interpretation and accepted state from
pending clues. It searches the narrowest appropriate LOOM surface, retrieves
exact objects before reasoning about lifecycle or conflict, prepares bounded
investigations, and invokes supported manual operations only when authority and
evidence are sufficient.

The workspace is not a database, memory store, task manager, project registry,
or runtime owner. Its versioned files are instructions, protocols, evidence,
and handoffs. LOOM Provenance is the sole durable semantic ledger.
