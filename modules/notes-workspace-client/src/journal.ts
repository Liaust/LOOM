import { openDB } from "idb";
import { draft } from "./draft";
import type { Ack, Binding, Intent, Publication } from "./protocol";
export type Operation = {
    intent: Intent;
    payload: string;
    state: "queued" | "publishing" | "uploaded" | "applied" | "conflict" | "held";
    reason?: string;
    revisions: Publication["revisions"];
    publication?: Publication;
    ack?: Ack;
    writing?: string;
    resolvedBy?: string;
    resolution?: {
        localHash: string;
        covered: string[];
        leaves: { revision: string; text: string; sha256: string }[];
    };
};
export type State = {
    version: 1;
    device: string;
    bindings: Record<string, Binding>;
    candidates: Record<string, Binding>;
    reflecting: Record<string, Binding>;
    operations: Operation[];
    acks?: Record<string, Ack>;
    holds: Record<string, string>;
    retiredLeaves?: Record<string, { path: string; revision: string; text: string; binding: string }>;
    intents?: Record<string, Intent>;
    publications?: Record<string, Publication>;
    resolutionDisplays?: Record<string, { binding: string; localHash: string }>;
};
export const unfinished = (o: Operation) => o.state !== "applied" && !o.resolvedBy;
export interface Persistence {
    load(): Promise<State | undefined>;
    save(state: State): Promise<void>;
    saveChanges?(state: State, changes: Change[]): Promise<void>;
    flush?(): Promise<void>;
    checkpointFailure?: string;
}
export type Change = { section: string; key: string; value?: unknown };
const maps = ["bindings", "candidates", "reflecting", "acks", "holds", "retiredLeaves", "intents", "publications", "resolutionDisplays"] as const;
const rows = (state: State): Change[] => [
    ...maps.flatMap(section => Object.entries(state[section] ?? {}).map(([key, value]) => ({ section, key, value }))),
    ...state.operations.map((value, index) => ({ section: "operations", key: String(index), value })),
];
const rowKey = (row: Change) => JSON.stringify([row.section, row.key]);
const rowBytes = (row: Change) => row.value === undefined ? 0 :
    new TextEncoder().encode(JSON.stringify([row.section, row.key, row.value])).length;

