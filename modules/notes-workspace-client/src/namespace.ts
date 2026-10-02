// A visible folder rename must not select a fresh native database or key space.
export function pinLocalVaultName(
    api: { getSystemVaultName(): string },
    vault: { vaultName(): string },
    settings: unknown,
) {
    if (!settings || typeof settings !== "object" || !("loomLocalVaultName" in settings)) return;
    const name = settings.loomLocalVaultName;
    if (typeof name !== "string" || !name.trim() || name.length > 200 || /[\/\\\x00-\x1f\x7f]/.test(name)) {
        throw new Error("invalid_local_vault_namespace");
    }
    api.getSystemVaultName = () => name;
    vault.vaultName = () => name;
}
