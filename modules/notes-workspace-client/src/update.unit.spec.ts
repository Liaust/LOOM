import { describe, expect, it } from "vitest";
import { UpdateBarrier, startupHeld } from "./update";
import { Journal, recoveryPersistence, type State } from "./journal";

describe("safe code update handoff", () => {
    it("drains durable local work before native DB shutdown without server receipts", async () => {
        const steps: string[] = [], exports: string[] = [];
        let stored: State | undefined;
        const persistence = recoveryPersistence({ load: async () => stored,
            save: async state => { stored = structuredClone(state); } }, "fixture-vault",
            async (_slot, text) => { steps.push("exports"); exports.push(text); });
        const journal = new Journal(persistence); await journal.ready;
        await journal.update(s => { s.operations.push({ state: "uploaded", payload: "offline edit", revisions: [],
            intent: { version: 1, kind: "intent", id: "pending-1", device: s.device, operation: "create", workspace: "main",
                collection: "personal", generation: "fixture", fileId: "file-1", base: null, path: "note.md", sha256: "a".repeat(64), length: 12 } }); });
        const before = JSON.stringify(journal.state.operations);
        const barrier = new UpdateBarrier({
            async flushAndFenceEditor() { steps.push("save"); return true; },
            async stopLocal() { steps.push("admissions", "publisher", "journal"); await journal.checkpoint(); },
            async stopNative() { steps.push("database"); },
        });
        const [a, b] = await Promise.all([barrier.prepare(), barrier.prepare()]);
        expect(a.state).toBe("ready"); expect(b).toEqual(a);
        await barrier.shutdown();
        expect(steps).toEqual(["save", "admissions", "publisher", "journal", "exports", "database"]);
        expect(JSON.stringify(stored!.operations)).toBe(before);
        expect(JSON.stringify(JSON.parse(exports[exports.length - 1]).state.operations)).toBe(before);
        const reopened = new Journal(persistence); await reopened.ready;
        expect(JSON.stringify(reopened.state.operations)).toBe(before);
        await reopened.checkpoint();
    });
    it("defers an unfenced editor without stopping anything", async () => {
        let stopped = false;
        const barrier = new UpdateBarrier({ async flushAndFenceEditor() { return false; }, async stopLocal() { stopped = true; }, async stopNative() {} });
        expect((await barrier.prepare()).state).toBe("restart-required");
        expect(stopped).toBe(false);
    });
    it("does not close the native DB after a journal failure", async () => {
        let closed = false;
        const barrier = new UpdateBarrier({ async flushAndFenceEditor() { return true; }, async stopLocal() { throw Error("export failed"); }, async stopNative() { closed = true; } });
        await expect(barrier.prepare()).rejects.toThrow("export failed");
        expect(closed).toBe(false);
        expect(barrier.resume().state).toBe("restart-required");
    });
    it("holds malformed, partial and wrong-code installs before native startup", () => {
        for (const marker of [null, {}, { schema: "unknown" }, { schema: "loom.client-install.v1", phase: "installing" }, { schema: "loom.client-install.v1", phase: "installed", next: { releaseId: "other" } }])
            expect(startupHeld(marker, "client-notes-0.1.0")).toBe(true);
        const release = { schema: "loom.client-release.v1", component: "notes-workspace", adapter: "obsidian-plugin",
            pluginId: "obsidian-livesync", releaseId: "client-notes-0.1.0", sequence: 1, version: "0.1.0", channel: "stable",
            sourceCommit: "a".repeat(40), upstream: { revision: "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", version: "1.0.32" },
            compatibility: { minAppVersion: "1.7.2", platforms: ["desktop", "mobile"], notesProtocol: 1,
                handoffAPI: 1, journalSchema: 1, activation: "reload", rollback: "same-schema" }, files:
            ["main.js", "manifest.json", "styles.css", "loom-client-build.json", "loom-release.json"].map(name => ({ name, bytes: 1, sha256: "a".repeat(64) })) };
        const marker = { schema: "loom.client-install.v1", phase: "installed", previous: release, next: release };
        expect(startupHeld(marker, "client-notes-0.1.0")).toBe(false);
        expect(startupHeld({ ...marker, extra: true }, "client-notes-0.1.0")).toBe(true);
    });
});
