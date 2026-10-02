import { Notice } from "../deps";
import { Publisher } from "./publisher";
import type { ServiceFileHandler } from "../serviceModules/FileHandler";
import type ObsidianLiveSyncPlugin from "../main";
import type { DatabaseFileAccess } from "@vrtmrz/livesync-commonlib/compat/interfaces/DatabaseFileAccess";
import type { LiveSyncLocalDB } from "@vrtmrz/livesync-commonlib/compat/pouchdb/LiveSyncLocalDB";
import type { FilePathWithPrefix } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { readContent } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import { ControlTransport } from "./control";
import { Journal, indexedDBPersistence, recoveryPersistence } from "./journal";
import { IntentClient } from "./client";
import { MAX_BYTES, MAX_REFERENCE_BYTES, referenceBytes, reserved, type Binding } from "./protocol";
import { convergeNative } from "./conflicts";
import { NotesConflictsModal } from "./conflict-ui";
import { PathWork, WakeLoop } from "./work";
export function createLoomClient(
    plugin: ObsidianLiveSyncPlugin,
    access: DatabaseFileAccess,
    local: () => LiveSyncLocalDB,
) {
    const appId = "appId" in plugin.app ? String(plugin.app.appId) : "";
    if (!appId) throw new Error("LOOM intent requires a stable vault appId");
    const work = new PathWork();
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
    });
    const publisher = new Publisher(client, access, {
        encrypted: () =>
            !!plugin.core.services.setting.currentSettings().encrypt &&
            !!plugin.core.services.setting.currentSettings().passphrase,
        locked: async (paths, run) =>
            (plugin.core.serviceModules.fileHandler as ServiceFileHandler).loomPublish(
                paths as FilePathWithPrefix[],
                run,
            ),
    });
    let cursor: string | number = 0;
    const retry = new Map<string, string>();
    let recoveryRequested = false;
    let watching = false;
    let cancelFeed: (() => void) | undefined;
    let feedFailed = false;
    const consume = (change: { id: string; doc?: { path?: unknown; _rev?: string } }) => {
        const doc = change.doc;
        const path = doc?.path === undefined ? undefined : String(doc.path);
        if (path && reserved(path) && !path.startsWith("LOOM-Control-v1/payload/")) {
            if (!retry.has(path) && retry.size >= 256) return false;
            retry.set(path, doc!._rev!);
        } else work.changed(change.id, path);
        return true;
    };
    const loop = new WakeLoop(async () => {
            await client.ready;
            if (client.stopped) return;
            const recovery = recoveryRequested;
            recoveryRequested = false;
            if (!watching) {
                const feed = local().localDatabase.changes({ since: "now", live: true, include_docs: true });
                watching = true;
                feedFailed = false;
                cancelFeed = () => feed.cancel();
                feed.on("change", change => {
                    if (change.doc && "path" in change.doc || work.interested(change.id)) loop.wake();
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
            const batch = await local().localDatabase.changes({ since: cursor, include_docs: true, limit: 128 });
            let overflow = false;
            for (const change of batch.results) {
                if (!consume(change)) {
                    overflow = true;
                    await client.hold("transport", "control_backlog_limit");
                    break;
                }
            }
            if (!overflow) cursor = batch.last_seq;
            if (!overflow && batch.results.length === 128) loop.wake();
            for (const [path, rev] of retry) {
                try {
                    await client.ingest(await transport.get(path, rev));
                    work.clear(path);
                    await journal.update(s => { delete s.holds[path]; }, s => !s.holds[path]);
                    retry.delete(path);
                } catch { await client.hold(path, "control_pending_or_invalid"); }
            }
            await publisher.drainAcks(recovery);
            let processed = 0;
            if (recovery) {
                for (const b of Object.values(journal.state.bindings)) {
                    if (client.stopped) return;
                    await client.mutation({ kind: "store", info: b.path as FilePathWithPrefix });
                    if (++processed % 32 === 0) await new Promise(resolve => setTimeout(resolve, 0));
                }
                for (const b of Object.values(journal.state.candidates)) work.add(b.path);
            }
            await publisher.pump();
            await publisher.drainAcks();
            const paths = work.take();
            for (let index = 0; index < paths.length; index++) {
                const path = paths[index];
                if (client.stopped) return;
                try { await client.reflectPath(path, recovery); }
                catch (error) {
                    for (const remaining of paths.slice(index)) work.add(remaining);
                    throw error;
                }
                if (++processed % 32 === 0) await new Promise(resolve => setTimeout(resolve, 0));
            }
            feedFailed = false;
            if (!overflow && !retry.size) await journal.update(s => { delete s.holds.transport; }, s => !s.holds.transport);
            client.io.changed();
        }, () => !watching || feedFailed || retry.size > 0 || work.pending || publisher.needsRetry,
        () => work.retry(), async () => {
            feedFailed = true;
            if (!client.stopped && !journal.failure) await client.hold("transport", "native_resume_pending");
        });
    client.onReflect = path => { work.add(path); loop.wake(); };
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
        work.retry();
        loop.wake();
        await loop.flush();
    };
    return client;
}

export async function startLoomClient(plugin: ObsidianLiveSyncPlugin) {
    const client = plugin.loomClient;
    const status = plugin.addStatusBarItem();
    status.setAttribute("aria-label", "Review LOOM note conflicts");
    status.setAttribute("title", "Review LOOM note conflicts");
    plugin.registerDomEvent(status, "click", () => new NotesConflictsModal(plugin, client).open());
    client.io.changed = () => {
        status.setText(client.details().split("\n")[0]);
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
            new Notice(client.details(), 15000);
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
    await client.resumeNative(true);
}
