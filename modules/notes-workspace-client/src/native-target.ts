import { digest, revision } from "./protocol";

export type NativeTarget = { kind: "absent" | "deleted" | "occupied" | "unknown"; token: string };
export type NativeLeaf = { revision: string; path?: string; type?: string; deleted?: boolean; branchDeleted?: boolean };

export async function classifyNativeTarget(path: string, leaves: NativeLeaf[]): Promise<NativeTarget> {
    const evidence = [...leaves].sort((a, b) => a.revision.localeCompare(b.revision));
    const token = await digest(JSON.stringify([path, evidence]));
    if (!evidence.length) return { kind: "absent", token };
    if (evidence.length > 32 || evidence.some(leaf => !revision(leaf.revision) || leaf.branchDeleted ||
        leaf.path !== path || !["plain", "newnote"].includes(leaf.type ?? ""))) return { kind: "unknown", token };
    return { kind: evidence.every(leaf => leaf.deleted === true) ? "deleted" : "occupied", token };
}

export type CreateRecoveryReview = { intentId: string; path: string; descendants: number; token: string };
