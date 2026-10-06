import { describe, expect, it } from "vitest";
import { Installer, type Handoff, type Store, type Transaction } from "./install";
import { COMPATIBILITY, FILES, encode, hash, identity, json, type Bundle, type Release } from "./releases";

const root = ".obsidian/plugins/loom-client-updater", target = ".obsidian/plugins/obsidian-livesync";
async function bundle(sequence: number): Promise<Bundle> {
    const release: Release = { schema: "loom.client-release.v1", component: "notes-workspace", adapter: "obsidian-plugin",
        pluginId: "obsidian-livesync", releaseId: `client-notes-0.1.${sequence}`, sequence, version: `0.1.${sequence}`, channel: "stable",
        sourceCommit: "a".repeat(40), upstream: { revision: "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", version: "1.0.32" },
        compatibility: COMPATIBILITY, files: [] };
    const files: Bundle["files"] = { "main.js": encode("code" + sequence), "styles.css": encode("style"), "manifest.json": encode({
        id: "obsidian-livesync", version: "1.0.32", minAppVersion: "1.7.2", isDesktopOnly: false }),
        "loom-release.json": encode(identity(release)), "loom-client-build.json": encode({}) };
    const hashes = Object.fromEntries(await Promise.all(FILES.filter(n => n !== "loom-client-build.json").map(async n => [n, await hash(files[n])])));
    files["loom-client-build.json"] = encode({ installed: false, upstream: release.upstream.revision, release: identity(release), files: hashes,
        patchManifestSha256: "b".repeat(64), node: "v26.7.0" });
    release.files = await Promise.all(FILES.map(async name => ({ name, bytes: files[name].length, sha256: await hash(files[name]) })));
    return { release, files };
}
async function setup() {
    const old = await bundle(1), next = await bundle(2), files = new Map<string, Uint8Array>();
    const directories = new Set([root, target]); let rejectWrite: string | undefined;
    const store: Store = {
        exists: async p => files.has(p) || directories.has(p),
        read: async p => { const value = files.get(p); if (!value) throw Error("file_missing"); return value.slice(); },
        write: async (p, v) => { if (rejectWrite === p) { rejectWrite = undefined; throw Error("disk_write_failed"); } files.set(p, v.slice()); },
        mkdir: async p => { directories.add(p); }, remove: async p => { files.delete(p); },
    };
    for (const name of FILES) files.set(`${target}/${name}`, old.files[name].slice());
    files.set(`${root}/adopted.json`, encode(old.release));
    files.set(`${target}/data.json`, encode({ device: "iphone", password: "fixture-only" }));
    files.set(`${target}/loom-intent-backup-a.json`, encode({ pending: [{ path: "note.md", text: "offline edit" }] }));
    const preserved = ["data.json", "loom-intent-backup-a.json"].map(n => files.get(`${target}/${n}`)!.slice());
    let defer = false, reloadable = true, shutdowns = 0, prepares = 0, delayStartup = false;
    const create = (r: Release, held = false): Handoff => {
        let prepared = false;
        const entry: Handoff = { apiVersion: 1, identity: identity(r), ready: !held && !delayStartup, held,
            prepared: () => prepared,
            prepare: async () => { prepares++; if (!defer) { prepared = true; entry.ready = false; } return { state: defer ? "restart-required" : "ready" }; },
            shutdown: async () => { shutdowns++; prepared = true; entry.ready = false; },
        }; return entry;
    };
    let current: Handoff | undefined = create(old.release);
    const host = { current: () => current, canReload: () => reloadable, reload: async () => {
        const r = json(files.get(`${target}/loom-release.json`)!) as Handoff["identity"];
        current = create({ ...r, schema: "loom.client-release.v1", files: [] });
    } };
    const installer = new Installer(store, host, "1.13.0", ".obsidian", async () => {});
    const assertPreserved = () => {
        for (let i = 0; i < preserved.length; i++) expect(files.get(`${target}/${["data.json", "loom-intent-backup-a.json"][i]}`)).toEqual(preserved[i]);
    };
    return { old, next, files, store, installer, assertPreserved, host,
        failOnce: (path: string) => { rejectWrite = path; },
        defer: (value: boolean) => { defer = value; }, reloadable: (value: boolean) => { reloadable = value; },
        held: () => { current = create(old.release, true); defer = false; }, unloaded: () => { current = undefined; },
        slowStartup: () => { delayStartup = true; }, ready: () => { current!.ready = true; },
        counts: () => ({ shutdowns, prepares }),
    };
}
describe("mobile-adapter code transactions", () => {
    it("replaces only verified code and reports actually loaded identity", async () => {
        const f = await setup(); await f.installer.stage(f.next);
        expect(await f.installer.install()).toBe("activated");
        expect((await f.installer.installed()).releaseId).toBe(f.next.release.releaseId);
        expect((await f.installer.transaction())?.loaded).toBe(f.next.release.releaseId);
        expect(f.counts().prepares).toBe(1); f.assertPreserved();
    });
    it("corrupt downloads and unknown local edits never replace target code", async () => {
        const f = await setup(); f.next.files["main.js"][0] ^= 1;
        await expect(f.installer.stage(f.next)).rejects.toThrow("bundle_hash_mismatch");
        expect(f.files.get(`${target}/main.js`)).toEqual(f.old.files["main.js"]);
        f.next.files["main.js"][0] ^= 1; await f.installer.stage(f.next);
        f.files.set(`${target}/main.js`, encode("local change"));
        await expect(f.installer.install()).rejects.toThrow("bundle_hash_mismatch");
        expect(f.counts().prepares).toBe(0); f.assertPreserved();
        const starting = await setup(); await starting.installer.stage(starting.next);
        starting.host.current()!.ready = false;
        await expect(starting.installer.install()).rejects.toThrow("update_handoff_initializing");
        expect(await starting.installer.transaction()).toBeNull(); starting.assertPreserved();
    });
    it("defers with an open editor, then completes through the held startup", async () => {
        const f = await setup(); await f.installer.stage(f.next); f.defer(true);
        expect(await f.installer.install()).toBe("restart-required");
        expect((await f.installer.transaction())?.phase).toBe("deferred");
        expect(f.files.get(`${target}/main.js`)).toEqual(f.old.files["main.js"]);
        f.held(); expect(await f.installer.recover()).toBe("activated"); f.assertPreserved();
    });
    it("restores exact previous code after one mid-write failure", async () => {
        const f = await setup(); await f.installer.stage(f.next); f.failOnce(`${target}/loom-release.json`);
        await expect(f.installer.install()).rejects.toThrow("disk_write_failed");
        expect((await f.installer.transaction())?.phase).toBe("rolled-back");
        expect((await f.installer.installed()).releaseId).toBe(f.old.release.releaseId); f.assertPreserved();
    });
    it("recovers an interrupted multi-file install even when partial code cannot load", async () => {
        const f = await setup(); await f.installer.stage(f.next);
        await f.store.mkdir(`${root}/previous`);
        for (const name of FILES) await f.store.write(`${root}/previous/${name}`, f.old.files[name]);
        await f.store.write(`${root}/previous/release.json`, encode(f.old.release));
        const tx: Transaction = { schema: "loom.client-install.v1", phase: "writing", previous: f.old.release, next: f.next.release };
        await f.store.write(`${root}/install.json`, encode(tx));
        f.files.set(`${target}/main.js`, encode("partial code")); f.unloaded();
        expect(await f.installer.recover()).toBe("restored");
        expect((await f.installer.installed()).releaseId).toBe(f.old.release.releaseId); f.assertPreserved();
    });
    it("keeps installation distinct from activation when reload is unavailable", async () => {
        const f = await setup(); await f.installer.stage(f.next); f.reloadable(false);
        expect(await f.installer.install()).toBe("restart-required");
        expect((await f.installer.transaction())?.phase).toBe("installed");
        expect((await f.installer.transaction())?.loaded).toBeUndefined();
        await f.host.reload(); expect(await f.installer.recover()).toBe("activated"); f.assertPreserved();
    });
    it("supports explicit compatible rollback without touching pending work", async () => {
        const f = await setup(); await f.installer.stage(f.next); await f.installer.install();
        expect(await f.installer.rollback()).toBe("activated");
        expect((await f.installer.installed()).sequence).toBe(1); f.assertPreserved();
    });
    it("does not roll back or close a correctly loaded client just because startup is slow", async () => {
        const f = await setup(); await f.installer.stage(f.next); f.slowStartup();
        expect(await f.installer.install()).toBe("initializing");
        expect((await f.installer.transaction())?.phase).toBe("installed");
        expect(f.counts().shutdowns).toBe(0);
        f.ready(); expect(await f.installer.recover()).toBe("activated"); f.assertPreserved();
    });
    it("preserves malformed recovery markers and refuses unsafe config paths", async () => {
        const f = await setup(); const bad = encode({ schema: "foreign", phase: "writing" });
        f.files.set(`${root}/install.json`, bad);
        await expect(f.installer.recover()).rejects.toThrow("update_journal_invalid");
        expect(f.files.get(`${root}/install.json`)).toEqual(bad); f.assertPreserved();
        expect(() => new Installer(f.store, f.host, "1.13.0", "../other-vault")).toThrow("config_path_invalid");
    });
});
