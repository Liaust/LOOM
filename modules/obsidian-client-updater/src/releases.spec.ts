import { describe, expect, it } from "vitest";
import { COMPATIBILITY, EventGate, FILES, ReleaseFeed, encode, hash, identity, parseRelease, verify, type Bundle, type Release } from "./releases";

async function fixture(): Promise<Bundle> {
    const release: Release = { schema: "loom.client-release.v1", component: "notes-workspace", adapter: "obsidian-plugin",
        pluginId: "obsidian-livesync", releaseId: "client-notes-0.1.0", sequence: 1, version: "0.1.0", channel: "stable",
        sourceCommit: "a".repeat(40), upstream: { revision: "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", version: "1.0.32" },
        compatibility: COMPATIBILITY, files: [] };
    const files: Bundle["files"] = { "main.js": encode("code"), "styles.css": encode("style"), "manifest.json": encode({
        id: "obsidian-livesync", version: "1.0.32", minAppVersion: "1.7.2", isDesktopOnly: false }),
        "loom-release.json": encode(identity(release)), "loom-client-build.json": encode({}) };
    const hashes = Object.fromEntries(await Promise.all(FILES.filter(n => n !== "loom-client-build.json").map(async n => [n, await hash(files[n])])));
    files["loom-client-build.json"] = encode({ installed: false, upstream: release.upstream.revision, release: identity(release), files: hashes,
        patchManifestSha256: "b".repeat(64), node: "v26.7.0" });
    release.files = await Promise.all(FILES.map(async name => ({ name, bytes: files[name].length, sha256: await hash(files[name]) })));
    return { release, files };
}
describe("closed release contract", () => {
    it("verifies exact code, embedded identity and receipt on desktop/mobile", async () => {
        const bundle = await fixture(); await verify(bundle, "1.7.2"); await verify(bundle, "1.13.0");
        bundle.files["main.js"][0] ^= 1;
        await expect(verify(bundle, "1.7.2")).rejects.toThrow("bundle_hash_mismatch");
    });
    it("rejects paths, duplicates, changed schemas and unsupported applications", async () => {
        const bundle = await fixture();
        for (const mutate of [
            (r: any) => r.files[0].name = "../data.json",
            (r: any) => r.files[0].name = r.files[1].name,
            (r: any) => r.compatibility.journalSchema = 2,
            (r: any) => r.pluginId = "some-other-plugin",
            (r: any) => r.releaseId = "v0.9.0",
            (r: any) => r.files[0].bytes = 17 * 1024 * 1024,
        ]) {
            const copy = structuredClone(bundle.release); mutate(copy);
            expect(() => parseRelease(copy, "1.13.0")).toThrow();
        }
        expect(() => parseRelease(bundle.release, "1.6.9")).toThrow("release_incompatible");
    });
    it("matches only component releases, fixed assets and cached conditional pages", async () => {
        const bundle = await fixture(), calls: string[] = [];
        const url = (name: string) => `https://github.com/Liaust/LOOM/releases/download/${bundle.release.releaseId}/${name}`;
        const assets = [...FILES, "release.json", "LICENSE.upstream"].map(name => ({ name, browser_download_url: url(name), size: 123 }));
        let indexRequests = 0;
        const feed = new ReleaseFeed(async (request, etag) => {
            calls.push(request);
            if (request.startsWith("https://api.github.com/")) {
                indexRequests++;
                if (indexRequests > 1) { expect(etag).toBe("page-1"); return { status: 304, bytes: new Uint8Array() }; }
                return { status: 200, etag: "page-1", bytes: encode([
                    { draft: false, prerelease: false, tag_name: "v0.9.0", assets: [] },
                    { draft: false, prerelease: false, tag_name: bundle.release.releaseId, assets },
                ]) };
            }
            return { status: 200, bytes: request.endsWith("/release.json") ? encode(bundle.release) : bundle.files[request.split("/").pop() as keyof Bundle["files"]] };
        }, "1.13.0");
        const found = await feed.latest(); expect(found?.release.releaseId).toBe(bundle.release.releaseId);
        await feed.download(found!); await feed.latest();
        expect(calls.some(c => c.includes("latest"))).toBe(false);
        found!.remote.assets[0].browser_download_url = "https://example.com/main.js";
        await expect(feed.download(found!)).rejects.toThrow("release_asset_invalid");
    });
    it("single-flights real events and retains failure cooldown without idle timers", async () => {
        let now = 30_000_000, calls = 0; const gate = new EventGate(0, () => now);
        let finish!: () => void;
        const action = async () => { calls++; await new Promise<void>(resolve => { finish = resolve; }); };
        const one = gate.run(false, action); expect(gate.run(true, action)).toBe(one);
        finish(); await one; await gate.run(false, action); expect(calls).toBe(1);
        await expect(gate.run(true, async () => { calls++; throw Error("offline"); })).rejects.toThrow("offline");
        now += 1000; await gate.run(false, action); expect(calls).toBe(2);
        now += 6 * 60 * 60 * 1000;
        const two = gate.run(false, action); finish(); await two; expect(calls).toBe(3);
    });
});
