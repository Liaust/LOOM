import { describe, it, expect, vi } from "vitest";
import { Journal, recoveryPersistence, type State, type Persistence } from "./journal";
import { IntentClient, type ClientIO } from "./client";
import { digest, validate, type Binding } from "./protocol";
import { readContent, readAsBlob } from "@vrtmrz/livesync-commonlib/compat/common/utils";
const base: Binding = {
    version: 1,
    kind: "binding",
    id: "b",
    workspace: "w",
    collection: "c",
    generation: "g",
    fileId: "f",
    collectionRoot: "Notes",
    path: "Notes/a.md",
    sourceBase: "A",
    nativeRevision: "1-a",
    sha256: "",
    writable: true,
};
describe("client recovery export", () => {
    it("honours device reference exclusions before payload reads and preserves durable state", async () => {
        const f = await fixture();
        const b = { ...f.b, path: "Notes/book.pdf", writable: false };
        await f.j.update(s => {
            s.bindings[b.path] = b;
            s.candidates[b.id] = b;
            s.reflecting[b.path] = b;
        });
        f.io.referenceAllowed = vi.fn(async () => false);
        f.io.readReference = vi.fn(async () => { throw Error("must_not_load_excluded_reference"); });
        const recovered = new IntentClient(f.j, f.io);
        await recovered.ready;
        expect(await recovered.mutation({ kind: "store", info: b.path as never })).toBe(true);
        const entry = { path: b.path, _rev: b.nativeRevision } as any;
        expect(await recovered.beforeReflect(entry)).toBe(true);
        await recovered.afterReflect(entry, true);
        expect(f.io.readReference).not.toHaveBeenCalled();
        expect(f.j.state.bindings[b.path]).toEqual(b);
        expect(f.j.state.reflecting[b.path]).toEqual(b);
        expect(f.j.state.holds[b.path]).toBeUndefined();
        // Markdown still follows the normal exact-base reflection path.
        await f.j.update(s => { s.candidates[f.b.id] = f.b; });
        expect(await recovered.beforeReflect({ path: f.b.path, _rev: f.b.nativeRevision } as any)).toBeUndefined();
        expect(f.io.referenceAllowed).not.toHaveBeenCalledWith(f.b.path, f.b.nativeRevision);
    });
    it.each(["canvas", "svg", "csv"])("decodes explicit binary %s metadata before extension fallback", async extension => {
        const expected = new TextEncoder().encode("Unicode \u03bb and independent chunks");
        const data = [expected.slice(0, 5), expected.slice(5)].map(bytes => btoa(String.fromCharCode(...bytes)));
        const entry = { path: `Notes/reference.${extension}`, type: "newnote", datatype: "newnote", data } as any;
        expect(new Uint8Array(readContent(entry) as ArrayBuffer)).toEqual(expected);
        expect(new Uint8Array(await readAsBlob(entry).arrayBuffer())).toEqual(expected);
        expect(await digest(new Uint8Array(readContent(entry) as ArrayBuffer))).toBe(await digest(expected));
        expect(readContent({ ...entry, type: "plain", datatype: "plain", data: ["plain text"] })).toBe("plain text");
        expect(readContent({ path: entry.path, data: ["legacy text"] } as any)).toBe("legacy text");
    });
    it("does not park a revision waiting for chunks as a permanent hash mismatch", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "late", nativeRevision: "2-b", sha256: await digest("B") };
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.io.reflect = vi.fn(async () => { await f.c.beforeReflect({ path: b.path, _rev: b.nativeRevision } as any); });
        await f.c.reflectPath(b.path);
        expect(f.j.state.holds[b.path]).toBe("reflection_chunks_pending");
        f.revisions.set(b.nativeRevision, "B");
        await f.c.reflectPath(b.path);
        expect(f.j.state.reflecting[b.path]).toEqual(b);
        expect(f.io.reflect).toHaveBeenCalledTimes(2);
    });
    it("parks a stable bad revision and retries only its path when explicitly requested", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "bad", nativeRevision: "2-b", sha256: await digest("B") };
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.io.reflect = vi.fn(async () => { await f.c.hold(b.path, "reflection_hash_mismatch"); });
        await f.c.reflectPath(b.path);
        await f.c.reflectPath(b.path);
        expect(f.io.reflect).toHaveBeenCalledTimes(1);
        await f.c.reflectPath("Notes/unrelated.md");
        expect(f.io.reflect).toHaveBeenCalledTimes(1);
        await f.c.reflectPath(b.path, true);
        expect(f.io.reflect).toHaveBeenCalledTimes(2);
        const next = { ...b, id: "repaired", nativeRevision: "3-c" };
        await f.j.update(s => { s.candidates[next.id] = next; });
        await f.c.reflectPath(b.path);
        expect(f.io.reflect).toHaveBeenLastCalledWith(next);
        expect(Object.keys(f.j.state.candidates)).toHaveLength(2);
    });
    it("checks no-op guards against serialized state before cloning", async () => {
        const save = vi.fn(async (_s: State) => {});
        const j = new Journal({ load: async () => undefined, save });
        await j.ready;
        await j.update(s => { s.holds.x = "pending"; });
        const clone = vi.spyOn(globalThis, "structuredClone");
        try {
            await j.update(() => { throw Error("must_not_run"); }, s => s.holds.x === "pending");
            expect(clone).not.toHaveBeenCalled();
            const first = j.update(s => { delete s.holds.x; });
            const second = j.update(s => { s.holds.x = "pending"; }, s => s.holds.x === "pending");
            await Promise.all([first, second]);
            expect(j.state.holds.x).toBe("pending");
            expect(save).toHaveBeenCalledTimes(4);
        } finally { clone.mockRestore(); }
    });
    it("does not rewrite durable recovery exports for unchanged state", async () => {
        const save = vi.fn(async (_s: State) => {});
        const j = new Journal({ load: async () => undefined, save });
        await j.ready;
        await j.update(() => {});
        expect(save).toHaveBeenCalledTimes(1);
        await j.update(s => { s.holds.x = "pending"; });
        await j.update(s => { s.holds.x = "pending"; });
        expect(save).toHaveBeenCalledTimes(2);
    });
    it("alternates full checkpoints only after durable save and preserves pending bytes", async () => {
        const slots = new Map<number, string>();
        let saved: State | undefined;
        const persistence = recoveryPersistence({ load: async () => saved, save: async s => { saved = s; } }, "vault-1",
            async (slot, content) => {
                expect(JSON.parse(content).state).toEqual(saved);
                slots.set(slot, content);
            });
        const j = new Journal(persistence);
        await j.ready;
        await j.checkpoint();
        await j.update(s => { s.holds["note.md"] = "preserve_local_variant"; });
        expect(slots.size).toBe(1);
        await j.checkpoint();
        expect(slots.size).toBe(2);
        expect(JSON.parse(slots.get(1)!).state.holds["note.md"]).toBe("preserve_local_variant");
        const previous = slots.get(1);
        await j.update(s => { s.holds["second.md"] = "pending"; });
        await j.checkpoint();
        expect(slots.get(1)).toBe(previous);
        expect(JSON.parse(slots.get(0)!).vaultId).toBe("vault-1");
    });
    it("does not treat failed durable storage as a backed-up save", async () => {
        const write = vi.fn();
        const persistence = recoveryPersistence({ load: async () => undefined, save: async () => { throw Error("disk"); } }, "vault", write);
        const j = new Journal(persistence);
        await expect(j.ready).rejects.toThrow("disk");
        expect(write).not.toHaveBeenCalled();
    });
});
export async function fixture() {
    const b = { ...base, sha256: await digest("A") };
    let saved: State | undefined;
    const persistence: Persistence = {
        load: async () => structuredClone(saved),
        save: async (s) => {
            saved = structuredClone(s);
        },
    };
    const j = new Journal(persistence);
    await j.ready;
    await j.update((s) => {
        s.bindings[b.path] = b;
    });
    const files = new Map([[b.path, "A"]]);
    const revisions = new Map([[b.nativeRevision, "A"]]);
    const io: ClientIO = {
        read: async (p) => files.get(p) ?? null,
        readRevision: async (_p, r) => revisions.get(r) ?? null,
        readControl: vi.fn(),
        descends: async (_p, r, a) => Number(r.split("-")[0]) > Number(a.split("-")[0]),
        reflect: async () => {},
        changed: vi.fn(),
    };
    const c = new IntentClient(j, io);
    await c.ready;
    return { b, j, c, io, files, revisions, persistence };
}
describe("durable admission", () => {
    it("registers a preserved local note through a proven enrollment transition", async () => {
        const f = await fixture();
        const old = { ...f.b, sourceSequence: 1 };
        const current = { ...old, id: "current", generation: "current", sourceSequence: 2 };
        await f.j.update(s => {
            s.bindings[old.path] = current;
            s.bindings["Notes/retained.md"] = { ...old, path: "Notes/retained.md", fileId: "retained" };
            s.candidates[old.id] = old;
            s.candidates[current.id] = current;
        });
        f.files.set("Notes/local.md", "preserved local bytes");
        await f.c.registerLocalNote("Notes/local.md");
        const op = f.j.state.operations[0];
        expect(op.intent.operation).toBe("create");
        expect(op.intent.generation).toBe("current");
        expect(op.intent.base).toBeNull();
        expect(op.payload).toBe("preserved local bytes");
        await expect(f.c.registerLocalNote("Notes/local.md")).rejects.toThrow("already_tracked");
        f.files.set("Notes/new.md", "new bytes");
        f.c.capture({ kind: "create", path: "Notes/new.md" });
        await f.c.settled();
        expect(f.j.state.operations[1].intent.generation).toBe("current");
    });
    it.each(["unproven", "branch", "cycle", "other_scope"])("holds %s enrollment ambiguity", async kind => {
        const f = await fixture();
        const old = { ...f.b, sourceSequence: 1 };
        const next = { ...old, id: "next", generation: "next", sourceSequence: 2 };
        await f.j.update(s => {
            s.bindings[old.path] = old;
            s.bindings["Notes/other.md"] = { ...next, path: "Notes/other.md", fileId: "other" };
            s.candidates[old.id] = old;
            if (kind !== "unproven") s.candidates[next.id] = next;
            if (kind === "branch") s.candidates.branch = { ...next, id: "branch", generation: "branch" };
            if (kind === "cycle") s.candidates.cycle = { ...old, id: "cycle", sourceSequence: 3 };
            if (kind === "other_scope") s.bindings.scope = { ...old, workspace: "another" };
        });
        f.files.set("Notes/local.md", "do not lose");
        await expect(f.c.registerLocalNote("Notes/local.md")).rejects.toThrow("scope_ambiguous");
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.files.get("Notes/local.md")).toBe("do not lose");
    });
    it("admits one create when local registration is requested concurrently", async () => {
        const f = await fixture();
        f.files.set("Notes/local.md", "local bytes");
        const results = await Promise.allSettled([
            f.c.registerLocalNote("Notes/local.md"), f.c.registerLocalNote("Notes/local.md"),
        ]);
        expect(results.filter(r => r.status === "fulfilled")).toHaveLength(1);
        expect(f.j.state.operations).toHaveLength(1);
    });
    it("skips installed candidates without traversing the retained candidate corpus", async () => {
        const f = await fixture();
        await f.j.update(s => { s.candidates[f.b.id] = f.b; });
        const values = vi.spyOn(Object, "values");
        f.io.reflect = vi.fn();
        try {
            await f.c.reflectCandidate(f.b);
            expect(values).not.toHaveBeenCalled();
            expect(f.io.reflect).not.toHaveBeenCalled();
        } finally { values.mockRestore(); }
        await f.c.hold(f.b.path, "native_conflict_requires_resolution");
        await f.c.reflectCandidate(f.b);
        expect(f.io.reflect).toHaveBeenCalledWith(f.b);
    });
    it("invalidates path lookup after durable candidate changes and reports reflection holds", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "second", nativeRevision: "2-b", sourceBase: "B", sha256: await digest("B") };
        await f.j.update(s => { s.candidates[f.b.id] = f.b; });
        await f.c.beforeReflect({ path: f.b.path, _rev: f.b.nativeRevision } as any);
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.revisions.set(b.nativeRevision, "B");
        expect(await f.c.beforeReflect({ path: b.path, _rev: b.nativeRevision } as any)).toBeUndefined();
        await f.c.hold("Notes/attachment.canvas", "reflection_hash_mismatch");
        expect(f.c.details().split("\n")[0]).toContain("1 reflection holds");
    });
    it("adopts an advanced enrollment without replaying withdrawn bindings", async () => {
        const f = await fixture();
        const old = { ...f.b, sourceSequence: 12 };
        const next = { ...old, id: "expanded", generation: "expanded", sourceSequence: 13,
            nativeRevision: "2-expanded", sourceBase: "expandedbase" };
        await f.j.update(s => { s.bindings[old.path] = old; s.candidates[next.id] = next; });
        f.revisions.set(next.nativeRevision, "A");
        const entry = { path: next.path, _rev: next.nativeRevision } as any;
        expect(await f.c.beforeReflect(entry)).toBeUndefined();
        await f.c.afterReflect(entry, true);
        expect(f.j.state.bindings[next.path]).toEqual(next);
        f.io.reflect = vi.fn();
        await f.c.reflectCandidate(old);
        expect(f.io.reflect).not.toHaveBeenCalled();
        await f.c.stopNative();
    });
    it("coalesces only accepted descendant display requests before installation", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        const older = { ...f.b, id: "older", nativeRevision: "2-b" };
        const newer = { ...f.b, id: "newer", nativeRevision: "3-c" };
        await f.j.update(s => { s.candidates[older.id] = older; s.candidates[newer.id] = newer; });
        await f.c.reflectCandidate(older);
        expect(f.io.reflect).not.toHaveBeenCalled();
        await f.c.reflectCandidate(newer);
        expect(f.io.reflect).toHaveBeenCalledWith(newer);
        expect(Object.keys(f.j.state.candidates)).toHaveLength(2);
        f.io.descends = async () => false;
        await f.c.reflectCandidate(older);
        expect(f.io.reflect).toHaveBeenLastCalledWith(older);
    });
    it("reflects exact binary references, preserves local changes and never publishes edits", async () => {
        const f = await fixture();
        const bytes = new Uint8Array([137, 80, 78, 71, 0, 255]);
        const b: Binding = { ...f.b, id: "reference", fileId: "ref", path: "Notes/image.png", writable: false, sha256: await digest(bytes) };
        expect(validate(b)).toEqual(b);
        expect(() => validate({ ...b, writable: true })).toThrow();
        let local: Uint8Array<ArrayBuffer> | null = null;
        f.io.readReference = async (_path, rev) => rev ? bytes : local;
        await f.j.update(s => { s.candidates[b.id] = b; });
        const entry = { path: b.path, _rev: b.nativeRevision } as any;
        expect(await f.c.beforeReflect(entry)).toBeUndefined();
        local = bytes;
        await f.c.afterReflect(entry, true);
        expect(f.j.state.bindings[b.path]).toEqual(b);
        const next = { ...b, id: "nextref", sourceBase: "nextbase", nativeRevision: "2-next" };
        await f.j.update(s => { s.candidates[next.id] = next; });
        local = new Uint8Array([1, 2, 3]);
        expect(await f.c.beforeReflect({ ...entry, _rev: next.nativeRevision })).toBe(true);
        expect(f.j.state.holds[b.path]).toBe("reflection_preserves_local_edit");
        f.c.capture({ kind: "edit", path: b.path });
        await f.c.settled();
        await f.c.mutation({ kind: "store", info: b.path as any });
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.j.state.holds[b.path]).toBe("reference_local_change");
    });
    it("does not reflect while a local edit or conflict is unresolved", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        f.files.set(f.b.path, "offline");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        const candidate = { ...f.b, id: "incoming", nativeRevision: "2-server" };
        await f.c.reflectCandidate(candidate);
        await f.j.update(s => { s.operations[0].state = "conflict"; });
        await f.c.reflectCandidate(candidate);
        expect(f.io.reflect).not.toHaveBeenCalled();
    });

    it.each(["rename", "delete"] as const)("skips retired %s history without hiding newer or unrelated controls", async kind => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        f.files.delete(f.b.path);
        const target = "Notes/renamed.md";
        if (kind === "rename") f.files.set(target, "A");
        f.c.capture({ kind, path: kind === "rename" ? target : f.b.path,
            ...(kind === "rename" ? { oldPath: f.b.path } : {}) });
        await f.c.settled();
        await f.j.update(s => {
            s.operations[0].state = "applied";
            delete s.bindings[f.b.path];
        });
        f.io.descends = vi.fn(async (_p, r, a) => r === "1-a" && a === "0-old");
        await f.c.reflectCandidate(f.b);
        await f.c.reflectCandidate({ ...f.b, id: "older", nativeRevision: "0-old" });
        expect(f.io.reflect).not.toHaveBeenCalled();
        for (const change of [{ nativeRevision: "2-new" }, { generation: "other" }, { fileId: "other" }]) {
            const candidate = { ...f.b, id: crypto.randomUUID(), ...change };
            await f.c.reflectCandidate(candidate);
            expect(f.io.reflect).toHaveBeenLastCalledWith(candidate);
        }
    });

    it("does not ask native reflection to materialize installed or proven ancestor controls", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        await f.c.reflectCandidate(f.b);
        const next = { ...f.b, id: "next", nativeRevision: "2-next" };
        await f.c.install(next);
        f.io.descends = vi.fn(async (_p, revision, ancestor) =>
            revision === "2-next" && ancestor === "1-a");
        await f.c.reflectCandidate(f.b);
        expect(f.io.reflect).not.toHaveBeenCalled();
        expect(f.io.descends).toHaveBeenCalledWith(f.b.path, "2-next", "1-a");
    });

    it("keeps newer, unrelated, different-scope and same-revision collision candidates visible", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        f.io.descends = vi.fn(async () => false);
        for (const change of [
            { nativeRevision: "2-new" },
            { nativeRevision: "1-other" },
            { sourceBase: "different" },
            { generation: "other", nativeRevision: "0-old" },
            { fileId: "other", nativeRevision: "0-old" },
        ]) {
            const candidate = { ...f.b, id: crypto.randomUUID(), ...change };
            await f.c.reflectCandidate(candidate);
            expect(f.io.reflect).toHaveBeenLastCalledWith(candidate);
        }
        expect(f.io.reflect).toHaveBeenCalledTimes(5);
    });

    it("clears supersession when the latest stable save equals the retained base", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "superseded");
        f.c.capture({ kind: "edit", path: f.b.path });
        f.files.set(f.b.path, "A");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        f.files.set(f.b.path, "later");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations[0].intent.base).toEqual(f.b);
        expect(f.j.state.operations[0].intent.predecessor).toBeUndefined();
    });

    it.each(["rename", "delete"] as const)("does not bypass an ambiguous %s with an edit", async (kind) => {
        const f = await fixture();
        f.files.set(f.b.path, "superseded");
        f.c.capture({ kind: "edit", path: f.b.path });
        const target = "Notes/b.md";
        f.files.delete(f.b.path);
        if (kind === "rename") f.files.set(target, "newer");
        f.c.capture({
            kind,
            path: kind === "rename" ? target : f.b.path,
            ...(kind === "rename" ? { oldPath: f.b.path } : {}),
        });
        await f.c.settled();
        f.files.set(kind === "rename" ? target : f.b.path, "later");
        f.c.capture({ kind: "edit", path: kind === "rename" ? target : f.b.path });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("keeps a missing-payload capture held instead of treating it as supersession", async () => {
        const f = await fixture();
        f.files.delete(f.b.path);
        f.c.capture({ kind: "edit", path: f.b.path });
        f.files.set(f.b.path, "later");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.j.state.holds[f.b.path]).toBe("predecessor_capture_held");
    });

    it("recovers overlapping ordinary saves and a later settled save without restarting", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "superseded");
        f.c.capture({ kind: "edit", path: f.b.path });
        f.files.set(f.b.path, "latest");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations.map((o) => o.payload)).toEqual(["latest"]);
        f.files.set(f.b.path, "third");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations.map((o) => o.payload)).toEqual(["latest", "third"]);
        expect(f.j.state.operations[0].intent.base).toEqual(f.b);
        expect(f.j.state.operations[0].intent.predecessor).toBeUndefined();
        expect(f.j.state.operations[1].intent.predecessor).toBe(f.j.state.operations[0].intent.id);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
    });

    it("queues an offline reversion to the old base after a pending edit", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "B");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        f.files.set(f.b.path, "A");
        await f.c.mutation({ kind: "store", info: f.b.path as never });
        expect(f.j.state.operations.map((o) => o.payload)).toEqual(["B", "A"]);
        expect(f.j.state.operations[1].intent.predecessor).toBe(f.j.state.operations[0].intent.id);
    });

    it("retains materialized A for stale delete and chains rename followed by edit", async () => {
        const f = await fixture();
        f.files.delete(f.b.path);
        f.files.set("Notes/b.md", "A");
        f.c.capture({ kind: "rename", oldPath: f.b.path, path: "Notes/b.md" });
        await f.c.settled();
        f.files.set("Notes/b.md", "B");
        f.c.capture({ kind: "edit", path: "Notes/b.md" });
        await f.c.settled();
        f.files.delete("Notes/b.md");
        f.c.capture({ kind: "delete", path: "Notes/b.md" });
        await f.c.settled();
        const ops = f.j.state.operations;
        expect(ops.map((o) => o.intent.operation)).toEqual(["rename", "edit", "delete"]);
        expect(ops[1].intent.predecessor).toBe(ops[0].intent.id);
        expect(ops[2].intent.predecessor).toBe(ops[1].intent.id);
        expect(ops[2].intent.base?.sourceBase).toBe("A");
        const restart = new Journal(f.persistence);
        await restart.ready;
        expect(restart.state.operations).toEqual(ops);
        expect(ops[1].payload).toBe("B");
    });
    it("captures offline edits, but never creates deletes from scanner absence or manual resolution", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "offline");
        await f.c.mutation({ kind: "store", info: f.b.path as never });
        expect(f.j.state.operations[0].payload).toBe("offline");
        f.files.delete(f.b.path);
        await f.c.mutation({ kind: "delete", info: f.b.path as never });
        expect(await f.c.mutation({ kind: "resolve", info: f.b.path as never })).toBe(false);
        expect(f.j.state.operations).toHaveLength(1);
        expect(f.j.state.holds[f.b.path]).toBe("native_conflict_requires_resolution");
    });
    it("installs exact reflected revision without generating user intent and preserves concurrent local edit", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "new", sourceBase: "B", nativeRevision: "2-b", sha256: await digest("B") };
        f.revisions.set("2-b", "B");
        await f.c.ingest(b);
        const entry = { path: b.path, _rev: b.nativeRevision } as never;
        expect(await f.c.beforeReflect(entry)).toBeUndefined();
        f.files.set(b.path, "B");
        f.c.capture({ kind: "edit", path: b.path });
        await f.c.settled();
        await f.c.afterReflect(entry, true);
        expect(f.j.state.bindings[b.path].sourceBase).toBe("B");
        expect(f.j.state.operations).toHaveLength(0);
        const next = { ...b, id: "next", sourceBase: "C", nativeRevision: "3-c", sha256: await digest("C") };
        f.revisions.set("3-c", "C");
        await f.c.ingest(next);
        f.files.set(b.path, "local");
        expect(await f.c.beforeReflect({ path: b.path, _rev: "3-c" } as never)).toBe(true);
        expect(f.j.state.operations[0].payload).toBe("local");
    });
    it("holds unknown files, cross-collection moves and journal failure without losing prior bytes", async () => {
        const f = await fixture();
        f.c.capture({ kind: "edit", path: "Unknown.md" });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
        f.files.set("Outside/b.md", "A");
        f.c.capture({ kind: "rename", oldPath: f.b.path, path: "Outside/b.md" });
        await f.c.settled();
        expect(f.j.state.holds[f.b.path]).toBe("outside_collection");
        const g = await fixture();
        g.files.set(g.b.path, "saved");
        g.c.capture({ kind: "edit", path: g.b.path });
        await g.c.settled();
        g.persistence.save = async () => {
            throw new Error("quota");
        };
        g.files.set(g.b.path, "new");
        g.c.capture({ kind: "edit", path: g.b.path });
        await g.c.settled();
        expect(g.j.failure).toContain("quota");
        expect(g.j.state.operations[0].payload).toBe("saved");
        expect(g.j.state.operations).toHaveLength(1);
    });
    it("expands a folder rename using known identities and holds the batch after a capture gap", async () => {
        const f = await fixture();
        f.files.delete(f.b.path);
        f.files.set("Notes/sub/a.md", "A");
        f.c.capture({ kind: "rename", oldPath: "Notes", path: "Notes/sub", folder: true });
        await f.c.settled();
        expect(f.j.state.operations[0].intent.target).toBe("Notes/sub/a.md");
        expect(f.j.state.operations[0].intent.fileId).toBe("f");
    });
    it.each(["rename", "delete"] as const)("captures every known descendant of a large folder %s", async kind => {
        const f = await fixture();
        await f.j.update(s => {
            for (let k = 0; k < 257; k++) {
                const path = `Notes/folder/${k}.md`;
                s.bindings[path] = { ...f.b, id: `b${k}`, fileId: `f${k}`, path };
                if (kind === "rename") f.files.set(`Notes/moved/${k}.md`, "A");
            }
        });
        const c = new IntentClient(f.j, f.io);
        await c.ready;
        c.capture({ kind, folder: true, path: kind === "rename" ? "Notes/moved" : "Notes/folder",
            ...(kind === "rename" ? { oldPath: "Notes/folder" } : {}) });
        await c.settled();
        expect(f.j.state.operations).toHaveLength(257);
        expect(new Set(f.j.state.operations.map(o => o.intent.fileId)).size).toBe(257);
        expect(f.j.state.operations.every(o => o.intent.operation === kind)).toBe(true);
        expect(f.j.state.bindings[f.b.path]).toEqual(f.b);
        expect(f.j.state.holds["Notes/folder"]).toBeUndefined();
    });
});
