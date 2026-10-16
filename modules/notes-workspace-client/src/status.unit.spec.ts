import { describe, it, expect, vi } from "vitest";
import type { Operation, State } from "./journal";
import { NotesStatus } from "./status";

function operation(id: string, state: Operation["state"] = "queued", fields: Partial<Operation["intent"]> = {}): Operation {
    return { intent: { version: 1, kind: "intent", id, device: "device", operation: "edit", workspace: "w",
        collection: "c", generation: "g", fileId: "file", path: `Notes/${id}.md`, base: null,
        sha256: "sha256:" + "0".repeat(64), length: 6, ...fields }, payload: "edited", state, revisions: [] };
}
function state(operations: Operation[] = [], holds: State["holds"] = {}): State {
    return { version: 1, device: "device", operations, holds, bindings: {}, candidates: {}, reflecting: {} };
}
function published(op: Operation): Operation {
    return { ...op, publication: { version: 1, kind: "publication", id: `pub-${op.intent.id}`,
        intentId: op.intent.id, intentDigest: "digest", revisions: [] } };
}
function chain(size: number): Operation[] {
    return Array.from({ length: size }, (_, n) => operation(String(n), n === 0 ? "held" : "queued",
        n === 0 ? { operation: "create" } : { predecessor: String(n - 1) }));
}

