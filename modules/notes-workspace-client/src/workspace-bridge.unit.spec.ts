import { describe, expect, it, vi } from "vitest";
import { parseWorkspaceLookup, projectWorkspaceLookup, startWorkspaceBridge, WORKSPACE_LOOKUP_SCHEMA,
    WORKSPACE_LOOKUP_BYTES, WORKSPACE_LOOKUP_TTL, type ExpectedBinding, type WorkspaceLookup, type WorkspaceLookupResponse,
    parseWorkspaceAction, workspaceActionProblem, executeWorkspaceAction, WORKSPACE_ACTION_SCHEMA,
    type WorkspaceAction, type WorkspaceActionRequest, type WorkspaceActionResponse, type WorkspaceActionOwner } from "./workspace-bridge";
import { digest, type Binding } from "./protocol";
import type { Operation, State } from "./journal";

const now = Date.parse("2026-11-01T12:00:00Z");
const expected: ExpectedBinding = { id: "binding_1", path: "Projects/alias/note.md", workspace: "main",
    collection: "notes_1", generation: "generation_1", fileId: "file_1" };
const binding = { ...expected, version: 1, kind: "binding", collectionRoot: "Projects/alias", sourceBase: "base_1",
    nativeRevision: "1-a", sha256: "sha256:" + "a".repeat(64), writable: true } as Binding;
const request = (entries = [expected]): WorkspaceLookup => ({ schema: WORKSPACE_LOOKUP_SCHEMA, id: "lookup_1", entries });
const state = (): State => ({ version: 1, device: "desktop", bindings: { [expected.path]: binding },
    candidates: {}, reflecting: {}, holds: {}, operations: [] });
const operation = (extra: Partial<Operation> = {}): Operation => ({ intent: { version: 1, kind: "intent", id: "intent_1",
    device: "desktop", operation: "edit", workspace: expected.workspace, collection: expected.collection,
    generation: expected.generation, fileId: expected.fileId, path: expected.path, base: binding, sha256: binding.sha256,
    length: 1 }, payload: "must not read", state: "queued", revisions: [], ...extra });
const actionRequest = (action: WorkspaceAction): WorkspaceActionRequest => ({ schema: WORKSPACE_ACTION_SCHEMA, id: "action_1", action });
function writableState() {
    const s = state();
    s.scopes = { "main/notes_1": { version: 1, kind: "scope", id: "scope_1", workspace: binding.workspace,
        collection: binding.collection, generation: binding.generation, collectionRoot: binding.collectionRoot, sourceSequence: 1, writable: true } };
    return s;
}
function actionOwner() {
    const parent = { path: "Projects/alias", children: [] as unknown[] };
    const file = { path: binding.path, stat: { size: 5, mtime: 1, ctime: 1 } };
    const files = new Map<string, unknown>([[parent.path, parent], [file.path, file]]);
    const vault = { getAbstractFileByPath: (path: string) => files.get(path) ?? null,
        create: vi.fn(async () => {}), createFolder: vi.fn(async () => {}), trash: vi.fn(async () => {}),
        adapter: { exists: vi.fn(async (path: string) => files.has(path)), stat: vi.fn(async () => file.stat) } };
    const renameFile = vi.fn(async () => {});
    const owner: WorkspaceActionOwner = { app: { vault, fileManager: { renameFile } } as unknown as WorkspaceActionOwner["app"],
        ready: vi.fn(async () => {}), allowed: vi.fn(async () => true), readText: vi.fn(async () => "local") };
    return { owner, file, files, vault, renameFile };
}

