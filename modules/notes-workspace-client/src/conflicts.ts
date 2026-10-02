import type { IntentClient } from "./client";
import { unfinished } from "./journal";
import { digest, encode, pair, MAX_BYTES, type Binding } from "./protocol";

export type ConflictReview = {
    path: string;
    binding: Binding;
    source: string;
    local: string;
    base: string | null;
    ids: string[];
    covered: string[];
    leaves: { revision: string; text: string; sha256: string }[];
    token: string;
};
export function conflictPaths(client: IntentClient): string[] {
    const state = client.journal.state;
    return [...new Set([
        ...state.operations.filter(o => unfinished(o) && (o.state === "conflict" || o.state === "held"))
            .map(o => o.intent.path),
        ...Object.entries(state.holds).filter(([path, reason]) =>
            !path.startsWith("LOOM-Control-v1/") && /conflict|ancestry|resolution/.test(reason)).map(([path]) => path),
    ])].sort();
}

// A comparison is a snapshot, not permission to apply whatever source exists
// later. Main still makes the final exact-base decision after replication.
export async function inspectConflict(client: IntentClient, path: string, settle = true): Promise<ConflictReview> {
    await client.ready;
    if (settle) await client.settled();
    const state = client.journal.state;
    const installed = state.bindings[path];
    if (!installed?.writable) throw Error("This note has no writable source binding.");
    const candidates = [...new Map([installed, ...Object.values(state.candidates),
        ...Object.values(state.acks ?? {}).flatMap(a => a.status === "applied" && a.binding ? [a.binding] : [])]
        .filter(b => b.path === path && b.writable && b.fileId === installed.fileId &&
            b.workspace === installed.workspace && b.collection === installed.collection &&
            b.generation === installed.generation).map(b => [b.id, b])).values()];
    const leaves = await client.io.conflictLeaves?.(path) ?? [];
    if (leaves.length > 32) throw Error("Too many competing revisions; keep this note for operator review.");
    const sameSource = (a: Binding, b: Binding) => a.sourceBase === b.sourceBase && a.sha256 === b.sha256;
    const sequence = Math.max(...candidates.map(b => b.sourceSequence ?? 0));
    const newest: Binding[] = [];
    for (const b of candidates) {
        if (sequence > 0) {
            if (b.sourceSequence === sequence) newest.push(b);
            continue;
        }
        let older = false;
        // An accepted edit and its later source export may be sibling native
        // revisions of the SAME source base. Either proves that base's ancestry.
        for (const next of candidates) if (!sameSource(next, b)) {
            for (const alias of candidates.filter(a => sameSource(a, b)))
                if (next.nativeRevision !== alias.nativeRevision &&
                    await client.io.descends(path, next.nativeRevision, alias.nativeRevision)) { older = true; break; }
            if (older) break;
        }
        if (!older) newest.push(b);
    }
    // Replication retains leaf bodies, not every historical body. An old
    // binding whose revision is no longer a live leaf is not a current choice.
    const liveMatches = newest.filter(b => leaves.some(l => l.revision === b.nativeRevision));
    const live = liveMatches.length ? liveMatches : newest;
    if (!live.length || (sequence > 0 && newest.some(b => !sameSource(b, newest[0]))) ||
        live.some(b => !sameSource(b, live[0])))
        throw Error("The current source binding is ambiguous; wait for sync and refresh.");
    const binding = live[0];
    const source = await client.io.readRevision(path, binding.nativeRevision);
    const local = await client.io.read(path);
    if (source === null || local === null || await digest(source) !== binding.sha256)
        throw Error("The source or local text is not available yet; wait for sync and refresh.");
    const pending = state.operations.filter(o => unfinished(o) && (o.intent.path === path || o.intent.target === path));
    if (pending.some(o => o.intent.operation !== "edit" || o.intent.fileId !== binding.fileId))
        throw Error("This conflict includes a rename, deletion or different file identity; keep it for operator review.");
    if (pending.some(o => o.resolution && !o.ack && o.state !== "held"))
        throw Error("A resolution is already waiting for source acceptance.");
    const ids = pending.filter(o => o.ack?.status === "conflict" && o.ack.reason === "source_conflict")
        .map(o => o.intent.id);
    if (ids.length > 32) throw Error("Resolve this long conflict history through the operator before continuing.");
    const covered = pending.map(o => o.intent.id);
    const ancestorBlocked = (id: string | undefined): boolean => {
        const seen = new Set<string>();
        while (id && !seen.has(id)) {
            seen.add(id);
            const op = state.operations.find(o => o.intent.id === id);
            if (!op) return false;
            if (op.state === "conflict" || op.state === "held") return true;
            id = op.intent.predecessor;
        }
        return false;
    };
    if (pending.some(o => !ancestorBlocked(o.intent.id)))
        throw Error("An ordinary edit is still awaiting acceptance; let it finish before resolving.");
    const evidence = await Promise.all(leaves.map(async leaf => ({ ...leaf, sha256: await digest(leaf.text) })));
    evidence.sort((a, b) => a.revision.localeCompare(b.revision));
    const old = pending[0]?.intent.base;
    let base = old ? await client.io.readRevision(path, old.nativeRevision) : null;
    if (base !== null && old && await digest(base) !== old.sha256) base = null;
    const token = await digest(JSON.stringify({ binding: encode(binding), local: await digest(local),
        pending: pending.map(o => [encode(o.intent), o.state, o.ack, o.resolvedBy]), evidence }));
    return { path, binding, source, local, base, ids, covered, leaves: evidence, token };
}
export function checkResolutionText(text: string) {
    if (text.includes("\0") || new TextEncoder().encode(text).length > MAX_BYTES)
        throw Error("The merged text exceeds the Notes text limit or contains binary data.");
}

