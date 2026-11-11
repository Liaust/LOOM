import { digest, directoryPath, relativePath, token, newerSource, type Binding } from "./protocol";
import type { Operation, State } from "./journal";
import type { App, TAbstractFile, TFolder } from "obsidian";

export const WORKSPACE_BRIDGE_DIRECTORY = "workspace-bridge";
export const WORKSPACE_LOOKUP_SCHEMA = "loom.workspace.lookup.v1";
export const WORKSPACE_ACTION_SCHEMA = "loom.workspace.action.v1";
export const WORKSPACE_LOOKUP_BYTES = 64 * 1024;
export const WORKSPACE_LOOKUP_TTL = 30_000;
const identity = ["id", "path", "workspace", "collection", "generation", "fileId"] as const;
export type ExpectedBinding = Pick<Binding, typeof identity[number]>;
export type WorkspaceLookup = { schema: typeof WORKSPACE_LOOKUP_SCHEMA; id: string; entries: ExpectedBinding[] };
export type WorkspaceLookupResponse = {
    schema: typeof WORKSPACE_LOOKUP_SCHEMA;
    id: string;
    observedAt: string;
    vaultRoot: string;
    entries: { path: string; bindingId: string; installed: boolean; pending: boolean | null; held: boolean | null }[];
};
export type WorkspaceAction = {
    operation: "create_note" | "create_folder" | "rename" | "trash";
    path: string;
    target?: string;
    text?: string;
    expectedBindingId?: string;
    expectedHash?: string;
};
export type WorkspaceActionRequest = { schema: typeof WORKSPACE_ACTION_SCHEMA; id: string; action: WorkspaceAction };
export type WorkspaceActionResponse = {
    schema: typeof WORKSPACE_ACTION_SCHEMA;
    id: string;
    observedAt: string;
    vaultRoot: string;
    action: { ok: boolean; operation: WorkspaceAction["operation"]; path: string; target?: string;
        status?: "local_applied_source_pending"; error?: string };
};

function object(value: unknown): value is Record<string, unknown> {
    return !!value && typeof value === "object" && !Array.isArray(value);
}

// The file's mtime supplies a deadline without changing the UI request envelope.
export function parseWorkspaceLookup(raw: string, mtime: number, now = Date.now()): WorkspaceLookup | undefined {
    // stat retains sub-millisecond precision, while Date.now() returns whole milliseconds.
    mtime = Math.floor(mtime);
    if (!Number.isFinite(now) || !Number.isFinite(mtime) || mtime > now || now >= mtime + WORKSPACE_LOOKUP_TTL ||
        raw.length > WORKSPACE_LOOKUP_BYTES || new TextEncoder().encode(raw).length > WORKSPACE_LOOKUP_BYTES) return;
    try {
        const value: unknown = JSON.parse(raw);
        if (!object(value) || Object.keys(value).some(k => !["schema", "id", "entries"].includes(k)) ||
            value.schema !== WORKSPACE_LOOKUP_SCHEMA || !token(value.id) || !Array.isArray(value.entries) || value.entries.length > 100) return;
        const paths = new Set<string>();
        for (const entry of value.entries) {
            if (!object(entry) || Object.keys(entry).length !== identity.length ||
                !identity.every(k => Object.prototype.hasOwnProperty.call(entry, k)) || !directoryPath(entry.path) || paths.has(entry.path) ||
                !identity.every(k => k === "path" || token(entry[k]))) return;
            paths.add(entry.path);
        }
        return value as WorkspaceLookup;
    } catch { return; }
}

export function parseWorkspaceAction(raw: string, mtime: number, now = Date.now()): WorkspaceActionRequest | undefined {
    mtime = Math.floor(mtime);
    if (!Number.isFinite(now) || !Number.isFinite(mtime) || mtime > now || now >= mtime + WORKSPACE_LOOKUP_TTL ||
        raw.length > WORKSPACE_LOOKUP_BYTES || new TextEncoder().encode(raw).length > WORKSPACE_LOOKUP_BYTES) return;
    try {
        const value: unknown = JSON.parse(raw);
        if (!object(value) || Object.keys(value).some(k => !["schema", "id", "action"].includes(k)) ||
            value.schema !== WORKSPACE_ACTION_SCHEMA || !token(value.id) || !object(value.action)) return;
        const a = value.action;
        if (Object.keys(a).some(k => !["operation", "path", "target", "text", "expectedBindingId", "expectedHash"].includes(k)) ||
            !["create_note", "create_folder", "rename", "trash"].includes(String(a.operation)) || !directoryPath(a.path)) return;
        if (a.operation === "rename" ? !directoryPath(a.target) || a.target === a.path || a.target.startsWith(a.path + "/") : a.target !== undefined) return;
        if (a.operation === "create_note") {
            if (!relativePath(a.path) || a.text !== undefined && (typeof a.text !== "string" || a.text.includes("\0") ||
                new TextEncoder().encode(a.text).length > WORKSPACE_LOOKUP_BYTES)) return;
        } else if (a.text !== undefined) return;
        const creating = a.operation === "create_note" || a.operation === "create_folder";
        if (creating ? a.expectedBindingId !== undefined || a.expectedHash !== undefined : !token(a.expectedBindingId)) return;
        if (a.expectedHash !== undefined && (typeof a.expectedHash !== "string" || !/^(sha256:)?[a-f0-9]{64}$/.test(a.expectedHash))) return;
        return value as WorkspaceActionRequest;
    } catch { return; }
}