describe("explicit workspace owner actions", () => {
    const rename: WorkspaceAction = { operation: "rename", path: binding.path, target: "Projects/alias/renamed.md", expectedBindingId: binding.id };
    it("validates only the explicit action envelope, safe paths, text limits and exact expected IDs", () => {
        const raw = (action: unknown) => JSON.stringify({ schema: WORKSPACE_ACTION_SCHEMA, id: "action_1", action });
        for (const a of [rename, { operation: "trash", path: binding.path, expectedBindingId: binding.id },
            { operation: "create_folder", path: "Projects/alias/new" }, { operation: "create_note", path: "Projects/alias/new.md", text: "hello" }])
            expect(parseWorkspaceAction(raw(a), now, now)).toBeDefined();
        const invalid = [{ ...rename, expectedBindingId: undefined }, { ...rename, expectedHash: "bad" }, { ...rename, target: rename.path },
            { ...rename, text: "unexpected" }, { ...rename, target: rename.path + "/child" }, { ...rename, operation: "delete" },
            { operation: "create_note", path: "Projects/alias/new.pdf", text: "media" },
            { operation: "create_note", path: "Projects/alias/new.md", text: "\0" },
            { operation: "create_note", path: "Projects/alias/new.md", text: "x".repeat(WORKSPACE_LOOKUP_BYTES) },
            ...["../outside.md", "/absolute.md", "Projects/alias/.private/a.md", "Projects/alias/a/../b.md", "Projects/alias\\b.md"]
                .flatMap(path => [{ ...rename, path }, { ...rename, target: path }])];
        for (const a of invalid) expect(parseWorkspaceAction(raw(a), now, now)).toBeUndefined();
        expect(parseWorkspaceAction(raw(rename), now + 0.75, now)).toBeDefined();
        expect(parseWorkspaceAction(raw(rename), now + 1, now)).toBeUndefined();
        expect(parseWorkspaceAction(raw(rename), now - WORKSPACE_LOOKUP_TTL + 0.75, now)).toBeUndefined();
        expect(parseWorkspaceAction(raw(rename), now - WORKSPACE_LOOKUP_TTL, now)).toBeUndefined();
    });
    it("requires writable current scope/bindings and refuses holds, pending edits, layouts and cross-scope moves", () => {
        expect(workspaceActionProblem(rename, writableState())).toBeUndefined();
        expect(workspaceActionProblem({ operation: "create_note", path: "Projects/alias/new.md" }, writableState())).toBeUndefined();
        expect(workspaceActionProblem(rename, undefined)).toBe("journal_unavailable");
        expect(workspaceActionProblem({ ...rename, expectedBindingId: "other" }, writableState())).toBe("installed_binding_mismatch");
        for (const change of [(s: State) => { s.operations = [operation()]; }, (s: State) => { s.holds[binding.path] = "held"; },
            (s: State) => { s.bindings[binding.path] = { ...binding, writable: false }; },
            (s: State) => { s.scopes!["main/notes_1"].writable = false; },
            (s: State) => { s.layouts = { layout_1: { state: "moving", control: { version: 1, kind: "layout", id: "layout_1", previous: binding, binding } } }; }]) {
            const s = writableState(); change(s); expect(workspaceActionProblem(rename, s)).toBeDefined();
        }
        expect(workspaceActionProblem({ ...rename, target: "Other/new.md" }, writableState())).toBe("destination_scope_unavailable");
        const s = writableState(); s.scopes!.other = { ...s.scopes!["main/notes_1"], collection: "other", collectionRoot: "Other" };
        expect(workspaceActionProblem({ ...rename, target: "Other/new.md" }, s)).toBe("cross_scope_move_not_supported");
        const directory = "Projects/alias/folder";
        const bound = state(); bound.bindings[directory] = { ...binding, id: "dir_1", path: directory, entryKind: "directory" };
        expect(workspaceActionProblem({ operation: "create_note", path: directory + "/new.md" }, bound)).toBeUndefined();
        bound.holds[directory + "/draft.md"] = "pending_draft";
        expect(workspaceActionProblem({ operation: "trash", path: directory, expectedBindingId: "dir_1" }, bound, true)).toBe("path_held");
    });
    it("calls only the public owner methods and reports local application, never acceptance", async () => {
        const f = actionOwner(), s = writableState(), current = async () => true;
        for (const a of [{ operation: "create_note", path: "Projects/alias/new.md", text: "hello" },
            { operation: "create_folder", path: "Projects/alias/new" }, rename,
            { operation: "trash", path: binding.path, expectedBindingId: binding.id }] as WorkspaceAction[]) {
            expect((await executeWorkspaceAction(actionRequest(a), () => s, f.owner, "/vault", current)).action)
                .toMatchObject({ ok: true, status: "local_applied_source_pending" });
        }
        expect(f.vault.create).toHaveBeenCalledWith("Projects/alias/new.md", "hello");
        expect(f.vault.createFolder).toHaveBeenCalledWith("Projects/alias/new");
        expect(f.renameFile).toHaveBeenCalledWith(f.file, rename.target);
        expect(f.vault.trash).toHaveBeenCalledWith(f.file, true);
    });
    it("refuses collisions, stale hashes, changed/pending state and superseded requests without mutating", async () => {
        const f = actionOwner(), s = writableState();
        const run = (a = rename, current = async () => true) => executeWorkspaceAction(actionRequest(a), () => s, f.owner, "/vault", current);
        f.files.set(rename.target!, {});
        expect((await run()).action.error).toBe("destination_exists");
        f.files.delete(rename.target!);
        expect((await run({ ...rename, expectedHash: "a".repeat(64) })).action.error).toBe("local_hash_mismatch");
        expect((await run(rename, async () => false)).action.error).toBe("request_expired_or_superseded");
        expect((await executeWorkspaceAction(actionRequest(rename), () => s, f.owner, "/vault", async () => true, Date.now() - 1)).action.error)
            .toBe("request_expired_or_superseded");
        f.owner.allowed = async () => { s.holds[binding.path] = "new_hold"; return true; };
        expect((await run()).action.ok).toBe(false);
        expect(f.renameFile).not.toHaveBeenCalled(); expect(f.vault.trash).not.toHaveBeenCalled();
        delete s.holds[binding.path]; f.owner.allowed = async () => true;
        expect((await run({ ...rename, expectedHash: await digest("local") })).action.ok).toBe(true);
    });
    it("protects unbound folder contents instead of inventing child bindings", async () => {
        const f = actionOwner(), s = writableState(), path = "Projects/alias/folder";
        const child = { path: path + "/unbound.md" }, directory = { path, children: [child] };
        f.files.set(path, directory); f.files.set(child.path, child);
        s.bindings[path] = { ...binding, id: "dir_1", path, entryKind: "directory" };
        const response = await executeWorkspaceAction(actionRequest({ operation: "trash", path, expectedBindingId: "dir_1" }), () => s,
            f.owner, "/vault", async () => true);
        expect(response.action).toMatchObject({ ok: false, error: "source_policy_unavailable" });
        expect(f.vault.trash).not.toHaveBeenCalled();
    });
});

