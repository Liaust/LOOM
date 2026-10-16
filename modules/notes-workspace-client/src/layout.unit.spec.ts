import { describe, it, expect, vi } from "vitest";
import { IntentClient, type ClientIO } from "./client";
import { Journal, recoveryPersistence, type State } from "./journal";
import { layoutAlias, layoutPolicyPaths, relocateLocal } from "./layout";
import { controlPath, decode, digest, encode, LAYOUT_PREFIX, validate, type Binding, type Layout } from "./protocol";

async function fixture() {
    const previous: Binding = { version: 1, kind: "binding", id: "old", workspace: "w", collection: "c", generation: "g",
        fileId: "f", collectionRoot: "Projects/Test", path: "Projects/Test/sub/a.md", sourceBase: "base",
        nativeRevision: "1-old", sha256: await digest("original"), writable: true, sourceSequence: 1 };
    const binding: Binding = { ...previous, id: "layout_new", collectionRoot: "Projects/_Passive/Test",
        path: "Projects/_Passive/Test/sub/a.md", nativeRevision: "1-new", sourceSequence: 2 };
    const layout: Layout = { version: 1, kind: "layout", id: "layout_move", previous, binding };
    let saved: State | undefined;
    const persistence = { load: async () => saved, save: async (s: State) => { saved = structuredClone(s); } };
    const j = new Journal(persistence);
    const files = new Map<string, string>([[previous.path, "original"]]);
    const dirs = new Set<string>();
    const io: ClientIO = {
        read: async p => files.get(p) ?? null, readRevision: async () => "original", readControl: async () => layout,
        descends: async () => false, reflect: vi.fn(async () => {}), changed: vi.fn(),
        directoryExists: async p => dirs.has(p),
        relocate: vi.fn(async (a, b) => {
            if (a.entryKind) { dirs.add(b.path); dirs.delete(a.path); return; }
            if (files.has(a.path)) { files.set(b.path, files.get(a.path)!); files.delete(a.path); }
        }),
    };
    const c = new IntentClient(j, io); c.onReflect = vi.fn();
    await c.ready;
    await c.install(previous);
    return { j, c, io, files, dirs, previous, binding, layout, persistence };
}

