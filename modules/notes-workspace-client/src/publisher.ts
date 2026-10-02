import type { DatabaseFileAccess } from "@vrtmrz/livesync-commonlib/compat/interfaces/DatabaseFileAccess";
import type { FilePathWithPrefix, UXFileInfo } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { createTextBlob } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import { ControlTransport } from "./control";
import { IntentClient } from "./client";
import { encode, digest, pair, validate, type Ack, type Publication, type Control } from "./protocol";
import { unfinished, type Operation } from "./journal";
export interface PublicationIO {
    encrypted(): boolean;
    locked(paths: string[], run: () => Promise<boolean>): Promise<boolean>;
}
export class Publisher {
    private running?: Promise<void>;
    private requested = false;
    private transport: ControlTransport;
    private operationIndex = new Map<string, number>();
    private indexedOperations = 0;
    private active = new Set<string>();
    private pendingAcks = new Set<string>();
    private ackIndex = new Map<string, Set<string>>();
    private receiptsSeeded = false;
    constructor(
        private client: IntentClient,
        private db: DatabaseFileAccess,
        private io: PublicationIO,
    ) {
        this.transport = new ControlTransport(db);
        client.onPublish = id => this.pump(id);
        client.onAck = (ack) => this.acknowledge(ack);
        client.onControl = record => this.controlArrived(record);
    }
    pump(id?: string): Promise<void> {
        if (this.client.stopped) return Promise.resolve();
        if (id) this.active.add(id);
        this.requested = true;
        if (this.running) return this.running;
        this.running = (async () => {
            do {
                this.requested = false;
                await this.run();
            } while (this.requested);
        })().finally(() => {
            this.running = undefined;
        });
        return this.running;
    }
    private get(id: string) {
        const operations = this.client.journal.state.operations;
        while (this.indexedOperations < operations.length) {
            const n = this.indexedOperations++, op = operations[n];
            this.operationIndex.set(op.intent.id, n);
            if (unfinished(op) && !["held", "conflict", "uploaded"].includes(op.state)) this.active.add(op.intent.id);
        }
        const index = this.operationIndex.get(id);
        return index === undefined ? undefined! : operations[index];
    }
    get needsRetry() { return [...this.active].some(id => {
        const op = this.get(id);
        return op && unfinished(op) && !["held", "conflict", "uploaded"].includes(op.state);
    }); }
    private indexAck(ack: Ack) {
        const ids = this.ackIndex.get(ack.intentId) ?? new Set<string>();
        ids.add(ack.id); this.ackIndex.set(ack.intentId, ids);
    }
    private async controlArrived(record: Control) {
        const id = record.kind === "publication" ? record.intentId : record.id;
        for (const ack of this.ackIndex.get(id) ?? []) this.pendingAcks.add(ack);
        await this.drainAcks();
    }
    async settled() { await this.running; }
    private async update(id: string, fn: (o: Operation) => void) {
        await this.client.journal.update((s) => {
            fn(s.operations.find((o) => o.intent.id === id)!);
        });
        this.client.io.changed();
    }
    private async run() {
        await this.client.ready;
        await this.client.settled();
        if (this.client.journal.failure) return;
        if (!this.io.encrypted()) {
            await this.client.hold("transport", "native_encryption_required");
            return;
        }
        this.get(""); // Index only newly appended operations; history is retained.
        for (const id of [...this.active]) {
            let op = this.get(id);
            if (!op || !unfinished(op) || ["conflict", "held", "uploaded"].includes(op.state)) {
                this.active.delete(id); continue;
            }
            if (this.client.journal.state.operations.some(attempt => unfinished(attempt) &&
                !attempt.ack && attempt.resolution?.covered.includes(id))) continue;
            if (op.writing) {
                await this.update(id, (o) => {
                    o.state = "held";
                    o.reason = "native_write_receipt_gap";
                });
                continue;
            }
            const pred = op.intent.predecessor ? this.get(op.intent.predecessor) : undefined;
            if (
                op.intent.predecessor &&
                (!pred || !pred.publication || ["held", "conflict"].includes(pred.state))
            )
                continue;
            try {
                await this.io.locked(
                    [op.intent.path, ...(op.intent.target ? [op.intent.target] : [])],
                    async () => {
                        op = this.get(id);
                        if (op.ack || ["applied", "conflict", "held"].includes(op.state)) return true;
                        const i = op.intent;
                        const intentDigest = await digest(encode(i));
                        if ((await digest(op.payload)) !== i.sha256 && op.state !== "applied")
                            throw new Error("journal_payload_mismatch");
                        await this.transport.put(i);
                        // CouchDB transfers leaf bodies, not every historical body.
                        // Retain each exact payload independently of the mutable note.
                        await this.transport.putPayload(i.id, op.payload);
                        if (!op.publication) {
                            const parent = pred
                                ? pred.publication?.revisions.find(
                                      (r) => r.role === "content" && r.path === i.path,
                                  )?.revision
                                : i.base?.nativeRevision;
                            if (i.operation !== "create" && !parent)
                                throw new Error("missing_exact_native_base");
                            const writes: { path: string; role: "content" | "deletion" }[] =
                                i.operation === "rename"
                                    ? [
                                          { path: i.target!, role: "content" },
                                          { path: i.path, role: "deletion" },
                                      ]
                                    : [
                                          {
                                              path: i.path,
                                              role: i.operation === "delete" ? "deletion" : "content",
                                          },
                                      ];
                            for (const w of writes) {
                                if (
                                    this.get(id).revisions.some((r) => r.path === w.path && r.role === w.role)
                                )
                                    continue;
                                if (!this.io.encrypted()) throw new Error("native_encryption_required");
                                // Only explicit create/rename can write an absent target;
                                // a colliding target is never silently adopted as a base.
                                if (
                                    w.role === "content" &&
                                    (i.operation === "create" || i.operation === "rename")
                                ) {
                                    if (
                                        await this.db.fetchEntryMeta(
                                            w.path as FilePathWithPrefix,
                                            undefined,
                                            true,
                                        )
                                    )
                                        throw new Error("native_target_exists");
                                }
                                await this.update(id, (o) => {
                                    o.state = "publishing";
                                    o.writing = w.path;
                                });
                                const now = Date.now();
                                const file = {
                                    path: w.path,
                                    name: w.path.split("/").pop()!,
                                    body: createTextBlob(op.payload),
                                    stat: { type: "file", ctime: now, mtime: now, size: i.length },
                                } as UXFileInfo;
                                const rev =
                                    w.role === "deletion"
                                        ? await this.db.storeDeletionWithBaseRevision(
                                              w.path as FilePathWithPrefix,
                                              parent!,
                                          )
                                        : i.operation === "create" || i.operation === "rename"
                                          ? await this.db.storeIndependentRevision(file, true)
                                          : await this.db.storeWithBaseRevision(file, parent!, true);
                                if (!rev) throw new Error("native_write_not_confirmed");
                                await this.update(id, (o) => {
                                    o.revisions.push({ ...w, revision: rev });
                                    delete o.writing;
                                });
                            }
                            const publication: Publication = {
                                version: 1,
                                kind: "publication",
                                id: "pub_" + id,
                                intentId: id,
                                intentDigest,
                                revisions: this.get(id).revisions,
                            };
                            if (!pair(i, publication)) throw new Error("invalid_native_pair");
                            await this.update(id, (o) => {
                                o.publication = publication;
                            });
                        }
                        await this.transport.put(this.get(id).publication!);
                        await this.update(id, (o) => {
                            if (o.ack) return;
                            o.state = "uploaded";
                            delete o.reason;
                        });
                        return true;
                    },
                );
                await this.drainAcks();
            } catch (e) {
                if (this.client.journal.failure) return;
                const reason = e instanceof Error ? e.message : "publication_failed";
                // Immutable/native ambiguities cannot be made safe by retrying.
                const terminal =
                    this.get(id).writing ||
                    [
                        "native_target_exists",
                        "control_collision",
                        "journal_payload_mismatch",
                        "missing_exact_native_base",
                    ].includes(reason);
                await this.update(id, (o) => {
                    if (o.ack) return;
                    o.state = terminal ? "held" : "queued";
                    o.reason = reason;
                });
            }
            op = this.get(id);
            if (op && ["uploaded", "applied", "held", "conflict"].includes(op.state)) this.active.delete(id);
        }
    }
    async acknowledge(ack: Ack) {
        validate(ack);
        await this.client.ready;
        await this.client.settled();
        await this.client.journal.update((s) => {
            s.acks ??= {};
            const old = s.acks[ack.id];
            if (old && encode(old) !== encode(ack)) throw new Error("ack_collision");
            s.acks[ack.id] = ack;
        }, s => !!s.acks?.[ack.id] && encode(s.acks[ack.id]) === encode(ack));
        this.indexAck(ack);
        this.pendingAcks.add(ack.id);
        await this.drainAcks();
    }
    async drainAcks(recover = false) {
        if (!this.receiptsSeeded || recover) {
            for (const ack of Object.values(this.client.journal.state.acks ?? {})) {
                this.indexAck(ack); this.pendingAcks.add(ack.id);
            }
            this.receiptsSeeded = true;
        }
        if (!this.pendingAcks.size) return;
        for (const id of [...this.pendingAcks]) {
            const ack = this.client.journal.state.acks![id];
            const op = this.get(ack.intentId);
            if (!op) {
                // Retained foreign receipts wake again when their exact join arrives.
                this.pendingAcks.delete(id);
                const i = this.client.journal.state.intents?.[ack.intentId];
                const p = this.client.journal.state.publications?.[ack.publicationId];
                const b = ack.binding;
                if (i && p && b && ack.status === "applied" && ack.resolves?.length &&
                    pair(i, p) && ack.intentDigest === await digest(encode(i)) &&
                    ack.intentDigest === p.intentDigest && JSON.stringify(ack.resolves) === JSON.stringify(i.resolves) &&
                    b.path === i.path && b.fileId === i.fileId && b.workspace === i.workspace &&
                    b.collection === i.collection && b.generation === i.generation && b.sha256 === i.sha256 &&
                    b.nativeRevision === p.revisions.find(r => r.role === "content")?.revision) {
                    await this.client.journal.update(s => {
                        for (const old of s.operations) if (ack.resolves!.includes(old.intent.id) && old.ack?.status === "conflict" &&
                            old.intent.path === i.path && old.intent.fileId === i.fileId && old.intent.workspace === i.workspace &&
                            old.intent.collection === i.collection && old.intent.generation === i.generation) {
                            old.resolvedBy = i.id;
                            (s.resolutionDisplays ??= {})[i.path] = { binding: b.id, localHash: old.intent.sha256 };
                        }
                        s.candidates[b.id] = b;
                    }, s => {
                        const covered = s.operations.filter(old => ack.resolves!.includes(old.intent.id) &&
                            old.ack?.status === "conflict" && old.intent.path === i.path && old.intent.fileId === i.fileId &&
                            old.intent.workspace === i.workspace && old.intent.collection === i.collection && old.intent.generation === i.generation);
                        const display = s.resolutionDisplays?.[i.path];
                        return JSON.stringify(s.candidates[b.id]) === JSON.stringify(b) &&
                            covered.every(old => old.resolvedBy === i.id) && (!covered.length ||
                                (display?.binding === b.id && display.localHash === covered[covered.length - 1].intent.sha256));
                    });
                    this.pendingAcks.delete(id);
                    this.client.onReflect(b.path);
                }
                continue;
            }
            if (!op.publication) continue; // arrival order is not authority
            if (op.ack) {
                if (encode(op.ack) !== encode(ack)) {
                    await this.client.hold(op.intent.path, "contradictory_source_ack");
                    continue;
                }
                if (op.state === ack.status && !(ack.status === "applied" && op.resolution &&
                    op.resolution.covered.some(id => this.get(id)?.resolvedBy !== op.intent.id))) {
                    this.pendingAcks.delete(id); continue;
                }
                // Older clients could overwrite accepted state when publication
                // finished after receipt delivery. Revalidate the retained ack.
            }
            const i = op.intent,
                p = op.publication;
            if (i.predecessor && this.get(i.predecessor)?.state !== "applied" && ack.status === "applied")
                continue;
            if (
                ack.intentDigest !== p.intentDigest ||
                ack.intentDigest !== (await digest(encode(i))) ||
                ack.publicationId !== p.id ||
                !pair(i, p)
            ) {
                await this.client.hold(i.path, "ack_pair_mismatch");
                continue;
            }
            if (ack.status === "applied" && JSON.stringify(ack.resolves ?? []) !== JSON.stringify(i.resolves ?? [])) {
                await this.client.hold(i.path, "resolution_receipt_mismatch");
                continue;
            }
            const b = ack.binding;
            if (ack.status === "applied" && i.operation !== "delete") {
                const content = p.revisions.find((r) => r.role === "content");
                if (
                    !b ||
                    b.fileId !== i.fileId ||
                    b.workspace !== i.workspace ||
                    b.collection !== i.collection ||
                    b.generation !== i.generation ||
                    b.path !== (i.target ?? i.path) ||
                    b.nativeRevision !== content?.revision ||
                    b.sha256 !== i.sha256
                ) {
                    await this.client.hold(i.path, "ack_binding_mismatch");
                    continue;
                }
            }
            // Receipt and retirement links are one durable journal update. A
            // restart must not observe accepted bytes with unresolved history.
            await this.client.journal.update(s => {
                const o = s.operations.find(o => o.intent.id === i.id)!;
                o.ack = ack;
                o.state = ack.status;
                o.reason = ack.reason;
                if (ack.status === "applied" && op.resolution && b) {
                    for (const old of s.operations) if (op.resolution!.covered.includes(old.intent.id)) old.resolvedBy = i.id;
                    (s.resolutionDisplays ??= {})[i.path] = { binding: b.id, localHash: op.resolution!.localHash };
                    s.candidates[b.id] = b;
                }
            });
            this.pendingAcks.delete(id);
            this.active.delete(i.id);
            this.client.onReflect(i.target ?? i.path);
            const later = this.client.journal.state.operations.some(
                (o) => unfinished(o) && o.intent.path === i.path,
            );
            if (ack.status === "applied" && !later) {
                if (i.operation === "delete") {
                    await this.client.journal.update((s) => {
                        if (s.bindings[i.path]?.fileId === i.fileId) delete s.bindings[i.path];
                    });
                } else if (b) {
                    // An acknowledgement does not prove which bytes are in the editor.
                    const text = await this.client.io.read(b.path);
                    if (text !== null && (await digest(text)) === b.sha256) {
                        if (i.operation === "rename")
                            await this.client.journal.update((s) => {
                                delete s.bindings[i.path];
                            });
                        await this.client.install(b);
                    } else if (op.resolution) await this.client.reflectCandidate(b);
                    else await this.client.hold(b.path, "applied_binding_waiting_for_local_match");
                }
            }
        }
        // Retain immutable receipt/intent metadata; retire only acknowledged
        // payloads with no unresolved descendant dependency. Never GC a conflict.
        await this.client.journal.update((s) => {
            const needed = new Set(
                s.operations
                    .filter(unfinished)
                    .map((o) => o.intent.predecessor)
                    .filter(Boolean),
            );
            let changed = true;
            while (changed) {
                changed = false;
                for (const o of s.operations)
                    if (
                        needed.has(o.intent.id) &&
                        o.intent.predecessor &&
                        !needed.has(o.intent.predecessor)
                    ) {
                        needed.add(o.intent.predecessor);
                        changed = true;
                    }
            }
            for (const o of s.operations)
                if (o.state === "applied" && !needed.has(o.intent.id)) o.payload = "";
        }, s => !s.operations.some(o => o.state === "applied" && o.payload !== ""));
        this.client.io.changed();
    }
}
