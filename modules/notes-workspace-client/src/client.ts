import type { MetaEntry } from "@vrtmrz/livesync-commonlib/compat/common/types";
import type { LoomIntentHooks } from "@vrtmrz/livesync-commonlib/compat/serviceModules/ServiceFileHandlerBase";
import { Journal, unfinished, type Operation } from "./journal";
import { checkResolutionText, inspectConflict, type ConflictReview } from "./conflicts";
import { SourceLayouts } from "./layout";
import type { CreateRecoveryReview } from "./native-target";
import { NotesStatus, type StatusPageOptions } from "./status";
import {
    MAX_BYTES,
    digest,
    relativePath,
    directoryPath,
    referencePath,
    reserved,
    newerSource,
    sameFile,
    pair,
    isControlPath,
    type Binding,
    type Control,
    type Intent,
} from "./protocol";
export interface ClientIO {
    read(path: string): Promise<string | null>;
    readControl(path: string, revision: string): Promise<Control>;
    readRevision(path: string, revision: string): Promise<string | null>;
    readReference?(path: string, revision?: string): Promise<Uint8Array<ArrayBuffer> | null>;
    referenceAllowed?(path: string, revision?: string): Promise<boolean>;
    descends(path: string, revision: string, ancestor: string): Promise<boolean>;
    reflect(binding: Binding): Promise<void>;
    conflictLeaves?(path: string): Promise<{ revision: string; text: string }[]>;
    retiredRevision?(path: string, revision: string): Promise<boolean>;
    changed(): void;
    directoryExists?(path: string): Promise<boolean>;
    retireEmptyDirectory?(binding: Binding): Promise<boolean>;
    reflectDirectory?(binding: Binding): Promise<void>;
    removeNote?(path: string): Promise<void>;
    relocate?(previous: Binding, binding: Binding): Promise<void>;
}
type Cursor = { binding: Binding; path: string; predecessor?: string; absent?: boolean };
type Event = {
    kind: "create" | "edit" | "delete" | "rename";
    path: string;
    oldPath?: string;
    folder?: boolean;
    transfer?: Operation["transfer"];
    transferDeleteOf?: string;
};
export class IntentClient implements LoomIntentHooks {
    readonly layouts = new SourceLayouts(this);
    readonly ready: Promise<void>;
    private cursors = new Map<string, Cursor>();
    private admissions = Promise.resolve();
    private started = false;
    private readonly status = new NotesStatus();
    private indexedCandidates?: Record<string, Binding>;
    private candidatesByPath = new Map<string, Binding[]>();
    private directoryControlsBySource = new Map<string, Binding[]>();
    private pathCandidates(path: string): Binding[] {
        const candidates = this.journal.state.candidates;
        if (this.indexedCandidates !== candidates) {
            this.candidatesByPath.clear();
            this.directoryControlsBySource.clear();
            for (const b of Object.values(candidates)) {
                const group = this.candidatesByPath.get(b.path) ?? [];
                group.push(b);
                this.candidatesByPath.set(b.path, group);
                if (b.entryKind === "directory" && (b.deleted || b.previousPath)) {
                    const from = b.previousPath ?? b.path;
                    const controls = this.directoryControlsBySource.get(from) ?? [];
                    controls.push(b);
                    this.directoryControlsBySource.set(from, controls);
                }
            }
            this.indexedCandidates = candidates;
        }
        return this.candidatesByPath.get(path) ?? [];
    }
    // Only a changed child prefix wakes a retained parent control. Stable holds
    // need neither a whole-vault retry nor a timer.
    wakeDirectoryParents(...paths: string[]) {
        if (this.stopped) return;
        const wake = new Set<string>();
        for (const path of paths) {
            this.pathCandidates(path);
            const parts = path.split("/");
            while (parts.length > 1) {
                parts.pop();
                for (const b of this.directoryControlsBySource.get(parts.join("/")) ?? [])
                    if (!this.reviewedSourceMove(b) && this.journal.state.bindings[b.path]?.id !== b.id)
                        wake.add(b.path);
            }
        }
        for (const r of this.layouts.records) if (["pending", "moving"].includes(r.state) &&
            r.control.previous.entryKind === "directory" &&
            paths.some(path => path.startsWith(r.control.previous.path + "/"))) wake.add(r.control.previous.path);
        for (const path of wake) this.onReflect(path);
    }
    // A superseded ordinary edit never reached the journal. Only a subsequent
    // ordinary edit can skip it; rename/delete and other ambiguity stay held.
    private resolutions = new Map<string, Cursor | { supersededEdit: Cursor } | false>();
    resumeNative: (recover?: boolean) => Promise<void> = async () => {};
    stopped = false;
    stopNative: () => Promise<void> = async () => { this.stopped = true; };
    onPublish: (id?: string) => Promise<void> = async () => {};
    inspectCreateRecovery: (id: string) => Promise<CreateRecoveryReview> = async () => { throw Error("Native recovery is unavailable."); };
    recoverCreate: (review: CreateRecoveryReview) => Promise<void> = async () => { throw Error("Native recovery is unavailable."); };
    onAck: (record: Extract<Control, { kind: "ack" }>) => Promise<void> = async () => {};
    onControl: (record: Control) => Promise<void> = async () => {};
    onReflect: (path: string) => void = path => {
        setTimeout(() => { void this.reflectPath(path).catch(() => this.io.changed()); }, 0);
    };
    private parked = new Map<string, string>();
    private handovers = new Set<string>();
    private transferEvents = new Map<string,string>();
    private transferResume?: Promise<void>;
    private reviewedSourceMove(b: Binding) {
        return this.journal.state.pathChanges?.["reviewed-source-move/"+b.id]===true||
            this.journal.state.pathChanges?.["reviewed-source-retirement/"+b.id]===true||
            this.layouts.sourceRetired(b);
    }
    private reviewedPathRetired(path: string) {
        const candidates=this.pathCandidates(path),installed=this.journal.state.bindings[path];
        return candidates.length>0 && (!installed||this.reviewedSourceMove(installed)) && candidates.every(b=>this.reviewedSourceMove(b));
    }
    private transferTargetOccupied(path: string) {
        if(this.cursors.has(path))return true;
        const candidates=this.pathCandidates(path).filter(b=>!this.reviewedSourceMove(b));
        return candidates.some(b=>!b.deleted&&!candidates.some(next=>newerSource(next,b))&&
            !this.journal.state.operations.some(o=>o.ack?.status==="applied"&&o.intent.operation==="delete"&&
                o.intent.path===path&&o.intent.fileId===b.fileId&&o.intent.base&&
                o.intent.workspace===b.workspace&&o.intent.collection===b.collection&&
                (o.intent.base.sourceSequence??0)>=(b.sourceSequence??0)));
    }
    private pathEvents = new Map<string, string>();
    private childChanges = new Map<string, Map<string, { id: string; kind: string; target?: string }>>();
    private rememberChild(event: Event, id: string, fileId: string) {
        const from = event.oldPath ?? event.path;
        this.pathEvents.set(JSON.stringify([event.kind, from, event.path]), fileId);
        const parts = from.split("/");
        while (parts.length > 1) {
            parts.pop();
            const parent = this.cursors.get(parts.join("/"));
            if (parent?.binding.entryKind !== "directory") continue;
            const changes = this.childChanges.get(parent.binding.fileId) ?? new Map();
            if (event.kind === "create") changes.delete(from);
            else if (event.kind === "rename" || event.kind === "delete")
                changes.set(from, { id, kind: event.kind, ...(event.kind === "rename" ? { target: event.path } : {}) });
            this.childChanges.set(parent.binding.fileId, changes);
        }
    }
    async reflectPath(path: string, retry = false) {
        await this.ready;
        await this.settled();
        await this.layouts.resume(path, retry);
        if (retry) this.parked.delete(path);
        for (const b of this.pathCandidates(path)) await this.reflectCandidate(b);
        this.wakeDirectoryParents(path);
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
                    binding: { ...b, fileId: i.fileId, ...(i.entryKind ? { entryKind: i.entryKind } : {}) },
                    path: i.target ?? i.path,
                    predecessor: i.id,
                    absent: !i.base,
                });
        }
        for (const op of this.journal.state.operations)
            if(op.transfer&&!op.transferResolvedBy)this.cursors.delete(op.transfer.source.path);
        this.started = true;
        for (const path of new Set(this.journal.state.operations.filter(o => unfinished(o) &&
            (o.intent.entryKind === "directory" && o.intent.operation === "create" ||
                this.journal.state.scopes?.[o.intent.workspace + "/" + o.intent.collection]?.generation !== undefined &&
                this.journal.state.scopes![o.intent.workspace + "/" + o.intent.collection].generation !== o.intent.generation)).map(o => o.intent.path)))
            this.onReflect(path);
        // A retained reflection marker authorises only that exact revision/hash.
        for (const b of Object.values(this.journal.state.reflecting)) {
            if (Object.values(this.journal.state.layouts ?? {}).some(r => r.state === "moving" && r.control.binding.id === b.id)) continue;
            if (!await this.referenceAllowed(b.path)) continue;
            const text = await this.readBinding(b);
            if (text !== null && (await digest(text)) === b.sha256) await this.install(b);
            else await this.hold(b.path, "interrupted_reflection");
        }
    }
    async hold(path: string, reason: string, expected?: Binding) {
        await this.journal.update((s) => {
            if (expected && (s.bindings[path]?.id !== expected.id || this.reviewedSourceMove(expected))) return;
            s.holds[path] = reason;
        }, s => s.holds[path] === reason);
        this.io.changed();
    }
    private referenceAllowed(path: string, revision?: string): Promise<boolean> {
        return referencePath(path) ? this.io.referenceAllowed?.(path, revision) ?? Promise.resolve(true) : Promise.resolve(true);
    }
    private async readBinding(b: Binding, revision?: string) {
        if (b.entryKind === "directory") return await this.io.directoryExists?.(b.path) ? "" : null;
        if (!await this.referenceAllowed(b.path, revision)) return null;
        if (referencePath(b.path)) return this.io.readReference?.(b.path, revision) ?? Promise.resolve(null);
        return revision ? this.io.readRevision(b.path, revision) : this.io.read(b.path);
    }
    private inside(b: Binding, path: string) {
        return directoryPath(path) && path.startsWith(b.collectionRoot + "/");
    }
    private pending(path: string) {
        return this.journal.state.operations.some(
            (o) => unfinished(o) && (o.intent.path === path || o.intent.target === path) ||
                !!o.transfer && !o.transferResolvedBy && o.transfer.source.path===path,
        );
    }
    private async unboundAbsent(path: string) {
        return !this.journal.state.bindings[path] && !this.pathCandidates(path).length && !this.pending(path) &&
            !await this.io.directoryExists?.(path) && await this.io.read(path) === null;
    }
    private async reviewedAbsent(path: string) {
        return this.reviewedPathRetired(path) && !this.pending(path) &&
            !await this.io.directoryExists?.(path) && await this.io.read(path) === null &&
            this.reviewedPathRetired(path) && !this.pending(path);
    }
    private snapshot(path: string) {
        const x = this.cursors.get(path);
        return x ? structuredClone(x) : undefined;
    }
    private creationBinding(path: string): Binding | undefined {
        const scopes = Object.values(this.journal.state.scopes ?? {}).filter(s => s.writable && path.startsWith(s.collectionRoot + "/"));
        const roots = new Set(scopes.map(s => s.collectionRoot));
        if (roots.size === 1) {
            const scope = scopes.sort((a,b) => b.sourceSequence - a.sourceSequence)[0];
            return { ...scope, kind: "binding", fileId: scope.id, path, sourceBase: scope.id,
                nativeRevision: "1-scope", sha256: "sha256:" + "0".repeat(64) };
        }
        if (roots.size > 1) return;
        const matches = Object.values(this.journal.state.bindings).filter(b => b.writable && this.inside(b, path));
        const knownScopes = new Set(matches.map(b => JSON.stringify([b.workspace, b.collection, b.collectionRoot])));
        if (knownScopes.size !== 1) return;
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
        const scope = this.creationBinding(path)!;
        const parents = String(path).split("/").slice(0,-1);
        for (let n = scope.collectionRoot.split("/").length+1; n <= parents.length; n++) {
            const directory = parents.slice(0,n).join("/");
            if(!this.cursors.has(directory)) {
                const source=this.pathCandidates(directory).filter(b=>b.entryKind==="directory"&&!b.deleted&&
                    b.workspace===scope.workspace&&b.collection===scope.collection&&b.generation===scope.generation)
                    .sort((a,b)=>(b.sourceSequence??0)-(a.sourceSequence??0))[0];
                if(source)await this.reflectCandidate(source);
            }
            if (!this.cursors.has(directory) && !this.pathCandidates(directory).length && await this.io.directoryExists?.(directory))
                this.capture({ kind:"create", path:directory, folder:true });
        }
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
    capture(event: Event): string | undefined {
        if (this.stopped) return;
        if (reserved(event.path) || (event.oldPath && reserved(event.oldPath))) return;
        if(event.kind==="delete"&&this.reviewedPathRetired(event.path)&&!this.pending(event.path))return;
        const moving = this.layouts.active;
        if (moving && (event.kind === "rename" && event.oldPath === moving.previous.path && event.path === moving.binding.path ||
            event.kind === "delete" && (event.path === moving.previous.path || event.folder &&
                event.path !== "Projects" && moving.previous.path.startsWith(event.path + "/")) ||
            event.kind === "create" && event.folder && moving.binding.path.startsWith(event.path + "/"))) return;
        this.parked.delete(event.path);
        if (event.oldPath) this.parked.delete(event.oldPath);
        if (!this.started) {
            this.admissions = this.admissions.then(async () => {
                await this.ready;
                await this.hold(event.path, "event_before_binding_ready");
            });
            return;
        }
        this.wakeDirectoryParents(event.oldPath ?? event.path, event.path);
        if (event.kind === "rename" && event.oldPath) {
            const key=JSON.stringify([event.oldPath,event.path]);
            const source=this.snapshot(event.oldPath) ?? [...this.cursors.entries()].find(([p])=>event.folder&&p.startsWith(event.oldPath!+"/"))?.[1];
            const previous=this.transferEvents.get(key) ?? this.journal.state.operations.find(o=>
                o.transfer?.source.path===event.oldPath && o.intent.path===event.path && !o.transferResolvedBy)?.intent.id;
            // Duplicate descendant callbacks have no remaining source cursor.
            // A real source cursor means a new move, including A-B-A-B cycles.
            if (previous && !source) return previous;
            if (source && !this.inside(source.binding,event.path)) return this.captureTransfer(event);
        }
        if (event.folder) {
            if (event.kind === "edit") return;
            const from = event.oldPath ?? event.path;
            if (Object.values(this.journal.state.reflecting).some(b =>
                b.entryKind === "directory" && (b.path === event.path || b.previousPath === from) ||
                event.kind === "create" && b.path.startsWith(event.path + "/"))) return;
            if (event.kind === "create") { this.captureDirectory(event); return; }
            const children = [...this.cursors.keys()].filter(p => p.startsWith(from + "/") &&
                this.cursors.get(p)!.binding.entryKind !== "directory");
            const parent = this.cursors.get(from);
            const dependencies = [...(this.childChanges.get(parent?.binding.fileId ?? "") ?? [])]
                .filter(([path, change]) => path.startsWith(from + "/") && change.kind === event.kind &&
                    (event.kind !== "rename" || change.target === event.path + path.slice(from.length)))
                .map(([, change]) => change.id);
            for (const path of children) {
                const id = this.capture({
                    kind: event.kind,
                    path: event.kind === "rename" ? event.path + path.slice(from.length) : path,
                    oldPath: event.kind === "rename" ? path : undefined,
                });
                if (id) dependencies.push(id);
            }
            const directories = [...this.cursors.keys()].filter(p => p.startsWith(from + "/") &&
                this.cursors.get(p)!.binding.entryKind === "directory").sort((a,b) => b.length-a.length);
            for (const path of directories) {
                const id = this.captureDirectory({ kind: event.kind, path: event.kind === "rename" ? event.path + path.slice(from.length) : path,
                    oldPath: event.kind === "rename" ? path : undefined, folder: true }, dependencies);
                if (id) dependencies.push(id);
            }
            this.captureDirectory(event, dependencies);
            return;
        }
        const path = event.oldPath ?? event.path;
        let cursor = this.snapshot(path);
        if (!cursor && (event.kind === "rename" || event.kind === "delete")) {
            const target = this.snapshot(event.path);
            const previous = this.pathEvents.get(JSON.stringify([event.kind,path,event.path])) ??
                this.journal.state.operations.find(o=>o.intent.operation===event.kind && o.intent.path===path &&
                    (o.intent.target??o.intent.path)===event.path &&
                    (event.kind==="delete" || o.intent.fileId===target?.binding.fileId))?.intent.fileId;
            if (previous && (event.kind==="delete" || target?.binding.fileId===previous)) return;
        }
        const reflection = this.journal.state.reflecting[event.path];
        if (referencePath(path) && reflection) return;
        if (cursor && !cursor.binding.writable) {
            if (!reflection) this.admissions = this.admissions.then(async () => this.hold(path,
                await this.referenceAllowed(path).catch(()=>true) ? "reference_only" : "device_excluded/reference_only"));
            return;
        }
        const id = (/\.canvas$/i.test(event.path) || /\.canvas$/i.test(event.oldPath ?? "") ? "canvas_" : "") + crypto.randomUUID();
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
            this.rememberChild(event, id, captured.binding.fileId);
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
                    ...(duringResolution ? { reason: "edit_during_resolution" } : {}),
                    ...(event.transfer ? {transfer:event.transfer} : {}),
                    ...(event.transferDeleteOf ? {transferDeleteOf:event.transferDeleteOf} : {}) });
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
        return id;
    }
    async settled() {
        await this.admissions;
    }
    private captureTransfer(event: Event): string | undefined {
        const from=event.oldPath!, target=this.creationBinding(event.path);
        const members=[...this.cursors.entries()].filter(([p])=>p===from||event.folder&&p.startsWith(from+"/"));
        const reject=(reason:string)=>{this.admissions=this.admissions.then(()=>this.hold(from,reason));return undefined;};
        if (!target?.writable || !members.length) return reject("outside_collection");
        if (this.transferTargetOccupied(event.path)) return reject("rename_target_collision");
        const sources: [string,Cursor][]=[];
        for (const [path,original] of members) {
            let source=original;
            const scope=this.journal.state.scopes?.[source.binding.workspace+"/"+source.binding.collection];
            if (!source.binding.writable || scope && !scope.writable || source.binding.entryKind!=="directory"&&referencePath(String(path))) return reject("cross_collection_reference_requires_review");
            if (scope && scope.generation!==source.binding.generation) {
                const current=this.pathCandidates(path).filter(b=>b.generation===scope.generation&&b.fileId===source.binding.fileId&&b.sha256===source.binding.sha256)
                    .sort((a,b)=>(b.sourceSequence??0)-(a.sourceSequence??0))[0];
                if (source.predecessor || !current) return reject("transfer_source_configuration_changed");
                source={binding:current,path};
            }
            const dest=event.path+path.slice(from.length), scopeTarget=this.creationBinding(dest);
            if (!scopeTarget || scopeTarget.collection!==target.collection || scopeTarget.workspace!==target.workspace ||
                this.transferTargetOccupied(dest) || !this.inside(source.binding,path)) return reject("rename_target_collision");
            sources.push([path,source]);
        }
        const childCreates: {path:string;id:string}[]=[];
        const directories=event.folder ? sources.filter(([,s])=>s.binding.entryKind==="directory").sort((a,b)=>a[0].length-b[0].length) : [];
        // Destination directories are ordinary creates. Source directory cleanup
        // is deferred until every exact child transfer has a source receipt.
        if (event.folder && !directories.some(([p])=>p===from)) this.captureDirectory({kind:"create",folder:true,path:event.path});
        for (const [path,source] of [...directories,...sources.filter(([,s])=>s.binding.entryKind!=="directory")]) {
            const destination=event.path+path.slice(from.length), transfer:NonNullable<Operation["transfer"]>={source,children:[]};
            const id=source.binding.entryKind==="directory" ?
                this.captureDirectory({kind:"create",folder:true,path:destination,transfer}) :
                this.capture({kind:"create",path:destination,transfer});
            if (!id) return reject("transfer_capture_failed");
            childCreates.push({path,id});this.cursors.delete(path);
            this.transferEvents.set(JSON.stringify([path,destination]),id);
            // Filled synchronously before admission persists the operation.
            if (source.binding.entryKind==="directory") for(const child of sources) if(child[0].startsWith(path+"/"))
                (transfer.children as string[]).push(child[0]);
        }
        const replacements=new Map(childCreates.map(r=>[r.path,r.id]));
        this.admissions=this.admissions.then(()=>this.journal.update(s=>{
            for(const row of childCreates){const op=s.operations.find(o=>o.intent.id===row.id);if(op?.transfer?.children)
                op.transfer.children=op.transfer.children.map(p=>replacements.get(p)).filter((id):id is string=>!!id);}
        }));
        const id=replacements.get(from)??childCreates[0]?.id;
        if(id)this.transferEvents.set(JSON.stringify([from,event.path]),id);
        return id;
    }
    resumeTransfers(): Promise<void> {
        if(this.transferResume)return this.transferResume;
        this.transferResume=(async()=>{
            await this.ready;await this.settled();
            for(const op of this.journal.state.operations){
                if(!op.transfer||op.transferResolvedBy||op.ack?.status!=="applied")continue;
                const old=op.transfer.source;
                const retirement=this.journal.state.operations.find(o=>o.transferDeleteOf===op.intent.id);
                if(retirement){
                    if(retirement.ack?.status==="applied")await this.journal.update(s=>{
                        s.operations.find(o=>o.intent.id===op.intent.id)!.transferResolvedBy=retirement.intent.id;
                        delete s.holds[old.path];
                    });
                    if(retirement.ack?.status==="applied")this.transferEvents.delete(JSON.stringify([old.path,op.intent.path]));
                    continue;
                }
                if(old.predecessor&&this.journal.state.operations.find(o=>o.intent.id===old.predecessor)?.ack?.status!=="applied")continue;
                const children=(op.transfer.children??[]).map(id=>this.journal.state.operations.find(o=>o.transferDeleteOf===id));
                if(children.some(child=>child?.ack?.status!=="applied"))continue;
                const scope=this.journal.state.scopes?.[old.binding.workspace+"/"+old.binding.collection];
                if(scope&&(!scope.writable||scope.generation!==old.binding.generation)){
                    await this.hold(old.path,"transfer_source_configuration_changed");continue;
                }
                if(old.binding.entryKind==="directory" ? await this.io.directoryExists?.(old.path) : await this.io.read(old.path)!==null){
                    await this.hold(old.path,"transfer_source_recreated");continue;
                }
                if(this.journal.state.operations.some(next=>unfinished(next)&&next.intent.path===old.path)){
                    await this.hold(old.path,"transfer_source_has_pending_changes");continue;
                }
                this.cursors.set(old.path,old);
                const event:Event={kind:"delete",path:old.path,transferDeleteOf:op.intent.id};
                if(old.binding.entryKind==="directory")this.captureDirectory({...event,folder:true},children.map(o=>o!.intent.id));
                else this.capture(event);
                await this.settled();
            }
        })().finally(()=>{this.transferResume=undefined;});
        return this.transferResume;
    }
    private captureDirectory(event: Event, dependencies: string[] = []): string | undefined {
        const from = event.oldPath ?? event.path;
        const childIDs = [...dependencies];
        if (!directoryPath(from) || !directoryPath(event.path)) return;
        const current = this.snapshot(from);
        const eventKey = JSON.stringify([event.kind, from, event.path]);
        const target = event.kind === "rename" ? this.snapshot(event.path) : undefined;
        const prior = this.pathEvents.get(eventKey) ?? this.journal.state.operations.find(o =>
            o.intent.entryKind === "directory" && o.intent.operation === event.kind && o.intent.path === from &&
            (o.intent.target ?? o.intent.path) === event.path)?.intent.fileId;
        if (!current && prior && (event.kind === "delete" || target?.binding.fileId === prior)) {
            // A parent event already captured the child; Obsidian can emit its
            // own descendant callback after our cursor moved to the new path.
            this.admissions = this.admissions.then(() => this.journal.update(s => {
                if (s.holds[from] === "directory_base_unavailable") delete s.holds[from];
            }, s => s.holds[from] !== "directory_base_unavailable"));
            return;
        }
        if (event.kind === "create" && current?.binding.entryKind === "directory") return;
        if (current && current.binding.entryKind !== "directory") {
            this.admissions = this.admissions.then(() => this.hold(from, "directory_path_is_file")); return;
        }
        const scope = current?.binding ?? this.creationBinding(event.path);
        if (!scope || !this.inside(scope, event.path) || !this.inside(scope, from)) {
            this.admissions = this.admissions.then(() => this.hold(from, "directory_scope_unavailable")); return;
        }
        const id = "dir_" + crypto.randomUUID();
        const cursor: Cursor = current ?? { binding: { ...scope, fileId: crypto.randomUUID(), entryKind: "directory", path: event.path }, path: event.path, absent: true };
        if (!current && event.kind !== "create") {
            this.admissions = this.admissions.then(async () => {
                if (event.kind === "delete" && await this.retiredMaterialized(from)) return;
                await this.hold(from, "directory_base_unavailable");
            }); return;
        }
        this.rememberChild(event, id, cursor.binding.fileId);
        this.childChanges.delete(cursor.binding.fileId);
        this.cursors.delete(from);
        if (event.kind !== "delete") this.cursors.set(event.path, { ...cursor, path: event.path, predecessor: id });
        this.admissions = this.admissions.then(async () => {
            const children = childIDs.filter(id => this.journal.state.operations.some(o =>
                o.intent.id === id && o.intent.path.startsWith(from + "/")));
            const intent: Intent = { version: 1, kind: "intent", id, device: this.journal.state.device,
                operation: event.kind as Intent["operation"], entryKind: "directory",
                workspace: scope.workspace, collection: scope.collection, generation: scope.generation,
                fileId: cursor.binding.fileId, base: cursor.absent ? null : cursor.binding,
                path: from, sha256: await digest(""), length: 0,
                ...(event.kind === "rename" ? { target: event.path } : {}),
                ...(cursor.predecessor ? { predecessor: cursor.predecessor } : {}),
                ...(children.length ? { dependencies: [...new Set(children)] } : {}) };
            await this.journal.update(s => {
                s.operations.push({ intent, payload: "", state: "queued", revisions: [],
                    ...(event.transfer ? {transfer:event.transfer} : {}),
                    ...(event.transferDeleteOf ? {transferDeleteOf:event.transferDeleteOf} : {}) });
                delete s.holds[from];
            });
            this.io.changed();
        }).catch(async () => { if (!this.journal.failure) await this.hold(from, "directory_capture_failed"); });
        void this.admissions.then(() => this.onPublish(id)).catch(() => this.io.changed());
        return id;
    }
    async resolve(review: ConflictReview, text: string): Promise<string> {
        checkResolutionText(text);
        await this.ready;
        await this.settled();
        if (this.stopped || this.journal.failure) throw Error("Notes sync is not ready.");
        const id = (/\.canvas$/i.test(review.path) ? "canvas_" : "") + crypto.randomUUID();
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
        if(input.kind==="delete"&&(this.reviewedPathRetired(path)&&!this.pending(path)||await this.observedSourceMove(path)))return true;
        if(input.kind==="rename"&&input.oldPath&&(this.transferEvents.has(JSON.stringify([input.oldPath,path]))||
            this.journal.state.operations.some(o=>o.transfer?.source.path===input.oldPath&&o.intent.path===path)))return true;
        const binding = this.journal.state.bindings[path];
        if (binding && !binding.writable) {
            if (!await this.referenceAllowed(path)) return true;
            const content = await this.readBinding(binding);
            // A source move or metadata handover may finish during the read.
            // This callback has no evidence about the replacement binding.
            if (this.journal.state.bindings[path]?.id !== binding.id || this.reviewedSourceMove(binding)) return true;
            if (content === null || await digest(content) !== binding.sha256)
                await this.hold(path, "reference_local_change", binding);
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
            await this.hold(path, input.kind === "delete" && await this.unboundAbsent(path) ?
                "observed_absence/uncaptured_delete" : "uncaptured_" + input.kind);
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
        if (record.kind === "layout") {
            await this.layouts.ingest(record);
        } else if (record.kind === "scope") {
            const key = record.workspace + "/" + record.collection;
            const previous = this.journal.state.scopes?.[key];
            if (previous && previous.sourceSequence >= record.sourceSequence) return;
            await this.journal.update(s => { (s.scopes ??= {})[key] = record; });
            for (const path of new Set(this.journal.state.operations.filter(o => unfinished(o) &&
                o.intent.workspace === record.workspace && o.intent.collection === record.collection).map(o => o.intent.path)))
                this.onReflect(path);
        } else if (record.kind === "intent" || record.kind === "publication") {
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
    private async handoverConfiguration(b: Binding): Promise<boolean> {
        const state = this.journal.state;
        const scope = state.scopes?.[b.workspace + "/" + b.collection];
        if (!scope?.writable || scope.generation !== b.generation || !b.writable || b.deleted || b.entryKind) return false;
        const pending = state.operations.filter(o => unfinished(o) && (o.intent.path === b.path || o.intent.target === b.path));
        if (!pending.length || !pending.some(o => o.intent.generation !== b.generation)) return false;
        if (this.handovers.has(b.path)) return true;
        if (pending.some(o => o.intent.operation !== "edit" || o.intent.fileId !== b.fileId || o.writing || o.state === "publishing")) return false;
        if (pending.some(o => o.resolution && !o.ack && o.state !== "held")) return true;
        this.handovers.add(b.path);
        try {
            const review = await inspectConflict(this, b.path);
            const base = pending[0].intent.base;
            const known = new Set(pending.map(o => o.intent.sha256)); known.add(b.sha256);
            if (review.binding.id !== b.id || !base || base.sha256 !== b.sha256 ||
                review.local !== pending[pending.length - 1].payload || review.leaves.some(l => !known.has(l.sha256))) {
                await this.hold(b.path, "configuration_change_requires_review"); return true;
            }
            // The old request keeps its original identity/receipt. Only this
            // new exact-current-base edit can retire its covered local history.
            await this.resolve(review, review.local);
            return true;
        } catch {
            await this.hold(b.path, "configuration_change_requires_review"); return true;
        } finally { this.handovers.delete(b.path); }
    }
    private async materialized(path: string, selected?: Binding): Promise<boolean> {
        if (this.pending(path)) return false;
        const state = this.journal.state, b = selected ?? state.bindings[path];
        if (!b) return state.holds[path] === "scanner_absence_is_not_delete" && !!await this.io.directoryExists?.(path);
        const scope = state.scopes?.[b.workspace + "/" + b.collection];
        if (scope && scope.generation !== b.generation || this.pathCandidates(path).some(next => newerSource(next, b))) return false;
        if (b.entryKind === "directory") return !b.deleted && !!await this.io.directoryExists?.(path) &&
            (!b.previousPath || !await this.io.directoryExists?.(b.previousPath));
        if (!await this.referenceAllowed(path, b.nativeRevision)) return false;
        const local = await this.readBinding(b);
        if (local === null || await digest(local) !== b.sha256) return false;
        const native = await this.readBinding(b, b.nativeRevision);
        if (native === null || await digest(native) !== b.sha256) return false;
        if (!referencePath(path) && this.io.conflictLeaves) {
            const leaves = await this.io.conflictLeaves(path);
            if (leaves.length !== 1 || leaves[0].revision !== b.nativeRevision) return false;
        }
        return true;
    }
    private async adoptEquivalentGeneration(path: string) {
        const current=this.journal.state.bindings[path];
        if(!current||this.pending(path))return false;
        const scope=this.journal.state.scopes?.[current.workspace+"/"+current.collection];
        if(!scope||scope.generation===current.generation||scope.collectionRoot!==current.collectionRoot)return false;
        const candidates=this.pathCandidates(path).filter(b=>b.workspace===current.workspace&&b.collection===current.collection&&
            b.generation===scope.generation&&!this.reviewedSourceMove(b));
        if(candidates.length!==1)return false;
        const b=candidates[0];
        if(b.deleted||b.entryKind||b.writable!==current.writable||b.collectionRoot!==current.collectionRoot||
            b.sha256!==current.sha256||b.nativeRevision!==current.nativeRevision||!await this.materialized(path,b)||
            this.pending(path)||this.journal.state.bindings[path]?.id!==current.id)return false;
        // A new enrollment may re-key an unchanged source. This adopts metadata
        // only, with exact local/native bytes and no unaccepted edit to rebind.
        await this.install(b);
        return true;
    }
    private async retirementPublication(path: string, revision?: string) {
        const state=this.journal.state;
        for(const ack of Object.values(state.acks??{})) {
            const i=state.intents?.[ack.intentId],p=state.publications?.[ack.publicationId];
            if(ack.status!=="applied"||!i||!p||i.path!==path||!["rename","delete"].includes(i.operation)||
                !pair(i,p)||ack.intentDigest!==p.intentDigest||ack.intentDigest!==await digest(JSON.stringify(i)))continue;
            const deletion=p.revisions.find(r=>r.path===path&&r.role==="deletion");
            if(deletion&&(!revision||deletion.revision===revision))return {intent:i,publication:p,deletion};
        }
    }
    private async retiredMaterialized(path: string) {
        if(this.pending(path)||await this.io.directoryExists?.(path)||await this.io.read(path)!==null)return false;
        const state=this.journal.state;
        // Filesystem callbacks can arrive after a current source tombstone has
        // removed its cursor. Never infer retirement from absence alone.
        if (!state.bindings[path] && !this.cursors.has(path)) {
            const candidates = this.pathCandidates(path);
            for (const b of candidates) {
                const scope = state.scopes?.[b.workspace + "/" + b.collection];
                if (b.entryKind === "directory" && b.deleted &&
                    scope?.generation === b.generation && scope.collectionRoot === b.collectionRoot &&
                    !candidates.some(next => next.workspace === b.workspace && next.collection === b.collection &&
                        next.generation === b.generation && (next.sourceSequence ?? 0) > (b.sourceSequence ?? 0))) return true;
            }
        }
        const retired=await this.retirementPublication(path);
        if(retired&&await this.io.retiredRevision?.(path,retired.deletion.revision))return true;
        for(const r of this.layouts.records) {
            const a=r.control.previous,b=r.control.binding,target=state.bindings[b.path];
            if(r.state!=="applied"||a.path!==path||!target||target.workspace!==b.workspace||
                target.collection!==b.collection||target.fileId!==b.fileId||
                (target.sourceSequence??0)<(b.sourceSequence??0)||!await this.materialized(b.path,target))continue;
            if(await this.io.retiredRevision?.(path,a.nativeRevision))return true;
        }
        return false;
    }
    private async observedSourceMove(path: string) {
        if(this.pending(path)||this.journal.state.bindings[path]||await this.io.directoryExists?.(path)||await this.io.read(path)!==null)return false;
        if(this.pathCandidates(path).some(b=>{const scope=this.journal.state.scopes?.[b.workspace+"/"+b.collection];
            return !this.reviewedSourceMove(b)&&(!scope||scope.generation===b.generation);}))return false;
        for(const r of this.layouts.records) {
            const next=r.control.binding,target=this.journal.state.bindings[next.path];
            if(r.state==="applied"&&r.control.sourceMove&&r.control.previous.path===path&&target&&
                sameFile(target,next)&&(target.sourceSequence??0)>=(next.sourceSequence??0)&&await this.materialized(next.path,target))return true;
        }
        return false;
    }
    async reconcileSafetyHold(path: string): Promise<boolean> {
        if(await this.retiredMaterialized(path)) {
            await this.journal.update(s=>{if(!this.pending(path))delete s.holds[path];});
            return true;
        }
        const current = this.journal.state.bindings[path];
        if (current) for (const b of this.pathCandidates(path)) if (newerSource(b, current) && await this.materialized(path, b)) {
            await this.install(b); return true;
        }
        if (!await this.materialized(path)) return false;
        const reason = this.journal.state.holds[path], binding = this.journal.state.bindings[path]?.id;
        if (reason) await this.journal.update(s => {
            if (s.holds[path] === reason && s.bindings[path]?.id === binding && !this.pending(path)) delete s.holds[path];
        });
        return true;
    }
    async reconcileSafetyHolds(after = "", limit = 64) {
        await this.ready; await this.settled();
        if (!Number.isInteger(limit) || limit < 1 || limit > 128) throw Error("Invalid warning page limit");
        if (!after) await this.layouts.retireEmptyParents();
        const paths = Object.keys(this.journal.state.holds).filter(p => p > after).sort().slice(0, limit);
        const proven: { path: string; reason: string; binding?: string }[] = [];
        const historical: {path:string;reason:string;binding:string}[] = [];
        const observations: {path:string;reason:string;next:string}[] = [];
        let cleared = 0;
        for (const path of paths) {
            const current = this.journal.state.bindings[path];
            const reasonAtStart=this.journal.state.holds[path];
            try { if(await this.retiredMaterialized(path)) {proven.push({path,reason:reasonAtStart,binding:current?.id});continue;} }
            catch { /* Missing retirement evidence must not hide an unresolved path. */ }
            try { if(await this.observedSourceMove(path)) {observations.push({path,reason:reasonAtStart,next:"observed_absence/accepted_source_move"});continue;} }
            catch { /* Unavailable destination or local bytes remain actionable. */ }
            if(!this.pending(path))try{
                const scopes=Object.values(this.journal.state.scopes??{});
                if(!current&&!this.pathCandidates(path).length&&["directory_scope_unavailable","folder_scope_or_limit"].includes(reasonAtStart)&&
                    await this.io.directoryExists?.(path)&&scopes.some(s=>s.collectionRoot===path||s.collectionRoot.startsWith(path+"/")||
                        !s.writable&&path.startsWith(s.collectionRoot+"/"))){
                    observations.push({path,reason:reasonAtStart,next:"source_managed_structure"});continue;
                }
                if(referencePath(String(path))&&!await this.referenceAllowed(path)){
                    if(!reasonAtStart.startsWith("device_excluded/"))observations.push({path,reason:reasonAtStart,next:"device_excluded/"+reasonAtStart});
                    continue;
                }
                if(!current&&!this.pathCandidates(path).length&&["missing_writable_binding","folder_scope_or_limit","directory_scope_unavailable","uncaptured_delete","missing_local_file"].includes(reasonAtStart)&&
                    !await this.io.directoryExists?.(path)&&await this.io.read(path)===null){
                    observations.push({path,reason:reasonAtStart,next:"observed_absence/"+reasonAtStart});continue;
                }
            }catch{/* Unreadable or changing paths retain the original warning. */}
            if (current && !this.pending(path)) {
                if(await this.adoptEquivalentGeneration(path)){cleared++;continue;}
                const newer = this.pathCandidates(path).filter(b => newerSource(b,current))
                    .sort((a,b)=>(b.sourceSequence??0)-(a.sourceSequence??0))[0];
                if (newer) try {
                    if (await this.materialized(path,newer)) { await this.install(newer); if (!this.journal.state.holds[path]) cleared++; continue; }
                } catch { /* Preserve unknown or competing native branches. */ }
            }
            const reason = this.journal.state.holds[path], binding = this.journal.state.bindings[path]?.id;
            if (current && !this.pending(path)) {
                const scope = this.journal.state.scopes?.[current.workspace + "/" + current.collection];
                if (scope && scope.generation !== current.generation && !this.pathCandidates(path).some(b=>b.generation===scope.generation)) {
                    try {
                        const local = await this.readBinding(current);
                        if (local === null || await digest(local) === current.sha256) {
                            historical.push({path,reason,binding:current.id}); continue;
                        }
                    } catch { /* Changed or unavailable local text remains actionable. */ }
                }
            }
            try { if (await this.materialized(path)) proven.push({path,reason,binding}); } catch { /* Keep unavailable evidence. */ }
        }
        await this.journal.update(s => {
            for (const p of proven) if (s.holds[p.path] === p.reason && s.bindings[p.path]?.id === p.binding && !this.pending(p.path)) {
                delete s.holds[p.path]; cleared++;
            }
            for (const p of historical) if (s.holds[p.path] === p.reason && s.bindings[p.path]?.id === p.binding && !this.pending(p.path))
                s.holds[p.path] = "configuration_retained_history";
            for(const p of observations)if(s.holds[p.path]===p.reason&&!this.pending(p.path))s.holds[p.path]=p.next;
        });
        if (cleared) this.io.changed();
        return {checked:paths.length,cleared,next:paths.length===limit?paths[paths.length-1]:undefined};
    }
    private async reconcileDirectoryEnsure(b: Binding) {
        if (b.entryKind !== "directory" || !b.writable || b.deleted || b.previousPath || !b.sourceSequence) return;
        const scope = this.journal.state.scopes?.[b.workspace + "/" + b.collection];
        if (!scope?.writable || scope.generation !== b.generation || scope.collectionRoot !== b.collectionRoot) return;
        if ([...Object.values(this.journal.state.bindings), ...this.pathCandidates(b.path)].some(next =>
            next.path === b.path && next.workspace === b.workspace && next.collection === b.collection &&
            next.generation === b.generation && next.id !== b.id &&
            (next.sourceSequence ?? 0) >= (b.sourceSequence ?? 0))) return;
        const pending = this.journal.state.operations.filter(o => unfinished(o) && o.intent.path === b.path);
        if (!pending.length || pending.some(o => o.intent.entryKind !== "directory" || o.intent.operation !== "create" ||
            o.intent.base !== null || o.intent.predecessor || o.intent.target || o.intent.dependencies?.length ||
            o.intent.workspace !== b.workspace || o.intent.collection !== b.collection || o.payload !== "" ||
            o.transfer || o.transferDeleteOf || o.resolution ||
            !["directory_identity_collision", "configuration_changed"].includes(o.ack?.reason ?? o.reason ?? ""))) return;
        if (this.journal.state.operations.some(o => unfinished(o) && pending.some(p =>
            o.intent.predecessor === p.intent.id || o.intent.dependencies?.includes(p.intent.id)))) return;
        if (!await this.io.directoryExists?.(b.path) || this.stopped) return;
        const ids = new Set(pending.map(o => o.intent.id));
        // The directory already exists at the authoritative source. Keep the
        // refused requests and their original acknowledgements, not false accepts.
        await this.journal.update(s => {
            for (const o of s.operations) if (ids.has(o.intent.id) && unfinished(o)) o.sourceSatisfiedBy = b;
            if (["directory_identity_collision", "configuration_changed"].includes(s.holds[b.path])) delete s.holds[b.path];
        });
        this.cursors.delete(b.path);
        this.io.changed();
    }
    async reflectCandidate(b: Binding) {
        if (this.stopped) return;
        await this.ready;
        if (this.reviewedSourceMove(b)) return;
        const scope = this.journal.state.scopes?.[b.workspace + "/" + b.collection];
        if (scope && scope.generation !== b.generation) return;
        if(await this.adoptEquivalentGeneration(b.path))return;
        await this.reconcileDirectoryEnsure(b);
        if (await this.handoverConfiguration(b)) return;
        const installed = this.journal.state.bindings[b.path];
        if ((!b.entryKind || !b.deleted && !b.previousPath) && installed && newerSource(b,installed) && b.nativeRevision === installed.nativeRevision &&
            b.sha256 === installed.sha256 && await this.materialized(b.path,b)) { await this.install(b); return; }
        if (this.journal.state.bindings[b.path]?.id === b.id &&
            this.journal.state.holds[b.path] !== "native_conflict_requires_resolution") {
            if (this.journal.state.holds[b.path]) await this.reconcileSafetyHold(b.path); return;
        }
        if (this.layouts.suppress(b)) return;
        if (referencePath(b.path) && !await this.referenceAllowed(b.path, b.nativeRevision)) {
            await this.journal.update(s => {
                if (["layout_reference_excluded", "layout_waiting_for_control"].includes(s.holds[b.path])) delete s.holds[b.path];
            }, s => !["layout_reference_excluded", "layout_waiting_for_control"].includes(s.holds[b.path]));
            return;
        }
        if (this.layouts.waiting(b)) return this.hold(b.path, "layout_waiting_for_control");
        if (this.parked.get(b.path) === b.id) return;
        if (this.pending(b.path)) {
            if (b.entryKind === "directory") await this.hold(b.path, "directory_pending_intent");
            return;
        }
        if (b.entryKind === "directory") {
            const from = b.previousPath ?? b.path;
            const inside = (p?: string) => !!p && [from, b.path].some(root => p === root || p.startsWith(root + "/"));
            const pending = () => this.pending(from) || this.pending(b.path) || this.journal.state.operations.some(o =>
                unfinished(o) && [o.intent.path, o.intent.target].some(inside) ||
                !!o.transfer && !o.transferResolvedBy && inside(o.transfer.source.path));
            if (pending()) return this.hold(b.path, "directory_pending_intent");
            const newer = [...Object.values(this.journal.state.bindings), ...Object.values(this.journal.state.candidates)]
                .some(next => next.entryKind === "directory" && (next.fileId === b.fileId || next.path === b.path) &&
                    next.collection === b.collection && next.workspace === b.workspace && (next.sourceSequence ?? 0) > (b.sourceSequence ?? 0));
            if (newer) return;
            const old = this.journal.state.bindings[b.path];
            if (old?.id === b.id) return;
            if (!b.deleted && !this.io.reflectDirectory) return this.hold(b.path, "directory_client_unavailable");
            const previous = this.journal.state.bindings[from];
            if (b.deleted || b.previousPath) {
                if (previous && (!sameFile(previous, b) || previous.entryKind !== "directory"))
                    return this.hold(b.path, "directory_previous_changed");
                if (Object.values(this.journal.state.bindings).some(child => !child.deleted && child.path.startsWith(from + "/")) ||
                    this.layouts.records.some(r => ["pending", "moving"].includes(r.state) && r.disposition !== "excluded" &&
                        [r.control.previous.path, r.control.binding.path].some(p => p.startsWith(from + "/"))))
                    return this.hold(b.path, "directory_waiting_for_children");
                if (!this.io.retireEmptyDirectory || !this.io.directoryExists)
                    return this.hold(b.path, "directory_client_unavailable");
            }
            await this.journal.update(s => { s.reflecting[b.path] = b; });
            try {
                if (b.deleted || b.previousPath) {
                    // Ensure the destination without a recursive folder rename.
                    if (!b.deleted) {
                        const { previousPath: _, ...target } = b;
                        await this.io.reflectDirectory!(target);
                    }
                    await this.settled();
                    if (pending()) return this.hold(b.path, "directory_pending_intent");
                    if (this.journal.state.bindings[from]?.id !== previous?.id)
                        return this.hold(b.path, "directory_previous_changed");
                    if (await this.io.directoryExists!(from)) {
                        if (!previous) return this.hold(b.path, "directory_previous_unavailable");
                        if (!await this.io.retireEmptyDirectory!(previous) && await this.io.directoryExists!(from))
                            return this.hold(b.path, "directory_preserves_local_children");
                    }
                    if (pending()) return this.hold(b.path, "directory_pending_intent");
                } else await this.io.reflectDirectory!(b);
                await this.install(b);
            } catch { await this.hold(b.path, "directory_reflection_pending"); }
            finally {
                await this.journal.update(s => { if (s.reflecting[b.path]?.id === b.id) delete s.reflecting[b.path]; },
                    s => s.reflecting[b.path]?.id !== b.id);
            }
            return;
        }
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
        await this.reflectPathChanges();
        if (["reflection_hash_mismatch", "reflection_preserves_local_edit", "native_conflict_requires_resolution",
            "reflection_ancestry_ambiguous", "missing_local_file"].includes(this.journal.state.holds[b.path]))
            this.parked.set(b.path, b.id);
    }
    async beforeReflect(entry: MetaEntry): Promise<boolean | undefined> {
        if (this.stopped) return true;
        await this.ready;
        await this.admissions;
        const path = String(entry.path);
        if (reserved(path) && !isControlPath(path)) return true;
        if (reserved(path)) {
            try {
                await this.ingest(await this.io.readControl(path, entry._rev!));
            } catch {
                await this.hold(path, "control_pending_or_invalid");
            }
            return true;
        }
        if (this.journal.failure) return true;
        if (!await this.referenceAllowed(path, entry._rev)) return true;
        if (await this.reviewedAbsent(path)) return true;
        const installed = this.journal.state.bindings[path];
        if (installed && this.journal.state.holds[path] && !this.pending(path) && installed.nativeRevision === entry._rev && !entry.deleted && !entry._deleted &&
            await this.reconcileSafetyHold(path)) return true;
        const arrivals = this.pathCandidates(path).filter(
            (b) => b.nativeRevision === entry._rev,
        );
        const candidates = arrivals.filter(b => {
            const scope = this.journal.state.scopes?.[b.workspace + "/" + b.collection];
            return !this.reviewedSourceMove(b) && (!scope || scope.generation === b.generation);
        });
        if (arrivals.length && !candidates.length) return true;
        if(candidates.length&&candidates.every(b=>this.layouts.suppress(b)))return true;
        if((entry.deleted||entry._deleted)&&await this.retirementPublication(path,entry._rev))return true;
        if (installed && !this.pending(path) && candidates.length===1 && newerSource(installed,candidates[0])) return true;
        if ((entry.deleted || entry._deleted) && this.journal.state.operations.some(o => o.state === "applied" &&
            o.intent.operation === "delete" && o.intent.path === path && o.publication?.revisions.some(r=>r.role === "deletion" && r.revision === entry._rev))) return true;
        const reviewedRetirement = this.journal.state.pathChanges?.[
            "reviewed-native-retirement/" + JSON.stringify([path, entry._rev])] === true;
        if ((entry.deleted || entry._deleted || reviewedRetirement) && await this.unboundAbsent(path)) {
            await this.hold(path, reviewedRetirement ? "observed_absence/reviewed_native_retirement" :
                "observed_absence/unbound_native_retirement");
            return true;
        }
        if (candidates.length !== 1 || entry.deleted || entry._deleted || this.pending(path)) {
            await this.hold(path, "reflection_waiting_for_binding_or_intent");
            return true;
        }
        const b = candidates[0],
            current = this.journal.state.bindings[path];
        if (this.layouts.suppress(b) || this.layoutTarget !== b.id && this.layouts.waiting(b)) return true;
        if (this.parked.get(path) === b.id) return true;
        if (current && newerSource(current, b)) return true;
        if (
            current &&
            current.fileId === b.fileId &&
            current.nativeRevision !== b.nativeRevision &&
            !newerSource(b, current) && (await this.io.descends(path, current.nativeRevision, b.nativeRevision))
        )
            return true;
        if (current && current.nativeRevision === b.nativeRevision && current.sourceBase !== b.sourceBase && !newerSource(b,current)) {
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
        if (!await this.referenceAllowed(b.path)) return;
        const local = await this.readBinding(b);
        if (success && local !== null && (await digest(local)) === b.sha256) await this.install(b);
        else await this.hold(b.path, "reflection_not_confirmed");
    }
    async install(b: Binding) {
        const previousPaths = Object.values(this.journal.state.bindings).filter(old => sameFile(old, b)).map(old => old.path);
        await this.journal.update((s) => {
            for (const [path, old] of Object.entries(s.bindings))
                if (
                    old.fileId === b.fileId &&
                    old.workspace === b.workspace &&
                    old.collection === b.collection &&
                    path !== b.path
                )
                    delete s.bindings[path];
            if (b.deleted) delete s.bindings[b.path];
            else s.bindings[b.path] = b;
            delete s.reflecting[b.path];
            delete s.holds[b.path];
            delete s.resolutionDisplays?.[b.path];
        });
        for (const [path, cursor] of this.cursors)
            if (path !== b.path && cursor.binding.fileId === b.fileId && cursor.binding.workspace === b.workspace &&
                cursor.binding.collection === b.collection) this.cursors.delete(path);
        if (b.deleted) this.cursors.delete(b.path);
        else this.cursors.set(b.path, { binding: b, path: b.path });
        this.io.changed();
        this.wakeDirectoryParents(b.path, ...previousPaths);
    }
    async reflectLayoutTarget(b: Binding) {
        // Only the layout owner may bypass cross-path admission for a device
        // that has never materialized the previous path.
        this.layoutTarget = b.id;
        try {
            if (b.entryKind === "directory") {
                if (!this.io.reflectDirectory) return;
                await this.io.reflectDirectory(b);
                await this.install(b);
            } else await this.io.reflect(b);
        }
        finally { this.layoutTarget = undefined; }
    }

    retireLayoutCursor(previous: Binding) {
        const cursor=this.cursors.get(previous.path);
        if (cursor && cursor.binding.workspace===previous.workspace && cursor.binding.collection===previous.collection &&
            cursor.binding.fileId===previous.fileId && (cursor.binding.sourceSequence??0)<=(previous.sourceSequence??0))
            this.cursors.delete(previous.path);
    }
    private layoutTarget?: string;
    // Applied foreign path receipts retire only the exact locally held source
    // base. A later device edit is never a reason to erase its old pathname.
    async reflectPathChanges() {
        if (!this.io.removeNote) return;
        for (const ack of Object.values(this.journal.state.acks ?? {})) {
            const i = this.journal.state.intents?.[ack.intentId];
            const p = this.journal.state.publications?.[ack.publicationId];
            if (!i || !p || i.entryKind || ack.status !== "applied" ||
                !["rename","delete"].includes(i.operation) || !i.base || this.pending(i.path) ||
                this.journal.state.pathChanges?.[i.id] || !pair(i,p) ||
                ack.intentDigest !== await digest(JSON.stringify(i)) || p.intentDigest !== ack.intentDigest) continue;
            const old = this.journal.state.bindings[i.path] ?? this.journal.state.candidates[i.base.id];
            if (!old || old.fileId !== i.fileId || old.sourceBase !== i.base.sourceBase ||
                old.workspace!==i.workspace||old.collection!==i.collection||old.generation!==i.generation) continue;
            if (i.operation === "rename") {
                const b=ack.binding,text=await this.io.read(i.target!);
                if (!b||b.path!==i.target||b.fileId!==i.fileId||b.sha256!==i.sha256||
                    b.nativeRevision!==p.revisions.find(r=>r.role==="content")?.revision ||
                    text===null || await digest(text)!==b.sha256) continue;
            }
            const local = await this.io.read(i.path);
            if (local !== null && await digest(local) !== old.sha256) {
                await this.hold(i.path,"path_change_preserves_local_edit"); continue;
            }
            if (local !== null) await this.io.removeNote(i.path);
            await this.journal.update(s => { delete s.bindings[i.path]; delete s.holds[i.path];
                (s.pathChanges??={})[i.id]=true; });
            this.cursors.delete(i.path);
        }
    }
    summary() {
        return this.status.summary(this.started ? this.journal.state : undefined, {
            starting: !this.started, failure: this.journal.failure, checkpointFailure: this.journal.checkpointFailure,
        });
    }
    statusDetails(options: StatusPageOptions = {}) {
        return this.status.overview(this.started ? this.journal.state : undefined, options, {
            starting: !this.started, failure: this.journal.failure, checkpointFailure: this.journal.checkpointFailure,
        });
    }
    statusItem(id: string, options: StatusPageOptions = {}) {
        return this.status.item(this.started ? this.journal.state : undefined, id, options);
    }
    statusOperation(id: string) {
        return this.status.operation(this.started ? this.journal.state : undefined, id);
    }
    details() {
        if (this.journal.failure) return "LOOM held: " + this.journal.failure;
        if (!this.started) return "LOOM starting";
        const states = this.journal.state.operations.reduce(
            (a, o) => {
                const state = o.sourceSatisfiedBy ? "source-satisfied" : o.resolvedBy ? "resolved" : o.state;
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
