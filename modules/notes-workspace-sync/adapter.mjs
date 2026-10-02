import pin from './upstream-lock.json' with { type: 'json' };
import { ControlTransport } from './control.ts';
import { decode, controlPath, validate, relativePath, referencePath, MAX_BYTES, MAX_CONTROL_BYTES, MAX_REFERENCE_BYTES, reserved } from './protocol.ts';
import { createHash } from 'node:crypto';
import { createTextBlob, createBinaryBlob, readContent } from '@vrtmrz/livesync-commonlib/compat/common/utils';

export const PROTOCOL = 1;
export const MAX_CONTENT_BYTES = MAX_BYTES;
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const validRev = (rev) => typeof rev === 'string' && /^[1-9][0-9]*-[a-zA-Z0-9]+$/.test(rev);
const hold = (reason) => ({ status: 'held', reason });
const fail = (code) => { throw Object.assign(new Error(code), { code }); };
const isFile = (doc) => doc.type === 'plain' || doc.type === 'newnote';

function writablePath(path) {
    if (!relativePath(path)) fail('invalid_path');
    return path;
}

// Native APIs own replication, codecs and revision construction. This object
// never receives a canonical source path or mutates a filesystem workspace.
export function createAdapter(core, replicate) {
    const native = core.services.database.localDatabase;
    const db = native.localDatabase;
    const files = core.serviceModules.databaseFileAccess;
    const controls = new ControlTransport(files);
    const hooks = typeof core.serviceModules.fileHandler.loomPublish === 'function';
    if (hooks) {
        core.services.path.loomControlPath = reserved;
        // Headless transport never reflects a replica into filesystem notes.
        core.serviceModules.fileHandler.loomIntent = {
            mutation: async () => false, beforeReflect: async () => true, afterReflect: async () => {},
        };
    }
    const encrypted = () => !!core.services.setting.currentSettings().encrypt && !!core.services.setting.currentSettings().passphrase;
    const requireControls = () => { if (!encrypted() || !hooks) fail('encrypted_control_hooks_required'); };

    async function metadata(doc) {
        const chain = doc._revisions;
        return {
            id: doc._id, revision: doc._rev, path: typeof doc.path === 'string' ? doc.path : null,
            ancestors: chain ? chain.ids.slice(1).map((id, i) => `${chain.start - i - 1}-${id}`) : [],
            ancestryAvailable: !!chain, ancestryCompleteToRoot: !!chain && chain.start === chain.ids.length,
            logicalDeleted: doc.deleted === true,
            branchDeleted: doc._deleted === true, size: doc.size ?? null,
            kind: isFile(doc) ? (reserved(String(doc.path)) ? 'control' : 'file') : doc._deleted ? 'unknown_tombstone' : 'system',
            disposition: doc.deleted || doc._deleted ? 'held_ambiguous_deletion' : 'requires_source_base_mapping',
        };
    }

    async function leaves(id) {
        const docs = await db.get(id, { open_revs: 'all', revs: true });
        if (!Array.isArray(docs) || docs.length > 64) fail('leaf_limit');
        return Promise.all(docs.map((item) => item.ok ? metadata(item.ok) : ({
            id, revision: item.missing, disposition: 'held_missing_revision',
        })));
    }

    async function read(request) {
        if (typeof request.id !== 'string' || !validRev(request.revision)) fail('exact_revision_required');
        const doc = await db.get(request.id, { rev: request.revision, revs: true });
        const result = await metadata(doc);
        if (!isFile(doc) || doc.deleted || doc._deleted) return { ...result, ...hold('non_live_file') };
        if (!Number.isFinite(doc.size) || doc.size > MAX_CONTENT_BYTES) return { ...result, ...hold('content_limit') };
        // localOnly avoids waiting for missing chunks or implicit network access.
        const entry = await files.fetchEntry(doc.path, doc._rev, false, true, true);
        if (!entry || entry._rev !== doc._rev) return { ...result, ...hold('content_unavailable') };
        if (entry.datatype !== 'plain') return { ...result, ...hold('reference_binary') };
        const bytes = Buffer.from(readContent(entry), 'utf8');
        if (bytes.length > MAX_CONTENT_BYTES) return { ...result, ...hold('content_limit') };
        return { ...result, status: 'available', sha256: hash(bytes), contentBase64: bytes.toString('base64') };
    }

    async function publish(request, epoch, reference = false) {
        if (reference) {
            requireControls();
            if (!referencePath(request.path)) fail('invalid_reference_path');
        } else writablePath(request.path);
        if (typeof request.operationId !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(request.operationId)) fail('invalid_operation_id');
        const limit = reference ? MAX_REFERENCE_BYTES : MAX_CONTENT_BYTES;
        if (typeof request.contentBase64 !== 'string' || request.contentBase64.length > 4 * Math.ceil(limit / 3)) fail('invalid_content');
        const bytes = Buffer.from(request.contentBase64, 'base64');
        // Canonical round-trip avoids a recursive regexp overflowing on PDFs.
        if (bytes.toString('base64') !== request.contentBase64) fail('invalid_content');
        if (bytes.length > limit) fail('content_limit');
        const text = reference ? null : new TextDecoder('utf-8', { fatal: true }).decode(bytes);
        const create = request.create === true;
        if (create ? request.baseRevision !== undefined : !validRev(request.baseRevision)) return hold('exact_base_required');
        const fingerprint = hash(JSON.stringify([epoch, request.path, request.baseRevision ?? null, create, hash(bytes)]));
        const receiptId = `_local/loom-publication-${request.operationId}`;
        let prior;
        try { prior = await db.get(receiptId); } catch (error) { if (error.status !== 404) throw error; }
        if (prior) {
            if (prior.fingerprint !== fingerprint) fail('operation_id_reused');
            return prior.result ?? hold('publication_outcome_unknown');
        }
        const id = await core.services.path.path2id(request.path);
        if (create) {
            try { await db.get(id, { open_revs: 'all' }); return hold('path_has_history'); }
            catch (error) { if (error.status !== 404) throw error; }
        } else {
            let base;
            try { base = await db.get(id, { rev: request.baseRevision }); }
            catch (error) { if (error.status === 404) return hold('base_unavailable'); throw error; }
            if (!isFile(base) || base.deleted || base._deleted || base.path !== request.path) return hold('base_not_live_file');
        }
        // A pending receipt is deliberately not automatically replayed after a
        // crash: native document and _local receipt writes are not one transaction.
        const pending = { _id: receiptId, fingerprint, state: 'pending' };
        const stored = await db.put(pending);
        const now = Date.now();
        const file = { path: request.path, name: request.path.split('/').pop(),
            body: reference ? createBinaryBlob(new Uint8Array(bytes)) : createTextBlob(text), stat: { type: 'file', size: bytes.length, ctime: now, mtime: now } };
        const revision = create ? await files.storeIndependentRevision(file) :
            await files.storeWithBaseRevision(file, request.baseRevision);
        if (!revision) return hold('publication_outcome_unknown');
        const result = { status: 'published', id, revision, baseRevision: request.baseRevision ?? null, sha256: hash(bytes) };
        await db.put({ ...pending, _rev: stored.rev, state: 'published', result });
        return result;
    }

    return async function dispatch(request) {
        try {
            if (!request || typeof request !== 'object' || Array.isArray(request) || request.protocol !== PROTOCOL) fail('invalid_protocol');
            const epoch = await db.id();
            if (request.method === 'status') return { status: 'ready', protocol: PROTOCOL, epoch, sourceWrites: false, encryptionEnabled: encrypted(), clientIntentVersion: hooks ? 1 : 0, upstreamRevision: pin.revision, upstreamVersion: pin.version };
            if (request.epoch !== epoch) return hold('replica_epoch_mismatch');
            switch (request.method) {
                case 'read': return await read(request);
                case 'read.path': {
                    requireControls(); writablePath(request.path);
                    try { return await read({ id: await core.services.path.path2id(request.path), revision: request.revision }); }
                    catch (error) {
                        if (error.status !== 404) throw error;
                        // Replication retains ancestry even when an intermediate
                        // body was never transferred. Content must come separately
                        // from the immutable per-intent payload, never a newer leaf.
                        const id = await core.services.path.path2id(request.path);
                        let branches;
                        try { branches = await leaves(id); }
                        catch (missing) { if (missing.status === 404) return hold('revision_unavailable'); throw missing; }
                        const matches = branches.filter((b) => b.path === request.path && b.ancestors?.includes(request.revision));
                        if (!matches.length) return hold('revision_unavailable');
                        const chains = matches.map((b) => b.ancestors.slice(b.ancestors.indexOf(request.revision) + 1));
                        if (chains.some((c) => JSON.stringify(c) !== JSON.stringify(chains[0]))) return hold('ancestry_ambiguous');
                        return { id, path: request.path, revision: request.revision, kind: 'file', status: 'history',
                            ancestors: chains[0], ancestryAvailable: true,
                            ancestryCompleteToRoot: matches.every((b) => b.ancestryCompleteToRoot) };
                    }
                }
                case 'payload.put': {
                    requireControls();
                    if (typeof request.intentId !== 'string' || !/^[A-Za-z0-9_-]{1,160}$/.test(request.intentId)) fail('invalid_intent_id');
                    if (typeof request.contentBase64 !== 'string' || request.contentBase64.length > 4 * Math.ceil(MAX_CONTENT_BYTES / 3)) fail('invalid_content');
                    const bytes = Buffer.from(request.contentBase64, 'base64');
                    if (bytes.length > MAX_CONTENT_BYTES || bytes.toString('base64') !== request.contentBase64) fail('invalid_content');
                    const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
                    const revision = await controls.putPayload(request.intentId, text);
                    return { status: 'published', revision };
                }
                case 'payload.read': {
                    requireControls();
                    if (typeof request.intentId !== 'string' || !/^[A-Za-z0-9_-]{1,160}$/.test(request.intentId)) fail('invalid_intent_id');
                    const path = `LOOM-Control-v1/payload/${request.intentId}.md`;
                    const id = await core.services.path.path2id(path);
                    let all;
                    try { all = await leaves(id); }
                    catch (error) { if (error.status === 404) return hold('payload_unavailable'); throw error; }
                    if (all.length !== 1 || !all[0].revision.startsWith('1-') || !all[0].ancestryCompleteToRoot)
                        return hold('payload_collision');
                    const result = await read({ id, revision: all[0].revision });
                    if (result.path !== path || result.logicalDeleted || result.branchDeleted) return hold('payload_collision');
                    return result;
                }
                case 'control.read': {
                    requireControls();
                    let result;
                    try { result = await read(request); }
                    catch (error) { if (error.status === 404) return hold('control_unavailable'); throw error; }
                    if (result.status !== 'available' || !reserved(result.path)) return hold('control_unavailable');
                    const all = await leaves(request.id);
                    if (all.length !== 1 || all[0].revision !== request.revision || !request.revision.startsWith('1-') || !result.ancestryCompleteToRoot) return hold('control_collision');
                    try { decode(result.path, Buffer.from(result.contentBase64, 'base64').toString('utf8')); }
                    catch { return hold('control_invalid'); }
                    return result;
                }
                case 'control.put': {
                    requireControls();
                    if (typeof request.contentBase64 !== 'string' || request.contentBase64.length > 4 * Math.ceil(MAX_CONTROL_BYTES / 3)) fail('invalid_content');
                    const bytes = Buffer.from(request.contentBase64, 'base64');
                    if (bytes.length > MAX_CONTROL_BYTES) fail('content_limit');
                    const value = validate(JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)));
                    const revision = await controls.put(value);
                    return { status: 'published', path: controlPath(value), revision };
                }
                case 'leaves': {
                    if (typeof request.id !== 'string') fail('invalid_document_id');
                    return { leaves: await leaves(request.id) };
                }
                case 'changes': {
                    if (!(typeof request.since === 'string' || Number.isSafeInteger(request.since)) || request.since === 'now') fail('explicit_sequence_required');
                    const limit = request.limit ?? 32;
                    if (!Number.isInteger(limit) || limit < 1 || limit > 128) fail('invalid_limit');
                    const page = await db.changes({ since: request.since, limit, style: 'all_docs', include_docs: false });
                    const changes = [];
                    for (const row of page.results) {
                        const evidence = await leaves(row.id);
                        if (evidence.some((leaf) => leaf.kind !== 'system')) changes.push({ id: row.id, sequence: row.seq, leaves: evidence });
                    }
                    return { status: 'available', epoch, nextSequence: page.last_seq, changes };
                }
                case 'publish': return await publish(request, epoch);
                case 'publish.reference': return await publish(request, epoch, true);
                case 'sync': return await replicate();
                case 'sync.start': return await replicate('start');
                case 'sync.stop': return await replicate('stop');
                case 'changes.wait': {
                    if (!(typeof request.since === 'string' || Number.isSafeInteger(request.since)) || request.since === 'now') fail('explicit_sequence_required');
                    if (!Number.isInteger(request.timeoutMs) || request.timeoutMs < 1 || request.timeoutMs > 5000) fail('invalid_wait');
                    // Wakeup only: Step owns advancement of the durable cursor.
                    // Starting from its exact sequence closes the check/listen race.
                    return await new Promise((resolve) => {
                        const feed = db.changes({ since: request.since, live: true, include_docs: false });
                        const finish = (result) => { clearTimeout(timer); feed.cancel(); resolve(result); };
                        const timer = setTimeout(() => finish({ status: 'idle' }), request.timeoutMs);
                        feed.once('change', () => finish({ status: 'changed' }));
                        feed.once('error', () => finish({ status: 'error', code: 'change_feed_failed' }));
                    });
                }
                case 'delete': case 'rename': return hold('client_intent_not_proven');
                default: fail('unknown_method');
            }
        } catch (error) {
            // Never surface native errors: they can embed credentials or note data.
            return { status: 'error', code: error.code && /^[a-z_]+$/.test(error.code) ? error.code :
                error.status === 404 ? 'revision_unavailable' : 'native_operation_failed' };
        }
    };
}