// Match the private pending(path) predicate, including unresolved transfers.
const unfinished = (op: Operation) => op.state !== "applied" && !op.resolvedBy && !op.sourceSatisfiedBy;
const sameFile = (a: ExpectedBinding, b: ExpectedBinding) => a.workspace === b.workspace &&
    a.collection === b.collection && a.fileId === b.fileId;

export function projectWorkspaceLookup(request: WorkspaceLookup, state: State | undefined, vaultRoot: string,
    now = Date.now()): WorkspaceLookupResponse {
    const entries = request.entries.map(expected => {
        const installed = state?.bindings[expected.path];
        return { path: expected.path, bindingId: expected.id,
            installed: !!installed && !installed.deleted && identity.every(k => installed[k] === expected[k]) &&
                !state?.pathChanges?.["reviewed-source-move/" + expected.id] &&
                !state?.pathChanges?.["reviewed-source-retirement/" + expected.id],
            pending: state ? false : null, held: state ? !!state.holds[expected.path] : null };
    });
    if (state) {
        const byPath = new Map(entries.map(entry => [entry.path, entry]));
        // Inspect operation metadata once, never payload, resolution bodies or the whole journal JSON.
        for (const op of state.operations) {
            if (unfinished(op)) {
                for (const path of [op.intent.path, op.intent.target]) {
                    const row = path && byPath.get(path);
                    if (row) { row.pending = true; row.held ||= op.state === "held" || op.state === "conflict"; }
                }
            }
            if (op.transfer && !op.transferResolvedBy) {
                const row = byPath.get(op.transfer.source.path);
                if (row) { row.pending = true; row.held ||= op.state === "held" || op.state === "conflict"; }
            }
        }
        for (const id in state.layouts ?? {}) {
            const layout = state.layouts![id], a = layout.retiredPrevious ?? layout.control.previous;
            for (let n = 0; n < entries.length; n++) {
                const row = entries[n], expected = request.entries[n];
                if (layout.state === "applied" && layout.control.sourceMove && sameFile(a, expected) && a.path === row.path &&
                    (a.sourceSequence ?? 0) >= (state.bindings[row.path]?.sourceSequence ?? 0)) row.installed = false;
                if (["pending", "moving"].includes(layout.state) &&
                    [layout.control.previous, layout.control.binding].some(b => b.path === row.path || sameFile(b, expected))) {
                    row.pending = true;
                    row.held ||= !!layout.reason;
                }
            }
        }
    }
    return { schema: WORKSPACE_LOOKUP_SCHEMA, id: request.id, observedAt: new Date(now).toISOString(), vaultRoot, entries };
}

const inside = (path: string, root: string) => path.startsWith(root + "/");
const parentPath = (path: string) => path.slice(0, path.lastIndexOf("/"));
const folder = (file: TAbstractFile): file is TFolder => "children" in file;
type WritableScope = Pick<Binding, "workspace" | "collection" | "generation" | "collectionRoot">;
const sameScope = (a: WritableScope, b: WritableScope) => a.workspace === b.workspace && a.collection === b.collection &&
    a.generation === b.generation && a.collectionRoot === b.collectionRoot;

function destinationScope(state: State, path: string): WritableScope | undefined {
    const scopes = Object.values(state.scopes ?? {}).filter(s => inside(path, s.collectionRoot));
    if (scopes.length) return scopes.length === 1 && scopes[0].writable ? scopes[0] : undefined;
    const b = state.bindings[parentPath(path)];
    if (b?.entryKind === "directory" && b.writable && !b.deleted && inside(path, b.collectionRoot)) {
        const scope = state.scopes?.[b.workspace + "/" + b.collection];
        if (!scope || scope.writable && scope.generation === b.generation && scope.collectionRoot === b.collectionRoot) return b;
    }
}

