import { Notice } from "../deps";
import { Publisher } from "./publisher";
import type { ServiceFileHandler } from "../serviceModules/FileHandler";
import type ObsidianLiveSyncPlugin from "../main";
import type { DatabaseFileAccess } from "@vrtmrz/livesync-commonlib/compat/interfaces/DatabaseFileAccess";
import type { LiveSyncLocalDB } from "@vrtmrz/livesync-commonlib/compat/pouchdb/LiveSyncLocalDB";
import type { FilePathWithPrefix } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { readContent } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import { ControlTransport, nativeChunk, nativeMetadataChange } from "./control";
import { Journal, indexedDBPersistence, indexedDBReplayPersistence, recoveryPersistence } from "./journal";
import { NativeReplay, nativeDatabaseIdentity, replayBacklog, replayControlPaths, NATIVE_REPLAY_BATCH, type ReplayWork } from "./native-replay";
import { IntentClient } from "./client";
import { MAX_BYTES, MAX_REFERENCE_BYTES, referenceBytes, reserved, isControlPath, digest, referencePath, type Binding } from "./protocol";
import { layoutAlias, layoutPolicyPaths, relocateLocal } from "./layout";
import { convergeNative } from "./conflicts";
import { NotesConflictsModal, NotesStatusModal } from "./conflict-ui";
import { classifyNativeTarget, type NativeLeaf } from "./native-target";
import { PathWork, WakeLoop } from "./work";
import { TextFileView, TFolder } from "obsidian";
import { UpdateBarrier, startupHeld } from "./update";
import release from "./release.json";

