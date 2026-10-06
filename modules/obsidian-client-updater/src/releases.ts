export const FILES = ["main.js", "manifest.json", "styles.css", "loom-client-build.json", "loom-release.json"] as const;
export type FileName = typeof FILES[number];
export const TARGET = "obsidian-livesync";
export const REPOSITORY = "Liaust/LOOM";
export const MAX_FILE = 16 * 1024 * 1024;
export const MAX_TOTAL = 32 * 1024 * 1024;
export const MAX_MANIFEST = 65536;
export const COMPATIBILITY = { minAppVersion: "1.7.2", platforms: ["desktop", "mobile"], notesProtocol: 1,
    handoffAPI: 1, journalSchema: 1, activation: "reload", rollback: "same-schema" };
export interface Identity {
    schema: "loom.client-bundle.v1"; component: "notes-workspace"; adapter: "obsidian-plugin";
    pluginId: typeof TARGET; releaseId: string; sequence: number; version: string; channel: "stable";
    sourceCommit: string; upstream: { revision: string; version: string }; compatibility: typeof COMPATIBILITY;
}
export interface Release extends Omit<Identity, "schema"> {
    schema: "loom.client-release.v1"; files: { name: FileName; bytes: number; sha256: string }[];
}
export interface Bundle { release: Release; files: Record<FileName, Uint8Array> }
export function fail(code: string): never { throw new Error(code); }
export function object(value: unknown): Record<string, unknown> {
    if (!value || typeof value !== "object" || Array.isArray(value)) return fail("invalid_object");
    return value as Record<string, unknown>;
}
function keys(value: Record<string, unknown>, expected: string[]) {
    if (Object.keys(value).sort().join("|") !== [...expected].sort().join("|")) fail("unexpected_fields");
}
export function canonical(value: unknown): string {
    if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
    if (value && typeof value === "object") return "{" + Object.entries(value).sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => JSON.stringify(k) + ":" + canonical(v)).join(",") + "}";
    return JSON.stringify(value);
}
export function identity(release: Release): Identity {
    const { files: _, ...rest } = release;
    return { ...rest, schema: "loom.client-bundle.v1" };
}
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
export function supportsApp(actual: string, minimum: string): boolean {
    const a = actual.match(/^(\d+)\.(\d+)\.(\d+)/), b = minimum.match(semver);
    if (!a || !b) return false;
    for (let i = 1; i <= 3; i++) { if (+a[i] !== +b[i]) return +a[i] > +b[i]; }
    return true;
}
export function parseRelease(value: unknown, appVersion: string): Release {
    const r = object(value);
    keys(r, ["schema", "component", "adapter", "pluginId", "releaseId", "sequence", "version", "channel",
        "sourceCommit", "upstream", "compatibility", "files"]);
    if (r.schema !== "loom.client-release.v1" || r.component !== "notes-workspace" || r.adapter !== "obsidian-plugin" ||
        r.pluginId !== TARGET || r.channel !== "stable" || typeof r.version !== "string" || !semver.test(r.version) ||
        r.releaseId !== "client-notes-" + r.version || !Number.isSafeInteger(r.sequence) || Number(r.sequence) < 1 ||
        typeof r.sourceCommit !== "string" || !/^[a-f0-9]{40}$/.test(r.sourceCommit)) fail("release_identity_invalid");
    if (canonical(r.upstream) !== canonical({ revision: "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", version: "1.0.32" }) ||
        canonical(r.compatibility) !== canonical(COMPATIBILITY) || !supportsApp(appVersion, COMPATIBILITY.minAppVersion))
        fail("release_incompatible");
    if (!Array.isArray(r.files) || r.files.length !== FILES.length) fail("release_files_invalid");
    const seen = new Set<string>(); let total = 0;
    for (const file of r.files) {
        const f = object(file); keys(f, ["name", "bytes", "sha256"]);
        if (typeof f.name !== "string" || !FILES.includes(f.name as FileName) || seen.has(f.name) ||
            !Number.isSafeInteger(f.bytes) || Number(f.bytes) < 1 || Number(f.bytes) > MAX_FILE ||
            (f.name.endsWith(".json") && Number(f.bytes) > MAX_MANIFEST) ||
            typeof f.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(f.sha256)) fail("release_files_invalid");
        total += Number(f.bytes); seen.add(f.name);
    }
    if (total > MAX_TOTAL) fail("release_too_large");
    return r as unknown as Release;
}
export async function hash(bytes: Uint8Array): Promise<string> {
    const result = await crypto.subtle.digest("SHA-256", new Uint8Array(bytes).buffer);
    return [...new Uint8Array(result)].map(v => v.toString(16).padStart(2, "0")).join("");
}
export const encode = (value: unknown) => new TextEncoder().encode(JSON.stringify(value, null, 2) + "\n");
export function json(bytes: Uint8Array, limit = MAX_MANIFEST): unknown {
    if (bytes.byteLength > limit) return fail("metadata_too_large");
    try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); }
    catch { return fail("metadata_invalid"); }
}
export async function verify(bundle: Bundle, appVersion: string): Promise<void> {
    const r = parseRelease(bundle.release, appVersion);
    if (Object.keys(bundle.files).sort().join("|") !== [...FILES].sort().join("|")) fail("bundle_files_invalid");
    for (const f of r.files) {
        const bytes = bundle.files[f.name];
        if (!(bytes instanceof Uint8Array) || bytes.byteLength !== f.bytes || await hash(bytes) !== f.sha256)
            fail("bundle_hash_mismatch");
    }
    const embedded = json(bundle.files["loom-release.json"]);
    if (canonical(embedded) !== canonical(identity(r))) fail("bundle_identity_mismatch");
    const manifest = object(json(bundle.files["manifest.json"]));
    if (manifest.id !== TARGET || manifest.version !== "1.0.32" || manifest.minAppVersion !== "1.7.2" ||
        manifest.isDesktopOnly !== false) fail("native_manifest_mismatch");
    const receipt = object(json(bundle.files["loom-client-build.json"]));
    keys(receipt, ["upstream", "patchManifestSha256", "node", "files", "release", "installed"]);
    const expected = Object.fromEntries(r.files.filter(f => f.name !== "loom-client-build.json").map(f => [f.name, f.sha256]));
    if (receipt.installed !== false || receipt.upstream !== r.upstream.revision ||
        typeof receipt.patchManifestSha256 !== "string" || !/^[a-f0-9]{64}$/.test(receipt.patchManifestSha256) ||
        typeof receipt.node !== "string" || !/^v\d+\.\d+\.\d+$/.test(receipt.node) ||
        canonical(receipt.release) !== canonical(embedded) || canonical(receipt.files) !== canonical(expected))
        fail("build_receipt_mismatch");
}
export interface Response { status: number; bytes: Uint8Array; etag?: string }
export type Fetch = (url: string, etag?: string) => Promise<Response>;
export interface PageCache { etag?: string; body: unknown }
interface RemoteRelease { tag_name: string; assets: { name: string; browser_download_url: string; size: number }[] }
function asset(release: RemoteRelease, name: string): string {
    const matches = release.assets.filter(a => a.name === name);
    const exact = `https://github.com/${REPOSITORY}/releases/download/${release.tag_name}/${name}`;
    if (matches.length !== 1 || matches[0].browser_download_url !== exact ||
        !Number.isSafeInteger(matches[0].size) || matches[0].size < 1 || matches[0].size > (name.endsWith(".json") ? MAX_MANIFEST : MAX_FILE))
        return fail("release_asset_invalid");
    return exact;
}
export class ReleaseFeed {
    constructor(private readonly fetch: Fetch, private readonly appVersion: string, public cache: PageCache[] = []) {}
    async latest(): Promise<{ release: Release; remote: RemoteRelease } | null> {
        const candidates: { release: Release; remote: RemoteRelease }[] = [];
        for (let page = 1; page <= 3; page++) {
            const response = await this.fetch(`https://api.github.com/repos/${REPOSITORY}/releases?per_page=50&page=${page}`, this.cache[page - 1]?.etag);
            const body = response.status === 304 ? this.cache[page - 1]?.body :
                response.status === 200 ? json(response.bytes, 1024 * 1024) : fail("release_feed_unavailable");
            if (!Array.isArray(body) || body.length > 50) fail("release_feed_invalid");
            this.cache[page - 1] = { etag: response.status === 304 ? response.etag ?? this.cache[page - 1]?.etag : response.etag, body };
            for (const value of body) {
                const r = object(value);
                if (r.draft !== false || r.prerelease !== false || typeof r.tag_name !== "string" ||
                    !/^client-notes-(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(r.tag_name)) continue;
                if (!Array.isArray(r.assets) || r.assets.length !== FILES.length + 2) fail("release_assets_invalid");
                const remote = r as unknown as RemoteRelease;
                const names = remote.assets.map(a => a.name).sort();
                if (names.join("|") !== [...FILES, "release.json", "LICENSE.upstream"].sort().join("|")) fail("release_assets_invalid");
                const data = await this.fetch(asset(remote, "release.json"));
                if (data.status !== 200) fail("release_manifest_unavailable");
                const release = parseRelease(json(data.bytes), this.appVersion);
                if (release.releaseId !== remote.tag_name) fail("release_tag_mismatch");
                candidates.push({ release, remote });
                if (candidates.length === 10) break;
            }
            if (body.length < 50 || candidates.length === 10) break;
        }
        candidates.sort((a, b) => b.release.sequence - a.release.sequence);
        if (candidates.length > 1 && candidates[0].release.sequence === candidates[1].release.sequence &&
            canonical(candidates[0].release) !== canonical(candidates[1].release)) fail("release_sequence_conflict");
        return candidates[0] ?? null;
    }
    async download(candidate: { release: Release; remote: RemoteRelease }): Promise<Bundle> {
        const files = {} as Record<FileName, Uint8Array>;
        for (const file of candidate.release.files) {
            const response = await this.fetch(asset(candidate.remote, file.name));
            if (response.status !== 200 || response.bytes.byteLength !== file.bytes) fail("release_download_failed");
            files[file.name] = response.bytes;
        }
        const bundle = { release: candidate.release, files }; await verify(bundle, this.appVersion); return bundle;
    }
}

// No timer: attempts (including failures) consume the cooldown until a real
// foreground/open event or explicit command invokes the gate again.
export class EventGate {
    private running?: Promise<void>;
    constructor(public lastCheck: number, private readonly now: () => number = Date.now) {}
    run(manual: boolean, action: () => Promise<void>): Promise<void> {
        if (this.running) return this.running;
        const now = this.now();
        if (!manual && now - this.lastCheck < 6 * 60 * 60 * 1000) return Promise.resolve();
        this.lastCheck = now;
        return this.running = action().finally(() => { this.running = undefined; });
    }
}
