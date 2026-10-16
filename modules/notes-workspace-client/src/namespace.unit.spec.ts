import { describe, it, expect } from "vitest";
import { pinLocalVaultName } from "./namespace";
import { PREFIX, STRUCTURE_PREFIX, LAYOUT_PREFIX, controlPath, decode, encode, isControlPath, reserved,
    type Ack, type Binding, type Layout } from "./protocol";

describe("deferred-phone layout namespace", () => {
    const binding: Binding = { version: 1, kind: "binding", id: "old", workspace: "w", collection: "c", generation: "g",
        fileId: "file", collectionRoot: "Projects/Test", path: "Projects/Test/a.md", sourceBase: "base", nativeRevision: "1-base",
        sha256: "sha256:" + "a".repeat(64), writable: true, sourceSequence: 1 };
    // The 0.2.3 predicate recognizes structure controls but skips other reserved
    // payloads entirely, retaining its old paths and journal cursors.
    const oldControlPath = (path: string) => reserved(path) &&
        (!path.startsWith(PREFIX + "payload/") || path.startsWith(STRUCTURE_PREFIX));
    it.each([
        { id: "layout_file", path: "Projects/_Passive/Test/a.md" },
        { id: "layout_canvas_file", path: "Projects/_Passive/Test/a.canvas" },
        { id: "layout_dir_folder", path: "Projects/_Passive/Test/Folder", entryKind: "directory" as const },
    ])("hides standalone and subsequent $id bindings from 0.2.3", patch => {
        const b = { ...binding, ...patch, collectionRoot: "Projects/_Passive/Test", sourceSequence: 2 };
        const path = controlPath(b);
        expect(path).toBe(LAYOUT_PREFIX + "binding/" + b.id + ".md");
        expect(isControlPath(path)).toBe(true); expect(oldControlPath(path)).toBe(false);
        expect(path.startsWith(STRUCTURE_PREFIX)).toBe(false);
        expect(decode(path, encode(b))).toEqual(b);
        expect(() => decode(PREFIX + "binding/" + b.id + ".md", encode(b))).toThrow("control_collision");
        const update = { ...b, id: b.id + "_update", sourceSequence: 3, nativeRevision: "2-update" };
        expect(oldControlPath(controlPath(update))).toBe(false);
    });
    it("hides Layout itself without changing ordinary, structural or embedded-ack routing", () => {
        const next = { ...binding, id: "layout_next", path: "Projects/_Passive/Test/a.md", collectionRoot: "Projects/_Passive/Test", sourceSequence: 2 };
        const layout: Layout = { version: 1, kind: "layout", id: "layout_move", previous: binding, binding: next };
        const path = controlPath(layout);
        expect(path).toBe(LAYOUT_PREFIX + "layout/layout_move.md");
        expect(oldControlPath(path)).toBe(false); expect(isControlPath(path)).toBe(true);
        expect(decode(path, encode(layout))).toEqual(layout);
        expect(controlPath(binding)).toBe(PREFIX + "binding/old.md");
        for (const id of ["dir_folder", "canvas_file"]) {
            const path = controlPath({ ...binding, id });
            expect(path).toBe(STRUCTURE_PREFIX + "binding/" + id + ".md");
            expect(oldControlPath(path)).toBe(true);
        }
        const ack: Ack = { version: 1, kind: "ack", id: "ack", intentId: "intent", intentDigest: binding.sha256,
            publicationId: "publication", status: "applied", reason: "accepted", binding };
        expect(controlPath(ack)).toBe(PREFIX + "ack/ack.md");
        expect(decode(controlPath(ack), encode(ack))).toEqual(ack);
        expect(isControlPath(PREFIX + "payload/intent.md")).toBe(false);
    });
});

describe("local vault namespace", () => {
    it("keeps database and device-local keys stable while the visible folder changes", () => {
        let visible = "loom-notes-test";
        const api = { getSystemVaultName: () => visible };
        const vault = { vaultName: () => visible, getVaultName() { return this.vaultName() + "-suffix"; } };
        pinLocalVaultName(api, vault, { loomLocalVaultName: visible });
        visible = "LOOM NOTES";
        expect(visible).toBe("LOOM NOTES");
        expect(api.getSystemVaultName()).toBe("loom-notes-test");
        expect(vault.getVaultName()).toBe("loom-notes-test-suffix");
    });
    it("leaves existing installations unchanged without an explicit pin", () => {
        const api = { getSystemVaultName: () => "existing" };
        const vault = { vaultName: () => "existing" };
        const original = api.getSystemVaultName;
        pinLocalVaultName(api, vault, {});
        expect(api.getSystemVaultName).toBe(original);
        expect(vault.vaultName()).toBe("existing");
    });
    it("rejects invalid pins before changing either namespace", () => {
        for (const name of ["", " ", "x/y", "x\\y", "x\n", "x".repeat(201), null, 1]) {
            const api = { getSystemVaultName: () => "old" };
            const vault = { vaultName: () => "old" };
            expect(() => pinLocalVaultName(api, vault, { loomLocalVaultName: name })).toThrow("invalid_local_vault_namespace");
            expect(api.getSystemVaultName()).toBe("old");
            expect(vault.vaultName()).toBe("old");
        }
    });
});
