export const VERSION = 1;
export const PREFIX = "LOOM-Control-v1/";
export const MAX_BYTES = 8 * 1024 * 1024;
export const MAX_CONTROL_BYTES = 1024 * 1024;
export const MAX_REFERENCE_BYTES = 2 * 1024 * 1024 * 1024;
export const MAX_INLINE_REFERENCE_BYTES = 64 * 1024 * 1024;
export function referenceBytes(content: string | ArrayBuffer): Uint8Array<ArrayBuffer> | null {
    // Native references can be text (Canvas/SVG/CSV) or binary. Preserve exact
    // UTF-8 bytes; the binding's hash remains the authority for either format.
    if (typeof content === "string" && content.length > MAX_REFERENCE_BYTES) return null;
    if (typeof content !== "string" && content.byteLength > MAX_REFERENCE_BYTES) return null;
    const bytes = typeof content === "string" ? new TextEncoder().encode(content) : new Uint8Array(content);
    return bytes.byteLength <= MAX_REFERENCE_BYTES ? bytes : null;
}
export type Binding = {
    version: 1;
    kind: "binding";
    id: string;
    workspace: string;
    collection: string;
    generation: string;
    fileId: string;
    collectionRoot: string;
    path: string;
    sourceBase: string;
    nativeRevision: string;
    sha256: string;
    writable: boolean;
    sourceSequence?: number;
};
export type Intent = {
    version: 1;
    kind: "intent";
    id: string;
    device: string;
    operation: "create" | "edit" | "delete" | "rename";
    workspace: string;
    collection: string;
    generation: string;
    fileId: string;
    base: Binding | null;
    predecessor?: string;
    resolves?: string[];
    path: string;
    target?: string;
    sha256: string;
    length: number;
};

export function newerSource(a: Binding, b: Binding): boolean {
    // The source sequence follows the stable file across an explicit server-side
    // enrollment transition. Generation alone is not an ordering relation.
    return (a.sourceSequence ?? 0) > (b.sourceSequence ?? 0) &&
        a.workspace === b.workspace && a.collection === b.collection &&
        a.collectionRoot === b.collectionRoot && a.fileId === b.fileId && a.path === b.path;
}
export type Publication = {
    version: 1;
    kind: "publication";
    id: string;
    intentId: string;
    intentDigest: string;
    revisions: { path: string; revision: string; role: "content" | "deletion" }[];
};
export type Ack = {
    version: 1;
    kind: "ack";
    id: string;
    intentId: string;
    intentDigest: string;
    publicationId: string;
    status: "applied" | "conflict" | "held";
    reason: string;
    binding?: Binding;
    resolves?: string[];
};
export type Control = Binding | Intent | Publication | Ack;
export const token = (v: unknown): v is string =>
    typeof v === "string" &&
    !["__proto__", "constructor", "prototype"].includes(v) &&
    /^[A-Za-z0-9_-]{1,160}$/.test(v);
export const revision = (v: unknown): v is string =>
    typeof v === "string" && /^[1-9][0-9]*-[a-zA-Z0-9]+$/.test(v);
