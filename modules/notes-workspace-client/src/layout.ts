import type { IntentClient } from "./client";
import type { State } from "./journal";
import { digest, exactBinding, referencePath, sameFile, validate, type Binding, type Layout } from "./protocol";

export interface LayoutFiles {
    stat(path: string): Promise<"file" | "folder" | null>;
    hash(path: string): Promise<string | null>;
    mkdir(path: string): Promise<void>;
    rename(from: string, to: string): Promise<void>;
    removeFile(path: string): Promise<void>;
    removeEmpty(path: string): Promise<void>;
}
export async function relocateLocal(a: Binding, b: Binding, fs: LayoutFiles) {
    const ensure = async (path: string) => {
        const parts = path.split("/");
        for (let n = 1; n <= parts.length; n++) {
            const p = parts.slice(0, n).join("/"), at = await fs.stat(p);
            if (at === "file") throw Error("layout_destination_collision");
            if (!at) await fs.mkdir(p);
        }
    };
    const old = await fs.stat(a.path), target = await fs.stat(b.path);
    if (a.entryKind === "directory") {
        if (old === "file" || target === "file") throw Error("layout_directory_collision");
        await ensure(b.path);
        // removeEmpty must recheck emptiness. Unknown children are never moved,
        // trashed recursively or interpreted as source-controlled files.
        if (old === "folder") await fs.removeEmpty(a.path);
    } else {
        if (old === "folder" || target === "folder") throw Error("layout_destination_collision");
        if (old && await fs.hash(a.path) !== a.sha256) throw Error("layout_preserves_local_edit");
        if (target && await fs.hash(b.path) !== b.sha256) throw Error("layout_destination_collision");
        if (!old && !target) throw Error("layout_previous_missing");
        if (old) {
            await ensure(b.path.split("/").slice(0, -1).join("/"));
            if (await fs.hash(a.path) !== a.sha256) throw Error("layout_preserves_local_edit");
            if (!target) {
                if (await fs.stat(b.path)) throw Error("layout_destination_collision");
                await fs.rename(a.path, b.path);
            } else await fs.removeFile(a.path);
        }
        if (await fs.hash(b.path) !== b.sha256) throw Error("layout_move_unconfirmed");
    }
    // Implicit source ancestors have no binding of their own. Trim only these
    // exact old parents, never their descendants or the shared Projects root.
    const parents = a.path.split("/").slice(0, -1);
    while (parents.length > 1 && parents.join("/") !== "Projects") {
        const p = parents.join("/");
        if (await fs.stat(p) === "folder") await fs.removeEmpty(p);
        parents.pop();
    }
}

