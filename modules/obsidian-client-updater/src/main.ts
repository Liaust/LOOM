import { Modal, Notice, Platform, Plugin, PluginSettingTab, Setting, apiVersion, requestUrl, type App } from "obsidian";
import { Installer, type Handoff, type Host, type Store } from "./install";
import { EventGate, MAX_FILE, MAX_MANIFEST, ReleaseFeed, TARGET, canonical, fail, type PageCache } from "./releases";

interface Manager {
    getPlugin(id: string): { loomClientUpdate?: Handoff } | undefined;
    disablePlugin(id: string): Promise<void>;
    enablePlugin(id: string): Promise<boolean | void>;
    loadManifests(): Promise<void>;
}
interface Settings {
    autoUpdate: boolean; lastCheck: number; cache: PageCache[];
    lastState: string; lastError: string | null; lastNotice: string;
}
class Status extends Modal {
    constructor(app: App, private readonly lines: string[]) { super(app); }
    onOpen() {
        this.contentEl.createEl("h3", { text: "LOOM client updates" });
        for (const text of this.lines) this.contentEl.createEl("p", { text });
    }
    onClose() { this.contentEl.empty(); }
}
export default class ClientUpdater extends Plugin {
    settings: Settings = { autoUpdate: false, lastCheck: 0, cache: [], lastState: "bootstrap-required", lastError: null, lastNotice: "" };
    private installer!: Installer;
    private gate!: EventGate;
    private feed!: ReleaseFeed;
    private running?: Promise<void>;
    private initialized = false;
    private stopped = false;
    private manager(): Partial<Manager> { return (this.app as App & { plugins?: Partial<Manager> }).plugins ?? {}; }
    private current(): Handoff | undefined { return this.manager().getPlugin?.(TARGET)?.loomClientUpdate; }
    async onload() {
        const saved = await this.loadData();
        if (saved && typeof saved === "object") {
            this.settings.autoUpdate = saved.autoUpdate === true;
            this.settings.lastCheck = Number.isSafeInteger(saved.lastCheck) && saved.lastCheck >= 0 ? saved.lastCheck : 0;
            this.settings.cache = Array.isArray(saved.cache) && saved.cache.length <= 3 ? saved.cache : [];
            this.settings.lastState = typeof saved.lastState === "string" ? saved.lastState.slice(0, 100) : "bootstrap-required";
            this.settings.lastError = typeof saved.lastError === "string" ? saved.lastError.slice(0, 100) : null;
            this.settings.lastNotice = typeof saved.lastNotice === "string" ? saved.lastNotice.slice(0, 100) : "";
        }
        const adapter = this.app.vault.adapter;
        const store: Store = {
            exists: path => adapter.exists(path),
            read: async path => {
                const stat = await adapter.stat(path);
                if (!stat || stat.type !== "file" || stat.size < 1 || stat.size > (path.endsWith(".json") ? MAX_MANIFEST : MAX_FILE))
                    return fail("local_bundle_file_invalid");
                return new Uint8Array(await adapter.readBinary(path));
            },
            write: (path, bytes) => adapter.writeBinary(path, new Uint8Array(bytes).buffer),
            mkdir: path => adapter.mkdir(path),
            remove: path => adapter.remove(path),
        };
        const host: Host = {
            current: () => this.current(),
            canReload: () => ["disablePlugin", "enablePlugin", "loadManifests", "getPlugin"].every(k => typeof this.manager()[k as keyof Manager] === "function"),
            reload: async () => {
                if (this.stopped) fail("updater_closed");
                const manager = this.manager() as Manager;
                await manager.disablePlugin(TARGET); await manager.loadManifests();
                if (await manager.enablePlugin(TARGET) === false) fail("target_enable_failed");
            },
        };
        this.installer = new Installer(store, host, apiVersion, this.app.vault.configDir);
        this.gate = new EventGate(this.settings.lastCheck);
        this.feed = new ReleaseFeed(async (url, etag) => {
            if (this.stopped) fail("updater_closed");
            const response = await requestUrl({ url, throw: false, headers: {
                Accept: url.startsWith("https://api.github.com/") ? "application/vnd.github+json" : "application/octet-stream",
                ...(etag ? { "If-None-Match": etag } : {}),
            } });
            return { status: response.status, bytes: new Uint8Array(response.arrayBuffer),
                etag: response.headers.etag ?? response.headers.ETag };
        }, apiVersion, this.settings.cache);
        this.addCommand({ id: "check", name: "Check for Notes client updates", callback: () => { void this.check(true); } });
        this.addCommand({ id: "install", name: "Install staged Notes client update", callback: () => { void this.action(async () => {
            await this.activate();
        }); } });
        this.addCommand({ id: "status", name: "Show Notes client update status", callback: () => { void this.action(async () => {
            const installed = await this.installer.installed(), staged = await this.installer.staged();
            const tx = await this.installer.transaction();
            new Status(this.app, [`Device: ${Platform.isMobile ? "mobile" : "desktop"}`,
                `Installed: ${installed.releaseId}`, `Loaded: ${this.current()?.identity.releaseId ?? "not observed"}`,
                `Initialized: ${this.current()?.ready === true ? "yes" : "no"}`,
                `Staged: ${staged?.releaseId ?? "none"}`, `State: ${tx?.phase ?? this.settings.lastState}`,
                `Automatic updates: ${this.settings.autoUpdate ? "on" : "off"}`,
                `Last error: ${tx?.error ?? this.settings.lastError ?? "none"}`]).open();
        }); } });
        this.addCommand({ id: "rollback", name: "Restore previous Notes client code (pause automatic updates)", callback: () => { void this.action(async () => {
            this.settings.autoUpdate = false; await this.saveData(this.settings);
            const result = await this.installer.rollback();
            await this.state(result); await this.notice("rollback-" + result, result === "activated" ?
                "Previous LOOM client loaded. Automatic updates are paused." : result === "initializing" ?
                "Previous LOOM client code is loaded; startup verification is pending. Automatic updates are paused." :
                "Previous LOOM client staged or installed; restart Obsidian. Automatic updates are paused.");
        }); } });
        this.addSettingTab(new UpdaterSettings(this.app, this));
        this.app.workspace.onLayoutReady(() => { void this.action(async () => {
            const result = await this.installer.recover();
            this.initialized = true; await this.state(result);
            if (result === "restart-required") await this.notice("restart-required", "Restart Obsidian to finish the LOOM client update.");
            else if (result !== "initializing") await this.checkBody(false);
        }); });
        this.registerDomEvent(document, "visibilitychange", () => { if (!document.hidden && this.initialized) void this.check(false); });
        this.registerDomEvent(window, "focus", () => { if (this.initialized) void this.check(false); });
    }
    onunload() { this.stopped = true; }
    private async state(value: string) {
        this.settings.lastState = value; this.settings.lastError = null; await this.saveData(this.settings);
    }
    private async notice(key: string, message: string) {
        if (this.settings.lastNotice === key) return;
        this.settings.lastNotice = key; await this.saveData(this.settings); new Notice(message, 8000);
    }
    private action(run: () => Promise<void>): Promise<void> {
        if (this.running) return this.running;
        return this.running = run().catch(async error => {
            this.settings.lastError = error instanceof Error && /^[a-z_]{1,80}$/.test(error.message) ? error.message : "update_request_failed";
            await this.saveData(this.settings);
            await this.notice("error-" + this.settings.lastError, `LOOM client update: ${this.settings.lastError}. Existing code and pending edits are retained.`);
        }).finally(() => { this.running = undefined; });
    }
    check(manual: boolean): Promise<void> { return this.action(() => this.checkBody(manual)); }
    private checkBody(manual: boolean): Promise<void> {
        return this.recoverThenCheck(manual);
    }
    private async recoverThenCheck(manual: boolean): Promise<void> {
        const tx = await this.installer.transaction();
        if (tx && tx.phase !== "activated" && tx.phase !== "rolled-back") {
            const result = await this.installer.recover(); await this.state(result);
            if (result === "restart-required" || result === "initializing") return;
            if (result === "activated") await this.notice("activated-" + tx.next.releaseId, "LOOM Notes client update is now initialized.");
        }
        return this.gate.run(manual, async () => {
            this.settings.lastCheck = this.gate.lastCheck; await this.saveData(this.settings);
            const installed = await this.installer.installed();
            try {
                const candidate = await this.feed.latest();
                if (!candidate || candidate.release.sequence <= installed.sequence) {
                    if (candidate?.release.sequence === installed.sequence && canonical(candidate.release) !== canonical(installed))
                        fail("release_sequence_conflict");
                    if (manual) new Notice("No newer compatible LOOM Notes client release.");
                    return;
                }
                const staged = await this.installer.staged();
                if (staged?.releaseId === candidate.release.releaseId && canonical(staged) !== canonical(candidate.release))
                    fail("release_identity_conflict");
                if (staged?.releaseId !== candidate.release.releaseId) {
                    const bundle = await this.feed.download(candidate);
                    if (this.stopped) fail("updater_closed");
                    await this.installer.stage(bundle);
                }
                await this.state("staged");
                if (this.settings.autoUpdate) await this.activate();
                else await this.notice("staged-" + candidate.release.releaseId, "A compatible LOOM Notes client update is staged. Use Install staged Notes client update to activate it.");
            } finally {
                this.settings.cache = this.feed.cache; await this.saveData(this.settings);
            }
        });
    }
    private async activate() {
        if (this.stopped) fail("updater_closed");
        const result = await this.installer.install(); await this.state(result);
        await this.notice(result + "-" + (await this.installer.staged())?.releaseId, result === "activated" ?
            "LOOM Notes client updated. Settings and pending edits were preserved." :
            result === "initializing" ? "New LOOM client code is loaded; startup verification is still pending." :
                "LOOM Notes client update is ready. Restart Obsidian to finish safely.");
    }
}
class UpdaterSettings extends PluginSettingTab {
    constructor(app: App, private readonly updater: ClientUpdater) { super(app, updater); }
    display() {
        this.containerEl.empty();
        new Setting(this.containerEl).setName("Automatic compatible Notes client updates")
            .setDesc("Checks on open/foreground only. Code from Liaust/LOOM client-notes releases; settings and pending edits stay local.")
            .addToggle(toggle => toggle.setValue(this.updater.settings.autoUpdate).onChange(async value => {
                this.updater.settings.autoUpdate = value; await this.updater.saveData(this.updater.settings);
            }));
    }
}
