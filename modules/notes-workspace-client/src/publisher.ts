import type { DatabaseFileAccess } from "@vrtmrz/livesync-commonlib/compat/interfaces/DatabaseFileAccess";
import type { FilePathWithPrefix, UXFileInfo } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { createTextBlob } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import { ControlTransport } from "./control";
import { IntentClient } from "./client";
import { encode, digest, pair, validate, type Ack, type Publication, type Control } from "./protocol";
import { unfinished, type Operation } from "./journal";
import type { CreateRecoveryReview, NativeTarget } from "./native-target";
// Pinned native trees retain 100 revisions. Leave room for exact source bases
// and rename branches while keeping ordinary editor publication pipelined.
const MAX_UNACCEPTED_PREDECESSORS = 32;
export interface PublicationIO {
    encrypted(): boolean;
    locked(paths: string[], run: () => Promise<boolean>): Promise<boolean>;
    target?(path: string): Promise<NativeTarget>;
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
        client.inspectCreateRecovery = id => this.inspectRecovery(id);
        client.recoverCreate = review => this.recover(review);
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
    private runnable(op: Operation | undefined) {
        if (!op || !unfinished(op) || ["held", "conflict", "uploaded"].includes(op.state)) return false;
        const pred = op.intent.predecessor ? this.get(op.intent.predecessor) : undefined;
        if (op.intent.predecessor && (!pred?.publication || ["held", "conflict"].includes(pred.state))) return false;
        const seen = new Set<string>();
        let ancestor = pred;
        let recovering = false;
        let unaccepted = 0;
        while (ancestor) {
            if (seen.has(ancestor.intent.id)) return false;
            seen.add(ancestor.intent.id);
            if (ancestor.state !== "applied" && ++unaccepted >= MAX_UNACCEPTED_PREDECESSORS) return false;
            if (ancestor.nativeRecovery) {
                recovering = true;
                if (ancestor.state !== "applied") return false;
            }
            ancestor = ancestor.intent.predecessor ? this.get(ancestor.intent.predecessor) : undefined;
        }
        // A retained recovery can exceed native revision-tree retention. Main
        // must verify each exact parent before later writes can prune it.
        if (recovering && pred?.state !== "applied") return false;
        return !op.intent.dependencies?.some(id => this.get(id)?.state !== "applied") &&
            !this.client.journal.state.operations.some(attempt => unfinished(attempt) &&
                !attempt.ack && attempt.resolution?.covered.includes(op.intent.id));
    }
    get needsRetry() { this.get(""); return [...this.active].some(id => this.runnable(this.get(id))); }
    private async target(path: string): Promise<NativeTarget> {
        if (this.io.target) return this.io.target(path);
        return { kind: await this.db.fetchEntryMeta(path as FilePathWithPrefix, undefined, true) ? "occupied" : "absent", token: "" };
    }
    private async inspectRecovery(id: string): Promise<CreateRecoveryReview> {
        await this.client.ready; await this.client.settled();
        const op = this.get(id);
        if (!op || op.state !== "held" || op.reason !== "native_target_exists" || op.intent.operation !== "create" ||
            op.intent.entryKind || op.intent.predecessor || op.intent.base || op.ack || op.publication || op.writing ||
            op.revisions.length || await digest(op.payload) !== op.intent.sha256)
            throw Error("This operation needs a different recovery; no edits were changed.");
        const target = await this.target(op.intent.path);
        if (!["deleted", "absent"].includes(target.kind)) throw Error("The destination has live or unknown revisions; recreation is refused.");
        const chain = new Set([id]);
        const descendants: Operation[] = [];
        for (const child of this.client.journal.state.operations) if (child.intent.predecessor && chain.has(child.intent.predecessor)) {
            chain.add(child.intent.id); descendants.push(child);
        }
        return { intentId: id, path: op.intent.path, descendants: descendants.length,
            token: await digest(JSON.stringify([encode(op.intent), op.payload, target.token,
                descendants.map(child => [encode(child.intent), child.payload, child.state, child.publication, child.ack])])) };
    }
    private async recover(review: CreateRecoveryReview) {
        const acquired = await this.io.locked([review.path], async () => {
            const current = await this.inspectRecovery(review.intentId);
            if (JSON.stringify(current) !== JSON.stringify(review)) throw Error("Recovery evidence changed; inspect it again.");
            await this.update(review.intentId, op => { op.state = "queued"; delete op.reason; op.nativeRecovery = true; });
            this.active.add(review.intentId);
            return true;
        });
        if (!acquired) throw Error("This note is busy; recovery was not applied.");
        await this.pump(review.intentId);
    }
    private indexAck(ack: Ack) {
        const ids = this.ackIndex.get(ack.intentId) ?? new Set<string>();
        ids.add(ack.id); this.ackIndex.set(ack.intentId, ids);
    }
    private async controlArrived(record: Control) {
        const id = record.kind === "publication" ? record.intentId : record.id;
        for (const ack of this.ackIndex.get(id) ?? []) this.pendingAcks.add(ack);
        await this.drainAcks();
        if (this.requested) void this.pump().catch(() => this.client.io.changed());
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
        // Reserve a folder's logical paths before publishing its child moves.
        // The complete publication still waits for exact child acknowledgements.
        for (const id of this.active) {
            const op = this.get(id);
            if (op?.intent.entryKind === "directory" && op.state === "queued")
                await this.transport.put(op.intent);
        }
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
            if (!this.runnable(op)) continue;
            const pred = op.intent.predecessor ? this.get(op.intent.predecessor) : undefined;
            if (
                op.intent.predecessor &&
                (!pred || !pred.publication || ["held", "conflict"].includes(pred.state))
            )
                continue;
            if (op.intent.dependencies?.some(id => this.get(id)?.state !== "applied")) continue;
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
                        const payloadRevision = await this.transport.putPayload(i.id, op.payload);
                        if (i.entryKind === "directory" && !op.publication) {
                            const publication: Publication = { version: 1, kind: "publication", id: "dir_pub_" + id,
                                intentId: id, intentDigest, entryKind: "directory",
                                revisions: [{ path: i.target ?? i.path, revision: payloadRevision,
                                    role: i.operation === "delete" ? "deletion" : "content" }] };
                            await this.update(id, o => { o.publication = publication; });
                            op = this.get(id);
                        }
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
                                    const target = await this.target(w.path);
                                    if (!["absent", "deleted"].includes(target.kind))
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
                                id: (id.startsWith("canvas_") ? "canvas_pub_" : "pub_") + id,
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
        if (this.requested) void this.pump().catch(() => this.client.io.changed());
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
                await this.client.reflectPathChanges();
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
            if (ack.status === "applied") this.requested = true;
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
                    const text = b.entryKind === "directory" ?
                        await this.client.io.directoryExists?.(b.path) ? "" : null : await this.client.io.read(b.path);
                    if (text !== null && (await digest(text)) === b.sha256) {
                        if (i.operation === "rename")
                            await this.client.journal.update((s) => {
                                delete s.bindings[i.path];
                                if (["missing_writable_binding","directory_base_unavailable"].includes(s.holds[i.path]))
                                    delete s.holds[i.path];
                            });
                        await this.client.install(b);
                    } else if (op.resolution) await this.client.reflectCandidate(b);
                    else await this.client.hold(b.path, "applied_binding_waiting_for_local_match");
                }
            }
        }
        for (const o of this.client.journal.state.operations)
            if (o.intent.dependencies?.length && o.state === "queued" &&
                o.intent.dependencies.every(id => this.get(id)?.state === "applied")) {
                this.active.add(o.intent.id);
                this.requested = true;
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
