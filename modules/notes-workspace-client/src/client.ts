import type { MetaEntry } from "@vrtmrz/livesync-commonlib/compat/common/types";
import type { LoomIntentHooks } from "@vrtmrz/livesync-commonlib/compat/serviceModules/ServiceFileHandlerBase";
import { Journal, unfinished } from "./journal";
import { checkResolutionText, inspectConflict, type ConflictReview } from "./conflicts";
import {
    MAX_BYTES,
    digest,
    relativePath,
    referencePath,
    reserved,
    newerSource,
    type Binding,
    type Control,
    type Intent,
} from "./protocol";
export interface ClientIO {
    read(path: string): Promise<string | null>;
    readControl(path: string, revision: string): Promise<Control>;
    readRevision(path: string, revision: string): Promise<string | null>;
    readReference?(path: string, revision?: string): Promise<Uint8Array<ArrayBuffer> | null>;
    descends(path: string, revision: string, ancestor: string): Promise<boolean>;
    reflect(binding: Binding): Promise<void>;
    conflictLeaves?(path: string): Promise<{ revision: string; text: string }[]>;
    changed(): void;
}
type Cursor = { binding: Binding; path: string; predecessor?: string; absent?: boolean };
type Event = {
    kind: "create" | "edit" | "delete" | "rename";
    path: string;
    oldPath?: string;
    folder?: boolean;
};
export class IntentClient implements LoomIntentHooks {
    readonly ready: Promise<void>;
    private cursors = new Map<string, Cursor>();
    private admissions = Promise.resolve();
    private started = false;
    private indexedCandidates?: Record<string, Binding>;
    private candidatesByPath = new Map<string, Binding[]>();
    private pathCandidates(path: string): Binding[] {
        const candidates = this.journal.state.candidates;
        if (this.indexedCandidates !== candidates) {
            this.candidatesByPath.clear();
            for (const b of Object.values(candidates)) {
                const group = this.candidatesByPath.get(b.path) ?? [];
                group.push(b);
                this.candidatesByPath.set(b.path, group);
            }
            this.indexedCandidates = candidates;
        }
        return this.candidatesByPath.get(path) ?? [];
    }
    // A superseded ordinary edit never reached the journal. Only a subsequent
    // ordinary edit can skip it; rename/delete and other ambiguity stay held.
    private resolutions = new Map<string, Cursor | { supersededEdit: Cursor } | false>();
    resumeNative: (recover?: boolean) => Promise<void> = async () => {};
    stopped = false;
    stopNative: () => Promise<void> = async () => { this.stopped = true; };
    onPublish: (id?: string) => Promise<void> = async () => {};
    onAck: (record: Extract<Control, { kind: "ack" }>) => Promise<void> = async () => {};
    onControl: (record: Control) => Promise<void> = async () => {};
    onReflect: (path: string) => void = path => {
        setTimeout(() => { void this.reflectPath(path).catch(() => this.io.changed()); }, 0);
    };
    private parked = new Map<string, string>();
    async reflectPath(path: string, retry = false) {
        if (retry) this.parked.delete(path);
        for (const b of this.pathCandidates(path)) await this.reflectCandidate(b);
    }
    constructor(
        readonly journal: Journal,
        readonly io: ClientIO,
    ) {
        this.ready = this.start();
        void this.ready.catch(() => io.changed());
    }
    private async start() {
        await this.journal.ready;
        for (const b of Object.values(this.journal.state.bindings))
            this.cursors.set(b.path, { binding: b, path: b.path });
        for (const op of this.journal.state.operations) {
            if (!unfinished(op)) continue;
            const i = op.intent;
            if (i.operation === "delete") {
                this.cursors.delete(i.path);
                continue;
            }
            if (i.operation === "rename") this.cursors.delete(i.path);
            const b =
                i.base ??
                Object.values(this.journal.state.bindings).find(
                    (b) =>
                        b.workspace === i.workspace &&
                        b.collection === i.collection &&
                        b.generation === i.generation,
                );
            if (b)
                for (const [path, cursor] of this.cursors)
                    if (cursor.binding.fileId === i.fileId && path !== (i.target ?? i.path))
                        this.cursors.delete(path);
            if (b)
                this.cursors.set(i.target ?? i.path, {
                    binding: { ...b, fileId: i.fileId },
                    path: i.target ?? i.path,
                    predecessor: i.id,
                    absent: !i.base,
                });
        }
        this.started = true;
        // A retained reflection marker authorises only that exact revision/hash.
        for (const b of Object.values(this.journal.state.reflecting)) {
            const text = await this.readBinding(b);
            if (text !== null && (await digest(text)) === b.sha256) await this.install(b);
            else await this.hold(b.path, "interrupted_reflection");
        }
    }
    async hold(path: string, reason: string) {
        await this.journal.update((s) => {
            s.holds[path] = reason;
        }, s => s.holds[path] === reason);
        this.io.changed();
    }
    private readBinding(b: Binding, revision?: string) {
        if (referencePath(b.path)) return this.io.readReference?.(b.path, revision) ?? Promise.resolve(null);
        return revision ? this.io.readRevision(b.path, revision) : this.io.read(b.path);
    }
    private inside(b: Binding, path: string) {
        return relativePath(path) && path.startsWith(b.collectionRoot + "/");
    }
    private pending(path: string) {
        return this.journal.state.operations.some(
            (o) => unfinished(o) && (o.intent.path === path || o.intent.target === path),
        );
    }
    private snapshot(path: string) {
        const x = this.cursors.get(path);
        return x ? structuredClone(x) : undefined;
    }
    private creationBinding(path: string): Binding | undefined {
        const matches = Object.values(this.journal.state.bindings).filter(b => b.writable && this.inside(b, path));
        const scopes = new Set(matches.map(b => JSON.stringify([b.workspace, b.collection, b.collectionRoot])));
        if (scopes.size !== 1) return;
        const first = matches[0];
        const evidence = [...matches, ...Object.values(this.journal.state.candidates).filter(b =>
            b.workspace === first.workspace && b.collection === first.collection && b.collectionRoot === first.collectionRoot)];
        const generations = new Set(evidence.map(b => b.generation));
        const edges = new Map<string, Set<string>>();
        const files = new Map<string, Binding[]>();
        for (const b of evidence) {
            const key = JSON.stringify([b.fileId, b.path]);
            const group = files.get(key) ?? [];
            group.push(b); files.set(key, group);
        }
        for (const group of files.values()) for (const old of group) for (const next of group) {
            if (old.generation === next.generation || !newerSource(next, old)) continue;
            const successors = edges.get(old.generation) ?? new Set<string>();
            successors.add(next.generation); edges.set(old.generation, successors);
        }
        const reaches = (from: string, to: string, seen = new Set<string>()): boolean => {
            if (from === to) return true;
            if (seen.has(from)) return false;
            seen.add(from);
            return [...(edges.get(from) ?? [])].some(next => reaches(next, to, seen));
        };
        // A generation is usable only with same-file enrollment proof from every
        // retained generation. Competing branches and cycles never choose a winner.
        const winners = [...generations].filter(g =>
            [...generations].every(other => reaches(other, g)) &&
            ![...(edges.get(g) ?? [])].some(next => reaches(next, g)));
        if (winners.length !== 1) return;
        return matches.find(b => b.generation === winners[0]);
    }
    async registerLocalNote(path: string): Promise<void> {
        await this.ready;
        await this.settled();
        if (this.stopped || !relativePath(path) || reserved(path) || referencePath(path))
            throw Error("local_note_not_writable");
        if (this.cursors.has(path) || this.journal.state.bindings[path] || this.pathCandidates(path).length ||
            this.pending(path) || this.journal.state.reflecting[path]) throw Error("local_note_already_tracked");
        if (!this.creationBinding(path)) throw Error("local_note_scope_ambiguous");
        if (await this.io.read(path) === null) throw Error("local_note_missing");
        // The file read yields; another creation or registration may have claimed
        // the path meanwhile. Never admit two independent creates for one note.
        if (this.cursors.has(path) || this.pathCandidates(path).length || this.pending(path))
            throw Error("local_note_already_tracked");
        this.capture({ kind: "create", path });
        await this.settled();
        if (!this.pending(path)) throw Error(this.journal.state.holds[path] ?? "local_note_registration_held");
    }
    // This call is synchronous at the original vault callback, before native
    // debounce/coalescing can turn rename/delete into a different operation.
    capture(event: Event): void {
        if (this.stopped) return;
        if (reserved(event.path) || (event.oldPath && reserved(event.oldPath))) return;
        this.parked.delete(event.path);
        if (event.oldPath) this.parked.delete(event.oldPath);
        if (!this.started) {
            this.admissions = this.admissions.then(async () => {
                await this.ready;
                await this.hold(event.path, "event_before_binding_ready");
            });
            return;
        }
        if (event.folder) {
            if (event.kind !== "rename" && event.kind !== "delete") return;
            const from = event.oldPath ?? event.path;
            const children = [...this.cursors.keys()].filter((p) => p.startsWith(from + "/"));
            if (children.length === 0 || children.length > 256) {
                this.admissions = this.admissions.then(() => this.hold(from, "folder_scope_or_limit"));
                return;
            }
            // Synchronous bounded expansion. Every child remains an independent,
            // durable operation. Untracked descendants are held by normal scans.
            for (const path of children)
                this.capture({
                    kind: event.kind,
                    path: event.kind === "rename" ? event.path + path.slice(from.length) : path,
                    oldPath: event.kind === "rename" ? path : undefined,
                });
            return;
        }
        const path = event.oldPath ?? event.path;
        let cursor = this.snapshot(path);
        const reflection = this.journal.state.reflecting[event.path];
        if (referencePath(path) && reflection) return;
        if (cursor && !cursor.binding.writable) {
            if (!reflection) this.admissions = this.admissions.then(() => this.hold(path, "reference_only"));
            return;
        }
        const id = crypto.randomUUID();
        // Only an explicit creation event may obtain a fresh logical identity.
        // A source binding declares scope; source owner still checks live membership.
        if (!cursor && event.kind === "create") {
            const binding = this.creationBinding(event.path);
            if (binding) {
                cursor = {
                    binding: { ...binding, fileId: crypto.randomUUID(), path: event.path },
                    path: event.path,
                    absent: true,
                };
            }
        }
        let captured = cursor;
        // Launch read at event admission, then verify stability before committing.
        const payloadRead = event.kind === "delete" ? Promise.resolve("") : this.io.read(event.path);
        void payloadRead.catch(() => {});
        if (captured) {
            const next = { ...captured, path: event.path, predecessor: id };
            if (event.kind === "rename" || event.kind === "delete") this.cursors.delete(path);
            if (event.kind !== "delete") this.cursors.set(event.path, next);
        }
        this.admissions = this.admissions
            .then(async () => {
                if (this.journal.failure) throw new Error(this.journal.failure);
                if (captured?.predecessor && this.resolutions.has(captured.predecessor)) {
                    const resolved = this.resolutions.get(captured.predecessor);
                    if (!resolved) {
                        this.resolutions.set(id, false);
                        return this.hold(path, "predecessor_capture_held");
                    }
                    if ("supersededEdit" in resolved) {
                        if (event.kind !== "edit") {
                            this.resolutions.set(id, false);
                            return this.hold(path, "predecessor_capture_held");
                        }
                        captured = { ...resolved.supersededEdit, path: captured.path };
                    } else {
                        captured = { ...resolved, path: captured.path };
                    }
                }
                this.resolutions.set(id, false);
                const payload = await payloadRead;
                if (payload === null) return this.hold(path, "event_payload_missing");
                const sha = await digest(payload);
                if (
                    (event.kind === "create" || event.kind === "edit") &&
                    reflection &&
                    sha === reflection.sha256
                ) {
                    // Native reflection provenance, never an mtime suppression window.
                    const origin = { binding: reflection, path: event.path };
                    this.resolutions.set(id, origin);
                    if (this.cursors.get(event.path)?.predecessor === id)
                        this.cursors.set(event.path, origin);
                    return;
                }
                if (!captured || !captured.binding.writable)
                    return this.hold(path, "missing_writable_binding");
                const b = captured.binding;
                if (!this.inside(b, event.path) || !this.inside(b, path))
                    return this.hold(path, "outside_collection");
                if (
                    event.kind === "rename" &&
                    this.journal.state.bindings[event.path]?.fileId !== undefined &&
                    this.journal.state.bindings[event.path].fileId !== b.fileId
                )
                    return this.hold(path, "rename_target_collision");
                const length = new TextEncoder().encode(payload).length;
                if (length > MAX_BYTES) return this.hold(path, "payload_limit");
                if (event.kind !== "delete" && (await this.io.read(event.path)) !== payload) {
                    if (event.kind === "edit") this.resolutions.set(id, { supersededEdit: captured });
                    return this.hold(path, "event_payload_changed");
                }
                if (event.kind === "edit" && !captured.predecessor && sha === b.sha256) {
                    this.resolutions.set(id, captured);
                    if (this.cursors.get(path)?.predecessor === id) this.cursors.set(path, captured);
                    if (this.journal.state.holds[path] === "event_payload_changed") {
                        await this.journal.update((s) => {
                            delete s.holds[path];
                        });
                        this.io.changed();
                    }
                    return;
                }
                const intent: Intent = {
                    version: 1,
                    kind: "intent",
                    id,
                    device: this.journal.state.device,
                    operation: event.kind,
                    workspace: b.workspace,
                    collection: b.collection,
                    generation: b.generation,
                    fileId: b.fileId,
                    base: captured.absent ? null : b,
                    predecessor: event.kind === "create" ? undefined : captured.predecessor,
                    path,
                    sha256: sha,
                    length,
                    ...(event.kind === "rename" ? { target: event.path } : {}),
                };
                await this.journal.update((s) => {
                    const predecessor = s.operations.find(o => o.intent.id === intent.predecessor);
                    const duringResolution = predecessor && unfinished(predecessor) &&
                        (!!predecessor.resolution || predecessor.reason === "edit_during_resolution");
                    s.operations.push({ intent, payload, state: duringResolution ? "held" : "queued", revisions: [],
                        ...(duringResolution ? { reason: "edit_during_resolution" } : {}) });
                    delete s.holds[path];
                });
                this.resolutions.set(id, { ...captured, predecessor: id, path: event.path });
                this.io.changed();
            })
            .catch(async (e) => {
                if (!this.journal.failure) await this.hold(path, "capture_failed");
                this.io.changed();
            });
        void this.admissions.then(() => this.onPublish(id)).catch(() => this.io.changed());
    }
    async settled() {
        await this.admissions;
    }
    async resolve(review: ConflictReview, text: string): Promise<string> {
        checkResolutionText(text);
        await this.ready;
        await this.settled();
        if (this.stopped || this.journal.failure) throw Error("Notes sync is not ready.");
        const id = crypto.randomUUID();
        const previous = this.snapshot(review.path);
        this.cursors.set(review.path, { binding: review.binding, path: review.path, predecessor: id });
        const run = this.admissions.then(async () => {
            const fresh = await inspectConflict(this, review.path, false);
            if (fresh.token !== review.token) throw Error("This note changed while you reviewed it. Refresh the comparison.");
            review = fresh;
            const intent: Intent = { version: 1, kind: "intent", id, device: this.journal.state.device,
                operation: "edit", workspace: review.binding.workspace, collection: review.binding.collection,
                generation: review.binding.generation, fileId: review.binding.fileId, base: review.binding,
                path: review.path, sha256: await digest(text), length: new TextEncoder().encode(text).length,
                ...(review.ids.length ? { resolves: review.ids } : {}) };
            const localHash = await digest(review.local);
            await this.journal.update(s => {
                s.operations.push({ intent, payload: text, state: "queued", revisions: [],
                    resolution: { localHash, covered: review.covered, leaves: review.leaves } });
            });
            this.resolutions.set(id, { binding: review.binding, path: review.path, predecessor: id });
            this.io.changed();
        });
        this.admissions = run.catch(() => {
            this.resolutions.set(id, false);
            if (this.cursors.get(review.path)?.predecessor === id) {
                if (previous) this.cursors.set(review.path, previous);
                else this.cursors.delete(review.path);
            }
        });
        await run;
        void this.onPublish(id).catch(() => this.io.changed());
        return id;
    }
    async mutation(input: Parameters<LoomIntentHooks["mutation"]>[0]): Promise<boolean> {
        if (this.stopped) return false;
        await this.ready;
        await this.admissions;
        const path = typeof input.info === "string" ? input.info : input.info.path;
        if (input.kind === "resolve") {
            await this.hold(path, "native_conflict_requires_resolution");
            return false;
        }
        if (reserved(path)) return true;
        if (this.journal.failure) return false;
        const binding = this.journal.state.bindings[path];
        if (binding && !binding.writable) {
            const content = await this.readBinding(binding);
            if (content === null || await digest(content) !== binding.sha256)
                await this.hold(path, "reference_local_change");
            return true;
        }
        if (referencePath(path)) return true;
        if (input.kind === "store") {
            const b = this.cursors.get(path);
            const text = await this.io.read(path);
            if (!b || text === null) {
                await this.hold(path, text === null ? "scanner_absence_is_not_delete" : "untracked_scan");
                return true;
            }
            const sha = await digest(text);
            const latest = [...this.journal.state.operations]
                .reverse()
                .find((o) => o.intent.fileId === b.binding.fileId);
            const expected = latest && unfinished(latest) ? latest.intent.sha256 : b.binding.sha256;
            if (sha !== expected) {
                this.capture({ kind: "edit", path });
                await this.admissions;
            }
        } else if (
            !this.journal.state.operations.some(
                (o) => o.intent.path === (input.oldPath ?? path) && o.intent.operation === input.kind,
            )
        ) {
            await this.hold(path, "uncaptured_" + input.kind);
        }
        // Always gate native scanner/rebuild/manual paths. Only the publisher
        // can write an enrolled mutation through exact-base APIs.
        setTimeout(() => {
            void this.onPublish().catch(() => this.io.changed());
        }, 0);
        return true;
    }
    async ingest(record: Control) {
        await this.ready;
        if (record.kind === "intent" || record.kind === "publication") {
            await this.journal.update(s => {
                const map = record.kind === "intent" ? (s.intents ??= {}) : (s.publications ??= {});
                if (map[record.id] && JSON.stringify(map[record.id]) !== JSON.stringify(record)) throw Error("control_collision");
                map[record.id] = record;
            }, s => JSON.stringify((record.kind === "intent" ? s.intents : s.publications)?.[record.id]) === JSON.stringify(record));
            await this.onControl(record);
        } else if (record.kind === "binding") {
            const existing = this.journal.state.candidates[record.id];
            if (existing && JSON.stringify(existing) === JSON.stringify(record)) return;
            await this.journal.update((s) => {
                const previous = s.candidates[record.id];
                if (previous && JSON.stringify(previous) !== JSON.stringify(record))
                    throw new Error("binding_collision");
                s.candidates[record.id] = record;
            });
            // Outside native path lock to avoid recursively taking the same lock.
            this.onReflect(record.path);
        } else if (record.kind === "ack") await this.onAck(record);
    }
    async reflectCandidate(b: Binding) {
        if (this.stopped) return;
        await this.ready;
        if (this.parked.get(b.path) === b.id) return;
        if (this.pending(b.path)) return;
        const current = this.journal.state.bindings[b.path];
        if (current && newerSource(current, b)) return;
        if (current?.id === b.id && this.journal.state.holds[b.path] !== "native_conflict_requires_resolution") return;
        // Coalesce display only, never durable operations. A newer accepted
        // descendant can already be queued before either binding is installed.
        for (const next of this.pathCandidates(b.path)) {
            if (newerSource(next, b)) return;
            if (next.path === b.path && next.workspace === b.workspace &&
                next.collection === b.collection && next.generation === b.generation &&
                next.fileId === b.fileId && !b.sourceSequence && !next.sourceSequence && next.nativeRevision !== b.nativeRevision &&
                await this.io.descends(b.path, next.nativeRevision, b.nativeRevision)) return;
        }
        // Renames/deletes retire the installed path binding. Their acknowledged
        // exact bases still prove which retained controls must not be replayed.
        for (const op of this.journal.state.operations) {
            const i = op.intent, base = i.base;
            if (op.state !== "applied" || !base ||
                !["rename", "delete"].includes(i.operation) || i.path !== b.path ||
                base.workspace !== b.workspace || base.collection !== b.collection ||
                base.generation !== b.generation || base.fileId !== b.fileId) continue;
            if (base.nativeRevision === b.nativeRevision ||
                await this.io.descends(b.path, base.nativeRevision, b.nativeRevision)) return;
        }
        // Retained controls include history. Filter proven ancestors before the
        // native API rejects their no-longer-live revisions and emits a notice.
        if (
            current &&
            current.workspace === b.workspace &&
            current.collection === b.collection &&
            current.generation === b.generation &&
            current.fileId === b.fileId &&
            current.nativeRevision !== b.nativeRevision &&
            !newerSource(b, current) && (await this.io.descends(b.path, current.nativeRevision, b.nativeRevision))
        )
            return;
        await this.io.reflect(b);
        if (["reflection_hash_mismatch", "reflection_preserves_local_edit", "native_conflict_requires_resolution",
            "reflection_ancestry_ambiguous", "missing_local_file"].includes(this.journal.state.holds[b.path]))
            this.parked.set(b.path, b.id);
    }
    async beforeReflect(entry: MetaEntry): Promise<boolean | undefined> {
        if (this.stopped) return true;
        await this.ready;
        await this.admissions;
        const path = String(entry.path);
        if (path.startsWith("LOOM-Control-v1/payload/")) return true;
        if (reserved(path)) {
            try {
                await this.ingest(await this.io.readControl(path, entry._rev!));
            } catch {
                await this.hold(path, "control_pending_or_invalid");
            }
            return true;
        }
        if (this.journal.failure) return true;
        const candidates = this.pathCandidates(path).filter(
            (b) => b.nativeRevision === entry._rev,
        );
        if (candidates.length !== 1 || entry.deleted || entry._deleted || this.pending(path)) {
            await this.hold(path, "reflection_waiting_for_binding_or_intent");
            return true;
        }
        const b = candidates[0],
            current = this.journal.state.bindings[path];
        if (this.parked.get(path) === b.id) return true;
        if (current && newerSource(current, b)) return true;
        if (
            current &&
            current.fileId === b.fileId &&
            current.nativeRevision !== b.nativeRevision &&
            !newerSource(b, current) && (await this.io.descends(path, current.nativeRevision, b.nativeRevision))
        )
            return true;
        if (current && current.nativeRevision === b.nativeRevision && current.sourceBase !== b.sourceBase) {
            await this.hold(path, "binding_base_collision");
            return true;
        }
        if (
            current &&
            (current.fileId !== b.fileId ||
                (current.nativeRevision !== b.nativeRevision &&
                    this.journal.state.resolutionDisplays?.[path]?.binding !== b.id &&
                    !newerSource(b, current) &&
                    !(await this.io.descends(path, b.nativeRevision, current.nativeRevision))))
        ) {
            await this.hold(path, "reflection_ancestry_ambiguous");
            return true;
        }
        const incoming = await this.readBinding(b, b.nativeRevision),
            local = await this.readBinding(b);
        if (incoming === null) {
            await this.hold(path, "reflection_chunks_pending");
            return true;
        }
        if ((await digest(incoming)) !== b.sha256) {
            await this.hold(path, "reflection_hash_mismatch");
            return true;
        }
        const reviewed = this.journal.state.resolutionDisplays?.[path];
        const resolutionMatch = reviewed?.binding === b.id && local !== null &&
            await digest(local) === reviewed.localHash;
        if (
            local !== null &&
            (await digest(local)) !== b.sha256 &&
            (!current || (await digest(local)) !== current.sha256) && !resolutionMatch
        ) {
            if (current?.writable) {
                this.capture({ kind: "edit", path });
                await this.admissions;
            }
            await this.hold(path, "reflection_preserves_local_edit");
            return true;
        }
        if (local === null && current) {
            await this.hold(path, "missing_local_file");
            return true;
        }
        await this.journal.update((s) => {
            s.reflecting[path] = b;
        });
        return undefined;
    }
    materializedRevision(path: string) {
        return this.journal.state.bindings[path]?.nativeRevision;
    }
    async reviewedReplacement(path: string, revision: string, text: string): Promise<boolean> {
        const display = this.journal.state.resolutionDisplays?.[path];
        const binding = this.journal.state.reflecting[path];
        return !!display && !!binding && display.binding === binding.id &&
            binding.nativeRevision === revision && !this.pending(path) && await digest(text) === display.localHash;
    }
    async afterReflect(entry: MetaEntry, success: boolean) {
        const b = this.journal.state.reflecting[String(entry.path)];
        if (!b || b.nativeRevision !== entry._rev) return;
        const local = await this.readBinding(b);
        if (success && local !== null && (await digest(local)) === b.sha256) await this.install(b);
        else await this.hold(b.path, "reflection_not_confirmed");
    }
    async install(b: Binding) {
        await this.journal.update((s) => {
            for (const [path, old] of Object.entries(s.bindings))
                if (
                    old.fileId === b.fileId &&
                    old.workspace === b.workspace &&
                    old.collection === b.collection &&
                    path !== b.path
                )
                    delete s.bindings[path];
            s.bindings[b.path] = b;
            delete s.reflecting[b.path];
            delete s.holds[b.path];
            delete s.resolutionDisplays?.[b.path];
        });
        this.cursors.set(b.path, { binding: b, path: b.path });
        this.io.changed();
    }
    details() {
        if (this.journal.failure) return "LOOM held: " + this.journal.failure;
        if (!this.started) return "LOOM starting";
        const states = this.journal.state.operations.reduce(
            (a, o) => {
                const state = o.resolvedBy ? "resolved" : o.state;
                a[state] = (a[state] ?? 0) + 1;
                return a;
            },
            {} as Record<string, number>,
        );
        const pending = (states.queued ?? 0) + (states.publishing ?? 0) + (states.uploaded ?? 0);
        return (
            `LOOM: ${pending} pending source acceptance; ${states.applied ?? 0} accepted; ${(states.conflict ?? 0) + (states.held ?? 0)} blocked; ${Object.keys(this.journal.state.holds).length} reflection holds\n` +
            "Pending edits are saved locally. Acceptance is separate from search indexing.\n" +
            (this.journal.checkpointFailure ? "Recovery export: " + this.journal.checkpointFailure + "\n" : "") +
            this.journal.state.operations
                .filter((o) => unfinished(o) && o.reason)
                .map((o) => `${o.intent.path}: ${o.state} (${o.reason})`)
                .join("\n") +
            "\n" +
            Object.entries(this.journal.state.holds)
                .map(([p, r]) => `${p}: ${r}`)
                .join("\n")
        );
    }
}
