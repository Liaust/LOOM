import { describe, it, expect, vi } from "vitest";
import { makeDevice, makeFile } from "./nativeFixture";
import { ControlTransport } from "./control";
import { controlPath, decode, encode, digest, pair, validate, newerSource, referenceBytes, MAX_REFERENCE_BYTES, type Binding, type Intent, reserved } from "./protocol";
import { getConfiguredFunctionsForEncryption } from "../../node_modules/@vrtmrz/livesync-commonlib/dist/pouchdb/encryption";
import type { MetaEntry } from "@vrtmrz/livesync-commonlib/compat/common/types";
export const binding: Binding = {
    version: 1,
    kind: "binding",
    id: "bindingA",
    workspace: "w",
    collection: "c",
    generation: "g",
    fileId: "fileA",
    collectionRoot: "Notes",
    path: "Notes/private-title.md",
    sourceBase: "private-source-base",
    nativeRevision: "1-abc",
    sha256: "sha256:" + "a".repeat(64),
    writable: true,
};
describe("encrypted native controls", () => {
    it("preserves exact native text and binary reference bytes without relaxing hashes", async () => {
        const text = '{"label":"caf\u00e9"}\r\n';
        const utf8 = new TextEncoder().encode(text);
        expect(referenceBytes(text)).toEqual(utf8);
        expect(await digest(referenceBytes(text)!)).toBe(await digest(utf8));
        expect(await digest(referenceBytes(text)!)).not.toBe(await digest(text.replace("\r\n", "\n")));
        const binary = new Uint8Array([0, 255, 137, 80, 78, 71]);
        expect(referenceBytes(binary.buffer)).toEqual(binary);
        expect(referenceBytes(new ArrayBuffer(MAX_REFERENCE_BYTES + 1))).toBeNull();
    });
    it("orders retained bindings across a source enrollment transition", () => {
        const old = { ...binding, sourceSequence: 12 };
        const next = { ...old, id: "expanded", generation: "expanded", sourceSequence: 13 };
        expect(newerSource(next, old)).toBe(true);
        expect(newerSource(old, next)).toBe(false);
        expect(newerSource({ ...next, sourceSequence: 1 }, old)).toBe(false);
        expect(newerSource({ ...next, collection: "other" }, old)).toBe(false);
        expect(newerSource({ ...next, collectionRoot: "Elsewhere" }, old)).toBe(false);
    });
    it("admits large Markdown intents while keeping attachments read-only", () => {
        const intent: Intent = { version: 1, kind: "intent", id: "large", device: "d", operation: "edit",
            workspace: "w", collection: "c", generation: "g", fileId: "fileA", base: binding,
            path: binding.path, sha256: binding.sha256, length: 1200000 };
        expect(validate(intent)).toEqual(intent);
        expect(() => validate({ ...intent, length: 8 * 1024 * 1024 + 1 })).toThrow();
        for (const extension of ["canvas", "wav", "svg", "csv", "xlsx", "dat", "json"]) {
            const reference = { ...binding, path: `Notes/attachment.${extension}`, writable: false };
            expect(validate(reference)).toEqual(reference);
            expect(() => validate({ ...reference, writable: true })).toThrow();
        }
    });
    it("requires exact-base bounded resolution references and accepted receipts", () => {
        const old: Intent = { version: 1, kind: "intent", id: "choice", device: "d", operation: "edit",
            workspace: "w", collection: "c", generation: "g", fileId: "fileA", base: binding,
            path: binding.path, sha256: binding.sha256, length: 4 };
        expect(encode(old)).toBe(JSON.stringify(old));
        const next = { ...old, resolves: ["offline"] };
        expect(decode(controlPath(next), encode(next))).toEqual(next);
        for (const resolves of [null, [], ["choice"], ["x", "x"], ["bad id"], "x", Array(33).fill("x")])
            expect(() => validate({ ...old, resolves })).toThrow("invalid_resolution");
        expect(() => validate({ ...next, predecessor: "parent" })).toThrow("invalid_resolution");
        expect(() => validate({ ...next, operation: "delete" })).toThrow("invalid_resolution");
        const ack = { version: 1, kind: "ack", id: "ack", intentId: "choice", intentDigest: binding.sha256,
            publicationId: "pub", status: "applied", reason: "source_applied", binding, resolves: ["offline"] };
        expect(validate(ack)).toEqual(ack);
        expect(() => validate({ ...ack, status: "conflict" })).toThrow("invalid_resolution_ack");
        expect(() => validate({ ...ack, binding: undefined })).toThrow("invalid_resolution_ack");
    });
    it("defers incomplete arrivals without issuing a native content read", async () => {
        const d = await makeDevice("control-pending-" + crypto.randomUUID(), true);
        try {
            let ready = false;
            const c = new ControlTransport(d.access, async () => ready);
            const rev = await c.put(binding);
            const read = vi.spyOn(d.access, "fetchEntry");
            await expect(c.get(controlPath(binding), rev)).rejects.toThrow("control_pending_chunks");
            expect(read).not.toHaveBeenCalled();
            ready = true;
            expect(await c.get(controlPath(binding), rev)).toEqual(binding);
        } finally {
            await d.db.destroy();
        }
    });
    it("keeps hookless native selection and reflection unchanged", async () => {
        const d = await makeDevice("optional-hooks-" + crypto.randomUUID(), true);
        try {
            const c = new ControlTransport(d.access),
                path = controlPath(binding);
            const rev = await c.put(binding);
            // Default commonlib must obey its ordinary include/exclude rules.
            d.settings.syncIgnoreRegEx = "LOOM-Control-v1" as typeof d.settings.syncIgnoreRegEx;
            expect(await d.access.fetchEntryMeta(path as never, rev, false)).toBe(false);
            d.services.path.loomControlPath = reserved;
            expect(await d.access.fetchEntryMeta(path as never, rev, false)).not.toBe(false);
            delete d.services.path.loomControlPath;
            d.settings.syncIgnoreRegEx = "" as typeof d.settings.syncIgnoreRegEx;
            const target = vi.spyOn(d.services.vault, "isTargetFile").mockResolvedValue(false);
            expect(await d.access.checkIsTargetFile(path as never)).toBe(false);
            d.services.path.loomControlPath = reserved;
            expect(await d.access.checkIsTargetFile(path as never)).toBe(true);
            delete d.services.path.loomControlPath;
            target.mockRestore();
            const meta = await d.access.fetchEntryMeta(path as never, rev, false);
            d.setStorage(makeFile("old", 1000000, path as never));
            const writes = vi.spyOn(d.storageAccess, "writeFileAuto");
            await (d.handler as any).applyDatabaseEntryToStorage(meta, undefined, true);
            expect(writes).toHaveBeenCalled();
            expect(await d.getStorage().body.text()).toBe(encode(binding));
        } finally {
            await d.db.destroy();
        }
    });

    it("round trips strict records and rejects unknown versions, path collisions and incomplete rename pairing", () => {
        expect(decode(controlPath(binding), encode(binding))).toEqual(binding);
        expect(() => decode(controlPath(binding), JSON.stringify({ ...binding, version: 2 }))).toThrow(
            "unknown_protocol",
        );
        expect(() => decode(controlPath(binding) + "x", encode(binding))).toThrow("control_collision");
        const i: Intent = {
            version: 1,
            kind: "intent",
            id: "i",
            device: "d",
            operation: "rename",
            workspace: "w",
            collection: "c",
            generation: "g",
            fileId: "fileA",
            base: binding,
            path: binding.path,
            target: "Notes/new.md",
            sha256: binding.sha256,
            length: 1,
        };
        expect(
            pair(i, {
                version: 1,
                kind: "publication",
                id: "p",
                intentId: "i",
                intentDigest: binding.sha256,
                revisions: [{ path: i.target!, revision: "1-a", role: "content" }],
            }),
        ).toBe(false);
    });
    it("stores native chunks, encrypts sensitive payloads, refuses reuse and never reflects control files", async () => {
        const d = await makeDevice("control-" + crypto.randomUUID(), true);
        try {
            const c = new ControlTransport(d.access);
            const rev = await c.put(binding);
            expect(await c.get(controlPath(binding), rev)).toEqual(binding);
            expect(await c.put(binding)).toBe(rev);
            await expect(c.put({ ...binding, sourceBase: "different" })).rejects.toThrow("control_collision");
            const rows = (await d.db.allDocs({ include_docs: true })).rows.flatMap((r) =>
                r.doc ? [r.doc] : [],
            );
            expect(rows.some((x) => x.type === "leaf")).toBe(true);
            const native = getConfiguredFunctionsForEncryption(
                "ephemeral-test-only",
                false,
                false,
                async () => new Uint8Array(32).fill(7),
                "v2",
            );
            for (const row of rows.filter(
                (x) => x.type === "leaf" || x.type === "plain" || x.type === "newnote",
            )) {
                const encrypted = await native.incoming(structuredClone(row));
                expect(JSON.stringify(encrypted)).not.toContain(binding.sourceBase);
                expect(JSON.stringify(encrypted)).not.toContain(binding.path);
                const plain = await native.outgoing(encrypted);
                if (row.type === "leaf" && "data" in plain) expect(plain.data).toEqual(row.data);
            }
            const meta = await d.access.fetchEntryMeta(controlPath(binding) as never, rev, true);
            const spy = vi.spyOn(d.storageAccess, "writeFileAuto");
            // The marked plugin installs its optional intent hook. Hookless hosts
            // retain native behavior (verified separately below).
            (d.handler as any).loomIntent = { beforeReflect: async () => true };
            await (d.handler as any).applyDatabaseEntryToStorage(meta as MetaEntry, undefined, true);
            expect(spy).not.toHaveBeenCalled();
            expect(await digest(encode(binding))).toMatch(/^sha256:/);
        } finally {
            await d.db.destroy();
        }
    });
});