export async function convergeNative(client: IntentClient, binding: Binding, io: {
    leaves(): Promise<{ revision: string; text: string }[]>;
    remove(revision: string): Promise<void>;
}): Promise<boolean> {
    if (!binding.writable) return false;
    if (client.journal.state.operations.some(o => unfinished(o) &&
        (o.intent.path === binding.path || o.intent.target === binding.path))) return false;
    const initial = await io.leaves();
    const accepted = initial.find(leaf => leaf.revision === binding.nativeRevision);
    if (!accepted || await digest(accepted.text) !== binding.sha256) return false;
    if (initial.length === 1) return true;
    const allowed = new Map<string, string>();
    for (const op of client.journal.state.operations) {
        if (op.ack?.status === "applied" && op.ack.binding?.id === binding.id)
            for (const leaf of op.resolution?.leaves ?? []) allowed.set(leaf.revision, leaf.sha256);
        if (op.resolvedBy && client.journal.state.acks && Object.values(client.journal.state.acks)
            .some(ack => ack.intentId === op.resolvedBy && ack.status === "applied" && ack.binding?.id === binding.id))
            for (const leaf of op.revisions) if (leaf.role === "content" && leaf.path === binding.path)
                allowed.set(leaf.revision, op.intent.sha256);
    }
    // Another device or the CLI may have resolved this conflict. Join its
    // immutable receipts before retiring only the exact losing publication.
    const state = client.journal.state;
    for (const ack of Object.values(state.acks ?? {})) {
        const i = state.intents?.[ack.intentId], p = state.publications?.[ack.publicationId];
        if (!i || !p || ack.status !== "applied" || ack.binding?.id !== binding.id ||
            !ack.resolves?.length || !pair(i, p) || ack.intentDigest !== await digest(encode(i)) ||
            ack.intentDigest !== p.intentDigest || JSON.stringify(ack.resolves) !== JSON.stringify(i.resolves) ||
            i.path !== binding.path || i.fileId !== binding.fileId || i.workspace !== binding.workspace ||
            i.collection !== binding.collection || i.generation !== binding.generation || i.sha256 !== binding.sha256 ||
            p.revisions.find(r => r.role === "content")?.revision !== binding.nativeRevision) continue;
        for (const id of ack.resolves) {
            const old = state.intents?.[id];
            const receipt = Object.values(state.acks ?? {}).find(a => a.intentId === id &&
                a.status === "conflict" && a.reason === "source_conflict");
            const publication = receipt && state.publications?.[receipt.publicationId];
            if (!old || !receipt || !publication || !pair(old, publication) ||
                receipt.intentDigest !== await digest(encode(old)) || receipt.intentDigest !== publication.intentDigest ||
                old.path !== i.path || old.fileId !== i.fileId || old.workspace !== i.workspace ||
                old.collection !== i.collection || old.generation !== i.generation) continue;
            for (const leaf of publication.revisions) if (leaf.role === "content" && leaf.path === binding.path)
                allowed.set(leaf.revision, old.sha256);
        }
    }
    for (const leaf of initial) {
        const sha = await digest(leaf.text);
        if (sha !== binding.sha256 && allowed.get(leaf.revision) !== sha) return false;
    }
    // Retain every losing body before any tombstone. Never purge history or
    // delete an unreviewed later leaf. Interrupted cleanup is replayable.
    await client.journal.update(s => {
        s.retiredLeaves ??= {};
        for (const leaf of initial) if (leaf.revision !== binding.nativeRevision)
            s.retiredLeaves[`${binding.path}@${leaf.revision}`] = { ...leaf, path: binding.path, binding: binding.id };
    });
    for (const leaf of initial) if (leaf.revision !== binding.nativeRevision) {
        if (client.journal.state.operations.some(o => unfinished(o) && o.intent.path === binding.path)) return false;
        const current = await io.leaves();
        if (!current.some(x => x.revision === binding.nativeRevision) ||
            current.some(x => !initial.some(y => y.revision === x.revision && y.text === x.text))) return false;
        if (current.some(x => x.revision === leaf.revision)) await io.remove(leaf.revision);
    }
    return (await io.leaves()).every(leaf => leaf.revision === binding.nativeRevision);
}
