import { describe, expect, it, vi } from "vitest";
import { NativeReplay, nativeDatabaseIdentity, replayControlPaths, NATIVE_REPLAY_READER, replayKey, type ReplayHeader, type ReplayPersistence, type ReplayRow } from "./native-replay";
import { PathWork, WakeLoop } from "./work";

function memory() {
    const readers = new Map<string, { header: ReplayHeader; rows: Map<string, ReplayRow> }>();
    let fail = false;
    const persistence: ReplayPersistence = {
        async load(reader) {
            const stored = readers.get(reader);
            return { header: structuredClone(stored?.header),
                rows: [...(stored?.rows ?? [])].map(([key, value]) => ({ key, value: structuredClone(value) })) };
        },
        async reset(reader, header) { readers.set(reader, { header: structuredClone(header), rows: new Map() }); },
        async commit(reader, header, put, remove) {
            if (fail) throw Error("transaction_aborted");
            const rows = new Map(readers.get(reader)?.rows);
            for (const key of remove) rows.delete(key);
            for (const row of put) rows.set(replayKey(row), structuredClone(row));
            readers.set(reader, { header: structuredClone(header), rows });
        },
    };
    return { persistence, readers, fail() { fail = true; }, recover() { fail = false; } };
}
const control = (revision = "1-exact") => ({ kind: "control" as const, path: "LOOM-Control-v1/binding/id.md", revision });
const path = (path = "Notes/a.md") => ({ kind: "path" as const, path });