describe("compact actionable Notes status", () => {
    it("keeps recovered-root acceptance separate from ordinary publication and pages holds independently", () => {
        const root = { ...published(operation("root", "uploaded", { operation: "create" })), nativeRecovery: true };
        const child = operation("child", "queued", { predecessor: "root" });
        const view = new NotesStatus(), s = state([root, child], { "Notes/untracked.md": "missing_writable_binding" });
        expect(view.summary(s)).toMatchObject({ pendingAcceptance: 1, pendingPublication: 0, waiting: 1, holds: 1 });
        expect(view.overview(s, { kind: "operations" }).groups.total).toBe(1);
        expect(view.overview(s, { kind: "holds" }).groups.total).toBe(1);
        expect(view.summary(state([{ ...root, state: "applied" }, child]))).toMatchObject({ pendingPublication: 1, waiting: 0 });
        const next = published(operation("next", "uploaded", { predecessor: "root" }));
        const later = operation("later", "queued", { predecessor: "next" });
        const recovering = state([{ ...root, state: "applied" }, next, later]);
        expect(view.summary(recovering)).toMatchObject({ accepted: 1, pendingAcceptance: 1, pendingPublication: 0, waiting: 1 });
        expect(view.overview(recovering, { kind: "operations" }).groups.total).toBe(1);
    });
    it("partitions every unfinished edit without counting a blocked chain as independent failures", () => {
        const operations = chain(145);
        operations[0].reason = "native_target_exists";
        operations.push(operation("queued"), operation("publishing", "publishing"), published(operation("uploaded", "uploaded")),
            operation("accepted", "applied"), { ...operation("resolved", "conflict"), resolvedBy: "exact-resolution" });
        const s = state(operations);
        const view = new NotesStatus();
        const summary = view.summary(s);
        expect(summary).toMatchObject({ phase: "ready", operations: 150, unfinished: 148, blocked: 1, blockedRoots: 1,
            waiting: 144, pendingPublication: 2, pendingAcceptance: 1, accepted: 1, resolved: 1, unknownDependencies: 0 });
        expect(summary.unfinished).toBe(summary.blocked + summary.waiting + summary.pendingPublication + summary.pendingAcceptance);
        const groups = view.overview(s).groups;
        expect(groups.items[0]).toMatchObject({ kind: "blocked", operationId: "0", operations: 145, waiting: 144,
            reason: "native_target_exists" });
        expect(groups.items.reduce((total, group) => total + group.operations, 0)).toBe(summary.unfinished);
        const children = view.item(s, groups.items[0].id)!.operations;
        expect(children).toMatchObject({ total: 145, offset: 0, omitted: 125, nextOffset: 20 });
        expect(children.items).toHaveLength(20);
        expect(children.items[0].stage).toBe("blocked");
        expect(children.items[1].stage).toBe("waiting");
    });

    it("groups a child-first retained rename/delete chain under its exact root", () => {
        const root = { ...operation("create", "held", { operation: "create" }), reason: "native_target_exists" };
        const rename = operation("rename", "queued", { operation: "rename", predecessor: "create", target: "Notes/new.md" });
        const remove = operation("delete", "queued", { operation: "delete", predecessor: "rename", path: "Notes/new.md" });
        const s = state([remove, rename, root]);
        const view = new NotesStatus();
        const group = view.overview(s).groups.items[0];
        expect(group).toMatchObject({ operationId: "create", operations: 3, waiting: 2 });
        expect(view.item(s, group.id)!.operations.items.find(op => op.id === "rename")?.target).toBe("Notes/new.md");
        expect(view.operation(s, "create")).toBe(root);
    });

    it("distinguishes publication predecessors from acknowledgement dependencies", () => {
        const source = published(operation("source", "uploaded"));
        const s = state([source, operation("ordinary-edit", "queued", { predecessor: "source" }),
            operation("folder", "queued", { dependencies: ["source"] })]);
        const view = new NotesStatus();
        expect(view.summary(s)).toMatchObject({ unfinished: 3, pendingPublication: 1, pendingAcceptance: 1, waiting: 1 });
        const sourceGroup = view.overview(s).groups.items.find(group => group.operationId === "source")!;
        expect(sourceGroup).toMatchObject({ kind: "acceptance", operations: 2, waiting: 1 });
        expect(view.item(s, sourceGroup.id)!.operations.items.map(op => op.stage)).toEqual(["acceptance", "waiting"]);
        const accepted = state([{ ...source, state: "applied" }, ...s.operations.slice(1)]);
        expect(view.summary(accepted)).toMatchObject({ accepted: 1, pendingPublication: 2, waiting: 0 });
    });

    it("does not reclassify an already uploaded operation as waiting for publication", () => {
        const s = state([operation("root", "held"), published(operation("uploaded", "uploaded", { predecessor: "root" }))]);
        expect(new NotesStatus().summary(s)).toMatchObject({ blocked: 1, pendingAcceptance: 1, waiting: 0 });
    });

    it("still requires acceptance when the same operation is both predecessor and explicit dependency", () => {
        const s = state([published(operation("parent", "uploaded")),
            operation("child", "queued", { predecessor: "parent", dependencies: ["parent"] })]);
        expect(new NotesStatus().summary(s)).toMatchObject({ pendingPublication: 0, pendingAcceptance: 1, waiting: 1 });
    });

    it("retains missing predecessors, missing dependencies and receipt gaps explicitly", () => {
        const s = state([operation("lost-parent", "queued", { predecessor: "missing" }),
            operation("lost-dependency", "queued", { dependencies: ["gone"] }),
            { ...operation("resolved", "held"), resolvedBy: "receipt" },
            operation("needs-resolved", "queued", { predecessor: "resolved" }),
            operation("applied-without-publication", "applied"),
            operation("needs-publication", "queued", { predecessor: "applied-without-publication" })]);
        const view = new NotesStatus();
        expect(view.summary(s)).toMatchObject({ unfinished: 4, waiting: 4, unknownDependencies: 4, blocked: 0, resolved: 1, accepted: 1 });
        expect(view.overview(s).groups.items.map(group => group.reason)).toEqual([
            "missing_predecessor", "missing_dependency", "dependency_receipt_unavailable", "dependency_receipt_unavailable"]);
    });

    it("uses one primary group per operation with multiple dependencies and exposes full dependencies on request", () => {
        const s = state([operation("free"), operation("blocked", "conflict"),
            operation("folder", "queued", { dependencies: ["free", "blocked", "blocked"] })]);
        const view = new NotesStatus();
        const groups = view.overview(s).groups.items;
        expect(groups[0]).toMatchObject({ kind: "blocked", operationId: "blocked", operations: 2, waiting: 1 });
        expect(groups.reduce((count, group) => count + group.operations, 0)).toBe(3);
        expect(view.operation(s, "folder")!.intent.dependencies).toEqual(["free", "blocked", "blocked"]);
    });

    it("does not conceal a missing secondary dependency behind a known blocked root", () => {
        const s = state([operation("blocked", "held"), operation("folder", "queued", { dependencies: ["blocked", "missing"] })]);
        const view = new NotesStatus();
        expect(view.summary(s)).toMatchObject({ unfinished: 2, blocked: 1, waiting: 1, unknownDependencies: 1 });
        expect(view.overview(s).groups.items[0]).toMatchObject({ kind: "blocked", operations: 2, waiting: 1 });
        expect(view.operation(s, "folder")!.intent.dependencies).toContain("missing");
    });

    it("represents cycles and their descendants as unknown rather than overflowing or dropping them", () => {
        const s = state([operation("b", "queued", { predecessor: "a" }), operation("a", "queued", { predecessor: "b" }),
            operation("tail", "queued", { predecessor: "b" })]);
        const view = new NotesStatus();
        expect(view.summary(s)).toMatchObject({ unfinished: 3, waiting: 3, unknownDependencies: 3 });
        expect(view.overview(s).groups.items).toEqual([expect.objectContaining({ kind: "unknown-dependency",
            reason: "dependency_cycle", operations: 3, waiting: 3 })]);
    });

    it("is stack-safe for a long child-first edit chain", () => {
        const s = state(chain(12000).reverse());
        const view = new NotesStatus();
        expect(view.summary(s)).toMatchObject({ unfinished: 12000, blocked: 1, waiting: 11999 });
        expect(view.overview(s).groups.total).toBe(1);
    });

    it("never hides legacy, capture-only or operation-less holds, even alongside accepted/resolved evidence", () => {
        const s = state([operation("old", "applied"), { ...operation("fixed", "held"), resolvedBy: "resolution" }], {
            "Notes/old.md": "missing_writable_binding", "Notes/fixed.md": "reflection_preserves_local_edit",
            "Notes/2020-01-01.md": "predecessor_capture_held", "missing-operation": "unknown_legacy_reason", transport: "native_resume_pending",
        });
        const view = new NotesStatus();
        expect(view.summary(s)).toMatchObject({ unfinished: 0, holds: 5, captureHolds: 2, reflectionHolds: 1, otherHolds: 2 });
        expect(view.overview(s).groups.total).toBe(5);
        const selected = view.overview(s).groups.items.find(group => group.path === "Notes/old.md")!;
        expect(view.item(s, selected.id)!.hold).toEqual({ path: "Notes/old.md", reason: "missing_writable_binding", kind: "capture" });
        expect(view.summary({ ...s, holds: {} }).holds).toBe(0);
    });

    it("does not touch payloads or resolution leaf texts to build summaries and bounded detail", () => {
        const op = operation("a", "held");
        Object.defineProperty(op, "payload", { get() { throw Error("payload_read"); } });
        Object.defineProperty(op, "resolution", { get() { throw Error("resolution_read"); } });
        const s = state([op]);
        const view = new NotesStatus();
        expect(view.summary(s).blocked).toBe(1);
        const row = view.overview(s).groups.items[0];
        expect(view.item(s, row.id)!.operations.total).toBe(1);
        expect(view.operation(s, "a")).toBe(op);
    });

    it("reuses cached history and holds on unchanged UI events, runtime errors and unrelated journal branches", () => {
        const enumerateOperations = vi.fn();
        const enumerateHolds = vi.fn();
        const s = state(new Proxy([operation("a")], { get(target, prop, receiver) {
            if (prop === Symbol.iterator) enumerateOperations();
            return Reflect.get(target, prop, receiver);
        } }), new Proxy({ "Notes/only.md": "capture_failed" }, { ownKeys(target) { enumerateHolds(); return Reflect.ownKeys(target); } }));
        const view = new NotesStatus();
        const first = view.summary(s);
        for (let n = 0; n < 100; n++) expect(view.summary(s)).toBe(first);
        expect(view.summary({ ...s, bindings: {} })).toBe(first);
        expect(view.summary(s, { checkpointFailure: "recovery_checkpoint_unavailable" }).text).toContain("recovery export failed");
        expect(view.summary(s, { failure: "journal_unavailable" }).phase).toBe("held");
        expect(enumerateOperations).toHaveBeenCalledTimes(1);
        expect(enumerateHolds).toHaveBeenCalledTimes(1);
        view.summary({ ...s, holds: { ...s.holds, "new.md": "event_payload_missing" } });
        expect(enumerateOperations).toHaveBeenCalledTimes(1);
    });

    it("invalidates operation snapshots on exact state transitions and preserves prior summaries", () => {
        const operations = chain(3);
        const s = state(operations, { "Notes/capture.md": "capture_failed" });
        const view = new NotesStatus();
        const first = view.summary(s);
        const next = state([published({ ...operations[0], state: "uploaded" }), ...operations.slice(1)], s.holds);
        expect(view.summary(next)).toMatchObject({ unfinished: 3, pendingPublication: 1, pendingAcceptance: 1, waiting: 1, blocked: 0, holds: 1 });
        expect(first).toMatchObject({ blocked: 1, waiting: 2 });
        expect(Object.isFrozen(first)).toBe(true);
        expect(view.summary(s)).toEqual(first);
    });

    it("bounds overview rows, disclosure and group pages without truncating totals or full-item access", () => {
        const longPath = "Notes/" + "x".repeat(400) + ".md";
        const longReason = "capture_" + "reason".repeat(100);
        const s = state(Array.from({ length: 123 }, (_, n) => operation(String(n))), { [longPath]: longReason });
        const view = new NotesStatus();
        const first = view.overview(s).groups;
        expect(first).toMatchObject({ total: 124, omitted: 104, nextOffset: 20 });
        expect(first.items[0]).toMatchObject({ kind: "hold", path: expect.stringMatching(/\.\.\.$/) });
        expect(first.items[0].path.length).toBe(160);
        expect(first.items[0].reason.length).toBe(160);
        expect(view.item(s, first.items[0].id)!.hold).toMatchObject({ path: longPath, reason: longReason });
        expect(view.overview(s, { limit: 1000000 }).groups.items).toHaveLength(100);
        expect(view.overview(s, { offset: 120, limit: 20 }).groups).toMatchObject({ total: 124, offset: 120, omitted: 120 });
        expect(view.overview(s, { offset: 124 }).groups).toMatchObject({ items: [], total: 124, omitted: 124 });
        expect(view.overview(s, { offset: -3, limit: 0 }).groups.items).toHaveLength(1);
        expect(view.overview(s, { offset: NaN, limit: Infinity }).groups.items).toHaveLength(20);
        expect(view.item(s, "unknown")).toBeUndefined();
        expect(view.operation(s, "missing")).toBeUndefined();
    });

    it("offers later descendants and full retained payload only through explicit item access", () => {
        const s = state(chain(201));
        const view = new NotesStatus();
        const group = view.overview(s).groups.items[0];
        const later = view.item(s, group.id, { offset: 200 })!.operations;
        expect(later).toMatchObject({ total: 201, offset: 200, items: [expect.objectContaining({ id: "200" })] });
        expect(later.nextOffset).toBeUndefined();
        expect(view.operation(s, "200")!.payload).toBe("edited");
        expect(view.item(s, group.id, { limit: 1000 })!.operations.items).toHaveLength(100);
    });

    it("keeps startup, unavailable journal and recovery-export failure distinct from source acceptance", () => {
        const view = new NotesStatus();
        expect(view.summary()).toMatchObject({ phase: "starting", text: "LOOM starting" });
        expect(view.summary(undefined, { failure: "journal_unavailable" })).toMatchObject({ phase: "held", text: "LOOM held: journal_unavailable" });
        const s = state([operation("pending")]);
        const summary = view.summary(s, { checkpointFailure: "recovery_checkpoint_unavailable" });
        expect(summary).toMatchObject({ phase: "ready", unfinished: 1, pendingPublication: 1, checkpointFailure: "recovery_checkpoint_unavailable" });
        expect(summary.text).toContain("recovery export failed");
        expect(view.summary(s).checkpointFailure).toBeUndefined();
    });
});