export function workspaceActionProblem(action: WorkspaceAction, state: State | undefined, directory = false): string | undefined {
    if (!state) return "journal_unavailable";
    const creating = action.operation === "create_note" || action.operation === "create_folder";
    const source = creating ? undefined : state.bindings[action.path];
    const destination = action.operation === "rename" ? action.target! : action.path;
    if (!creating) {
        if (!source || source.id !== action.expectedBindingId || source.path !== action.path || !source.writable || source.deleted ||
            (source.entryKind === "directory") !== directory) return "installed_binding_mismatch";
        if (!directory && (!relativePath(action.path) || action.operation === "rename" && !relativePath(destination))) return "media_write_not_supported";
        if (!inside(action.path, source.collectionRoot)) return "scope_root_mutation_not_supported";
        const scope = state.scopes?.[source.workspace + "/" + source.collection];
        if (scope && (!scope.writable || scope.generation !== source.generation || scope.collectionRoot !== source.collectionRoot)) return "scope_not_writable";
        const row = projectWorkspaceLookup({ schema: WORKSPACE_LOOKUP_SCHEMA, id: "action_guard", entries: [source] }, state, "").entries[0];
        if (!row.installed || row.pending || row.held) return "source_pending_or_held";
        if (directory && action.expectedHash) return "directory_hash_not_supported";
    }
    if (creating || action.operation === "rename") {
        const scope = destinationScope(state, destination);
        if (!scope) return "destination_scope_unavailable";
        if (source && !sameScope(source, scope)) return "cross_scope_move_not_supported";
        if (state.bindings[destination]) return "destination_binding_exists";
        for (const id in state.candidates) if (state.candidates[id].path === destination) return "destination_binding_pending";
    }
    const paths = action.target ? [action.path, action.target] : [action.path];
    const related = (path: string) => paths.some(p => path === p || inside(p, path) || directory && inside(path, p));
    for (const path in state.holds) if (state.holds[path] && related(path)) return "path_held";
    for (const path in state.reflecting) if (related(path)) return "reflection_in_progress";
    for (const op of state.operations) {
        if (unfinished(op) && [op.intent.path, op.intent.target].some(p => p && related(p)) ||
            op.transfer && !op.transferResolvedBy && related(op.transfer.source.path)) return "path_pending";
    }
    for (const id in state.layouts ?? {}) {
        const r = state.layouts![id];
        if (["pending", "moving"].includes(r.state) && [r.control.previous.path, r.control.binding.path].some(related)) return "layout_in_progress";
    }
    if (source) for (const id in state.candidates) {
        const b = state.candidates[id];
        if (b.path === source.path && newerSource(b, source)) return "newer_binding_pending";
    }
}

export type WorkspaceActionOwner = {
    app: Pick<App, "vault" | "fileManager">;
    ready(): Promise<void>;
    allowed(path: string): Promise<boolean>;
    readText(path: string): Promise<string | null>;
};