// Yield between small groups; don't serialize the full corpus in one editor turn.
async function checkpointJSON(state: State, vaultId: string) {
    const chunks = [`{"schema":"loom.client_recovery.v1","vaultId":${JSON.stringify(vaultId)},"capturedAt":${JSON.stringify(new Date().toISOString())},"state":{`];
    let count = 0;
    const yieldUI = async () => { if (++count % 64 === 0) await new Promise(resolve => setTimeout(resolve, 0)); };
    const fields = Object.entries(state).filter(([, value]) => value !== undefined);
    for (let index = 0; index < fields.length; index++) {
        const [field, value] = fields[index];
        if (index) chunks.push(",");
        chunks.push(JSON.stringify(field) + ":");
        if (value && typeof value === "object") {
            const array = Array.isArray(value);
            chunks.push(array ? "[" : "{");
            const entries = Object.entries(value).filter(([, at]) => array || at !== undefined);
            for (let n = 0; n < entries.length; n++) {
                if (n) chunks.push(",");
                if (!array) chunks.push(JSON.stringify(entries[n][0]) + ":");
                chunks.push(JSON.stringify(entries[n][1]));
                await yieldUI();
            }
            chunks.push(array ? "]" : "}");
        } else chunks.push(JSON.stringify(value));
    }
    chunks.push("}}");
    return chunks.join("");
}
// Two alternating complete exports preserve one previous checkpoint even if a
// device stops during a write. They are recovery evidence, never auto-imported
// into a new device or silently substituted for missing native receipts.
export function recoveryPersistence(inner: Persistence, vaultId: string,
    write: (slot: number, content: string) => Promise<void>): Persistence {
    let slot = 0, latest: State | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined, writing: Promise<void> | undefined;
    const flush = async (): Promise<void> => {
        if (timer) clearTimeout(timer);
        timer = undefined;
        if (writing) { await writing; return flush(); }
        if (!latest) return;
        const state = latest;
        latest = undefined;
        writing = (async () => {
            try {
                await write(slot, await checkpointJSON(state, vaultId));
                slot = 1 - slot;
                result.checkpointFailure = undefined;
            } catch {
                if (!latest) latest = state;
                result.checkpointFailure = "recovery_checkpoint_unavailable";
                throw Error(result.checkpointFailure);
            }
        })();
        try { await writing; } finally { writing = undefined; }
    };
    const schedule = (state: State) => {
        latest = state;
        if (!timer) timer = setTimeout(() => { timer = undefined; void flush().catch(() => {}); }, 5000);
    };
    const result: Persistence = {
        load: () => inner.load(),
        async save(state) {
            await inner.save(state);
            schedule(state);
        },
        async saveChanges(state, changes) {
            if (inner.saveChanges) await inner.saveChanges(state, changes);
            else await inner.save(state);
            schedule(state);
        },
        async flush() { do { await flush(); } while (latest); },
    };
    return result;
}
export function indexedDBPersistence(vaultId: string): Persistence {
    // Deliberately outside LiveSync's rebuild/reset database namespace.
    const db = openDB("loom-client-intent-v1:" + vaultId, 1, {
        upgrade(db) {
            db.createObjectStore("journal");
        },
    });
    return {
        async load() {
            const tx = (await db).transaction("journal", "readonly");
            const header = await tx.store.get("records-v1");
            if (!header) return tx.store.get("state");
            if (header.version !== 1 || typeof header.device !== "string" || !Array.isArray(header.sections) ||
                !Number.isSafeInteger(header.operationCount) || header.operationCount < 0 ||
                !["bindings", "candidates", "reflecting", "holds"].every(section => header.sections.includes(section)) ||
                header.sections.some((section: string) => !maps.includes(section as any)))
                throw Error("journal_records_invalid");
            const state = { version: header.version, device: header.device, operations: [] } as unknown as State;
            for (const section of header.sections) (state as any)[section] = {};
            let cursor = await tx.store.openCursor();
            while (cursor) {
                if (Array.isArray(cursor.key) && cursor.key[0] === "row") {
                    const [, section, key] = cursor.key as string[];
                    if (section !== "operations" && !header.sections.includes(section)) throw Error("journal_records_invalid");
                    if (section === "operations" && (!/^(0|[1-9]\d*)$/.test(key) || Number(key) >= header.operationCount))
                        throw Error("journal_records_invalid");
                    (state as any)[section][key] = cursor.value;
                }
                cursor = await cursor.continue();
            }
            await tx.done;
            if (state.operations.length !== header.operationCount ||
                Array.from({ length: header.operationCount }, (_, i) => state.operations[i]).some(o => !o))
                throw Error("journal_records_incomplete");
            return state;
        },
        async save(state) {
            if (await (await db).get("journal", "records-v1")) return;
            await this.saveChanges!(state, rows(state));
        },
        async saveChanges(state, changes) {
            const tx = (await db).transaction("journal", "readwrite", { durability: "strict" });
            const pending: Promise<unknown>[] = changes.map(row => row.value === undefined ?
                tx.store.delete(["row", row.section, row.key]) : tx.store.put(row.value, ["row", row.section, row.key]));
            pending.push(tx.store.put({ version: state.version, device: state.device,
                sections: maps.filter(section => state[section] !== undefined), operationCount: state.operations.length }, "records-v1"));
            await Promise.all(pending);
            await tx.done; // request success is NOT transaction durability.
        },
    };
}
export class Journal {
    state!: State;
    failure?: string;
    readonly ready: Promise<void>;
    private serial = Promise.resolve();
    private sizes = new Map<string, number>();
    private bytes = 0;
    constructor(private persistence: Persistence) {
        this.ready = this.open();
        // Retain the rejected promise for callers, but do not emit an unhandled rejection.
        void this.ready.catch(() => {});
    }
    private async open() {
        try {
            const old = await this.persistence.load();
            if (old && old.version !== 1) throw new Error("unknown_journal_version");
            this.state = old ?? {
                version: 1,
                device: crypto.randomUUID(),
                bindings: {},
                candidates: {},
                reflecting: {},
                operations: [],
                holds: {},
            };
            for (const row of rows(this.state)) {
                const size = rowBytes(row);
                this.sizes.set(rowKey(row), size); this.bytes += size;
            }
            if (this.bytes > 64 * 1024 * 1024) throw Error("journal_backlog_limit");
            await this.persistence.save(this.state);
        } catch (e) {
            this.failure = "journal_unavailable";
            throw e;
        }
    }
    update(fn: (next: State) => void, unchanged?: (state: Readonly<State>) => boolean): Promise<void> {
        const run = this.serial.then(async () => {
            await this.ready;
            if (this.failure) throw new Error(this.failure);
            // Evaluate under serialization, before touching any journal records.
            if (unchanged?.(this.state)) return;
            const staged = draft(this.state);
            fn(staged.value);
            const next = staged.finish();
            if (next === this.state) return;
            if (next.version !== this.state.version || next.device !== this.state.device)
                throw Error("journal_identity_change");
            const changes: Change[] = [];
            for (const section of [...maps, "operations"] as const) {
                for (const key of staged.keys(section)) {
                    if (key === "length") continue;
                    const value = (next[section] as any)?.[key];
                    if (JSON.stringify(value) !== JSON.stringify((this.state[section] as any)?.[key]))
                        changes.push({ section, key, value });
                }
            }
            if (!changes.length) return;
            const sizes = changes.map(row => [rowKey(row), rowBytes(row)] as const);
            const bytes = sizes.reduce((total, [key, size]) => total + size - (this.sizes.get(key) ?? 0), this.bytes);
            // Bounded total state; never discard pending bytes to satisfy the bound.
            if (bytes > 64 * 1024 * 1024)
                throw new Error("journal_backlog_limit");
            if (this.persistence.saveChanges) await this.persistence.saveChanges(next, changes);
            else await this.persistence.save(next);
            this.state = next;
            this.bytes = bytes;
            for (const [key, size] of sizes) this.sizes.set(key, size);
        });
        this.serial = run.catch((e) => {
            this.failure = String(e);
        });
        return run;
    }
    get checkpointFailure() { return this.persistence.checkpointFailure; }
    async checkpoint() { await this.serial; await this.persistence.flush?.(); }
}