export const digestPattern = /^sha256:[0-9a-f]{64}$/;
export function relativePath(v: unknown): v is string {
    return workspacePath(v) && /\.(md|markdown|txt)$/i.test(v);
}
export function referencePath(v: unknown): v is string {
    return workspacePath(v) && /\.(pdf|png|jpe?g|gif|webp|canvas|wav|svg|csv|xlsx|dat|json)$/i.test(v);
}
function workspacePath(v: unknown): v is string {
    return (
        typeof v === "string" &&
        v.length <= 1024 &&
        v.normalize("NFC") === v &&
        !/[\\:\x00-\x1f\x7f]/.test(v) &&
        !v.startsWith(PREFIX) &&
        v
            .split("/")
            .every(
                (p) =>
                    !!p &&
                    !p.startsWith(".") &&
                    p.trim() === p &&
                    !["credentials", "private_no_index"].includes(p),
            )
    );
}
export function collectionRoot(v: unknown): v is string {
    return typeof v === "string" && relativePath(v + "/placeholder.md");
}
export function controlPath(record: Control): string {
    return `${PREFIX}${record.kind}/${record.id}.md`;
}
export const reserved = (path: string) => path === PREFIX.slice(0, -1) || path.startsWith(PREFIX);
export async function digest(text: string | Uint8Array<ArrayBuffer>): Promise<string> {
    const bytes = typeof text === "string" ? new TextEncoder().encode(text) : text;
    const result = await crypto.subtle.digest("SHA-256", bytes);
    return "sha256:" + [...new Uint8Array(result)].map((v) => v.toString(16).padStart(2, "0")).join("");
}
function object(v: unknown): v is Record<string, unknown> {
    return !!v && typeof v === "object" && !Array.isArray(v);
}
function hash(v: unknown) {
    return typeof v === "string" && digestPattern.test(v);
}
function resolutionIDs(v: unknown, self: unknown) {
    return Array.isArray(v) && v.length > 0 && v.length <= 32 &&
        v.every(id => token(id) && id !== self) && new Set(v).size === v.length;
}
function scope(v: Record<string, unknown>) {
    return ["workspace", "collection", "generation", "fileId"].every((k) => token(v[k]));
}
// Strict field allowlists prevent accidentally transporting a host path/secret in
// an ignored extension property. Unknown versions are never downgraded.
function fields(v: Record<string, unknown>, names: string[]) {
    if (Object.keys(v).some((k) => !["version", "kind", "id", ...names].includes(k)))
        throw new Error("unknown_field");
}
export function validate(value: unknown): Control {
    if (!object(value) || value.version !== VERSION || !token(value.id)) throw new Error("unknown_protocol");
    const v = value;
    switch (v.kind) {
        case "binding":
            fields(v, [
                "workspace",
                "collection",
                "generation",
                "fileId",
                "collectionRoot",
                "path",
                "sourceBase",
                "nativeRevision",
                "sha256",
                "writable",
                "sourceSequence",
            ]);
            if (
                !scope(v) ||
                !collectionRoot(v.collectionRoot) ||
                !(relativePath(v.path) || (v.writable === false && referencePath(v.path))) ||
                !String(v.path).startsWith(String(v.collectionRoot) + "/") ||
                !token(v.sourceBase) ||
                !revision(v.nativeRevision) ||
                !hash(v.sha256) ||
                typeof v.writable !== "boolean" ||
                (v.sourceSequence !== undefined && (!Number.isSafeInteger(v.sourceSequence) || Number(v.sourceSequence) < 1))
            )
                throw new Error("invalid_binding");
            break;
        case "intent":
            fields(v, [
                "device",
                "operation",
                "workspace",
                "collection",
                "generation",
                "fileId",
                "base",
                "predecessor",
                "resolves",
                "path",
                "target",
                "sha256",
                "length",
            ]);
            if (
                !scope(v) ||
                !token(v.device) ||
                !relativePath(v.path) ||
                !hash(v.sha256) ||
                !Number.isSafeInteger(v.length) ||
                Number(v.length) < 0 ||
                Number(v.length) > MAX_BYTES ||
                (v.predecessor !== undefined && !token(v.predecessor))
            )
                throw new Error("invalid_intent");
            if (!["create", "edit", "delete", "rename"].includes(String(v.operation)))
                throw new Error("invalid_operation");
            if (v.resolves !== undefined && (v.operation !== "edit" || v.base === null ||
                v.predecessor !== undefined || !resolutionIDs(v.resolves, v.id)))
                throw new Error("invalid_resolution");
            if (v.operation === "create") {
                if (v.base !== null || v.predecessor !== undefined)
                    throw new Error("create_requires_absence");
            } else if (v.base === null) {
                if (!token(v.predecessor)) throw new Error("missing_create_predecessor");
            } else {
                const b = validate(v.base);
                if (
                    b.kind !== "binding" ||
                    !b.writable ||
                    b.workspace !== v.workspace ||
                    b.collection !== v.collection ||
                    b.generation !== v.generation ||
                    b.fileId !== v.fileId
                )
                    throw new Error("invalid_base");
            }
            if (
                v.operation === "rename"
                    ? !relativePath(v.target) || v.target === v.path
                    : v.target !== undefined
            )
                throw new Error("invalid_target");
            if (v.operation === "delete" && v.length !== 0) throw new Error("invalid_deletion");
            break;
        case "publication":
            fields(v, ["intentId", "intentDigest", "revisions"]);
            if (
                !token(v.intentId) ||
                !hash(v.intentDigest) ||
                !Array.isArray(v.revisions) ||
                v.revisions.length < 1 ||
                v.revisions.length > 2
            )
                throw new Error("invalid_publication");
            for (const r of v.revisions) {
                if (
                    !object(r) ||
                    Object.keys(r).some((k) => !["path", "revision", "role"].includes(k)) ||
                    !relativePath(r.path) ||
                    !revision(r.revision) ||
                    !["content", "deletion"].includes(String(r.role))
                )
                    throw new Error("invalid_revision");
            }
            break;
        case "ack":
            fields(v, ["intentId", "intentDigest", "publicationId", "status", "reason", "binding", "resolves"]);
            if (
                !token(v.intentId) ||
                !token(v.publicationId) ||
                !hash(v.intentDigest) ||
                !["applied", "conflict", "held"].includes(String(v.status)) ||
                typeof v.reason !== "string" ||
                v.reason.length > 512
            )
                throw new Error("invalid_ack");
            if (v.binding !== undefined && validate(v.binding).kind !== "binding")
                throw new Error("invalid_ack_binding");
            if (v.resolves !== undefined && (v.status !== "applied" || v.binding === undefined ||
                !resolutionIDs(v.resolves, v.intentId))) throw new Error("invalid_resolution_ack");
            break;
        default:
            throw new Error("unknown_control_kind");
    }
    return value as Control;
}
export function encode(record: Control): string {
    return JSON.stringify(validate(record));
}
export function decode(path: string, text: string): Control {
    if (new TextEncoder().encode(text).length > MAX_CONTROL_BYTES) throw new Error("control_size");
    const record = validate(JSON.parse(text));
    if (path !== controlPath(record)) throw new Error("control_collision");
    return record;
}
export function pair(intent: Intent, publication: Publication): boolean {
    const r = publication.revisions;
    if (publication.intentId !== intent.id) return false;
    if (intent.operation === "rename")
        return (
            r.length === 2 &&
            r.some((x) => x.path === intent.path && x.role === "deletion") &&
            r.some((x) => x.path === intent.target && x.role === "content")
        );
    return (
        r.length === 1 &&
        r[0].path === intent.path &&
        r[0].role === (intent.operation === "delete" ? "deletion" : "content")
    );
}