describe("durable native replay", () => {
    it("does not request the full binding walk on a completed second start; invalid cache and manual resume do", async () => {
        const store = memory();
        let replay = await NativeReplay.open(store.persistence, "db");
        const bindingWalk = vi.fn();
        expect(replay.checkpointReused).toBe(false);
        if (replay.requiresRecovery()) bindingWalk();
        expect(bindingWalk).toHaveBeenCalledTimes(1);
        await replay.beginRecovery();
        await replay.accept("catch-up", [path()]);
        await replay.finishRecovery();
        replay = await NativeReplay.open(store.persistence, "db");
        expect(replay.checkpointReused).toBe(true);
        if (replay.requiresRecovery()) bindingWalk();
        expect(bindingWalk).toHaveBeenCalledTimes(1);
        expect(replay.pendingPaths).toEqual(["Notes/a.md"]);
        expect(replay.requiresRecovery(true)).toBe(true);
        store.readers.get(NATIVE_REPLAY_READER)!.header.pendingCount++;
        replay = await NativeReplay.open(store.persistence, "db");
        expect(replay.checkpointReused).toBe(false);
        if (replay.requiresRecovery()) bindingWalk();
        expect(bindingWalk).toHaveBeenCalledTimes(2);
    });
    it("keeps bootstrap incomplete until candidate-only work is durable, including an interrupted marker write", async () => {
        const store = memory();
        let replay = await NativeReplay.open(store.persistence, "db");
        await replay.beginRecovery();
        await replay.accept("partway", [path("Notes/first-candidate.md")]);
        // A second candidate was retained in the journal but not yet in the inbox.
        replay = await NativeReplay.open(store.persistence, "db");
        expect(replay.checkpointReused).toBe(true); // cursor is reusable, bootstrap is not done
        expect(replay.requiresRecovery()).toBe(true);
        await replay.enqueue(["Notes/first-candidate.md", "Notes/candidate-only.md"]);
        store.fail();
        await expect(replay.finishRecovery()).rejects.toThrow("transaction_aborted");
        expect(replay.requiresRecovery()).toBe(true);
        store.recover();
        await replay.finishRecovery();
        replay = await NativeReplay.open(store.persistence, "db");
        expect(replay.requiresRecovery()).toBe(false);
        const work = new PathWork();
        expect(work.runnable).toBe(false);
        expect(replay.hasPending).toBe(true);
        for (const p of replay.pendingPaths) work.add(p); // retry rehydrates without a wait map
        expect(work.take()).toEqual(["Notes/first-candidate.md", "Notes/candidate-only.md"]);
    });
    it("uses the actual bound PouchDB id method, never path/appId/sequence substitutes", async () => {
        const db = { id: vi.fn(async function(this: object) { expect(this).toBe(db); return "actual-pouch-id"; }) };
        expect(await nativeDatabaseIdentity(db)).toBe("actual-pouch-id");
        expect(db.id).toHaveBeenCalledTimes(1);
        expect(await nativeDatabaseIdentity({ name: "same-path", appId: "same-vault", update_seq: 876 })).toBeUndefined();
        expect(await nativeDatabaseIdentity({ id: async () => { throw Error("unavailable"); } })).toBeUndefined();
        expect(await nativeDatabaseIdentity({ id: async () => 876 })).toBeUndefined();
    });
    it("reopens the same database at its opaque cursor with exact pending rows and no payloads", async () => {
        const store = memory();
        const replay = await NativeReplay.open(store.persistence, "pouch-instance-a");
        expect(replay.cursor).toBe(0);
        await replay.accept("0008-g1/opaque:Z", [control(), { ...path(), payload: "never copy native bytes" } as any]);
        const reopened = await NativeReplay.open(store.persistence, "pouch-instance-a");
        expect(reopened.cursor).toBe("0008-g1/opaque:Z");
        expect(reopened.pending).toEqual(replay.pending);
        for (const invalid of [-1, 1.5, Number.MAX_SAFE_INTEGER + 1, NaN, Infinity])
            await expect(reopened.accept(invalid, [])).rejects.toThrow("native_replay_sequence_invalid");
        expect(reopened.cursor).toBe("0008-g1/opaque:Z");
        expect(JSON.stringify(await store.persistence.load(NATIVE_REPLAY_READER))).not.toContain("never copy");
        await reopened.accept("0009-not-a-number", [path("Notes/closed-app-edit.md")]);
        expect((await NativeReplay.open(store.persistence, "pouch-instance-a")).pending).toHaveLength(3);
    });
    it("cannot expose progress or new rows from an aborted batch and can retry", async () => {
        const store = memory(), replay = await NativeReplay.open(store.persistence, "db");
        await replay.accept("before", [path()]);
        store.fail();
        await expect(replay.accept("after", [control()])).rejects.toThrow("transaction_aborted");
        expect(replay.cursor).toBe("before");
        const crashed = await NativeReplay.open(store.persistence, "db");
        expect(crashed.cursor).toBe("before");
        expect(crashed.pending).toHaveLength(1);
        store.recover();
        await replay.accept("after", [control()]);
        expect(replay.cursor).toBe("after");
    });
    it("replays the ingestion/removal crash gap and atomically retains follow-up reflection", async () => {
        const store = memory();
        let replay = await NativeReplay.open(store.persistence, "db");
        await replay.accept(47, [control()]);
        const durableIngestions = new Set<string>();
        durableIngestions.add(replay.controls[0].revision);
        // Crash after the journal transaction but before replay completion.
        replay = await NativeReplay.open(store.persistence, "db");
        durableIngestions.add(replay.controls[0].revision);
        expect(durableIngestions.size).toBe(1);
        store.fail();
        await expect(replay.complete(replay.controls[0], ["Notes/a.md"])).rejects.toThrow();
        expect(replay.controls).toHaveLength(1);
        store.recover();
        // Duplicate ingestion can omit onReflect; exact control hints still survive.
        await replay.complete(replay.controls[0], replayControlPaths({ kind: "binding", path: "Notes/a.md" } as any));
        replay = await NativeReplay.open(store.persistence, "db");
        expect(replay.cursor).toBe(47);
        expect(replay.controls).toEqual([]);
        expect(replay.pending).toEqual([expect.objectContaining(path())]);
        await replay.complete(replay.pending[0]);
        expect((await NativeReplay.open(store.persistence, "db")).pending).toEqual([]);
    });
    it("retains failed controls and reconstructs waiting paths before header-only chunks arrive", async () => {
        const store = memory();
        let replay = await NativeReplay.open(store.persistence, "db");
        await replay.accept("metadata", [control(), path()]);
        const work = new PathWork();
        work.wait("Notes/a.md", ["h:chunk-exact"]);
        // Wait maps are transient: the row, not a snapshot of the map, survives.
        replay = await NativeReplay.open(store.persistence, "db");
        const resumed = new PathWork();
        for (const row of replay.pending) if (row.kind === "path") resumed.add(row.path);
        expect(resumed.take()).toEqual(["Notes/a.md"]);
        resumed.wait("Notes/a.md", ["h:chunk-exact"]);
        resumed.changed("h:unrelated");
        expect(resumed.take()).toEqual([]);
        resumed.changed("h:chunk-exact"); // no chunk body or native metadata read
        await replay.accept("chunks", resumed.take().map(p => path(p)));
        expect(replay.controls).toHaveLength(1); // a failed control is not a consumed control
        expect(replay.path("Notes/a.md")).toBeDefined();
        resumed.clear("Notes/a.md");
        await replay.complete(replay.path("Notes/a.md")!);
        expect((await NativeReplay.open(store.persistence, "db")).controls).toHaveLength(1);
    });
    it("invalidates only replay metadata for rebuilt/unknown databases, invalid checkpoints and new readers", async () => {
        const store = memory();
        let replay = await NativeReplay.open(store.persistence, "db-a");
        await replay.accept("old", [control()]);
        replay = await NativeReplay.open(store.persistence, "db-b");
        expect(replay.cursor).toBe(0);
        expect(replay.pending).toEqual([]);
        await replay.accept("untrusted", [path()]);
        const unknown = await NativeReplay.open(store.persistence);
        expect(unknown.cursor).toBe(0);
        await unknown.accept("session-only", [path()]);
        expect((await NativeReplay.open(store.persistence)).cursor).toBe(0);
        replay = await NativeReplay.open(store.persistence, "db-b");
        await replay.accept("old-reader", [path()]);
        const next = await NativeReplay.open(store.persistence, "db-b", "control-path-v2");
        expect(next.cursor).toBe(0);
        expect((await NativeReplay.open(store.persistence, "db-b")).cursor).toBe("old-reader");
        store.readers.get(NATIVE_REPLAY_READER)!.header.pendingCount++;
        expect((await NativeReplay.open(store.persistence, "db-b")).cursor).toBe(0);
        await replay.accept("bad-row", [path()]);
        (store.readers.get(NATIVE_REPLAY_READER)!.rows.values().next().value as any).payload = "invalid extra field";
        expect((await NativeReplay.open(store.persistence, "db-b")).cursor).toBe(0);
    });
    it("bounds row count, controls and bytes without dropping old rows or advancing", async () => {
        for (const bounds of [{ rows: 1, controls: 2, bytes: 4096 },
            { rows: 3, controls: 1, bytes: 4096 }, { rows: 3, controls: 3, bytes: 200 }]) {
            const store = memory();
            const replay = await NativeReplay.open(store.persistence, "db", NATIVE_REPLAY_READER, bounds);
            await replay.accept("saved", [control()]);
            await expect(replay.accept("must-not-advance", [control("2-next")])).rejects.toThrow("native_replay_backlog_limit");
            expect(replay.cursor).toBe("saved");
            expect((await NativeReplay.open(store.persistence, "db")).pending).toHaveLength(1);
        }
    });
    it("does not complete a path enqueued again during an older attempt", async () => {
        const store = memory(), replay = await NativeReplay.open(store.persistence, "db");
        await replay.accept("first", [path()]);
        const old = replay.path("Notes/a.md")!;
        await replay.accept("arrived-during-work", [path()]);
        await replay.complete(old);
        expect(replay.path("Notes/a.md")!.ticket).not.toBe(old.ticket);
        expect((await NativeReplay.open(store.persistence, "db")).cursor).toBe("arrived-during-work");
    });
    it("preserves live wakeups while catch-up is awaiting persistence", async () => {
        vi.useFakeTimers();
        try {
            const store = memory(), replay = await NativeReplay.open(store.persistence, "db");
            const since: (number | string)[] = [];
            let loop: WakeLoop;
            loop = new WakeLoop(async () => {
                since.push(replay.cursor);
                if (since.length === 1) {
                    const commit = replay.accept("first-batch", [path()]);
                    loop.wake(); // live feed arrival during catch-up
                    await commit;
                } else await replay.accept("live-arrival", [path("Notes/new.md")]);
            }, () => false, () => {}, async () => {});
            await loop.flush();
            expect(since).toEqual([0, "first-batch"]);
            expect((await NativeReplay.open(store.persistence, "db")).pending).toHaveLength(2);
            await loop.stop();
        } finally { vi.useRealTimers(); }
    });
});
