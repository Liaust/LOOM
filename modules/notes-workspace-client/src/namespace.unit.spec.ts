import { describe, it, expect } from "vitest";
import { pinLocalVaultName } from "./namespace";

describe("local vault namespace", () => {
    it("keeps database and device-local keys stable while the visible folder changes", () => {
        let visible = "loom-notes-test";
        const api = { getSystemVaultName: () => visible };
        const vault = { vaultName: () => visible, getVaultName() { return this.vaultName() + "-suffix"; } };
        pinLocalVaultName(api, vault, { loomLocalVaultName: visible });
        visible = "LOOM NOTES";
        expect(visible).toBe("LOOM NOTES");
        expect(api.getSystemVaultName()).toBe("loom-notes-test");
        expect(vault.getVaultName()).toBe("loom-notes-test-suffix");
    });
    it("leaves existing installations unchanged without an explicit pin", () => {
        const api = { getSystemVaultName: () => "existing" };
        const vault = { vaultName: () => "existing" };
        const original = api.getSystemVaultName;
        pinLocalVaultName(api, vault, {});
        expect(api.getSystemVaultName).toBe(original);
        expect(vault.vaultName()).toBe("existing");
    });
    it("rejects invalid pins before changing either namespace", () => {
        for (const name of ["", " ", "x/y", "x\\y", "x\n", "x".repeat(201), null, 1]) {
            const api = { getSystemVaultName: () => "old" };
            const vault = { vaultName: () => "old" };
            expect(() => pinLocalVaultName(api, vault, { loomLocalVaultName: name })).toThrow("invalid_local_vault_namespace");
            expect(api.getSystemVaultName()).toBe("old");
            expect(vault.vaultName()).toBe("old");
        }
    });
});
