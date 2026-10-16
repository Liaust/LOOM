import { describe, expect, it, vi } from "vitest";
import { Journal, indexedDBPersistence, indexedDBReplayPersistence, recoveryPersistence, type Change, type State } from "./journal";
import { draft } from "./draft";
import { replayKey, type ReplayHeader, type ReplayRow } from "./native-replay";

const idb = vi.hoisted(() => ({ openDB: vi.fn() }));
vi.mock("idb", () => idb);

describe("incremental journal", () => {
    it("shares untouched records, reports replacement/deletion keys and finalizes nested drafts", () => {
        const base = { map: { a: { n: 1 }, b: { n: 2 } }, operations: [{ n: 1 }, { n: 2 }], untouched: {} };
        const d = draft(base);
        d.value.map.a.n = 3;
        d.value.operations[0].n = 4;
        d.value.operations.length = 1;
        const next = d.finish();
        expect(base.map.a.n).toBe(1);
        expect(next.map.b).toBe(base.map.b);
        expect(next.untouched).toBe(base.untouched);
        expect(d.keys("map")).toEqual(["a"]);
        expect(d.keys("operations")).toContain("1");
        const replacement = draft(base);
        replacement.value.map = { c: { ...replacement.value.map.a } } as any;
        expect(replacement.finish().map).toEqual({ c: { n: 1 } });
        expect(replacement.keys("map").sort()).toEqual(["a", "b", "c"]);
        const removal = draft(base);
        delete (removal.value.map as any).a;
        expect(Object.keys(removal.finish().map)).toEqual(["b"]);
        expect(removal.keys("map")).toEqual(["a"]);
    });
    it("commits only changed rows and reconstructs the same state after restart", async () => {
        let stored: State | undefined;
        const transactions: Change[][] = [];
        const persistence = {
            load: async () => structuredClone(stored),
            save: async (s: State) => { stored ??= structuredClone(s); },
            saveChanges: async (s: State, changes: Change[]) => {
                transactions.push(structuredClone(changes));
                for (const row of changes) {
                    const section = (stored as any)[row.section] ??= {};
                    if (row.value === undefined) delete section[row.key];
                    else section[row.key] = structuredClone(row.value);
                }
                stored!.operations.length = s.operations.length;
            },
        };
        const j = new Journal(persistence);
        await j.ready;
        const candidates = j.state.candidates;
        const clone = vi.spyOn(globalThis, "structuredClone");
        await j.update(s => { s.holds.a = "pending"; });
        expect(clone).toHaveBeenCalledTimes(2); // persistence test adapter only
        clone.mockRestore();
        expect(j.state.candidates).toBe(candidates);
        expect(transactions[0]).toEqual([{ section: "holds", key: "a", value: "pending" }]);
        await j.update(s => { s.holds = { b: "held" }; });
        expect(transactions[1]).toContainEqual({ section: "holds", key: "a", value: undefined });
        const reopened = new Journal(persistence);
        await reopened.ready;
        expect(reopened.state).toEqual(j.state);
        await j.update(s => { s.holds.b = "held"; });
        expect(transactions).toHaveLength(2);
    });
    it("does not expose a failed transaction or mutate the prior snapshot", async () => {
        const j = new Journal({ load: async () => undefined, save: async () => {},
            saveChanges: async () => { throw Error("transaction_aborted"); } });
        await j.ready;
        const old = j.state;
        await expect(j.update(s => { s.holds.a = "pending"; })).rejects.toThrow("transaction_aborted");
        expect(j.state).toBe(old);
        expect(j.state.holds).toEqual({});
    });
    it("keeps committed edits durable when a secondary export fails, then flushes latest state", async () => {
        const write = vi.fn(async (_slot: number, _content: string): Promise<void> => { throw Error("export disk"); });
        let stored: State | undefined;
        const j = new Journal(recoveryPersistence({ load: async () => stored,
            save: async s => { stored = s; } }, "vault", write));
        await j.ready;
        await j.update(s => { s.holds.a = "one"; });
        expect(write).not.toHaveBeenCalled();
        await expect(j.checkpoint()).rejects.toThrow("recovery_checkpoint_unavailable");
        expect(j.failure).toBeUndefined();
        expect(stored!.holds.a).toBe("one");
        await j.update(s => { s.holds.a = "two"; });
        await j.update(s => { delete s.holds.a; s.holds.b = "latest"; });
        write.mockImplementation(async (_slot?: number, _content?: string) => {});
        await j.checkpoint();
        const [slot, text] = write.mock.calls[write.mock.calls.length - 1];
        expect(slot).toBe(0);
        expect(JSON.parse(text).state).toEqual(j.state);
        expect(Object.keys(JSON.parse(text).state.holds)).toEqual(["b"]);
        expect(j.checkpointFailure).toBeUndefined();
    });
    it("yields full export batches and keeps a coherent snapshot during a concurrent edit", async () => {
        const checkpoints: State[] = [];
        const j = new Journal(recoveryPersistence({ load: async () => undefined, save: async () => {} }, "vault",
            async (_slot, text) => { checkpoints.push(JSON.parse(text).state); }));
        await j.ready;
        await j.update(s => { for (let i = 0; i < 300; i++) s.holds[String(i)] = "old"; });
        const flush = j.checkpoint();
        await new Promise(resolve => setTimeout(resolve, 0));
        await j.update(s => { s.holds["0"] = "new"; });
        await flush;
        expect(checkpoints[0].holds["0"]).toBe("old");
        expect(checkpoints[checkpoints.length - 1].holds["0"]).toBe("new");
    });
});

