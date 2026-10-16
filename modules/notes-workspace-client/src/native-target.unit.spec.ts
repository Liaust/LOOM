import { describe, expect, it } from "vitest";
import { classifyNativeTarget } from "./native-target";
import { makeDevice, makeFile } from "./nativeFixture";
import type { FilePathWithPrefix } from "@vrtmrz/livesync-commonlib/compat/common/types";

describe("native creation targets", () => {
    const path = "Notes/reused.md";
    const leaf = { revision: "2-deleted", path, type: "plain", deleted: true };
    it("distinguishes absence, logical deletion and a live collision", async () => {
        expect((await classifyNativeTarget(path, [])).kind).toBe("absent");
        expect((await classifyNativeTarget(path, [leaf])).kind).toBe("deleted");
        expect((await classifyNativeTarget(path, [leaf, { ...leaf, revision: "1-live", deleted: false }])).kind).toBe("occupied");
    });
    it("refuses unknown or branch-deleted leaves and binds the complete evidence", async () => {
        for (const changed of [{ branchDeleted: true }, { path: "elsewhere.md" }, { type: "system" }, { revision: "bad" }])
            expect((await classifyNativeTarget(path, [{ ...leaf, ...changed }])).kind).toBe("unknown");
        expect((await classifyNativeTarget(path, [leaf])).token).not.toBe(
            (await classifyNativeTarget(path, [{ ...leaf, revision: "3-changed" }])).token);
    });
    it("preserves a deleted branch and replicates a genuine independent root", async () => {
        const a = await makeDevice("recreate-a-" + crypto.randomUUID(), true);
        const b = await makeDevice("recreate-b-" + crypto.randomUUID(), true);
        try {
            const filePath = path as FilePathWithPrefix;
            const old = await a.access.storeIndependentRevision(makeFile("old", 1000000, filePath), true);
            expect(old).toBeTruthy();
            const deleted = await a.access.storeDeletionWithBaseRevision(filePath, old as string);
            expect(deleted).toBeTruthy();
            const fresh = await a.access.storeIndependentRevision(makeFile("new", 2000000, filePath), true);
            expect(fresh).toMatch(/^1-/);
            await a.db.replicate.to(b.db);
            const exact = await b.db.get(path, { rev: fresh as string, revs: true });
            expect(exact._revisions).toEqual({ start: 1, ids: [(fresh as string).slice(2)] });
            const tombstone = await b.db.get(path, { rev: deleted as string, revs: true });
            expect(tombstone).toMatchObject({ deleted: true });
            const leaves = await b.db.get(path, { open_revs: "all" });
            expect(leaves.filter(row => "ok" in row)).toHaveLength(2);
        } finally { await a.db.destroy(); await b.db.destroy(); }
    });
});