export async function executeWorkspaceAction(request: WorkspaceActionRequest, state: () => State | undefined,
    owner: WorkspaceActionOwner | undefined, vaultRoot: string, current: () => Promise<boolean>, deadline = Infinity): Promise<WorkspaceActionResponse> {
    const a = request.action;
    const result = (error?: string): WorkspaceActionResponse => ({ schema: WORKSPACE_ACTION_SCHEMA, id: request.id,
        vaultRoot, observedAt: new Date().toISOString(), action: { ok: !error, operation: a.operation, path: a.path,
            ...(a.target ? { target: a.target } : {}), ...(error ? { error } : { status: "local_applied_source_pending" as const }) } });
    if (!owner) return result("owner_unavailable");
    try {
        await owner.ready();
        const vault = owner.app.vault, creating = a.operation === "create_note" || a.operation === "create_folder";
        const source = creating ? undefined : vault.getAbstractFileByPath(a.path);
        if (!creating && !source) return result("source_unavailable");
        const directory = !!source && folder(source);
        const problem = workspaceActionProblem(a, state(), directory);
        if (problem) return result(problem);
        const destination = a.target ?? a.path;
        const parent = creating || a.target ? vault.getAbstractFileByPath(parentPath(destination)) : undefined;
        if ((creating || a.target) && (!parent || !folder(parent))) return result("destination_parent_unavailable");
        const members: TAbstractFile[] = source ? [source] : [];
        const memberBindings = new Map<TAbstractFile, string>(), children = new Map<TFolder, TAbstractFile[]>();
        // Inspect only this small selected subtree. Unknown drafts/media remain untouched.
        for (let n = 0; n < members.length; n++) {
            const file = members[n], s = state(), b = s?.bindings[file.path];
            if (!directoryPath(file.path) || !b || !await owner.allowed(file.path)) return result("source_policy_unavailable");
            const held = workspaceActionProblem({ operation: "trash", path: file.path, expectedBindingId: b.id }, state(), folder(file));
            if (held) return result(held);
            memberBindings.set(file, b.id);
            const rootBinding = state()?.bindings[a.path];
            if (!rootBinding || !sameScope(b, rootBinding)) return result("cross_scope_move_not_supported");
            if (a.target) {
                const target = a.target + file.path.slice(a.path.length), latest = state();
                if (!directoryPath(target) || !await owner.allowed(target)) return result("destination_policy_unavailable");
                if (latest?.bindings[target] || Object.values(latest?.candidates ?? {}).some(candidate => candidate.path === target)) return result("destination_binding_exists");
            }
            if (folder(file)) {
                if (members.length + file.children.length > 100) return result("directory_action_limit");
                children.set(file, file.children.slice());
                members.push(...file.children);
            }
        }
        if (!await owner.allowed(destination)) return result("destination_policy_unavailable");
        if (creating || a.target) {
            if (vault.getAbstractFileByPath(destination) || await vault.adapter.exists(destination)) return result("destination_exists");
        }
        let stat: { size: number; mtime: number } | undefined;
        if (source && a.expectedHash) {
            stat = await vault.adapter.stat(a.path) ?? undefined;
            if (!stat || stat.size > WORKSPACE_LOOKUP_BYTES) return result("hash_read_limit");
            const text = await owner.readText(a.path);
            if (text === null || new TextEncoder().encode(text).length > WORKSPACE_LOOKUP_BYTES ||
                (await digest(text)).replace(/^sha256:/, "") !== a.expectedHash.replace(/^sha256:/, "")) return result("local_hash_mismatch");
        }
        if (stat) {
            const after = await vault.adapter.stat(a.path);
            if (!after || after.size !== stat.size || after.mtime !== stat.mtime) return result("source_changed");
        }
        if (!await current()) return result("request_expired_or_superseded");
        const changed = workspaceActionProblem(a, state(), directory);
        if (changed) return result(changed);
        if (source && (vault.getAbstractFileByPath(a.path) !== source || members.some(file => vault.getAbstractFileByPath(file.path) !== file))) return result("source_changed");
        for (const file of members) {
            if (workspaceActionProblem({ operation: "trash", path: file.path, expectedBindingId: memberBindings.get(file) }, state(), folder(file))) return result("source_changed");
        }
        for (const [file, snapshot] of children) if (file.children.length !== snapshot.length || file.children.some((child, n) => child !== snapshot[n])) return result("directory_changed");
        if ((creating || a.target) && (vault.getAbstractFileByPath(destination) || vault.getAbstractFileByPath(parentPath(destination)) !== parent)) return result("destination_changed");
        if (Date.now() >= deadline) return result("request_expired_or_superseded");
        if (a.operation === "create_note") await vault.create(a.path, a.text ?? "");
        else if (a.operation === "create_folder") await vault.createFolder(a.path);
        else if (a.operation === "rename") await owner.app.fileManager.renameFile(source!, a.target!);
        else await vault.trash(source!, true);
        return result();
    } catch { return result("owner_action_failed"); }
}

type DesktopBridge = {
    fs: typeof import("fs");
    paths: typeof import("path");
    pluginDirectory: string;
    vaultRoot: string;
    state(): State | undefined;
    owner?: WorkspaceActionOwner;
};

