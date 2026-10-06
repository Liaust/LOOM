export type Preparation = { state: "ready" | "restart-required"; reason?: string };
export interface UpdateDependencies {
    flushAndFenceEditor(): Promise<boolean>;
    stopLocal(): Promise<void>;
    stopNative(): Promise<void>;
}

// Native unload is irreversible. Preparation may defer before stopping anything;
// after a failed/finished shutdown, recovery reloads code, never a closed DB.
export class UpdateBarrier {
    private shutdownPromise?: Promise<void>;
    private preparation?: Promise<Preparation>;
    stopped = false;
    constructor(private readonly dependencies: UpdateDependencies) {}
    prepare(): Promise<Preparation> {
        if (this.stopped) return Promise.resolve({ state: "ready" });
        if (this.preparation) return this.preparation;
        this.preparation = (async () => {
            if (!await this.dependencies.flushAndFenceEditor())
                return { state: "restart-required", reason: "active_editor_not_fenced" } as Preparation;
            await this.shutdown();
            return { state: "ready" } as Preparation;
        })().finally(() => { this.preparation = undefined; });
        return this.preparation;
    }
    shutdown(): Promise<void> {
        return this.shutdownPromise ??= (async () => {
            await this.dependencies.stopLocal();
            await this.dependencies.stopNative();
            this.stopped = true;
        })();
    }
    resume(): Preparation { return { state: "restart-required", reason: "reload_required_after_shutdown" }; }
}

function canonical(value: unknown): string {
    if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
    if (value && typeof value === "object") return "{" + Object.entries(value).sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => JSON.stringify(k) + ":" + canonical(v)).join(",") + "}";
    return JSON.stringify(value);
}
function validRelease(value: unknown): value is { releaseId: string } {
    if (!value || typeof value !== "object" || Array.isArray(value)) return false;
    const r = value as Record<string, unknown>;
    const fields = ["schema", "component", "adapter", "pluginId", "releaseId", "sequence", "version", "channel",
        "sourceCommit", "upstream", "compatibility", "files"];
    if (Object.keys(r).sort().join("|") !== fields.sort().join("|") || r.schema !== "loom.client-release.v1" ||
        r.component !== "notes-workspace" || r.adapter !== "obsidian-plugin" || r.pluginId !== "obsidian-livesync" ||
        r.channel !== "stable" || typeof r.version !== "string" || !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(r.version) ||
        r.releaseId !== "client-notes-" + r.version || !Number.isSafeInteger(r.sequence) || Number(r.sequence) < 1 ||
        typeof r.sourceCommit !== "string" || !/^[a-f0-9]{40}$/.test(r.sourceCommit) ||
        canonical(r.upstream) !== canonical({ revision: "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", version: "1.0.32" }) ||
        canonical(r.compatibility) !== canonical({ minAppVersion: "1.7.2", platforms: ["desktop", "mobile"], notesProtocol: 1,
            handoffAPI: 1, journalSchema: 1, activation: "reload", rollback: "same-schema" }) ||
        !Array.isArray(r.files) || r.files.length !== 5) return false;
    const seen = new Set<string>(); let total = 0;
    for (const file of r.files) {
        if (!file || typeof file !== "object" || Array.isArray(file)) return false;
        const f = file as Record<string, unknown>;
        if (Object.keys(f).sort().join("|") !== "bytes|name|sha256" || typeof f.name !== "string" ||
            !["main.js", "manifest.json", "styles.css", "loom-client-build.json", "loom-release.json"].includes(f.name) ||
            seen.has(f.name) || !Number.isSafeInteger(f.bytes) || Number(f.bytes) < 1 || Number(f.bytes) > 16 * 1024 * 1024 ||
            (f.name.endsWith(".json") && Number(f.bytes) > 65536) ||
            typeof f.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(f.sha256)) return false;
        seen.add(f.name); total += Number(f.bytes);
    }
    return total <= 32 * 1024 * 1024;
}
export function startupHeld(marker: unknown, releaseId: string): boolean {
    if (!marker || typeof marker !== "object") return true;
    const value = marker as Record<string, unknown>;
    if (Object.keys(value).some(k => !["schema", "phase", "previous", "next", "loaded", "error"].includes(k)) ||
        value.schema !== "loom.client-install.v1" || !validRelease(value.previous) || !validRelease(value.next) ||
        (value.loaded !== undefined && typeof value.loaded !== "string") ||
        (value.error !== undefined && (typeof value.error !== "string" || !/^[a-z_]{1,80}$/.test(value.error)))) return true;
    if (value.phase === "activated" && value.loaded !== value.next.releaseId) return true;
    if (value.phase === "activated" || value.phase === "installed") return value.next.releaseId !== releaseId;
    if (value.phase === "rolled-back") return value.previous.releaseId !== releaseId;
    return true;
}
