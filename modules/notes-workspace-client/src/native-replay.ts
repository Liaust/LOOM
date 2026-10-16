// Disposable transport progress, not part of the authoritative edit journal.
// Change the reader key when control interpretation changes, not just its format.
import type { Control } from "./protocol";
export const NATIVE_REPLAY_READER = "control-path-v1";
export const NATIVE_REPLAY_BATCH = 128;
export const NATIVE_REPLAY_ROWS = 1024;
export type ReplaySequence = string | number;
export type ReplayWork = { kind: "control"; path: string; revision: string } | { kind: "path"; path: string };
export type ReplayRow = ReplayWork & { ticket: string };
export type ReplayHeader = { version: 1; reader: string; database: string | null;
    cursor: ReplaySequence; pendingCount: number; recoveryComplete?: boolean };
export interface ReplayPersistence {
    load(reader: string): Promise<{ header: unknown; rows: { key: string; value: unknown }[] }>;
    reset(reader: string, header: ReplayHeader): Promise<void>;
    commit(reader: string, header: ReplayHeader, put: ReplayRow[], remove: string[]): Promise<void>;
}
export const replayKey = (row: ReplayWork) => JSON.stringify(row.kind === "control" ?
    [row.kind, row.path, row.revision] : [row.kind, row.path]);
const limits = { rows: NATIVE_REPLAY_ROWS, controls: 256, bytes: 2 * 1024 * 1024 };
const text = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length <= 8192;
const sequence = (value: unknown): value is ReplaySequence => text(value) || typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
export async function nativeDatabaseIdentity(database: object): Promise<string | undefined> {
    // Pinned PouchDB implements id(), but its published Database type omits it.
    const db = database as { id?: () => Promise<unknown> };
    try {
        const id = await db.id?.();
        return text(id) ? id : undefined;
    } catch { return undefined; }
}
const validRow = (row: any): row is ReplayRow => row && text(row.path) && text(row.ticket) &&
    (row.kind === "path" || row.kind === "control" && text(row.revision)) &&
    Object.keys(row).every(key => ["kind", "path", "ticket", ...(row.kind === "control" ? ["revision"] : [])].includes(key));
export function replayControlPaths(record: Control): string[] {
    if (record.kind === "binding") return [record.path];
    if (record.kind === "layout") return [...new Set([record.previous.path, record.binding.path])];
    return record.kind === "ack" && record.binding ? [record.binding.path] : [];
}

export class NativeReplay {
    private rows = new Map<string, ReplayRow>();
    private serial = Promise.resolve();
    private reused = false;
    private constructor(private persistence: ReplayPersistence, private header: ReplayHeader,
        private bounds: typeof limits) {}
    static async open(persistence: ReplayPersistence, database?: string, reader = NATIVE_REPLAY_READER,
        bounds = limits) {
        const identity = text(database) ? database : null;
        const stored = await persistence.load(reader);
        const header = stored.header as ReplayHeader | undefined;
        const replay = new NativeReplay(persistence,
            { version: 1, reader, database: identity, cursor: 0, pendingCount: 0, recoveryComplete: false }, bounds);
        if (identity && header?.version === 1 && header.reader === reader && header.database === identity &&
            sequence(header.cursor) && header.pendingCount === stored.rows.length &&
            (header.recoveryComplete === undefined || typeof header.recoveryComplete === "boolean") &&
            stored.rows.every(row => validRow(row.value) && replayKey(row.value) === row.key)) {
            replay.rows = new Map(stored.rows.map(row => [row.key, row.value as ReplayRow]));
            try {
                replay.check(replay.rows);
                if (replay.rows.size !== header.pendingCount) throw Error("native_replay_invalid");
                replay.header = header;
                replay.reused = true;
                return replay;
            } catch { /* Invalid/oversized cache: rediscover; never reset the edit journal. */ }
        }
        replay.rows.clear();
        await persistence.reset(reader, replay.header);
        return replay;
    }
    get cursor() { return this.header.cursor; }
    get checkpointReused() { return this.reused; }
    get recoveryNeeded() { return this.header.recoveryComplete !== true; }
    requiresRecovery(manual = false) { return manual || this.recoveryNeeded; }
    get hasPending() { return this.rows.size > 0; }
    get pendingPaths() { return this.pending.filter(row => row.kind === "path").map(row => row.path); }
    get pending() { return [...this.rows.values()]; }
    get controls() { return this.pending.filter((row): row is ReplayRow & { kind: "control"; revision: string } => row.kind === "control"); }
    path(path: string) { return this.rows.get(replayKey({ kind: "path", path })); }
    private check(rows: Map<string, ReplayRow>) {
        if (rows.size > this.bounds.rows || [...rows.values()].filter(row => row.kind === "control").length > this.bounds.controls ||
            [...rows.values()].reduce((n, row) => n + new TextEncoder().encode(JSON.stringify(row)).length, 0) > this.bounds.bytes)
            throw Error("native_replay_backlog_limit");
    }
    // Cursor and exact pending records enter one IndexedDB transaction. Tokens
    // prevent completion of an older path attempt from erasing a newer arrival.
    accept(cursor: ReplaySequence, work: ReplayWork[]) { return this.write(cursor, work); }
    enqueue(paths: string[]) { return this.write(undefined, paths.map(path => ({ kind: "path", path }))); }
    complete(row: ReplayRow, paths: string[] = []) { return this.write(undefined,
        paths.map(path => ({ kind: "path", path })), row); }
    beginRecovery() { return this.write(undefined, [], undefined, false); }
    finishRecovery() { return this.write(undefined, [], undefined, true); }
    private write(cursor: ReplaySequence | undefined, work: ReplayWork[], completed?: ReplayRow,
        recoveryComplete?: boolean): Promise<void> {
        const run = this.serial.then(async () => {
            if (cursor !== undefined && !sequence(cursor)) throw Error("native_replay_sequence_invalid");
            const remove = completed && this.rows.get(replayKey(completed))?.ticket === completed.ticket ? [replayKey(completed)] : [];
            const put = new Map<string, ReplayRow>();
            for (const item of work) {
                // Explicit fields only: never persist a native metadata body or note bytes.
                const row: ReplayRow = item.kind === "control" ?
                    { kind: "control", path: item.path, revision: item.revision, ticket: crypto.randomUUID() } :
                    { kind: "path", path: item.path, ticket: crypto.randomUUID() };
                if (!validRow(row)) throw Error("native_replay_work_invalid");
                put.set(replayKey(row), row);
            }
            if (!put.size && !remove.length && cursor === undefined &&
                (recoveryComplete === undefined || recoveryComplete === this.header.recoveryComplete)) return;
            const next = new Map(this.rows);
            for (const key of remove) next.delete(key);
            for (const [key, row] of put) next.set(key, row);
            this.check(next);
            const header = { ...this.header, cursor: cursor ?? this.header.cursor, pendingCount: next.size };
            if (recoveryComplete !== undefined) header.recoveryComplete = recoveryComplete;
            await this.persistence.commit(header.reader, header, [...put.values()], remove);
            this.rows = next;
            this.header = header;
        });
        this.serial = run.catch(() => {});
        return run;
    }
}