export function createUpdateHandoff(plugin: ObsidianLiveSyncPlugin) {
    let heldAtStartup = false;
    let ready = false;
    const barrier = new UpdateBarrier({
        async flushAndFenceEditor() {
            const views: TextFileView[] = [];
            let canvasOpen = false;
            plugin.app.workspace.iterateAllLeaves(leaf => {
                if (leaf.view instanceof TextFileView) views.push(leaf.view);
                if (leaf.view.getViewType() === "canvas") canvasOpen = true;
            });
            for (const view of views) await view.save();
            // Obsidian exposes save(), but no public editor-input fence. Defer
            // live replacement with editors open; a held startup has no writer.
            return heldAtStartup || views.length === 0 && !canvasOpen;
        },
        async stopLocal() { await plugin.loomClient?.ready; await plugin.loomClient?.stopNative(); },
        async stopNative() { await plugin.core.services.control.onUnload(); },
    });
    return {
        apiVersion: 1,
        identity: release,
        get ready() { return ready && !barrier.stopped; },
        get held() { return heldAtStartup; },
        prepared: () => barrier.stopped,
        prepare: () => barrier.prepare(),
        shutdown: () => barrier.shutdown(),
        resume: () => barrier.resume(),
        markReady() { ready = true; },
        async startupAllowed() {
            const path = `${plugin.app.vault.configDir}/plugins/loom-client-updater/install.json`;
            try {
                if (!await plugin.app.vault.adapter.exists(path)) return true;
                const text = await plugin.app.vault.adapter.read(path);
                heldAtStartup = text.length > 65536 || startupHeld(JSON.parse(text), release.releaseId);
            } catch { heldAtStartup = true; }
            if (heldAtStartup) new Notice("LOOM Notes sync is held until the client updater repairs an interrupted code installation.");
            return !heldAtStartup;
        },
    };
}
export function createLoomClient(
    plugin: ObsidianLiveSyncPlugin,
    access: DatabaseFileAccess,
    local: () => LiveSyncLocalDB,
) {
    const appId = "appId" in plugin.app ? String(plugin.app.appId) : "";
    if (!appId) throw new Error("LOOM intent requires a stable vault appId");
    const work = new PathWork();
    // Device exclusions and size limits apply to background recovery too,
    // before native chunks are assembled or local binary files are loaded.
    const referenceAllowed = async (path: string, revision?: string) => {
        for (const policyPath of layoutPolicyPaths(journal?.state, path))
            if (!await plugin.core.services.vault.isTargetFile(policyPath) ||
                !local().isTargetFile(policyPath as FilePathWithPrefix)) return false;
        let size: number | undefined;
        if (revision) {
            const meta = await access.fetchEntryMeta(path as FilePathWithPrefix, revision, true);
            if (!meta || meta.deleted || meta._deleted) return false;
            size = meta.size;
        } else {
            const stat = await plugin.app.vault.adapter.stat(path);
            if (stat?.type === "file") size = stat.size;
        }
        return size === undefined || (Number.isSafeInteger(size) && size >= 0 &&
            size <= MAX_REFERENCE_BYTES && !plugin.core.services.vault.isFileSizeTooLarge(size));
    };
    const revisionReady = async (path: string, revision: string) => {
        const meta = await access.fetchEntryMeta(path as FilePathWithPrefix, revision, true);
        if (!meta) { work.wait(path, []); return false; }
        if (!("children" in meta)) { work.clear(path); return true; }
        const embedded = new Set(Object.keys(meta.eden ?? {}));
        const keys = meta.children.filter(id => !embedded.has(id));
        if (!keys.length) { work.clear(path); return true; }
        const rows = await local().localDatabase.allDocs({ keys });
        const missing = rows.rows.filter(row => "error" in row || row.value.deleted).map(row => row.key);
        if (missing.length) { work.wait(path, missing); return false; }
        work.clear(path);
        return true;
    };
    // Replication may deliver metadata before chunks. Wait for their change
    // event instead of asking native reads to report an expected partial arrival.
    const transport = new ControlTransport(access, revisionReady);
    const journal = new Journal(recoveryPersistence(indexedDBPersistence(appId), appId, async (slot, content) => {
        const directory = `${plugin.app.vault.configDir}/plugins/${plugin.manifest.id}`;
        await plugin.app.vault.adapter.write(`${directory}/loom-intent-backup-${slot}.json`, content);
    }));
    const nativeLeaves = async (path: string): Promise<{ revision: string; text: string }[]> => {
        const meta = await access.fetchEntryMeta(path as FilePathWithPrefix, undefined, true);
        if (!meta) return [];
        const rows = await local().localDatabase.get(meta._id, { open_revs: "all" });
        const leaves = rows.filter(row => "ok" in row && !row.ok._deleted);
        if (leaves.length > 32) throw Error("native_conflict_limit");
        const result: { revision: string; text: string }[] = [];
        for (const row of leaves) {
            if (!("ok" in row) || !row.ok._rev) continue;
            const rev = row.ok._rev;
            if (!await revisionReady(path, rev)) throw Error("native_conflict_chunks_pending");
            const entry = await access.fetchEntry(path as FilePathWithPrefix, rev, false, true, true);
            if (!entry || entry.deleted || entry._deleted || entry.size > MAX_BYTES) throw Error("native_conflict_text_unavailable");
            const text = readContent(entry);
            if (typeof text !== "string") throw Error("native_conflict_not_text");
            result.push({ revision: rev, text });
        }
        return result;
    };
    const client = new IntentClient(journal, {
        async read(path) {
            const stat = await plugin.app.vault.adapter.stat(path);
            if (!stat || stat.type !== "file") return null;
            if (stat.size > MAX_BYTES) throw new Error("payload_limit");
            return plugin.app.vault.adapter.read(path);
        },
        readControl: (path, revision) => transport.get(path, revision),
        conflictLeaves: nativeLeaves,
        async readRevision(path, revision) {
            if (!await revisionReady(path, revision)) return null;
            const entry = await access.fetchEntry(path as FilePathWithPrefix, revision, false, true, true);
            const text = entry && !entry.deleted && !entry._deleted ? readContent(entry) : null;
            return typeof text === "string" ? text : null;
        },
        async readReference(path, revision) {
            if (!await referenceAllowed(path, revision)) return null;
            if (revision) {
                if (!await revisionReady(path, revision)) return null;
                const entry = await access.fetchEntry(path as FilePathWithPrefix, revision, false, true, true);
                if (!entry || entry.deleted || entry._deleted || entry.size > MAX_REFERENCE_BYTES) return null;
                return referenceBytes(readContent(entry));
            }
            const stat = await plugin.app.vault.adapter.stat(path);
            if (!stat || stat.type !== "file") return null;
            if (stat.size > MAX_REFERENCE_BYTES) throw new Error("reference_limit");
            const bytes = await plugin.app.vault.adapter.readBinary(path);
            if (bytes.byteLength > MAX_REFERENCE_BYTES) throw new Error("reference_limit");
            return new Uint8Array(bytes);
        },
        referenceAllowed,
        async relocate(previous, binding) {
            const vault = plugin.app.vault;
            await (plugin.core.serviceModules.fileHandler as ServiceFileHandler).loomPublish(
                [previous.path, binding.path] as FilePathWithPrefix[], async () => {
                    await relocateLocal(previous, binding, {
                        async stat(path) { return (await vault.adapter.stat(path))?.type ?? null; },
                        async hash(path) {
                            if ((await vault.adapter.stat(path))?.type !== "file") return null;
                            if (referencePath(path)) {
                                if (!await referenceAllowed(path)) throw Error("layout_reference_excluded");
                                return digest(new Uint8Array(await vault.adapter.readBinary(path)));
                            }
                            const stat = await vault.adapter.stat(path);
                            if (!stat || stat.size > MAX_BYTES) throw Error("layout_content_limit");
                            return digest(await vault.adapter.read(path));
                        },
                        async mkdir(path) { await vault.createFolder(path); },
                        async rename(from, to) {
                            const file = vault.getAbstractFileByPath(from);
                            if (!file || file instanceof TFolder || await vault.adapter.exists(to)) throw Error("layout_move_pending");
                            // vault.rename does not invoke FileManager's authored-link rewrite.
                            await vault.rename(file, to);
                        },
                        async removeFile(path) {
                            const file = vault.getAbstractFileByPath(path);
                            if (file && !(file instanceof TFolder)) await vault.trash(file, false);
                        },
                        async removeEmpty(path) {
                            if (path !== previous.path && journal.state.bindings[path]?.entryKind === "directory") return;
                            const folder = vault.getAbstractFileByPath(path);
                            if (folder instanceof TFolder && !folder.children.length &&
                                !(await vault.adapter.list(path)).files.length && !(await vault.adapter.list(path)).folders.length)
                                await vault.trash(folder, false);
                        },
                    });
                    return true;
                });
        },
        async descends(path, revision, ancestor) {
            try {
                const meta = await access.fetchEntryMeta(path as FilePathWithPrefix, undefined, true);
                if (!meta) return false;
                const rows = await local().localDatabase.get(meta._id, { open_revs: "all", revs: true });
                return rows.some(row => {
                    if (!("ok" in row)) return false;
                    const r = (row.ok as { _revisions?: { start: number; ids: string[] } })._revisions;
                    if (!r) return false;
                    const chain = r.ids.map((id, i) => `${r.start - i}-${id}`);
                    const child = chain.indexOf(revision), parent = chain.indexOf(ancestor);
                    return child >= 0 && parent >= child;
                });
            } catch {
                return false;
            }
        },
        async reflect(binding: Binding) {
            if (!binding.writable && !await referenceAllowed(binding.path, binding.nativeRevision)) return;
            if (!await revisionReady(binding.path, binding.nativeRevision)) {
                await client.hold(binding.path, "reflection_chunks_pending");
                return;
            }
            const meta = await access.fetchEntryMeta(binding.path as FilePathWithPrefix, undefined, true);
            if (!meta) return;
            const current = await local().localDatabase.get(meta._id, { conflicts: true });
            if (current._conflicts?.length) {
                const converged = binding.writable && await (plugin.core.serviceModules.fileHandler as ServiceFileHandler).loomPublish(
                    [binding.path as FilePathWithPrefix], () => convergeNative(client, binding, {
                        leaves: () => nativeLeaves(binding.path),
                        remove: async revision => { await local().localDatabase.remove(meta._id, revision); },
                    }));
                if (!converged) {
                    await client.hold(binding.path, "native_conflict_requires_resolution");
                    return;
                }
            }
            if (current._rev !== binding.nativeRevision && !current._conflicts?.includes(binding.nativeRevision)) {
                // A later native revision arrived before its acceptance. Retain
                // the binding and wait for that receipt; do not request a dead leaf.
                return;
            }
            await plugin.core.serviceModules.fileHandler.dbToStorageWithSpecificRev(
                binding.path as FilePathWithPrefix,
                binding.nativeRevision,
                false,
            );
        },
        changed() {
            /* The status element is connected after the native startup lifecycle. */
        },
        async directoryExists(path) {
            return (await plugin.app.vault.adapter.stat(path))?.type === "folder";
        },
        async reflectDirectory(binding) {
            const vault = plugin.app.vault;
            const ensure = async (path: string) => {
                let current = "";
                for (const part of path.split("/")) {
                    current = current ? current + "/" + part : part;
                    const at = vault.getAbstractFileByPath(current);
                    if (at && !(at instanceof TFolder)) throw Error("directory_path_collision");
                    if (!at) await vault.createFolder(current);
                }
            };
            const path = binding.previousPath ?? binding.path;
            const old = vault.getAbstractFileByPath(path);
            if (old && !(old instanceof TFolder)) throw Error("directory_path_collision");
            if (binding.deleted) {
                if (old instanceof TFolder) {
                    if (old.children.length) throw Error("directory_preserves_local_children");
                    await vault.trash(old, false);
                }
                return;
            }
            if (binding.previousPath && old) {
                const target = vault.getAbstractFileByPath(binding.path);
                if (!target) {
                    const parent = binding.path.split("/").slice(0,-1).join("/");
                    if (parent) await ensure(parent);
                    await vault.rename(old,binding.path);
                } else {
                    if (!(target instanceof TFolder) || !(old instanceof TFolder) || old.children.length)
                        throw Error("directory_preserves_local_children");
                    await vault.trash(old,false);
                }
            } else await ensure(binding.path);
        },
        async removeNote(path) {
            const file = plugin.app.vault.getAbstractFileByPath(path);
            if (file && !("children" in file)) await plugin.app.vault.trash(file, false);
        },
    });
    const publisher = new Publisher(client, access, {
        encrypted: () =>
            !!plugin.core.services.setting.currentSettings().encrypt &&
            !!plugin.core.services.setting.currentSettings().passphrase,
        async target(path) {
            let rows;
            try { rows = await local().localDatabase.get(await plugin.core.services.path.path2id(path as FilePathWithPrefix), { open_revs: "all" }); }
            catch (error) {
                if ((error as { status?: number }).status === 404) return classifyNativeTarget(path, []);
                throw error;
            }
            const leaves: NativeLeaf[] = [];
            for (const row of rows) {
                if (!("ok" in row)) { leaves.push({ revision: "" }); continue; }
                const raw = row.ok;
                if (raw._deleted) { leaves.push({ revision: raw._rev!, branchDeleted: true }); continue; }
                const meta = await access.fetchEntryMeta(path as FilePathWithPrefix, raw._rev, true);
                leaves.push(meta ? { revision: raw._rev!, path: String(meta.path), type: meta.type,
                    deleted: meta.deleted, branchDeleted: meta._deleted } : { revision: raw._rev! });
            }
            return classifyNativeTarget(path, leaves);
        },
        locked: async (paths, run) =>
            (plugin.core.serviceModules.fileHandler as ServiceFileHandler).loomPublish(
                paths as FilePathWithPrefix[],
                run,
            ),
    });
    const replayPersistence = indexedDBReplayPersistence(appId);
    let replay: NativeReplay | undefined;
    let replayDatabase: LiveSyncLocalDB["localDatabase"] | undefined;
    let replayIdentity: string | undefined;
    let replayBlocked = false;
    let recoveryRequested = false;
    let recoveryWalked = false;
    let watching = false;
    let cancelFeed: (() => void) | undefined;
    let feedFailed = false;
    const retryPaths = () => {
        work.retry();
        for (const path of replay?.pendingPaths ?? []) work.add(path);
    };
    const loop = new WakeLoop(async () => {
            await client.ready;
            if (client.stopped) return;
            const database = local().localDatabase;
            const identity = await nativeDatabaseIdentity(database);
            if (!replay || replayDatabase !== database || replayIdentity !== identity) {
                cancelFeed?.();
                watching = false;
                replay = await NativeReplay.open(replayPersistence, identity);
                replayDatabase = database;
                replayIdentity = identity;
                work.restart();
                for (const row of replay.pending) if (row.kind === "path") work.add(row.path);
                recoveryWalked = !replay.recoveryNeeded;
                recoveryRequested ||= replay.requiresRecovery();
            }
            const activeReplay = replay;
            const recovery = recoveryRequested;
            replayBlocked = false;
            let processed = 0;
            const reflect = async (path: string) => {
                if (client.stopped) return false;
                await client.reflectPath(path, recovery);
                if (++processed % 32 === 0) await new Promise(resolve => setTimeout(resolve, 0));
                return !client.stopped;
            };
            if (!watching) {
                const feed = database.changes({ since: "now", live: true, include_docs: false });
                watching = true;
                feedFailed = false;
                cancelFeed = () => feed.cancel();
                feed.on("change", change => {
                    if (!nativeChunk(change.id) || work.interested(change.id)) loop.wake();
                });
                feed.on("error", () => {
                    watching = false;
                    feedFailed = true;
                    feed.cancel();
                    if (!client.stopped) {
                        void client.hold("native-change-feed", "native_change_feed_unavailable").catch(() => {});
                        loop.wake();
                    }
                });
                plugin.register(() => feed.cancel());
                await journal.update(s => { delete s.holds["native-change-feed"]; }, s => !s.holds["native-change-feed"]);
            }
            // Retire persisted work before admitting controls or transient recovery
            // paths, including when the inbox starts completely full.
            await activeReplay.drainPaths(work, reflect);
            for (const row of activeReplay.controls) {
                const path = row.path;
                work.clear(path);
                try {
                    const record = await transport.get(path, row.revision);
                    await client.ingest(record);
                    work.clear(path);
                    await journal.update(s => { delete s.holds[path]; }, s => !s.holds[path]);
                    const paths = replayControlPaths(record);
                    try { await activeReplay.complete(row, paths); }
                    finally { for (const path of paths) work.add(path); }
                } catch (error) {
                    if (replayBacklog(error)) replayBlocked = true;
                    else await client.hold(path, "control_pending_or_invalid");
                }
            }
            await publisher.drainAcks(recovery);
            if (recovery) {
                recoveryRequested = false;
                recoveryWalked = false;
                try {
                    await activeReplay.beginRecovery();
                    for (const b of Object.values(journal.state.bindings)) {
                        if (client.stopped) return;
                        await client.mutation({ kind: "store", info: b.path as FilePathWithPrefix });
                        if (++processed % 32 === 0) await new Promise(resolve => setTimeout(resolve, 0));
                    }
                    for (const b of Object.values(journal.state.candidates)) work.add(b.path);
                    recoveryWalked = true;
                } catch (error) {
                    recoveryRequested = true;
                    throw error;
                }
            }
            await publisher.pump();
            await publisher.drainAcks();
            await client.layouts.resume(undefined, recovery);
            await activeReplay.drainPaths(work, reflect);
            work.take(Infinity, isControlPath); // Controls retry through their durable rows.
            if (!activeReplay.hasRunnablePaths(work)) {
                const paths = work.take(NATIVE_REPLAY_BATCH, path => !isControlPath(path) && !activeReplay.path(path));
                try {
                    if (await activeReplay.enqueueAvailable(paths) < paths.length) replayBlocked = true;
                } finally {
                    // Admitted paths are now runnable durable work; the rest stay
                    // transient for a later page, never dropped to make room.
                    for (const path of paths) work.add(path);
                }
            }
            const batch = await database.changes({ since: activeReplay.cursor, include_docs: false, limit: NATIVE_REPLAY_BATCH });
            const pending: ReplayWork[] = [];
            for (const change of batch.results) {
                const metadata = await nativeMetadataChange(change, (id, revision) => database.get(id, { rev: revision }));
                const path = metadata.doc?.path === undefined ? undefined : String(metadata.doc.path);
                if (path && isControlPath(path)) {
                    if (!metadata.doc?._rev) throw Error("native_change_revision_missing");
                    pending.push({ kind: "control", path, revision: metadata.doc._rev });
                } else if (path) pending.push({ kind: "path", path });
                else for (const path of work.changed(change.id))
                    if (!isControlPath(path)) pending.push({ kind: "path", path });
            }
            try {
                await activeReplay.accept(batch.last_seq, pending);
                for (const row of pending) if (row.kind === "path") work.add(row.path);
                if (pending.length || batch.results.length === NATIVE_REPLAY_BATCH) loop.wake();
            } catch (error) {
                if (!replayBacklog(error)) throw error;
                replayBlocked = true;
            }
            if (replayBlocked) await client.hold("transport", "native_replay_backlog_limit");
            feedFailed = false;
            // Until every initial candidate path is durable (or completed), a
            // crash must request full recovery again, even with a valid cursor.
            if (recoveryWalked && activeReplay.recoveryNeeded && !work.runnable && !replayBlocked)
                await activeReplay.finishRecovery();
            if (activeReplay.hasRunnablePaths(work) || !replayBlocked && work.runnable) loop.wake();
            if (!replayBlocked && !activeReplay.controls.length) await journal.update(s => { delete s.holds.transport; }, s => !s.holds.transport);
            client.io.changed();
        }, () => !watching || feedFailed || replayBlocked || !!replay?.hasPending || work.pending || work.runnable || publisher.needsRetry,
        retryPaths, async () => {
            feedFailed = true;
            if (!client.stopped && !journal.failure) await client.hold("transport", "native_resume_pending");
        });
    client.onReflect = path => { work.add(path); loop.wake(); };
    const cache = plugin.app.metadataCache;
    const originalDest = cache.getFirstLinkpathDest;
    const aliasDest: typeof originalDest = function(path, source) {
        const result = originalDest.call(cache, path, source);
        if (result) return result;
        const alias = journal.state && layoutAlias(journal.state, path);
        return alias ? originalDest.call(cache, alias, source) : null;
    };
    cache.getFirstLinkpathDest = aliasDest;
    plugin.register(() => { if (cache.getFirstLinkpathDest === aliasDest) cache.getFirstLinkpathDest = originalDest; });
    client.onPublish = id => { const result = publisher.pump(id); loop.wake(); return result; };
    client.stopNative = async () => {
        client.stopped = true;
        cancelFeed?.();
        await loop.stop();
        await client.settled();
        await publisher.settled();
        await journal.checkpoint();
    };
    client.resumeNative = async (recover = false) => {
        if (client.stopped) return;
        recoveryRequested ||= recover;
        retryPaths();
        loop.wake();
        await loop.flush();
    };
    return client;
}

