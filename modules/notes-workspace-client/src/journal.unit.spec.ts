import { describe, expect, it, vi } from "vitest";
import { Journal, recoveryPersistence, type Change, type State } from "./journal";
import { draft } from "./draft";

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