describe("source-controlled layouts", () => {
    it("strictly validates scope, immutable content, suffix and increasing source order", async () => {
        const { layout } = await fixture();
        expect(decode(controlPath(layout), encode(layout))).toEqual(layout);
        expect(controlPath(layout)).toBe(LAYOUT_PREFIX + "layout/layout_move.md");
        for (const patch of [{ generation: "other" }, { sourceBase: "other" }, { fileId: "other" }, { workspace: "other" },
            { collection: "other" }, { sha256: await digest("different") }, { sourceSequence: 1 }, { path: "Projects/_Passive/Test/other.md" },
            { hostPath: "/secret" }, { writable: "false" }, { entryKind: "directory" }])
            expect(() => validate({ ...layout, binding: { ...layout.binding, ...patch } })).toThrow();
        expect(() => validate({ ...layout, hostPath: "/secret" })).toThrow("unknown_field");
        expect(() => validate({ ...layout, id: "layout_" })).toThrow();
        const archived = { ...layout, binding: { ...layout.binding, generation: "archive", sourceBase: "archive_base", writable: false } };
        expect(validate(archived)).toEqual(archived);
        expect(validate({ ...layout, previous: archived.binding, binding: { ...layout.previous, sourceSequence: 3 } }).kind).toBe("layout");
    });
    it("moves one exact leaf and ignores historical old controls after restart", async () => {
        const f = await fixture();
        await f.c.ingest(f.layout); await f.c.reflectPath(f.previous.path);
        expect(f.files.has(f.previous.path)).toBe(false);
        expect(f.j.state.bindings[f.binding.path]).toEqual(f.binding);
        expect(f.j.state.layouts?.[f.layout.id].state).toBe("applied");
        const c = new IntentClient(new Journal(f.persistence), f.io); c.onReflect = vi.fn(); await c.ready;
        await c.ingest(f.previous); await c.reflectPath(f.previous.path);
        expect(f.io.reflect).not.toHaveBeenCalled();
        expect(layoutAlias(c.journal.state, f.previous.path.slice(0, -3))).toBe(f.binding.path);
        expect(layoutAlias(c.journal.state, "a")).toBeUndefined();
    });
    it("accepts an exact previous directory binding retaining an earlier rename path", async () => {
        const f = await fixture();
        const a: Binding = { ...f.previous, entryKind: "directory", path: "Projects/Test/sub", previousPath: "Projects/Test/earlier", sha256: await digest("") };
        const b: Binding = { ...f.binding, entryKind: "directory", path: "Projects/_Passive/Test/sub", sha256: a.sha256 };
        expect(validate({ ...f.layout, previous: a, binding: b }).kind).toBe("layout");
        expect(() => validate({ ...f.layout, previous: a, binding: { ...b, previousPath: a.path } })).toThrow("invalid_layout");
        await f.c.install(a); f.dirs.add(a.path);
        await f.c.ingest({ ...f.layout, previous: a, binding: b }); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[f.layout.id].state).toBe("applied");
        expect(f.io.relocate).toHaveBeenCalledWith(a, b);
    });
    it("blocks a target binding that arrives before its layout and does not duplicate old content", async () => {
        const f = await fixture();
        await f.c.ingest(f.binding); await f.c.reflectPath(f.binding.path);
        expect(f.io.reflect).not.toHaveBeenCalled(); expect(f.files.has(f.previous.path)).toBe(true);
        await f.c.ingest(f.layout); await f.c.reflectPath(f.binding.path);
        expect(f.files.has(f.previous.path)).toBe(false);
        expect(f.files.get(f.binding.path)).toBe("original");
    });
    it.each(["canvas", "md"])("reflects an ordinary canonical %s rename without a layout control", async extension => {
        const f = await fixture();
        const a = { ...f.previous, id: extension + "_old", path: "Projects/Test/sub/a." + extension };
        const b = { ...a, id: extension + "_renamed", path: "Projects/Test/sub/renamed." + extension,
            sourceSequence: 2, nativeRevision: "1-renamed" };
        f.files.delete(f.previous.path); f.files.set(a.path, "original"); await f.c.install(a);
        await f.j.update(s => { s.holds[b.path] = "layout_waiting_for_control"; });
        f.io.reflect = vi.fn(async binding => {
            const entry = { path: binding.path, _rev: binding.nativeRevision } as any;
            expect(await f.c.beforeReflect(entry)).toBeUndefined();
            f.files.set(binding.path, "original"); await f.c.afterReflect(entry, true);
        });
        await f.c.ingest(b); await f.c.reflectPath(b.path);
        expect(f.c.layouts.waiting(b)).toBe(false); expect(f.io.reflect).toHaveBeenCalledWith(b);
        expect(f.j.state.layouts).toBeUndefined(); expect(f.j.state.bindings[b.path]).toEqual(b);
        expect(f.j.state.holds[b.path]).toBeUndefined();
    });
    it("reflects an ordinary previousPath directory rename with no layout publication", async () => {
        const f = await fixture();
        const a: Binding = { ...f.previous, id: "dir_old", path: "Projects/Test/sub", entryKind: "directory", sha256: await digest("") };
        const b: Binding = { ...a, id: "dir_renamed", path: "Projects/Test/renamed", previousPath: a.path, sourceSequence: 2 };
        await f.c.install(a); f.dirs.add(a.path);
        await f.j.update(s => { s.holds[b.path] = "layout_waiting_for_control"; });
        f.io.reflectDirectory = vi.fn(async binding => { f.dirs.delete(binding.previousPath!); f.dirs.add(binding.path); });
        await f.c.ingest(b); await f.c.reflectPath(b.path);
        expect(f.c.layouts.waiting(b)).toBe(false); expect(f.io.reflectDirectory).toHaveBeenCalledWith(b);
        expect(f.j.state.layouts).toBeUndefined(); expect(f.j.state.bindings[b.path]).toEqual(b);
        expect(f.j.state.holds[b.path]).toBeUndefined();
    });
    it("does not infer a relocation from a layout_ binding without cross-root evidence", async () => {
        const f = await fixture();
        await f.j.update(s => { s.bindings = {}; s.candidates = {}; }); f.files.clear();
        f.io.reflect = vi.fn(async binding => {
            const entry = { path: binding.path, _rev: binding.nativeRevision } as any;
            expect(await f.c.beforeReflect(entry)).toBeUndefined();
            f.files.set(binding.path, "original"); await f.c.afterReflect(entry, true);
        });
        await f.c.ingest(f.binding); await f.c.reflectPath(f.binding.path);
        expect(f.c.layouts.waiting(f.binding)).toBe(false); expect(f.io.reflect).toHaveBeenCalledWith(f.binding);
        expect(f.files.get(f.binding.path)).toBe("original");
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[f.layout.id].state).toBe("applied");
        // Legacy aliases are retired by the applied layout, not by a corpus
        // scan on every ordinary control during startup.
        await f.c.ingest(f.previous); await f.c.reflectCandidate(f.previous);
        expect(f.c.layouts.waiting(f.previous)).toBe(false); expect(f.io.reflect).toHaveBeenCalledTimes(1);
        expect(f.j.state.bindings[f.binding.path]).toEqual(f.binding);
        expect(f.c.layouts.suppress(f.previous)).toBe(true);
    });
    it("does not enumerate bindings or candidates when checking legacy controls", async () => {
        const f = await fixture(), bindings = f.j.state.bindings, candidates = f.j.state.candidates;
        const values = vi.spyOn(Object, "values");
        try {
            for (const id of ["old", "canvas_legacy", "dir_legacy", "layoutish_legacy"])
                expect(f.c.layouts.waiting({ ...f.previous, id })).toBe(false);
            expect(values.mock.calls.filter(([map]) => map === bindings || map === candidates)).toHaveLength(0);
        } finally { values.mockRestore(); }
    });
    it("holds a true relocation when only an old cross-root candidate has arrived", async () => {
        const f = await fixture();
        await f.j.update(s => { s.bindings = {}; s.candidates[f.previous.id] = f.previous; });
        await f.c.ingest(f.binding); await f.c.reflectPath(f.binding.path);
        expect(f.io.reflect).not.toHaveBeenCalled(); expect(f.c.layouts.waiting(f.binding)).toBe(true);
        expect(await f.c.beforeReflect({ path: f.binding.path, _rev: f.binding.nativeRevision } as any)).toBe(true);
        expect(f.files.has(f.binding.path)).toBe(false);
    });
    it("keeps explicit pending layouts gated even when their target ID is not prefixed", async () => {
        const f = await fixture();
        const b = { ...f.binding, id: "legacy_target" };
        await f.c.ingest({ ...f.layout, binding: b });
        expect(f.c.layouts.waiting(b)).toBe(true);
        await f.c.reflectCandidate(b); expect(f.io.reflect).not.toHaveBeenCalled();
    });
    it("allows same-path updates after a layout without reopening retired source paths", async () => {
        const f = await fixture();
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        const update = { ...f.binding, id: "layout_updated", nativeRevision: "2-updated", sourceSequence: 3 };
        await f.c.ingest(update); await f.c.reflectCandidate(update);
        expect(f.c.layouts.waiting(update)).toBe(false); expect(f.io.reflect).toHaveBeenCalledWith(update);
        expect(f.c.layouts.suppress(f.previous)).toBe(true);
    });
    it("reflects layout-owned directory creates and renames after grouping with old-root history retained", async () => {
        const f = await fixture();
        const a: Binding = { ...f.previous, id: "dir_old", path: "Projects/Test/sub", entryKind: "directory", sha256: await digest("") };
        const b: Binding = { ...f.binding, id: "layout_dir_moved", path: "Projects/_Passive/Test/sub", entryKind: "directory", sha256: a.sha256 };
        await f.c.install(a); f.dirs.add(a.path);
        await f.c.ingest({ ...f.layout, previous: a, binding: b }); await f.c.layouts.resume();
        f.io.reflectDirectory = vi.fn(async binding => { if (binding.previousPath) f.dirs.delete(binding.previousPath); f.dirs.add(binding.path); });
        const created = { ...b, id: "layout_dir_created", fileId: "created_dir", path: b.collectionRoot + "/Created", sourceSequence: 3 };
        await f.c.ingest(created); await f.c.reflectPath(created.path);
        const renamed = { ...b, id: "layout_dir_renamed", path: b.collectionRoot + "/Renamed", previousPath: b.path, sourceSequence: 4 };
        await f.c.ingest(renamed); await f.c.reflectPath(renamed.path);
        expect(f.io.reflectDirectory).toHaveBeenCalledWith(created); expect(f.io.reflectDirectory).toHaveBeenCalledWith(renamed);
        expect(f.j.state.bindings[created.path]).toEqual(created); expect(f.j.state.bindings[renamed.path]).toEqual(renamed);
        expect(f.j.state.candidates[a.id]).toEqual(a); expect(f.c.layouts.suppress(a)).toBe(true);
        expect(f.j.state.holds).toEqual({}); expect(Object.keys(f.j.state.layouts!)).toHaveLength(1);
    });
    it("keeps dirty old content and captures its edit against the old path/base", async () => {
        const f = await fixture(); f.files.set(f.previous.path, "offline edit");
        await f.c.ingest(f.layout); await f.c.reflectPath(f.binding.path);
        expect(f.j.state.holds[f.previous.path]).toBe("layout_preserves_local_edit");
        f.c.capture({ kind: "edit", path: f.previous.path }); await f.c.settled();
        const op = f.j.state.operations[0], frozen = JSON.stringify(op.intent);
        expect(op.intent.base).toEqual(f.previous); expect(op.payload).toBe("offline edit");
        await f.c.reflectPath(f.previous.path);
        expect(f.j.state.holds[f.previous.path]).toBe("layout_pending_intent");
        expect(JSON.stringify(f.j.state.operations[0].intent)).toBe(frozen);
        expect(f.io.relocate).not.toHaveBeenCalled();
    });
    it.each(["file", "directory"])("moves an unchanged %s across a metadata-only predecessor refresh and retains old history", async kind => {
        const f = await fixture();
        const old: Binding = kind === "directory" ? { ...f.previous, path: "Projects/Test/sub", entryKind: "directory", sha256: await digest("") } : f.previous;
        await f.c.install(old); if (old.entryKind) f.dirs.add(old.path);
        const previous = { ...old, id: "refreshed_previous", generation: "refreshed_generation", sourceBase: "refreshed_base", sourceSequence: 3 };
        const binding = { ...previous, id: "layout_refreshed", collectionRoot: f.binding.collectionRoot,
            path: f.binding.collectionRoot + old.path.slice(old.collectionRoot.length), sourceSequence: 4,
            nativeRevision: old.entryKind ? previous.nativeRevision : f.binding.nativeRevision };
        await f.c.ingest({ ...f.layout, previous, binding }); await f.c.layouts.resume();
        expect(f.io.relocate).toHaveBeenCalledWith(previous, binding);
        expect(f.j.state.layouts?.[f.layout.id].state).toBe("applied");
        expect(f.j.state.bindings[binding.path]).toEqual(binding);
        expect(f.j.state.candidates[old.id]).toEqual(old); expect(f.j.state.candidates[previous.id]).toEqual(previous);
        expect(f.j.state.operations).toHaveLength(0); expect(f.j.state.holds[old.path]).toBeUndefined();
    });
    it.each(["file native revision", "file hash", "directory native revision", "directory hash"])("refuses predecessor refinement with changed %s", async change => {
        const f = await fixture();
        const old: Binding = change.startsWith("directory") ? { ...f.previous, path: "Projects/Test/sub", entryKind: "directory", sha256: await digest("") } : f.previous;
        await f.c.install(old); if (old.entryKind) f.dirs.add(old.path);
        const previous = { ...old, id: "refreshed_previous", generation: "refreshed_generation", sourceBase: "refreshed_base", sourceSequence: 3,
            ...(change.endsWith("native revision") ? { nativeRevision: "2-changed" } : { sha256: await digest("changed") }) };
        const binding = { ...previous, id: "layout_refreshed", collectionRoot: f.binding.collectionRoot,
            path: f.binding.collectionRoot + old.path.slice(old.collectionRoot.length), sourceSequence: 4 };
        await f.c.ingest({ ...f.layout, previous, binding }); await f.c.layouts.resume();
        expect(f.io.relocate).not.toHaveBeenCalled(); expect(f.j.state.bindings[old.path]).toEqual(old);
        expect(f.j.state.layouts?.[f.layout.id].reason).toBe("layout_previous_changed");
    });
    it("replaces an old held layout after an offline edit is accepted at the old root", async () => {
        const f = await fixture(); f.files.set(f.previous.path, "accepted offline edit");
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        const a = { ...f.previous, id: "accepted", sourceBase: "accepted_base", sourceSequence: 4,
            nativeRevision: "2-accepted", sha256: await digest("accepted offline edit") };
        await f.c.install(a);
        const b = { ...f.binding, id: "accepted_move", sourceBase: a.sourceBase, sourceSequence: 5, sha256: a.sha256 };
        const next = { ...f.layout, id: "layout_accepted", previous: a, binding: b };
        await f.c.ingest(next); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[f.layout.id]).toMatchObject({ state: "superseded", supersededBy: next.id });
        expect(f.j.state.layouts?.[next.id].state).toBe("applied");
        expect(f.j.state.holds[f.previous.path]).toBeUndefined();
        expect(f.files.get(b.path)).toBe("accepted offline edit");
        await f.c.reflectCandidate(f.previous); expect(f.io.reflect).not.toHaveBeenCalled();
    });
    it("does not materialize a later hop while the first hop retains a dirty old file", async () => {
        const f = await fixture(); f.files.set(f.previous.path, "offline edit");
        const b = { ...f.binding, id: "archived", path: "Projects/_Archived/Test/sub/a.md", collectionRoot: "Projects/_Archived/Test",
            generation: "archive", sourceBase: "archive_base", writable: false, sourceSequence: 3 };
        await f.c.ingest({ ...f.layout, id: "layout_archive", previous: f.binding, binding: b });
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        expect(f.io.reflect).not.toHaveBeenCalled(); expect(f.io.relocate).not.toHaveBeenCalled();
        expect(f.files.get(f.previous.path)).toBe("offline edit");
        expect(f.j.state.layouts?.layout_archive.reason).toBe("layout_chain_pending");
    });
    it("marks a replaced layout terminal even when its stale control arrives last", async () => {
        const f = await fixture();
        const a = { ...f.previous, id: "latest_old", sourceSequence: 4, nativeRevision: "2-latest" };
        const b = { ...f.binding, id: "latest_new", sourceSequence: 5 };
        await f.c.install(a);
        await f.c.ingest({ ...f.layout, id: "layout_latest", previous: a, binding: b });
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[f.layout.id]).toMatchObject({ state: "superseded", supersededBy: "layout_latest" });
        expect(f.j.state.bindings[b.path]).toEqual(b);
    });
    it("reflects a later accepted old-path edit into a proven unmodified moved destination", async () => {
        const f = await fixture();
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        const a = { ...f.previous, id: "foreign_accepted", sourceSequence: 3, nativeRevision: "2-old",
            sourceBase: "accepted", sha256: await digest("accepted edit") };
        const b = { ...f.binding, id: "foreign_moved", sourceSequence: 4, nativeRevision: "2-new", sourceBase: a.sourceBase, sha256: a.sha256 };
        f.io.descends = async () => true;
        f.io.reflect = vi.fn(async binding => { f.files.set(binding.path, "accepted edit"); await f.c.install(binding); });
        await f.c.ingest({ ...f.layout, id: "layout_foreign", previous: a, binding: b }); await f.c.layouts.resume();
        expect(f.j.state.layouts?.layout_foreign.state).toBe("applied");
        expect(f.files.get(b.path)).toBe("accepted edit"); expect(f.files.has(a.path)).toBe(false);
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("retains a concrete per-leaf move failure reason", async () => {
        const f = await fixture(); f.io.relocate = async () => { throw Error("layout_destination_collision"); };
        await f.c.ingest(f.layout); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[f.layout.id].reason).toBe("layout_destination_collision");
    });
    it.each(["old", "target"])("waits for pending edits at either %s path", async side => {
        const f = await fixture(); const path = side === "old" ? f.previous.path : f.binding.path;
        await f.j.update(s => { s.operations.push({ intent: { version: 1, kind: "intent", id: "pending", device: "device",
            operation: "edit", workspace: "w", collection: "c", generation: "g", fileId: "f", base: f.previous,
            path, sha256: f.previous.sha256, length: 8 }, payload: "original", revisions: [], state: "queued" }); });
        await f.c.ingest(f.layout); await f.c.reflectPath(f.binding.path);
        expect(f.j.state.holds[f.previous.path]).toBe("layout_pending_intent"); expect(f.io.relocate).not.toHaveBeenCalled();
    });
    it.each(["unmanaged", "original"])("preserves an unmanaged destination even with %s bytes", async text => {
        const f = await fixture(); f.files.set(f.binding.path, text);
        await f.c.ingest(f.layout); await f.c.reflectPath(f.binding.path);
        expect(f.j.state.holds[f.previous.path]).toBe("layout_destination_collision");
        expect(f.files.get(f.previous.path)).toBe("original"); expect(f.files.get(f.binding.path)).toBe(text);
    });
    it("resumes a move interrupted after the vault rename without inventing an intent", async () => {
        const f = await fixture();
        f.io.relocate = vi.fn(async (a, b) => {
            f.files.set(b.path, f.files.get(a.path)!); f.files.delete(a.path);
            f.c.capture({ kind: "rename", oldPath: a.path, path: b.path });
            throw Error("stopped after rename");
        });
        await f.c.ingest(f.layout); await f.c.reflectPath(f.binding.path);
        expect(f.j.state.layouts?.[f.layout.id].state).toBe("moving"); expect(f.j.state.operations).toHaveLength(0);
        f.io.relocate = vi.fn(async () => {});
        const c = new IntentClient(new Journal(f.persistence), f.io); c.onReflect = vi.fn(); await c.ready;
        await c.reflectPath(f.binding.path);
        expect(c.journal.state.layouts?.[f.layout.id].state).toBe("applied");
        expect(c.journal.state.bindings[f.previous.path]).toBeUndefined();
    });
    it("applies an out-of-order chain and a reversal; aliases select the latest destination", async () => {
        const f = await fixture();
        const back = { ...f.previous, id: "back", nativeRevision: "3-back", sourceSequence: 3 };
        const reverse: Layout = { ...f.layout, id: "layout_back", previous: f.binding, binding: back };
        await f.c.ingest(reverse); await f.c.ingest(f.layout); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[reverse.id].state).toBe("applied");
        expect(f.files.has(f.binding.path)).toBe(false); expect(f.j.state.bindings[back.path]).toEqual(back);
        expect(layoutAlias(f.j.state, f.binding.path)).toBe(back.path);
        expect(layoutAlias(f.j.state, f.previous.path)).toBeUndefined();
        await f.c.reflectCandidate(f.binding); expect(f.io.reflect).not.toHaveBeenCalled();
    });
    it("accepts read-only archive metadata without turning a draft into a new source edit", async () => {
        const f = await fixture();
        const archive = { ...f.binding, writable: false, generation: "archive", sourceBase: "archive_base" };
        await f.c.ingest({ ...f.layout, binding: archive }); await f.c.layouts.resume();
        f.files.set(archive.path, "private draft");
        f.c.capture({ kind: "edit", path: archive.path }); await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0); expect(f.files.get(archive.path)).toBe("private draft");
        expect(f.j.state.holds[archive.path]).toBe("reference_only");
    });
    it("quietly records excluded references without reading bytes or claiming delivery", async () => {
        const f = await fixture();
        const a = { ...f.previous, path: "Projects/Test/book.pdf", writable: false };
        const b = { ...f.binding, path: "Projects/_Passive/Test/book.pdf", writable: false };
        f.io.referenceAllowed = async () => false; f.io.readReference = vi.fn(async () => { throw Error("excluded"); });
        await f.j.update(s => { s.bindings = { [a.path]: a }; });
        await f.c.ingest({ ...f.layout, previous: a, binding: b }); await f.c.layouts.resume();
        expect(f.io.readReference).not.toHaveBeenCalled(); expect(f.io.relocate).not.toHaveBeenCalled();
        expect(f.j.state.layouts?.[f.layout.id]).toMatchObject({ state: "pending", disposition: "excluded", reason: "layout_reference_excluded" });
        expect(f.j.state.bindings[a.path]).toEqual(a); expect(f.j.state.bindings[b.path]).toBeUndefined();
        expect(f.j.state.holds).toEqual({}); expect(layoutAlias(f.j.state, a.path)).toBeUndefined();
        const c = new IntentClient(new Journal(f.persistence), f.io); c.onReflect = vi.fn(); await c.ready;
        await c.layouts.resume(); await c.reflectCandidate(a); await c.reflectCandidate(b);
        expect(c.layouts.suppress(a)).toBe(true); expect(c.layouts.suppress(b)).toBe(true);
        expect(c.journal.state.holds).toEqual({}); expect(f.io.reflect).not.toHaveBeenCalled();
        expect(f.io.readReference).not.toHaveBeenCalled();
    });
    it.each(["old-prefix exclusion", "50 MiB ceiling"])("quietly skips a new reference binding and layout under the existing %s", async reason => {
        const f = await fixture();
        const settings = Object.freeze({ syncIgnoreRegEx: "^Projects/Goodnotes Intake/Notebooks(?:/|$)", syncMaxSizeInMB: 50 });
        const before = JSON.stringify(settings), ignored = new RegExp(settings.syncIgnoreRegEx);
        const project = reason === "old-prefix exclusion" ? "Goodnotes Intake" : "Other";
        const a = { ...f.previous, path: `Projects/${project}/Notebooks/book.pdf`, collectionRoot: `Projects/${project}`, writable: false };
        const b = { ...f.binding, path: `Projects/_Passive/${project}/Notebooks/book.pdf`, collectionRoot: `Projects/_Passive/${project}`, writable: false };
        f.io.referenceAllowed = async path => layoutPolicyPaths(f.j.state, path).every(p => !ignored.test(p)) &&
            (reason === "old-prefix exclusion" ? 1024 : 50 * 1024 * 1024 + 1) <= settings.syncMaxSizeInMB * 1024 * 1024;
        f.io.readReference = vi.fn(async () => { throw Error("must_not_assemble_pdf"); });
        await f.j.update(s => { s.bindings = { [a.path]: a }; });
        await f.c.ingest(b); await f.c.reflectCandidate(b);
        expect(f.j.state.holds).toEqual({});
        await f.c.ingest({ ...f.layout, previous: a, binding: b }); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[f.layout.id].disposition).toBe("excluded");
        expect(f.j.state.holds).toEqual({}); expect(f.io.readReference).not.toHaveBeenCalled();
        expect(f.io.reflect).not.toHaveBeenCalled(); expect(f.io.relocate).not.toHaveBeenCalled();
        expect(JSON.stringify(settings)).toBe(before);
    });
    it("can explicitly retry an excluded layout after a deliberate device policy change", async () => {
        const f = await fixture();
        const a = { ...f.previous, path: "Projects/Test/book.pdf", writable: false };
        const b = { ...f.binding, path: "Projects/_Passive/Test/book.pdf", writable: false };
        f.files.delete(f.previous.path); f.files.set(a.path, "original");
        f.io.referenceAllowed = async () => false;
        f.io.readReference = async path => f.files.has(path) ? new TextEncoder().encode(f.files.get(path)!) : null;
        await f.j.update(s => { s.bindings = { [a.path]: a }; });
        await f.c.ingest({ ...f.layout, previous: a, binding: b }); await f.c.layouts.resume();
        f.io.referenceAllowed = async () => true;
        await f.c.layouts.resume(); expect(f.io.relocate).not.toHaveBeenCalled();
        await f.c.layouts.resume(undefined, true);
        expect(f.j.state.layouts?.[f.layout.id].state).toBe("applied");
        expect(f.j.state.layouts?.[f.layout.id].disposition).toBeUndefined();
        expect(f.files.has(a.path)).toBe(false); expect(f.files.get(b.path)).toBe("original");
    });
    it("does not leave a directory blocked on intentionally excluded child layouts", async () => {
        const f = await fixture();
        const a = { ...f.previous, path: "Projects/Test/sub/book.pdf", writable: false };
        const b = { ...f.binding, path: "Projects/_Passive/Test/sub/book.pdf", writable: false };
        const parent = { ...f.previous, fileId: "parent", id: "dir_old", path: "Projects/Test/sub", entryKind: "directory" as const, sha256: await digest("") };
        const next = { ...f.binding, fileId: "parent", id: "dir_new", path: "Projects/_Passive/Test/sub", entryKind: "directory" as const, sha256: parent.sha256 };
        f.io.referenceAllowed = async () => false;
        await f.j.update(s => { s.bindings = { [a.path]: a, [parent.path]: parent }; }); f.dirs.add(parent.path);
        await f.c.ingest({ ...f.layout, previous: a, binding: b });
        await f.c.ingest({ ...f.layout, id: "layout_parent", previous: parent, binding: next });
        await f.c.layouts.resume(); await f.c.layouts.resume();
        expect(f.j.state.layouts?.layout_parent.state).toBe("applied"); expect(f.j.state.holds).toEqual({});
    });
    it("retains old-prefix reference exclusions before control arrival and across persisted layout chains", async () => {
        const path = "Projects/_Passive/Goodnotes Intake/notebooks/book.pdf";
        expect(layoutPolicyPaths(undefined, path)).toContain("Projects/Goodnotes Intake/notebooks/book.pdf");
        const f = await fixture();
        await f.c.ingest(f.layout);
        const paths = layoutPolicyPaths(f.j.state, f.binding.path);
        expect(paths).toContain(f.previous.path);
        expect(paths).toContain(f.binding.path);
        const legacyPolicy = (p: string) => !p.startsWith("Projects/Test/");
        expect(paths.every(legacyPolicy)).toBe(false);
        expect(layoutPolicyPaths(undefined, "Projects/_Archived/Test/book.pdf")).toEqual([
            "Projects/_Archived/Test/book.pdf", "Projects/Test/book.pdf"]);
    });
    it("keeps directory layout pending until its known leaf moves finish", async () => {
        const f = await fixture();
        const a: Binding = { ...f.previous, id: "dir_old", fileId: "dir", path: "Projects/Test/sub", entryKind: "directory", sha256: await digest("") };
        const b: Binding = { ...a, ...f.binding, id: "dir_new", fileId: "dir", path: "Projects/_Passive/Test/sub", entryKind: "directory", sha256: a.sha256 };
        f.dirs.add(a.path); await f.c.install(a);
        const dir = { ...f.layout, id: "layout_dir", previous: a, binding: b };
        await f.c.ingest(dir); await f.c.ingest(f.layout);
        f.files.set(f.previous.path, "dirty"); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[dir.id].state).toBe("pending"); expect(f.dirs.has(a.path)).toBe(true);
        f.files.set(f.previous.path, "original"); await f.c.layouts.resume(); await f.c.layouts.resume();
        expect(f.j.state.layouts?.[dir.id].state).toBe("applied");
    });
    it("exports layouts without requiring them in a legacy journal", async () => {
        const f = await fixture(); await f.c.ingest(f.layout);
        let exported = "";
        const persistence = recoveryPersistence(f.persistence, "vault", async (_, text) => { exported = text; });
        await persistence.save(f.j.state); await persistence.flush!();
        expect(JSON.parse(exported).state.layouts.layout_move.control).toEqual(f.layout);
        const legacy = structuredClone(f.j.state); delete legacy.layouts;
        const j = new Journal({ load: async () => legacy, save: async () => {} }); await j.ready;
        expect(j.state.layouts).toBeUndefined(); expect(j.state.bindings).toEqual(legacy.bindings);
    });
    it("creates a target directory without recursively moving unknown old children", async () => {
        const f = await fixture(), paths = new Map<string, "file" | "folder">();
        const a = { ...f.previous, path: "Projects/Test/sub", entryKind: "directory" as const };
        const b = { ...f.binding, path: "Projects/_Passive/Test/sub", entryKind: "directory" as const };
        paths.set(a.path, "folder"); paths.set(a.path + "/unknown.txt", "file");
        const rename = vi.fn();
        await relocateLocal(a, b, { stat: async p => paths.get(p) ?? null, hash: async () => null,
            mkdir: async p => { paths.set(p, "folder"); }, rename, removeFile: vi.fn(),
            removeEmpty: async p => { if (![...paths.keys()].some(x => x.startsWith(p + "/"))) paths.delete(p); } });
        expect(rename).not.toHaveBeenCalled(); expect(paths.has(a.path + "/unknown.txt")).toBe(true);
        expect(paths.has(b.path)).toBe(true);
    });
    it("trims only empty exact source ancestors and stops before Projects", async () => {
        const f = await fixture(), paths = new Map<string, "file" | "folder">();
        for (const p of ["Projects", "Projects/Test", "Projects/Test/sub"]) paths.set(p, "folder");
        paths.set(f.previous.path, "file"); paths.set("Projects/unrelated.md", "file");
        const removed: string[] = [];
        await relocateLocal(f.previous, f.binding, { stat: async p => paths.get(p) ?? null,
            hash: async p => paths.get(p) === "file" ? f.previous.sha256 : null,
            mkdir: async p => { paths.set(p, "folder"); },
            rename: async (a, b) => { paths.delete(a); paths.set(b, "file"); }, removeFile: vi.fn(),
            removeEmpty: async p => {
                if (![...paths.keys()].some(x => x.startsWith(p + "/"))) { paths.delete(p); removed.push(p); }
            } });
        expect(removed).toEqual(["Projects/Test/sub", "Projects/Test"]);
        expect(paths.has("Projects")).toBe(true); expect(paths.has("Projects/unrelated.md")).toBe(true);
    });
});
