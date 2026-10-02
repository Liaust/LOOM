import { describe, it, expect, vi } from "vitest";
import { makeDevice } from "./nativeFixture";
import { createTextBlob, createBinaryBlob, readContent, readAsBlob } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import type { UXFileInfo, FilePathWithPrefix } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { Journal, type State } from "./journal";
import { IntentClient } from "./client";
import { Publisher } from "./publisher";
import { ControlTransport } from "./control";
import { digest, encode, type Binding, type Ack } from "./protocol";
import { createConflictResolutionOperations } from "../serviceFeatures/conflictResolution/operations";
import { inspectConflict, convergeNative, conflictPaths } from "./conflicts";
const file = (path: string, text: string) =>
    ({
        path,
        name: path,
        body: createTextBlob(text),
        stat: { type: "file", ctime: 1000000, mtime: 2000000, size: text.length },
    }) as UXFileInfo;
async function fixture() {
    const d = await makeDevice("publisher-" + crypto.randomUUID(), true);
    const path = "Notes/a.md";
    const rev = await d.access.storeIndependentRevision(file(path, "A"), true);
    if (!rev) throw Error("seed");
    const b: Binding = {
        version: 1,
        kind: "binding",
        id: "b",
        workspace: "w",
        collection: "c",
        generation: "g",
        fileId: "f",
        collectionRoot: "Notes",
        path,
        sourceBase: "A",
        nativeRevision: rev,
        sha256: await digest("A"),
        writable: true,
    };
    let saved: State | undefined;
    const persistence = {
        load: async () => structuredClone(saved),
        save: async (s: State) => {
            saved = structuredClone(s);
        },
    };
    const j = new Journal(persistence);
    await j.ready;
    await j.update((s) => {
        s.bindings[path] = b;
    });
    const files = new Map([[path, "A"]]);
    const transport = new ControlTransport(d.access);
    const c = new IntentClient(j, {
        read: async (p) => files.get(p) ?? null,
        readControl: (p, r) => transport.get(p, r),
        readRevision: async (p, r) => {
            const e = await d.access.fetchEntry(p as FilePathWithPrefix, r, false, true, true);
            return e ? String(readContent(e)) : null;
        },
        descends: async () => true,
        reflect: async () => {},
        changed: vi.fn(),
    });
    await c.ready;
    const publisher = () =>
        new Publisher(c, d.access, {
            encrypted: () => true,
            locked: (paths, run) => d.handler.loomPublish(paths as FilePathWithPrefix[], run),
        });
    return { d, b, c, j, files, publisher, persistence };
}
describe("native exact-base publication and source acknowledgement", () => {
    it("does not republish uploaded operations on unrelated wakeups", async () => {
        const f = await fixture();
        try {
            const p = f.publisher();
            f.files.set(f.b.path, "B");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled(); await p.pump();
            expect(f.j.state.operations[0].state).toBe("uploaded");
            const transport = (p as unknown as { transport: ControlTransport }).transport;
            const put = vi.spyOn(transport, "put");
            await p.pump(); await p.pump(); await p.drainAcks();
            expect(put).not.toHaveBeenCalled();
            expect(p.needsRetry).toBe(false);
            expect(f.j.state.operations[0].payload).toBe("B");
        } finally { await f.d.db.destroy(); }
    });
    it("decodes binary text-like references exactly through native storage and reflection readers", async () => {
        const f = await fixture();
        try {
            for (const ext of ["canvas", "svg", "csv"]) {
                const path = `Notes/reference.${ext}` as FilePathWithPrefix;
                const bytes = new TextEncoder().encode("{\"label\":\"Unicode: \\u03b1\"}\r\n".repeat(6000));
                const revision = await f.d.access.storeIndependentRevision({ path, name: path,
                    body: createBinaryBlob(bytes), stat: { type: "file", size: bytes.length, ctime: 1, mtime: 2 } } as UXFileInfo, true);
                expect(revision).toBeTruthy();
                if (!revision) throw Error("reference_not_stored");
                const entry = await f.d.access.fetchEntry(path, revision, false, true, true);
                expect(entry).toBeTruthy();
                if (!entry) throw Error("reference_not_read");
                expect(entry.datatype).toBe("newnote");
                expect(readContent(entry)).toBeInstanceOf(ArrayBuffer);
                expect(await digest(new Uint8Array(readContent(entry) as ArrayBuffer))).toBe(await digest(bytes));
                expect(await digest(new Uint8Array(await readAsBlob(entry).arrayBuffer()))).toBe(await digest(bytes));
            }
        } finally { await f.d.db.destroy(); }
    });
    async function conflicting() {
        const f = await fixture(), p = f.publisher();
        const remote = await f.d.access.storeWithBaseRevision(file(f.b.path, "source"), f.b.nativeRevision, true);
        if (!remote) throw Error("source seed");
        f.files.set(f.b.path, "draft");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled(); await p.pump();
        const old = f.j.state.operations[0];
        await p.acknowledge({ version: 1, kind: "ack", id: "conflict", intentId: old.intent.id,
            publicationId: old.publication!.id, intentDigest: await digest(encode(old.intent)),
            status: "conflict", reason: "source_conflict" });
        const b = { ...f.b, id: "source", sourceBase: "source", nativeRevision: remote, sha256: await digest("source") };
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.c.io.descends = async (_path, rev, ancestor) => {
            const doc = await f.d.db.get(f.b.path, { rev, revs: true });
            return doc._revisions!.ids.some((id, i) => `${doc._revisions!.start - i}-${id}` === ancestor);
        };
        f.c.io.conflictLeaves = async () => {
            const rows = await f.d.db.get(f.b.path, { open_revs: "all" });
            const leaves: { revision: string; text: string }[] = [];
            for (const row of rows) if ("ok" in row && !row.ok._deleted) {
                const text = await f.c.io.readRevision(f.b.path, row.ok._rev!);
                if (text === null) throw Error("missing leaf");
                leaves.push({ revision: row.ok._rev!, text });
            }
            return leaves;
        };
        return { ...f, p, old: f.j.state.operations[0], sourceBinding: b };
    }
    it("resolves a source conflict with an exact new edit, retains history and converges reviewed native leaves", async () => {
        const f = await conflicting();
        try {
            const review = await inspectConflict(f.c, f.b.path);
            expect(review.local).toBe("draft"); expect(review.source).toBe("source");
            const id = await f.c.resolve(review, "merged"); await f.p.pump();
            const op = f.j.state.operations.find(o => o.intent.id === id)!;
            expect(op.intent.resolves).toEqual([f.old.intent.id]);
            expect(op.intent.base).toEqual(f.sourceBinding);
            const binding = { ...f.sourceBinding, id: "merged", sourceBase: "merged",
                nativeRevision: op.revisions[0].revision, sha256: await digest("merged") };
            const ack: Ack = { version: 1, kind: "ack", id: "resolved", status: "applied", reason: "source_applied",
                intentId: id, publicationId: op.publication!.id, intentDigest: await digest(encode(op.intent)),
                binding, resolves: op.intent.resolves };
            await f.p.acknowledge(ack);
            expect(f.j.state.operations[0].ack).toEqual(f.old.ack);
            expect(f.j.state.operations[0].payload).toBe("draft");
            expect(f.j.state.operations[0].resolvedBy).toBe(id);
            expect(conflictPaths(f.c)).toEqual([]);
            const io = { leaves: () => f.c.io.conflictLeaves!(f.b.path),
                remove: async (rev: string) => { await f.d.db.remove(f.b.path, rev); } };
            expect(await convergeNative(f.c, binding, io)).toBe(true);
            expect(await f.d.access.getConflictedRevs(f.b.path as FilePathWithPrefix)).toHaveLength(0);
            expect(Object.values(f.j.state.retiredLeaves!).some(l => l.text === "draft")).toBe(true);
            expect(await convergeNative(f.c, binding, io)).toBe(true);
            f.d.setStorage(file(f.b.path, "draft"));
            f.c.io.read = async () => String(await f.d.getStorage().body.text());
            (f.d.handler as unknown as { loomIntent: IntentClient }).loomIntent = f.c;
            await f.d.handler.dbToStorageWithSpecificRev(f.b.path as FilePathWithPrefix, binding.nativeRevision, false);
            expect(await f.d.getStorage().body.text()).toBe("merged");
            expect(f.j.state.bindings[f.b.path].id).toBe(binding.id);
            expect(await f.c.reviewedReplacement(f.b.path, binding.nativeRevision, "later edit")).toBe(false);
        } finally { await f.d.db.destroy(); }
    });
    it("recognizes a re-export alias of an older source base without using revision numbers as a clock", async () => {
        const f = await conflicting();
        try {
            const alias = await f.d.access.storeWithBaseRevision(file(f.b.path, "A"), f.b.nativeRevision, true);
            if (!alias) throw Error("alias seed");
            await f.j.update(s => { s.candidates.alias = { ...f.b, id: "alias", nativeRevision: alias }; });
            expect((await inspectConflict(f.c, f.b.path)).binding).toEqual(f.sourceBinding);
            await f.j.update(s => { s.candidates.alias.sourceBase = "different-source"; });
            await expect(inspectConflict(f.c, f.b.path)).rejects.toThrow("ambiguous");
        } finally { await f.d.db.destroy(); }
    });
    it("does not treat unavailable historical bodies as live competing source versions", async () => {
        const f = await conflicting();
        try {
            f.c.io.descends = async () => false;
            expect((await inspectConflict(f.c, f.b.path)).binding).toEqual(f.sourceBinding);
        } finally { await f.d.db.destroy(); }
    });
    it("refreshes a changed comparison and preserves later edits during resolution", async () => {
        const f = await conflicting();
        try {
            const stale = await inspectConflict(f.c, f.b.path);
            f.files.set(f.b.path, "changed during comparison");
            await expect(f.c.resolve(stale, "bad")).rejects.toThrow("changed while you reviewed");
            expect(f.j.state.operations).toHaveLength(1);
            const fresh = await inspectConflict(f.c, f.b.path);
            const id = await f.c.resolve(fresh, "choice");
            f.files.set(f.b.path, "later edit");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled(); await f.p.pump();
            const later = f.j.state.operations[f.j.state.operations.length - 1];
            expect(later.payload).toBe("later edit"); expect(later.state).toBe("held");
            expect(later.reason).toBe("edit_during_resolution");
            const choice = f.j.state.operations.find(o => o.intent.id === id)!;
            expect(choice.state).toBe("uploaded");
            await f.p.acknowledge({ version: 1, kind: "ack", id: "choice-ack", intentId: id,
                intentDigest: await digest(encode(choice.intent)), publicationId: choice.publication!.id,
                status: "applied", reason: "source_applied", resolves: choice.intent.resolves,
                binding: { ...f.sourceBinding, id: "choice", sourceBase: "choice",
                    sha256: await digest("choice"), nativeRevision: choice.revisions[0].revision } });
            expect(f.j.state.operations[f.j.state.operations.length - 1].resolvedBy).toBeUndefined();
            expect(f.files.get(f.b.path)).toBe("later edit");
            const restart = new Journal(f.persistence); await restart.ready;
            expect(restart.state.operations[restart.state.operations.length - 1].payload).toBe("later edit");
        } finally { await f.d.db.destroy(); }
    });
    it("orders source bindings even when native revision ancestry was pruned", async () => {
        const f = await conflicting();
        try {
            f.c.io.descends = async () => false;
            await f.j.update(s => {
                s.bindings[f.b.path].sourceSequence = 1;
                s.candidates[f.sourceBinding.id].sourceSequence = 2;
                s.candidates.legacy = { ...f.b, id: "legacy", nativeRevision: "107-old", sourceBase: "old" };
            });
            expect((await inspectConflict(f.c, f.b.path)).binding.sourceSequence).toBe(2);
            await f.j.update(s => { s.candidates.legacy.sourceSequence = 2; });
            await expect(inspectConflict(f.c, f.b.path)).rejects.toThrow("ambiguous");
        } finally { await f.d.db.destroy(); }
    });
    it("converges a remotely resolved publication only after its exact receipts arrive", async () => {
        const f = await conflicting();
        try {
            const review = await inspectConflict(f.c, f.b.path);
            const id = await f.c.resolve(review, "merged"); await f.p.pump();
            const op = f.j.state.operations.find(o => o.intent.id === id)!;
            const binding = { ...f.sourceBinding, id: "merged", sourceBase: "merged",
                nativeRevision: op.revisions[0].revision, sha256: await digest("merged") };
            const ack: Ack = { version: 1, kind: "ack", id: "resolved", status: "applied", reason: "source_applied",
                intentId: id, publicationId: op.publication!.id, intentDigest: await digest(encode(op.intent)),
                binding, resolves: op.intent.resolves };
            await f.j.update(s => {
                s.intents = { [id]: op.intent, [f.old.intent.id]: f.old.intent };
                s.publications = { [op.publication!.id]: op.publication!, [f.old.publication!.id]: f.old.publication! };
                s.acks = { [ack.id]: ack };
                s.operations = [];
            });
            await f.p.drainAcks();
            const clone = vi.spyOn(globalThis, "structuredClone");
            try {
                await f.p.drainAcks();
                expect(clone).not.toHaveBeenCalled();
            } finally { clone.mockRestore(); }
            const io = { leaves: () => f.c.io.conflictLeaves!(f.b.path),
                remove: async (rev: string) => { await f.d.db.remove(f.b.path, rev); } };
            expect(await convergeNative(f.c, binding, io)).toBe(false);
            await f.j.update(s => { s.acks![f.old.ack!.id] = f.old.ack!; });
            expect(await convergeNative(f.c, binding, io)).toBe(true);
            expect(Object.values(f.j.state.retiredLeaves!).some(l => l.text === "draft")).toBe(true);
        } finally { await f.d.db.destroy(); }
    });
    it("converges identical native branches but preserves a new unknown branch", async () => {
        const f = await fixture();
        try {
            let leaves = [{ revision: "2-kept", text: "A" }, { revision: "2-copy", text: "A" }];
            const binding = { ...f.b, nativeRevision: "2-kept" };
            const remove = vi.fn(async (revision: string) => { leaves = leaves.filter(l => l.revision !== revision); });
            expect(await convergeNative(f.c, binding, { leaves: async () => leaves, remove })).toBe(true);
            expect(remove).toHaveBeenCalledWith("2-copy");
            leaves.push({ revision: "2-unknown", text: "different" });
            remove.mockClear();
            expect(await convergeNative(f.c, binding, { leaves: async () => leaves, remove })).toBe(false);
            expect(remove).not.toHaveBeenCalled();
        } finally { await f.d.db.destroy(); }
    });
    it("holds native conflicts without claiming deletion or queuing an endless merge", async () => {
        const f = await fixture();
        try {
            const path = f.b.path as FilePathWithPrefix;
            const left = await f.d.access.storeWithBaseRevision(file(path, "same"), f.b.nativeRevision, true);
            const rightFile = file(path, "same");
            rightFile.stat.mtime++;
            await f.d.access.storeWithBaseRevision(rightFile, f.b.nativeRevision, true);
            const before = await f.d.access.getConflictedRevs(path);
            expect(left).toBeTruthy();
            expect(before).toHaveLength(1);
            (f.d.handler as unknown as { loomIntent: IntentClient }).loomIntent = f.c;
            await expect(f.d.handler.deleteRevisionFromDB(path, before[0])).resolves.toBe(false);
            const autoMerge = vi.fn();
            const queue = vi.fn();
            const replicate = vi.fn();
            const log = vi.fn();
            const operations = createConflictResolutionOperations({
                events: { emitEvent: vi.fn() }, databaseFileAccess: f.d.access,
                fileHandler: f.d.handler, localDatabase: () => ({ tryAutoMerge: autoMerge }),
                conflict: { queueCheckFor: queue, resolveByDeletingRevision: vi.fn(), resolveByUserInteraction: vi.fn() },
                replication: { replicateUnattendedByEvent: replicate },
                appLifecycle: { isSuspended: () => false }, vault: { getActiveFilePath: () => undefined },
                storageAccess: { getFileNames: vi.fn() },
                currentSettings: () => ({ disableMarkdownAutoMerge: false, resolveConflictsByNewerFile: false,
                    syncAfterMerge: true, showMergeDialogOnlyOnActive: false }), log,
            });
            await operations.resolve(path);
            await operations.resolve(path);
            expect(autoMerge).not.toHaveBeenCalled();
            expect(queue).not.toHaveBeenCalled();
            expect(replicate).not.toHaveBeenCalled();
            expect(log.mock.calls.some(([message]) => /Automatically merged|has been deleted/.test(message))).toBe(false);
            expect(await f.d.access.getConflictedRevs(path)).toEqual(before);
            expect(f.j.state.holds[path]).toBe("native_conflict_requires_resolution");
            expect(f.j.state.operations).toHaveLength(0);
        } finally {
            await f.d.db.destroy();
        }
    });
    it("stops new capture and publication without dropping already captured edits", async () => {
        const f = await fixture();
        try {
            f.files.set(f.b.path, "saved before close");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            await f.c.stopNative();
            f.files.set(f.b.path, "after close");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            await f.publisher().pump();
            expect(f.j.state.operations).toHaveLength(1);
            expect(f.j.state.operations[0]).toMatchObject({ state: "queued", payload: "saved before close" });
        } finally {
            await f.d.db.destroy();
        }
    });
    it("keeps acceptance when its receipt arrives before publication returns and repairs old regressions", async () => {
        const f = await fixture();
        try {
            f.files.set(f.b.path, "B");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            const p = f.publisher();
            const transport = (p as unknown as { transport: ControlTransport }).transport;
            const put = transport.put.bind(transport);
            vi.spyOn(transport, "put").mockImplementation(async record => {
                const rev = await put(record);
                if (record.kind === "publication") {
                    const op = f.j.state.operations[0];
                    await p.acknowledge({ version: 1, kind: "ack", id: "race-ack",
                        intentId: op.intent.id, intentDigest: record.intentDigest,
                        publicationId: record.id, status: "applied", reason: "source_applied",
                        binding: { ...f.b, id: "new-base", sourceBase: "B",
                            nativeRevision: record.revisions[0].revision, sha256: await digest("B") } });
                }
                return rev;
            });
            await p.pump();
            expect(f.j.state.operations[0].state).toBe("applied");
            await f.j.update(s => { s.operations[0].state = "uploaded"; });
            await p.drainAcks(true);
            expect(f.j.state.operations[0].state).toBe("applied");
            expect(f.j.state.bindings[f.b.path].sourceBase).toBe("B");
        } finally {
            await f.d.db.destroy();
        }
    });
    it("replicates immutable payloads when multiple offline saves collapse to one native leaf", async () => {
        const f = await fixture();
        const remote = await makeDevice("receipt-" + crypto.randomUUID(), true);
        try {
            const publisher = f.publisher();
            for (const text of ["B", "C", "D"]) {
                f.files.set(f.b.path, text);
                f.c.capture({ kind: "edit", path: f.b.path });
                await f.c.settled();
                await publisher.pump();
            }
            await f.d.db.replicate.to(remote.db);
            const first = f.j.state.operations[0];
            await expect(remote.db.get(f.b.path, { rev: first.revisions[0].revision })).rejects.toMatchObject({ status: 404 });
            for (const op of f.j.state.operations) {
                const path = `LOOM-Control-v1/payload/${op.intent.id}.md` as FilePathWithPrefix;
                const entry = await remote.access.fetchEntry(path, undefined, false, true, true);
                expect(entry && readContent(entry)).toBe(op.payload);
            }
            const transport = new ControlTransport(f.d.access);
            await expect(transport.putPayload(first.intent.id, "tampered")).rejects.toThrow("control_collision");
        } finally {
            await f.d.db.destroy();
            await remote.db.destroy();
        }
    });
    it.each(["queued", "published"])(
        "recovers rapid saves after a %s predecessor without rebasing",
        async (state) => {
            const f = await fixture();
            try {
                f.files.set(f.b.path, "durable");
                f.c.capture({ kind: "edit", path: f.b.path });
                await f.c.settled();
                const p = f.publisher();
                if (state === "published") await p.pump();
                f.files.set(f.b.path, "superseded");
                f.c.capture({ kind: "edit", path: f.b.path });
                f.files.set(f.b.path, "latest");
                f.c.capture({ kind: "edit", path: f.b.path });
                await f.c.settled();
                await p.pump();
                f.files.set(f.b.path, "third");
                f.c.capture({ kind: "edit", path: f.b.path });
                await f.c.settled();
                await p.pump();
                const ops = f.j.state.operations;
                expect(ops.map((o) => o.payload)).toEqual(["durable", "latest", "third"]);
                expect(ops.every((o) => o.state === "uploaded" && o.intent.base?.sourceBase === "A")).toBe(
                    true,
                );
                for (let i = 1; i < ops.length; i++) {
                    expect(ops[i].intent.predecessor).toBe(ops[i - 1].intent.id);
                    const raw = await f.d.db.get(f.b.path, { rev: ops[i].revisions[0].revision, revs: true });
                    expect(raw._revisions!.ids[1]).toBe(ops[i - 1].revisions[0].revision.split("-")[1]);
                }
            } finally {
                await f.d.db.destroy();
            }
        },
    );

    it("rejects a mismatched acknowledgement without retiring pending bytes", async () => {
        const f = await fixture();
        try {
            const p = f.publisher();
            f.files.set(f.b.path, "local");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            await p.pump();
            const o = f.j.state.operations[0];
            await p.acknowledge({
                version: 1,
                kind: "ack",
                id: "bad",
                intentId: o.intent.id,
                intentDigest: await digest("wrong intent"),
                publicationId: o.publication!.id,
                status: "applied",
                reason: "",
            });
            expect(f.j.state.holds[f.b.path]).toBe("ack_pair_mismatch");
            expect(f.j.state.operations[0].payload).toBe("local");
            expect(f.j.state.operations[0].state).toBe("uploaded");
        } finally {
            await f.d.db.destroy();
        }
    });
    it("uses the actual native reflection path and remembers the exact exported base", async () => {
        const f = await fixture();
        try {
            f.d.setStorage(file(f.b.path, "A"));
            f.c.io.read = async () => String(await f.d.getStorage().body.text());
            (f.d.handler as unknown as { loomIntent: IntentClient }).loomIntent = f.c;
            const rev = await f.d.access.storeWithBaseRevision(file(f.b.path, "B"), f.b.nativeRevision, true);
            if (!rev) throw Error("seed B");
            const b = { ...f.b, id: "B", sourceBase: "B", nativeRevision: rev, sha256: await digest("B") };
            f.c.io.descends = async (_p, r, a) => r === rev && a === f.b.nativeRevision;
            await f.c.ingest(b);
            await f.d.handler.dbToStorageWithSpecificRev(f.b.path as FilePathWithPrefix, rev, true);
            expect(await f.d.getStorage().body.text()).toBe("B");
            expect(f.j.state.bindings[f.b.path].nativeRevision).toBe(rev);
            expect(f.j.state.operations).toHaveLength(0);
            f.d.setStorage(file(f.b.path, "C"));
            await f.d.handler.storeFileToDB(f.b.path as FilePathWithPrefix);
            expect(f.j.state.operations[0].intent.base?.sourceBase).toBe("B");
            expect(f.j.state.operations[0].payload).toBe("C");
        } finally {
            await f.d.db.destroy();
        }
    });

    it("publishes stale A edit as a branch while preserving B, then matches source ack before retiring bytes", async () => {
        const f = await fixture();
        try {
            const p = f.publisher();
            const remote = await f.d.access.storeWithBaseRevision(
                file(f.b.path, "B"),
                f.b.nativeRevision,
                true,
            );
            f.files.set(f.b.path, "C");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            await p.pump();
            const op = f.j.state.operations[0];
            expect(op.state).toBe("uploaded");
            expect(op.payload).toBe("C");
            const rev = op.publication!.revisions[0].revision;
            expect(rev).not.toBe(remote);
            const raw = await f.d.db.get(f.b.path, { rev, revs: true });
            expect(raw._revisions!.ids[1]).toBe(f.b.nativeRevision.split("-")[1]);
            expect(await f.d.access.getConflictedRevs(f.b.path as FilePathWithPrefix)).toHaveLength(1);
            const ack: Ack = {
                version: 1,
                kind: "ack",
                id: "ack",
                intentId: op.intent.id,
                intentDigest: await digest(encode(op.intent)),
                publicationId: op.publication!.id,
                status: "applied",
                reason: "source applied",
                binding: {
                    ...f.b,
                    id: "accepted",
                    sourceBase: "C",
                    nativeRevision: rev,
                    sha256: await digest("C"),
                },
            };
            await p.acknowledge(ack);
            await p.acknowledge(ack);
            expect(f.j.state.bindings[f.b.path].sourceBase).toBe("C");
            expect(f.j.state.operations[0].payload).toBe("");
        } finally {
            await f.d.db.destroy();
        }
    });
    it("pairs native rename target and deletion and uses predecessor native revision for the following edit", async () => {
        const f = await fixture();
        try {
            const p = f.publisher();
            f.files.delete(f.b.path);
            f.files.set("Notes/b.md", "A");
            f.c.capture({ kind: "rename", path: "Notes/b.md", oldPath: f.b.path });
            await f.c.settled();
            await p.pump();
            const first = f.j.state.operations[0];
            expect(first.publication!.revisions.map((r) => r.role)).toEqual(["content", "deletion"]);
            f.files.set("Notes/b.md", "after rename");
            f.c.capture({ kind: "edit", path: "Notes/b.md" });
            await f.c.settled();
            await p.pump();
            const second = f.j.state.operations[1];
            expect(second.state).toBe("uploaded");
            const raw = await f.d.db.get("Notes/b.md", { rev: second.revisions[0].revision, revs: true });
            expect(raw._revisions!.ids[1]).toBe(first.revisions[0].revision.split("-")[1]);
            expect(second.intent.fileId).toBe(first.intent.fileId);
            expect(first.payload).toBe("A");
            expect(second.payload).toBe("after rename");
        } finally {
            await f.d.db.destroy();
        }
    });
    it("retains an ack that arrives first, joins after publication, and preserves conflicted bytes on restart", async () => {
        const f = await fixture();
        try {
            f.files.set(f.b.path, "pending");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            const op = f.j.state.operations[0],
                p = f.publisher();
            const ack: Ack = {
                version: 1,
                kind: "ack",
                id: "ack",
                intentId: op.intent.id,
                intentDigest: await digest(encode(op.intent)),
                publicationId: "pub_" + op.intent.id,
                status: "conflict",
                reason: "source changed",
            };
            await p.acknowledge(ack);
            expect(f.j.state.operations[0].state).toBe("queued");
            await p.pump();
            expect(f.j.state.operations[0].state).toBe("conflict");
            const restart = new Journal(f.persistence);
            await restart.ready;
            expect(restart.state.operations[0].payload).toBe("pending");
        } finally {
            await f.d.db.destroy();
        }
    });
    it("holds a native write/receipt crash gap", async () => {
        const f = await fixture();
        try {
            f.files.set(f.b.path, "pending");
            f.c.capture({ kind: "edit", path: f.b.path });
            await f.c.settled();
            await f.j.update((s) => {
                s.operations[0].writing = f.b.path;
            });
            const spy = vi.spyOn(f.d.access, "storeWithBaseRevision");
            await f.publisher().pump();
            expect(spy).not.toHaveBeenCalled();
            expect(f.j.state.operations[0].reason).toBe("native_write_receipt_gap");
            expect(f.j.state.operations[0].payload).toBe("pending");
        } finally {
            await f.d.db.destroy();
        }
    });
    it("holds publication without native encryption and chains a fresh create without adopting another source base", async () => {
        const f = await fixture();
        try {
            f.files.set("Notes/new.md", "new");
            f.c.capture({ kind: "create", path: "Notes/new.md" });
            await f.c.settled();
            f.files.set("Notes/new.md", "newer");
            f.c.capture({ kind: "edit", path: "Notes/new.md" });
            await f.c.settled();
            expect(f.j.state.operations.map((o) => o.intent.base)).toEqual([null, null]);
            const refused = new Publisher(f.c, f.d.access, {
                encrypted: () => false,
                locked: async (_p, fn) => fn(),
            });
            await refused.pump();
            expect(f.j.state.holds.transport).toBe("native_encryption_required");
            await f.publisher().pump();
            expect(f.j.state.operations.map((o) => o.state)).toEqual(["uploaded", "uploaded"]);
        } finally {
            await f.d.db.destroy();
        }
    });
});