// Node is supplied only by the desktop adapter; importing this module is safe on mobile.
export function startWorkspaceBridge(desktop?: DesktopBridge): () => void {
    let disposed = false, watcher: import("fs").FSWatcher | undefined;
    const dispose = () => { if (!disposed) { disposed = true; watcher?.close(); } };
    if (!desktop) return dispose;
    const { fs, paths } = desktop, io = fs.promises;
    void (async () => {
        const vaultRoot = await io.realpath(desktop.vaultRoot), pluginRoot = await io.realpath(desktop.pluginDirectory);
        const relative = paths.relative(vaultRoot, pluginRoot);
        if (!relative || relative.startsWith("..") || paths.isAbsolute(relative) || disposed) return;
        const directory = paths.join(pluginRoot, WORKSPACE_BRIDGE_DIRECTORY);
        await io.mkdir(directory, { mode: 0o700 }).catch(error => { if (error.code !== "EEXIST") throw error; });
        const directoryInfo = await io.lstat(directory);
        if (!directoryInfo.isDirectory() || directoryInfo.isSymbolicLink() || disposed) return;
        const requestPath = paths.join(directory, "request.json"), responsePath = paths.join(directory, "response.json");
        const temporary = paths.join(directory, "response.tmp.json");
        const same = (a: import("fs").Stats, b: import("fs").Stats) => a.dev === b.dev && a.ino === b.ino &&
            a.size === b.size && a.mtimeMs === b.mtimeMs;
        const safeDirectory = async () => {
            const current = await io.lstat(directory);
            return !disposed && current.isDirectory() && !current.isSymbolicLink() &&
                current.dev === directoryInfo.dev && current.ino === directoryInfo.ino && await io.realpath(directory) === directory;
        };
        if (!await safeDirectory()) return;
        const seen = new Map<string, number>();
        let dirty = false, running = false, actionEvent = false;
        const process = async (allowAction: boolean) => {
            if (!await safeDirectory()) return;
            const handle = await io.open(requestPath, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
            let before: import("fs").Stats, raw: string;
            try {
                before = await handle.stat();
                if (!before.isFile() || before.nlink !== 1 || before.size > WORKSPACE_LOOKUP_BYTES) return;
                const bytes = new Uint8Array(WORKSPACE_LOOKUP_BYTES + 1);
                let count = 0;
                while (count < bytes.length) {
                    const result = await handle.read(bytes, count, bytes.length - count, count);
                    if (!result.bytesRead) break;
                    count += result.bytesRead;
                }
                if (count !== before.size || count > WORKSPACE_LOOKUP_BYTES || !same(before, await handle.stat()) ||
                    !same(before, await io.lstat(requestPath))) return;
                raw = new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(0, count));
            } finally { await handle.close(); }
            const now = Date.now(), request = parseWorkspaceLookup(raw, before.mtimeMs, now) ?? parseWorkspaceAction(raw, before.mtimeMs, now);
            for (const [id, deadline] of seen) if (deadline <= now) seen.delete(id);
            if (!request || seen.has(request.id) || seen.size >= 128) return;
            const deadline = Math.floor(before.mtimeMs) + WORKSPACE_LOOKUP_TTL;
            const current = async () => {
                if (!await safeDirectory()) return false;
                const latest = await io.lstat(requestPath);
                return !disposed && Date.now() < deadline && same(before, latest);
            };
            let response: string;
            if (request.schema === WORKSPACE_ACTION_SCHEMA) {
                // Never replay a persisted action at startup, or retry an uncertain owner call.
                seen.set(request.id, deadline);
                if (!allowAction) return;
                response = JSON.stringify(await executeWorkspaceAction(request, desktop.state, desktop.owner, vaultRoot, current, deadline));
            } else response = JSON.stringify(projectWorkspaceLookup(request, desktop.state(), vaultRoot, now));
            if (new TextEncoder().encode(response).length > WORKSPACE_LOOKUP_BYTES || !await safeDirectory()) return;
            const output = await io.open(temporary, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK, 0o600);
            try {
                // Reuse only the fixed private response temporary, never a link or arbitrary file.
                const info = await output.stat();
                if (!info.isFile() || info.nlink !== 1 || info.size > WORKSPACE_LOOKUP_BYTES || !same(info, await io.lstat(temporary))) return;
                await output.truncate(0);
                await output.writeFile(response, "utf8");
                await output.sync();
                await output.close();
                if (disposed || Date.now() >= deadline || !await safeDirectory()) return;
                if (!same(before, await io.lstat(requestPath))) { dirty = true; return; }
                await io.rename(temporary, responsePath);
                seen.set(request.id, deadline);
            } finally {
                await output.close().catch(() => {});
            }
        };
        const wake = (allowAction = false) => {
            dirty = true;
            actionEvent ||= allowAction;
            if (running || disposed) return;
            running = true;
            void (async () => {
                try { while (dirty && !disposed) { dirty = false; const allowAction = actionEvent; actionEvent = false; await process(allowAction).catch(() => {}); } }
                finally { running = false; }
            })();
        };
        if (disposed) return;
        watcher = fs.watch(directory, { persistent: false }, (_event, filename) => {
            if (filename === null || filename.toString() === "request.json") wake(filename !== null);
        });
        watcher.on("error", dispose);
        wake();
    })().catch(dispose);
    return dispose;
}