export async function startLoomClient(plugin: ObsidianLiveSyncPlugin) {
    const client = plugin.loomClient;
    const status = plugin.addStatusBarItem();
    status.setAttribute("aria-label", "Review LOOM Notes status");
    status.setAttribute("title", "Review LOOM Notes status");
    plugin.registerDomEvent(status, "click", () => new NotesStatusModal(plugin, client).open());
    let shown: string | undefined;
    let scheduled = false;
    client.io.changed = () => {
        if (scheduled) return;
        scheduled = true;
        queueMicrotask(() => {
            scheduled = false;
            const text = client.summary().text;
            if (text !== shown) { shown = text; status.setText(text); }
        });
    };
    plugin.addCommand({
        id: "loom-conflicts",
        name: "LOOM: review and resolve note conflicts",
        callback: () => new NotesConflictsModal(plugin, client).open(),
    });
    plugin.addCommand({
        id: "loom-intent-status",
        name: "LOOM: show source acknowledgement and holds",
        callback: () => {
            new NotesStatusModal(plugin, client).open();
        },
    });
    plugin.addCommand({
        id: "loom-register-local-note",
        name: "LOOM: register local note",
        callback: () => {
            const path = plugin.app.workspace.getActiveFile()?.path;
            if (!path) return void new Notice("Open the local note to register first.");
            void client.registerLocalNote(path).then(() => {
                new Notice("Local note saved for source acceptance.");
            }).catch(error => new Notice("LOOM registration: " + String(error)));
        },
    });
    plugin.addCommand({
        id: "loom-intent-resume",
        name: "LOOM: retry pending receipt delivery",
        callback: () => {
            void client.resumeNative(true);
        },
    });
    client.io.changed();
    await client.resumeNative(false);
    plugin.loomClientUpdate.markReady();
}