export function layoutPolicyPaths(state: State | undefined, path: string): string[] {
    const paths = new Set([path]);
    const records = Object.values(state?.layouts ?? {});
    const files = records.filter(r => r.control.previous.path === path || r.control.binding.path === path)
        .map(r => r.control.previous);
    for (const r of records) if (files.some(b => sameFile(b, r.control.previous))) {
        paths.add(r.control.previous.path); paths.add(r.control.binding.path);
    }
    // New binding metadata can precede its layout. Grouping must never broaden
    // the device's established ungrouped Projects exclusions in that window.
    for (const p of paths) paths.add(p.replace(/^Projects\/_(Passive|Archived)\//, "Projects/"));
    return [...paths];
}

// Aliases are display metadata only. Authored links and mutation intents retain
// their original bytes/paths; a real destination always wins over an alias.
export function layoutAlias(state: State, path: string): string | undefined {
    if (!path.includes("/")) return;
    const applied = Object.values(state.layouts ?? {}).filter(r => r.state === "applied");
    const matches = applied.filter(({ control: l }) => l.previous.path === path ||
        /\.md$/i.test(l.previous.path) && l.previous.path.slice(0, -3) === path);
    if (!matches.length) return;
    const first = matches[0].control.previous;
    if (matches.some(r => !sameFile(r.control.previous, first))) return;
    const latest = applied.filter(r => sameFile(r.control.binding, first))
        .sort((a, b) => b.control.binding.sourceSequence! - a.control.binding.sourceSequence!);
    const b = latest[0].control.binding;
    if (latest.some(r => r.control.binding.sourceSequence === b.sourceSequence && !exactBinding(r.control.binding, b))) return;
    return b.path === path ? undefined : b.path;
}

export class SourceLayouts {
    private serial = Promise.resolve();
    active?: Layout;
    constructor(private client: IntentClient) {}
    get records() { return Object.values(this.client.journal.state.layouts ?? {}); }
    suppress(b: Binding) {
        return this.records.some(r => sameFile(r.control.binding, b) &&
            (r.state === "applied" && r.control.binding.path !== b.path &&
                r.control.binding.sourceSequence! > (b.sourceSequence ?? 0) ||
                r.disposition === "excluded" && ["pending", "moving"].includes(r.state) &&
                r.control.binding.sourceSequence! >= (b.sourceSequence ?? 0)));
    }
    // A target binding arriving ahead of its layout must not retire the installed
    // old cursor. Old-path edits remain publishable until source acceptance.
    waiting(b: Binding) {
        if (this.records.some(r => ["pending", "moving"].includes(r.state) && exactBinding(r.control.binding, b))) return true;
        if (!b.id.startsWith("layout_")) return false;
        if (this.records.some(r => r.state === "applied" && exactBinding(r.control.binding, b))) return false;
        // The namespace also carries ordinary creates, renames and updates.
        // Only retained cross-root evidence implies a move; proven retired
        // aliases must not block later same-root mutations.
        return [...Object.values(this.client.journal.state.bindings), ...Object.values(this.client.journal.state.candidates)]
            .some(old => sameFile(old, b) && old.collectionRoot !== b.collectionRoot && !this.suppress(old));
    }
    async ingest(control: Layout) {
        validate(control);
        await this.client.journal.update(s => {
            const old = s.layouts?.[control.id];
            if (old && (!exactBinding(old.control.previous, control.previous) || !exactBinding(old.control.binding, control.binding)))
                throw Error("layout_collision");
            (s.layouts ??= {})[control.id] ??= { control, state: "pending" };
            for (const [id, r] of Object.entries(s.layouts!)) {
                if (id === control.id || !["pending", "moving"].includes(r.state) ||
                    !sameFile(r.control.previous, control.previous) ||
                    r.control.previous.path !== control.previous.path || r.control.binding.path !== control.binding.path ||
                    r.control.binding.sourceSequence! >= control.binding.sourceSequence!) continue;
                r.state = "superseded"; r.supersededBy = control.id;
                if (s.holds[r.control.previous.path] === r.reason) delete s.holds[r.control.previous.path];
                delete r.reason;
            }
            const replacement = Object.values(s.layouts!).filter(r => sameFile(r.control.previous, control.previous) &&
                r.control.previous.path === control.previous.path && r.control.binding.path === control.binding.path &&
                r.control.binding.sourceSequence! > control.binding.sourceSequence!)
                .sort((a, b) => b.control.binding.sourceSequence! - a.control.binding.sourceSequence!)[0];
            const inserted = s.layouts![control.id];
            if (replacement && ["pending", "moving"].includes(inserted.state)) {
                inserted.state = "superseded"; inserted.supersededBy = replacement.control.id;
                if (s.holds[control.previous.path] === inserted.reason) delete s.holds[control.previous.path];
                delete inserted.reason;
            }
            for (const b of [control.previous, control.binding]) {
                if (s.candidates[b.id] && !exactBinding(s.candidates[b.id], b)) throw Error("binding_collision");
                s.candidates[b.id] = b;
            }
        });
        this.client.onReflect(control.previous.path);
        this.client.onReflect(control.binding.path);
    }
    resume(path?: string, retryExcluded = false): Promise<void> {
        const run = this.serial.then(async () => {
            await this.client.settled();
            // Earlier moves establish exact intermediate provenance for a chain.
            for (const r of this.records.sort((a, b) => a.control.binding.sourceSequence! - b.control.binding.sourceSequence!)) {
                if (!["pending", "moving"].includes(r.state) || path && r.control.previous.path !== path && r.control.binding.path !== path) continue;
                if (r.disposition === "excluded") {
                    if (!retryExcluded) continue;
                    await this.client.journal.update(s => {
                        delete s.layouts![r.control.id].disposition;
                        delete s.layouts![r.control.id].reason;
                    });
                }
                await this.apply(r.control);
            }
        });
        this.serial = run.catch(() => {});
        return run;
    }
    private async read(b: Binding) {
        const io = this.client.io;
        if (b.entryKind === "directory") return await io.directoryExists?.(b.path) ? "" : null;
        return referencePath(b.path) ? await io.readReference?.(b.path) ?? null : io.read(b.path);
    }
    private async apply(l: Layout) {
        const { journal, io } = this.client, a = l.previous, b = l.binding;
        if (this.client.stopped) return;
        const held = async (reason: string) => {
            await journal.update(s => { const r = s.layouts![l.id]; r.reason = reason; },
                s => s.layouts![l.id].reason === reason);
            await this.client.hold(a.path, reason);
        };
        const pending = () => journal.state.operations.some(o => o.state !== "applied" && !o.resolvedBy &&
            [o.intent.path, o.intent.target].some(p => p === a.path || p === b.path ||
                a.entryKind === "directory" && !!p && (p.startsWith(a.path + "/") || p.startsWith(b.path + "/"))));
        if (this.records.some(r => r.control.id !== l.id && sameFile(r.control.binding, b) &&
            r.control.binding.sourceSequence === b.sourceSequence && !exactBinding(r.control.binding, b)))
            return held("layout_sequence_collision");
        if (pending()) return held("layout_pending_intent");
        if (referencePath(b.path) && (!await (io.referenceAllowed?.(a.path) ?? true) ||
            !await (io.referenceAllowed?.(b.path, b.nativeRevision) ?? true))) {
            await journal.update(s => {
                const r = s.layouts![l.id];
                r.disposition = "excluded"; r.reason = "layout_reference_excluded";
                for (const path of [a.path, b.path])
                    if (["layout_reference_excluded", "layout_waiting_for_control"].includes(s.holds[path])) delete s.holds[path];
                if (s.reflecting[b.path] && exactBinding(s.reflecting[b.path], b)) delete s.reflecting[b.path];
            });
            this.wakeParents(a);
            io.changed();
            return;
        }
        if (a.entryKind === "directory" && this.records.some(r => ["pending", "moving"].includes(r.state) &&
            r.disposition !== "excluded" && r.control.previous.path.startsWith(a.path + "/"))) return held("layout_waiting_for_children");
        const old = journal.state.bindings[a.path];
        let target = journal.state.bindings[b.path];
        const newer = Object.values(journal.state.bindings).find(x => sameFile(x, b) && (x.sourceSequence ?? 0) > b.sourceSequence!);
        if (newer) {
            await journal.update(s => {
                const r = s.layouts![l.id]; r.state = "superseded";
                r.supersededBy = this.records.find(x => exactBinding(x.control.binding, newer))?.control.id ?? newer.id;
                if (s.holds[a.path] === r.reason) delete s.holds[a.path];
                delete r.reason;
            });
            return;
        }
        if (Object.values(journal.state.bindings).some(x => sameFile(x, b) && x.path !== a.path && x.path !== b.path))
            return held("layout_chain_pending");
        // A metadata-only enrollment refresh can advance the authoritative
        // predecessor while this device still displays the same exact leaf.
        if (old && !exactBinding(old, a) && !(sameFile(old, a) && old.path === a.path &&
            old.collectionRoot === a.collectionRoot && old.entryKind === a.entryKind &&
            old.sha256 === a.sha256 && old.writable === a.writable && !old.deleted &&
            (old.sourceSequence ?? 0) < (a.sourceSequence ?? 0) && old.nativeRevision === a.nativeRevision))
            return held("layout_previous_changed");
        if (!old && target && !exactBinding(target, b) && sameFile(target, b) &&
            this.records.some(r => r.state === "applied" && exactBinding(r.control.binding, target!))) {
            // Another device's accepted old-path edit can advance an already
            // moved source. Reuse ordinary exact-ancestry reflection only for
            // our proven, still-unmodified earlier layout destination.
            const local = await this.read(target);
            if (local === null || await digest(local) !== target.sha256) return held("layout_preserves_local_edit");
            if (a.entryKind !== "directory" && !await io.descends(b.path, b.nativeRevision, target.nativeRevision))
                return held("layout_destination_collision");
            await this.client.reflectLayoutTarget(b);
            target = journal.state.bindings[b.path];
        }
        if (target && !exactBinding(target, b)) return held("layout_destination_collision");
        if (!io.relocate) return held("layout_client_unavailable");
        const local = await this.read(a), dest = await this.read(b);
        if (local !== null && await digest(local) !== a.sha256) return held("layout_preserves_local_edit");
        const moving = journal.state.layouts![l.id].state === "moving";
        if (!a.entryKind && dest !== null && ((!moving && !target) || await digest(dest) !== b.sha256))
            return held("layout_destination_collision");
        if (local === null && !moving) {
            // A new device has no old copy to relocate. Permit only ordinary
            // exact target reflection, then complete this layout on its next wake.
            if (old) return held("layout_previous_missing");
            if (dest !== null && (!target || await digest(dest) !== b.sha256)) return held("layout_destination_collision");
            if (!target) {
                await this.client.reflectLayoutTarget(b);
                if (!journal.state.bindings[b.path] || !exactBinding(journal.state.bindings[b.path], b))
                    return held("layout_target_pending");
            }
        } else {
            if (local === null && (dest === null || await digest(dest) !== b.sha256)) return held("layout_move_unconfirmed");
            if (pending()) return held("layout_pending_intent");
            await journal.update(s => {
                if (old) s.candidates[old.id] ??= old;
                s.layouts![l.id].state = "moving"; s.reflecting[b.path] = b;
            });
            try {
                this.active = l;
                await io.relocate(a, b);
            } catch (error) {
                const reason = error instanceof Error && /^layout_[a-z_]+$/.test(error.message) ? error.message : "layout_move_pending";
                return held(reason);
            }
            finally { this.active = undefined; }
            const result = await this.read(b);
            if (pending() || result === null || await digest(result) !== b.sha256) return held("layout_move_unconfirmed");
        }
        await journal.update(s => {
            s.layouts![l.id].state = "applied";
            delete s.layouts![l.id].reason;
            delete s.bindings[a.path];
            s.bindings[b.path] = b;
            delete s.holds[a.path];
            delete s.reflecting[b.path];
        });
        await this.client.install(b);
        this.wakeParents(a);
    }
    private wakeParents(a: Binding) {
        for (const r of this.records) if (["pending", "moving"].includes(r.state) && r.control.previous.entryKind === "directory" &&
            a.path.startsWith(r.control.previous.path + "/")) this.client.onReflect(r.control.previous.path);
    }
}