describe("desktop workspace metadata bridge", () => {
    it("validates exact metadata, directory paths, request IDs and a strict freshness deadline", () => {
        expect(parseWorkspaceLookup(JSON.stringify(request()), now, now)).toEqual(request());
        expect(parseWorkspaceLookup(JSON.stringify(request()), now + 0.75, now)).toEqual(request());
        expect(parseWorkspaceLookup(JSON.stringify(request()), now - WORKSPACE_LOOKUP_TTL + 0.75, now)).toBeUndefined();
        expect(parseWorkspaceLookup(JSON.stringify(request(Array.from({ length: 100 }, (_, n) =>
            ({ ...expected, path: `Notes/${n}.md` })))), now, now)?.entries).toHaveLength(100);
        expect(parseWorkspaceLookup(JSON.stringify(request([{ ...expected, path: "Projects/alias/empty" }])), now, now)).toBeDefined();
        expect(parseWorkspaceLookup(JSON.stringify(request()), now - WORKSPACE_LOOKUP_TTL, now)).toBeUndefined();
        expect(parseWorkspaceLookup(JSON.stringify(request()), now + 1, now)).toBeUndefined();
        expect(parseWorkspaceLookup(JSON.stringify({ ...request(), id: "../response" }), now, now)).toBeUndefined();
        expect(parseWorkspaceLookup(JSON.stringify({ ...request(), schema: "wrong" }), now, now)).toBeUndefined();
    });
    it("rejects oversized, malformed, excessive, duplicate, incomplete and path-bearing requests", () => {
        const invalid = ["{", " ".repeat(WORKSPACE_LOOKUP_BYTES + 1), JSON.stringify({ ...request(), payload: "secret" }),
            JSON.stringify(request(Array.from({ length: 50 }, (_, n) => ({ ...expected, path: `Notes/${n}-` + "\u65e5".repeat(500) + ".md" })))),
            JSON.stringify(request(Array.from({ length: 101 }, (_, n) => ({ ...expected, path: `Notes/${n}.md` })))),
            JSON.stringify(request([expected, expected])), JSON.stringify(request([{ ...expected, fileId: undefined } as any])),
            ...["../secret.md", "/absolute.md", "Notes/.obsidian/data.json", "Notes/private_no_index/a.md", "Notes\\a.md", "Notes/a/../b.md"]
                .map(path => JSON.stringify(request([{ ...expected, path }]))),
            JSON.stringify(request([{ ...expected, payload: "secret" } as any]))];
        for (const raw of invalid) expect(parseWorkspaceLookup(raw, now, now)).toBeUndefined();
    });
    it("matches every required identity field and does not promote candidates or reflecting receipts", () => {
        expect(projectWorkspaceLookup(request(), state(), "/vault", now)).toEqual({ schema: WORKSPACE_LOOKUP_SCHEMA,
            id: "lookup_1", observedAt: new Date(now).toISOString(), vaultRoot: "/vault",
            entries: [{ path: expected.path, bindingId: expected.id, installed: true, pending: false, held: false }] });
        for (const field of ["id", "path", "workspace", "collection", "generation", "fileId"] as const) {
            const s = state(); s.bindings[expected.path] = { ...binding, [field]: "other" };
            expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].installed).toBe(false);
        }
        const s = state(); s.bindings = {}; s.candidates[binding.id] = binding; s.reflecting[binding.path] = binding;
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].installed).toBe(false);
        expect(projectWorkspaceLookup(request(), undefined, "/vault", now).entries[0]).toMatchObject({ installed: false, pending: null, held: null });
    });
    it("reads only pending metadata, including targets, transfers, holds and resolved exclusions", () => {
        for (const status of ["queued", "publishing", "uploaded", "held", "conflict"] as const) {
            const s = state(), op = operation({ state: status });
            Object.defineProperty(op, "payload", { get() { throw Error("note_bytes_read"); } });
            Object.defineProperty(op, "resolution", { get() { throw Error("resolution_bytes_read"); } });
            s.operations = [op]; s.holds[expected.path] = "capture_held";
            expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0]).toMatchObject({ installed: true, pending: true, held: true });
        }
        for (const extra of [{ state: "applied" }, { resolvedBy: "resolution_1" }, { sourceSatisfiedBy: binding }] as Partial<Operation>[]) {
            const s = state(); s.operations = [operation(extra)];
            expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].pending).toBe(false);
        }
        const s = state(), op = operation(); op.intent = { ...op.intent, path: "Notes/old.md", target: expected.path };
        s.operations = [op]; expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].pending).toBe(true);
        op.intent.target = undefined; op.state = "applied";
        op.transfer = { source: { path: expected.path, binding } };
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].pending).toBe(true);
        op.transferResolvedBy = "transfer_1";
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].pending).toBe(false);
    });
    it("qualifies pending layouts and refuses deleted or explicitly retired installed bindings", () => {
        const s = state(); s.layouts = { layout_1: { state: "moving", reason: "layout_pending_intent", control: {
            version: 1, kind: "layout", id: "layout_1", previous: binding, binding: { ...binding, path: "Projects/new/note.md" } } } };
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0]).toMatchObject({ pending: true, held: true });
        s.layouts.layout_1.state = "applied"; s.layouts.layout_1.control.sourceMove = true;
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].installed).toBe(false);
        delete s.layouts;
        s.pathChanges = { ["reviewed-source-retirement/" + binding.id]: true };
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].installed).toBe(false);
        delete s.pathChanges; s.bindings[binding.path] = { ...binding, deleted: true };
        expect(projectWorkspaceLookup(request(), s, "/vault", now).entries[0].installed).toBe(false);
    });
    it("does not require Node APIs on mobile", () => { expect(startWorkspaceBridge()).toBeTypeOf("function"); });
    it("answers fresh startup and atomic filesystem requests, then disposes its native watcher", async () => {
        const fs = await import("node:fs"), paths = await import("node:path"), os = await import("node:os");
        const root = await fs.promises.mkdtemp(paths.join(os.tmpdir(), "loom-workspace-bridge-"));
        const pluginDirectory = paths.join(root, ".obsidian/plugins/obsidian-livesync");
        const directory = paths.join(pluginDirectory, "workspace-bridge");
        await fs.promises.mkdir(directory, { recursive: true });
        const responsePath = paths.join(directory, "response.json");
        const requestPath = paths.join(directory, "request.json");
        let cancelWait = () => {}, dispose = () => {};
        const changedResponse = <T = WorkspaceLookupResponse>(id: string) => new Promise<T>((resolve, reject) => {
            const timer = setTimeout(() => { stop(); reject(Error("response_deadline")); }, 2000);
            const watch = fs.watch(directory, (_event, filename) => {
                if (filename?.toString() !== "response.json") return;
                void fs.promises.readFile(responsePath, "utf8").then(raw => {
                    const response = JSON.parse(raw);
                    if (response.id === id) { stop(); resolve(response); }
                }).catch(reject);
            });
            const stop = () => { clearTimeout(timer); watch.close(); };
            cancelWait = stop;
        });
        const watches: import("node:fs").FSWatcher[] = [];
        const watch = ((...args: Parameters<typeof fs.watch>) => {
            const watcher = fs.watch(...args);
            vi.spyOn(watcher, "close"); watches.push(watcher);
            return watcher;
        }) as typeof fs.watch;
        try {
            await fs.promises.writeFile(requestPath, JSON.stringify(request()));
            const first = changedResponse("lookup_1");
            const f = actionOwner();
            dispose = startWorkspaceBridge({ fs: { ...fs, watch }, paths, pluginDirectory, vaultRoot: root, state: writableState, owner: f.owner });
            expect((await first).entries[0]).toMatchObject({ installed: true, pending: false, held: false });
            const second = changedResponse("lookup_2");
            const temporary = paths.join(directory, "request.tmp.json");
            await fs.promises.writeFile(temporary, JSON.stringify({ ...request(), id: "lookup_2" }));
            await fs.promises.rename(temporary, requestPath);
            expect((await second).vaultRoot).toBe(await fs.promises.realpath(root));
            const applied = changedResponse<WorkspaceActionResponse>("action_event");
            await fs.promises.writeFile(temporary, JSON.stringify({ ...actionRequest({ operation: "create_folder", path: "Projects/alias/new" }), id: "action_event" }));
            await fs.promises.rename(temporary, requestPath);
            expect((await applied).action.status).toBe("local_applied_source_pending");
            expect(f.vault.createFolder).toHaveBeenCalledTimes(1);
            expect(watches).toHaveLength(1);
            dispose(); dispose();
            expect(watches[0].close).toHaveBeenCalledTimes(1);
        } finally { dispose(); cancelWait(); await fs.promises.rm(root, { recursive: true, force: true }); }
    });
    it("does not execute an action already on disk at plugin startup", async () => {
        const fs = await import("node:fs"), paths = await import("node:path"), os = await import("node:os");
        const root = await fs.promises.mkdtemp(paths.join(os.tmpdir(), "loom-workspace-action-startup-"));
        const pluginDirectory = paths.join(root, ".obsidian/plugins/obsidian-livesync"), directory = paths.join(pluginDirectory, "workspace-bridge");
        await fs.promises.mkdir(directory, { recursive: true });
        const requestPath = paths.join(directory, "request.json");
        await fs.promises.writeFile(requestPath, JSON.stringify(actionRequest({ operation: "create_folder", path: "Projects/alias/new" })));
        const canonicalRequestPath = await fs.promises.realpath(requestPath);
        let readRequest = () => {};
        const read = new Promise<void>(resolve => { readRequest = resolve; });
        const io = { ...fs.promises, open: async (...args: Parameters<typeof fs.promises.open>) => {
            const handle = await fs.promises.open(...args);
            if (String(args[0]) === canonicalRequestPath) {
                const close = handle.close.bind(handle);
                handle.close = async () => { await close(); readRequest(); };
            }
            return handle;
        } };
        const f = actionOwner();
        const dispose = startWorkspaceBridge({ fs: { ...fs, promises: io }, paths, pluginDirectory, vaultRoot: root, state: writableState, owner: f.owner });
        try {
            await read;
            await new Promise<void>(resolve => setImmediate(resolve));
            expect(f.owner.ready).not.toHaveBeenCalled();
            expect(f.vault.createFolder).not.toHaveBeenCalled();
        } finally { dispose(); await fs.promises.rm(root, { recursive: true, force: true }); }
    });
});