describe("independent IndexedDB replay records", () => {
    const header: ReplayHeader = { version: 1, reader: "test-reader-v1", database: "pouch-id", cursor: "004-opaque", pendingCount: 1 };
    const row: ReplayRow = { kind: "control", path: "LOOM-Control-v1/binding/id.md", revision: "2-exact", ticket: "delivery" };
    function setup(entries: { key: any; value: any }[] = []) {
        vi.stubGlobal("IDBKeyRange", { bound: (lower: any, upper: any) => ({ lower, upper }) });
        const cursor = (index: number): any => index >= entries.length ? null :
            { ...entries[index], continue: async () => cursor(index + 1) };
        const store = { get: vi.fn(async (_key: any): Promise<any> => header),
            put: vi.fn(async (_value: any, _key: any) => {}), delete: vi.fn(async (_key: any) => {}),
            openCursor: vi.fn(async (_range?: any) => cursor(0)) };
        const tx = { store, done: Promise.resolve() };
        const transaction = vi.fn((_name: any, _mode: any, _options?: any) => tx);
        idb.openDB.mockResolvedValue({ transaction, get: store.get });
        return { tx, store, transaction };
    }
    it("uses the same private database without changing its version and awaits strict transaction completion", async () => {
        const { tx, store, transaction } = setup();
        try {
            let committed!: () => void;
            tx.done = new Promise(resolve => { committed = resolve; });
            const replay = indexedDBReplayPersistence("owned-vault");
            let finished = false;
            const pending = replay.commit(header.reader, header, [row], ["previous"]).then(() => { finished = true; });
            await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2));
            expect(finished).toBe(false);
            expect(idb.openDB).toHaveBeenLastCalledWith("loom-client-intent-v1:owned-vault", 1, expect.any(Object));
            expect(transaction).toHaveBeenCalledWith("journal", "readwrite", { durability: "strict" });
            expect(store.put.mock.calls).toEqual([
                [row, ["native-replay-v1", header.reader, "row", replayKey(row)]],
                [header, ["native-replay-v1", header.reader, "header"]],
            ]);
            expect(store.delete).toHaveBeenCalledWith(["native-replay-v1", header.reader, "row", "previous"]);
            committed();
            await pending;
            expect(finished).toBe(true);
        } finally { vi.unstubAllGlobals(); }
    });
    it("propagates transaction aborts even when individual requests succeeded", async () => {
        const { tx, store } = setup();
        try {
            let abort!: (error: Error) => void;
            tx.done = new Promise((_resolve, reject) => { abort = reject; });
            const pending = indexedDBReplayPersistence("owned-vault").commit(header.reader, header, [row], []);
            const rejected = expect(pending).rejects.toThrow("transaction_aborted");
            await vi.waitFor(() => expect(store.put).toHaveBeenCalledTimes(2));
            abort(Error("transaction_aborted"));
            await rejected;
        } finally { vi.unstubAllGlobals(); }
    });
    it("loads and invalidates only the selected replay reader key range", async () => {
        const { store } = setup([{ key: ["native-replay-v1", header.reader, "header"], value: header },
            { key: ["native-replay-v1", header.reader, "row", replayKey(row)], value: row }]);
        try {
            const persistence = indexedDBReplayPersistence("owned-vault");
            expect(await persistence.load(header.reader)).toEqual({ header, rows: [{ key: replayKey(row), value: row }] });
            expect(store.openCursor).toHaveBeenCalledWith({ lower: ["native-replay-v1", header.reader], upper: ["native-replay-v1", header.reader, []] });
            await persistence.reset(header.reader, { ...header, cursor: 0, pendingCount: 0 });
            expect(store.delete.mock.calls).toEqual([[{ lower: ["native-replay-v1", header.reader], upper: ["native-replay-v1", header.reader, []] }]]);
            expect(store.put.mock.calls.every(([, key]) => key[0] === "native-replay-v1")).toBe(true);
        } finally { vi.unstubAllGlobals(); }
    });
    it("ignores replay records when reconstructing the authoritative journal and recovery state", async () => {
        const { store } = setup([{ key: ["row", "holds", "a"], value: "durable safety hold" },
            { key: ["native-replay-v1", header.reader, "header"], value: header },
            { key: ["native-replay-v1", header.reader, "row", replayKey(row)], value: row }]);
        try {
            store.get.mockResolvedValue({ version: 1, device: "unchanged-device", sections: ["bindings", "candidates", "reflecting", "holds"], operationCount: 0 });
            const journal = new Journal(indexedDBPersistence("owned-vault"));
            await journal.ready;
            expect(journal.state).toEqual({ version: 1, device: "unchanged-device", bindings: {}, candidates: {}, reflecting: {}, operations: [], holds: { a: "durable safety hold" } });
            expect(JSON.stringify(journal.state)).not.toContain("pouch-id");
            expect(store.put).not.toHaveBeenCalled();
        } finally { vi.unstubAllGlobals(); }
    });
    it("bounds corrupt cache reads and rejects malformed replay keys", async () => {
        setup(Array.from({ length: 1100 }, (_, n) => ({ key: ["native-replay-v1", header.reader, "row", String(n)], value: row })));
        try {
            expect((await indexedDBReplayPersistence("owned-vault").load(header.reader)).rows).toHaveLength(1025);
            setup([{ key: ["native-replay-v1", header.reader, "row", replayKey(row), "unexpected"], value: row }]);
            expect((await indexedDBReplayPersistence("owned-vault").load(header.reader)).rows[0].key).toBe("");
        } finally { vi.unstubAllGlobals(); }
    });
});
