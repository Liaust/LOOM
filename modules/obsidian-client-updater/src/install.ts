import { FILES, TARGET, canonical, encode, fail, identity, json, object, parseRelease, verify,
    type Bundle, type FileName, type Identity, type Release } from "./releases";

export interface Store {
    exists(path: string): Promise<boolean>;
    read(path: string): Promise<Uint8Array>;
    write(path: string, bytes: Uint8Array): Promise<void>;
    mkdir(path: string): Promise<void>;
    remove(path: string): Promise<void>;
}
export interface Handoff {
    apiVersion: number; identity: Identity; ready: boolean; held: boolean;
    prepared(): boolean;
    prepare(): Promise<{ state: "ready" | "restart-required"; reason?: string }>;
    shutdown(): Promise<void>;
}
export interface Host {
    current(): Handoff | undefined;
    canReload(): boolean;
    reload(): Promise<void>;
}
type Phase = "prepared" | "deferred" | "writing" | "installed" | "activated" | "restoring" | "rolled-back" | "failed";
export interface Transaction {
    schema: "loom.client-install.v1"; phase: Phase; previous: Release; next: Release;
    loaded?: string; error?: string;
}
const terminal = (phase: Phase) => phase === "activated" || phase === "rolled-back";
export type Activation = "activated" | "restart-required" | "initializing";
export class Installer {
    private busy = false;
    readonly root: string;
    readonly target: string;
    constructor(private readonly store: Store, private readonly host: Host, private readonly appVersion: string, configDir: string,
        private readonly activationWait: () => Promise<void> = () => new Promise(resolve => setTimeout(resolve, 100))) {
        if (!configDir || configDir.includes("\\") || configDir.split("/").some(p => !p || p === "." || p === "..")) fail("config_path_invalid");
        this.root = `${configDir}/plugins/loom-client-updater`;
        this.target = `${configDir}/plugins/${TARGET}`;
    }
    private async locked<T>(action: () => Promise<T>): Promise<T> {
        if (this.busy) fail("update_busy");
        this.busy = true; try { return await action(); } finally { this.busy = false; }
    }
    private async writeJSON(path: string, value: unknown) {
        const data = encode(value);
        await this.store.write(path, data);
        if (canonical(json(await this.store.read(path))) !== canonical(value)) fail("update_journal_write_failed");
    }
    async transaction(): Promise<Transaction | null> {
        const path = `${this.root}/install.json`;
        if (!await this.store.exists(path)) return null;
        const value = object(json(await this.store.read(path)));
        if (Object.keys(value).some(k => !["schema", "phase", "previous", "next", "loaded", "error"].includes(k)) ||
            value.schema !== "loom.client-install.v1" || !["prepared", "deferred", "writing", "installed", "activated", "restoring", "rolled-back", "failed"].includes(String(value.phase)) ||
            (value.loaded !== undefined && typeof value.loaded !== "string") ||
            (value.error !== undefined && (typeof value.error !== "string" || !/^[a-z_]{1,80}$/.test(value.error)))) fail("update_journal_invalid");
        parseRelease(value.previous, this.appVersion); parseRelease(value.next, this.appVersion);
        if (value.phase === "activated" && value.loaded !== (value.next as Release).releaseId) fail("update_journal_invalid");
        return value as unknown as Transaction;
    }
    private async mark(tx: Transaction, phase: Phase) {
        tx.phase = phase; await this.writeJSON(`${this.root}/install.json`, tx);
    }
    private async readSet(directory: string, release: Release): Promise<Bundle> {
        const files = {} as Record<FileName, Uint8Array>;
        for (const name of FILES) files[name] = await this.store.read(`${directory}/${name}`);
        const bundle = { release, files }; await verify(bundle, this.appVersion); return bundle;
    }
    private async storedSet(name: "staged" | "previous"): Promise<Bundle> {
        const directory = `${this.root}/${name}`;
        const release = parseRelease(json(await this.store.read(`${directory}/release.json`)), this.appVersion);
        return this.readSet(directory, release);
    }
    private async writeSet(name: "staged" | "previous", bundle: Bundle) {
        const directory = `${this.root}/${name}`;
        if (!await this.store.exists(directory)) await this.store.mkdir(directory);
        for (const file of FILES) await this.store.write(`${directory}/${file}`, bundle.files[file]);
        await this.writeJSON(`${directory}/release.json`, bundle.release);
        await this.readSet(directory, bundle.release);
    }
    async installed(): Promise<Release> {
        // Bootstrap installs this externally verified manifest; never adopt a
        // modified local bundle merely because it rewrote its own receipt.
        const release = parseRelease(json(await this.store.read(`${this.root}/adopted.json`)), this.appVersion);
        await this.readSet(this.target, release);
        return release;
    }
    async staged(): Promise<Release | null> {
        if (!await this.store.exists(`${this.root}/staged/release.json`)) return null;
        return (await this.storedSet("staged")).release;
    }
    async stage(bundle: Bundle): Promise<void> {
        return this.locked(async () => {
            await verify(bundle, this.appVersion);
            const tx = await this.transaction();
            if (tx && !terminal(tx.phase)) fail("update_recovery_required");
            await this.writeSet("staged", bundle);
        });
    }
    private handoff(expected: Release): Handoff {
        const current = this.host.current();
        if (!current || current.apiVersion !== 1 || canonical(current.identity) !== canonical(identity(expected)))
            return fail("update_handoff_unavailable");
        if (!current.ready && !current.held) return fail("update_handoff_initializing");
        return current;
    }
    private async replace(bundle: Bundle) {
        // Main goes last, but replacement is still journaled, not multi-file atomic.
        for (const name of [...FILES.filter(n => n !== "main.js"), "main.js" as const])
            await this.store.write(`${this.target}/${name}`, bundle.files[name]);
        await this.readSet(this.target, bundle.release);
    }
    private async activate(tx: Transaction): Promise<Activation> {
        await this.readSet(this.target, tx.next);
        await this.writeJSON(`${this.root}/adopted.json`, tx.next);
        if (!this.host.canReload()) return "restart-required";
        await this.host.reload();
        // Bounded only during activation. There is no idle polling timer.
        for (let i = 0; i < 100; i++) {
            const current = this.host.current();
            if (current?.apiVersion === 1 && canonical(current.identity) === canonical(identity(tx.next)) && current.ready) {
                tx.loaded = tx.next.releaseId; delete tx.error;
                await this.mark(tx, "activated"); return "activated";
            }
            await this.activationWait();
        }
        const current = this.host.current();
        if (current?.apiVersion === 1 && !current.held && canonical(current.identity) === canonical(identity(tx.next)))
            return "initializing";
        return fail("update_activation_unverified");
    }
    private async apply(tx: Transaction, bundle: Bundle): Promise<Activation> {
        try {
            await this.mark(tx, "writing");
            await this.replace(bundle);
            await this.mark(tx, "installed");
            return await this.activate(tx);
        } catch (error) {
            tx.error = error instanceof Error && /^[a-z_]{1,80}$/.test(error.message) ? error.message : "update_install_failed";
            // If new code started, first retain its new local work. If shutdown
            // fails, leave the marker held for recovery rather than overwrite it.
            try {
                const current = this.host.current();
                if (current && !current.prepared()) {
                    if (!current.ready && !current.held || (await current.prepare()).state !== "ready")
                        fail("update_recovery_required");
                }
                await current?.shutdown();
                await this.restore(tx);
            } catch {
                await this.mark(tx, "failed");
                fail("update_recovery_required");
            }
            throw error;
        }
    }
    private async restore(tx: Transaction) {
        const previous = await this.storedSet("previous");
        if (canonical(previous.release) !== canonical(tx.previous)) fail("previous_bundle_mismatch");
        await this.mark(tx, "restoring");
        await this.replace(previous);
        await this.writeJSON(`${this.root}/adopted.json`, previous.release);
        delete tx.loaded;
        await this.mark(tx, "rolled-back");
        // Keep the failed-update error visible. A restored file is not yet
        // proof of a loaded client; the next startup/reload supplies that.
        if (this.host.canReload()) await this.host.reload();
    }
    async install(explicitRollback = false): Promise<Activation> {
        return this.locked(async () => {
            const old = await this.transaction();
            if (old && !terminal(old.phase)) fail("update_recovery_required");
            const previous = await this.installed(), next = await this.storedSet("staged");
            if (!explicitRollback && next.release.sequence <= previous.sequence) fail("release_not_newer");
            const handoff = this.handoff(previous);
            await this.writeSet("previous", await this.readSet(this.target, previous));
            const tx: Transaction = { schema: "loom.client-install.v1", phase: "prepared", previous, next: next.release };
            await this.mark(tx, "prepared");
            const ready = await handoff.prepare();
            if (ready.state !== "ready") { await this.mark(tx, "deferred"); return "restart-required"; }
            return this.apply(tx, next);
        });
    }
    async recover(): Promise<Activation | "restored" | "unchanged"> {
        return this.locked(async () => {
            const tx = await this.transaction();
            if (!tx || terminal(tx.phase)) { await this.installed(); return "unchanged"; }
            const current = this.host.current();
            if (tx.phase === "installed") {
                await this.readSet(this.target, tx.next);
                if (current?.ready && canonical(current.identity) === canonical(identity(tx.next))) {
                    await this.writeJSON(`${this.root}/adopted.json`, tx.next);
                    tx.loaded = tx.next.releaseId; await this.mark(tx, "activated"); return "activated";
                }
                if (current && !current.held) return canonical(current.identity) === canonical(identity(tx.next)) ? "initializing" : "restart-required";
                await current?.shutdown(); return this.activate(tx);
            }
            if (!current) { await this.restore(tx); return "restored"; }
            if (!current.held || current.apiVersion !== 1) return "restart-required";
            if (tx.phase === "deferred") {
                await this.readSet(this.target, tx.previous);
                const next = await this.storedSet("staged");
                if (canonical(next.release) !== canonical(tx.next)) fail("staged_bundle_mismatch");
                if ((await this.handoff(tx.previous).prepare()).state !== "ready") return "restart-required";
                return this.apply(tx, next);
            }
            await current.shutdown(); await this.restore(tx); return "restored";
        });
    }
    async rollback(): Promise<Activation> {
        // The caller durably pauses auto-updates before reaching this method.
        const previous = await this.storedSet("previous");
        await this.stage(previous);
        return this.install(true);
    }
}
