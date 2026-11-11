import pin from './upstream-lock.json' with { type: 'json' };
import { ControlTransport } from './control.ts';
import { decode, controlPath, validate, relativePath, referencePath, collectionRoot, token, MAX_BYTES, MAX_CONTROL_BYTES, MAX_REFERENCE_BYTES, MAX_INLINE_REFERENCE_BYTES, reserved } from './protocol.ts';
import { createHash } from 'node:crypto';
import { openAsBlob } from 'node:fs';
import { open, lstat } from 'node:fs/promises';
import { constants } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { hasExactAncestry, readRemoteHistory } from './history.mjs';
import { createTextBlob, createBinaryBlob, readContent } from '@vrtmrz/livesync-commonlib/compat/common/utils';
import { ContentSplitterV1 } from '../../../../node_modules/@vrtmrz/livesync-commonlib/dist/ContentSplitter/ContentSplitterV1.js';

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
export function createAdapter(core, replicate, referenceDirectory) {
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

    // Only our file-backed reference blobs use the upstream streaming splitter.
    // V2/Rabin-Karp materialize the entire blob; ordinary text/settings stay intact.
    const referenceBlobs = new WeakSet();
    const splitter = native.managers.splitter;
    const splitContent = splitter.splitContent.bind(splitter);
    const boundedSplitter = new ContentSplitterV1({ settingService: {
        currentSettings: () => ({ ...core.services.setting.currentSettings(), disableWorkerForGeneratingChunks: true }),
    } });
    splitter.splitContent = (entry) => referenceBlobs.has(entry.data) ? boundedSplitter.splitContent(entry) : splitContent(entry);

    function validateReferenceMetadata(request) {
        if (!Number.isSafeInteger(request.size) || request.size < 0 || request.size > MAX_REFERENCE_BYTES ||
            typeof request.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(request.sha256)) fail('invalid_reference_snapshot');
    }

    function validateReferenceRequest(request) {
        validateReferenceMetadata(request);
        if (!referenceDirectory || typeof request.snapshot !== 'string' ||
            dirname(resolve(request.snapshot)) !== resolve(referenceDirectory) ||
            !/^reference-[A-Za-z0-9]+$/.test(request.snapshot.split('/').pop())) fail('invalid_reference_snapshot');
    }

    function publicationFingerprint(request, epoch, create, contentHash) {
        const parts = [epoch, request.path, request.baseRevision ?? null, create, contentHash];
        if (request.adoptIdentical) parts.push('adopt-identical-v1');
        return hash(JSON.stringify(parts));
    }

    async function referenceBlob(request) {
        const directory = await lstat(referenceDirectory);
        if (!directory.isDirectory() || directory.uid !== process.getuid() || (directory.mode & 0o777) !== 0o700) fail('invalid_reference_snapshot');
        const file = await open(request.snapshot, constants.O_RDONLY | constants.O_NOFOLLOW);
        try {
            const before = await file.stat();
            if (!before.isFile() || before.nlink !== 1 || before.uid !== process.getuid() ||
                (before.mode & 0o777) !== 0o600 || before.size !== request.size) fail('invalid_reference_snapshot');
            const digest = createHash('sha256');
            for await (const chunk of file.createReadStream({ autoClose: false, highWaterMark: 256 * 1024 })) digest.update(chunk);
            const after = await file.stat();
            const named = await lstat(request.snapshot);
            if (before.size !== after.size || before.mtimeMs !== after.mtimeMs || before.ctimeMs !== after.ctimeMs ||
                named.dev !== before.dev || named.ino !== before.ino || digest.digest('hex') !== request.sha256) fail('reference_snapshot_changed');
            const body = await openAsBlob(request.snapshot, { type: 'application/octet-stream' });
            referenceBlobs.add(body);
            return body;
        } finally { await file.close(); }
    }

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

    async function publicationTarget(request, create) {
        const id = await core.services.path.path2id(request.path);
        if (create) {
            let existing;
            try { existing = await leaves(id); }
            catch (error) { if (error.status !== 404) throw error; }
            return { id, existing };
        }
        let base;
        try { base = await db.get(id, { rev: request.baseRevision }); }
        catch (error) { if (error.status === 404) return hold('base_unavailable'); throw error; }
        if (!isFile(base) || base.deleted || base._deleted || base.path !== request.path) return hold('base_not_live_file');
        return { id };
    }

    async function publicationPreflight(request, epoch) {
        requireControls();
        if (Object.keys(request).some(k => !['protocol', 'epoch', 'method', 'path', 'create', 'baseRevision', 'soleParent',
            'operationId', 'sha256', 'size'].includes(k))) fail('unknown_field');
        if (!referencePath(request.path)) fail('invalid_reference_path');
        const create = request.create === true;
        if (request.create !== undefined && typeof request.create !== 'boolean' ||
            request.soleParent !== undefined && typeof request.soleParent !== 'boolean' || create && request.soleParent === true ||
            (create ? request.baseRevision !== undefined : !validRev(request.baseRevision))) return hold('exact_base_required');
        if (['operationId', 'sha256', 'size'].some(k => Object.hasOwn(request, k))) {
            if (typeof request.operationId !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(request.operationId)) fail('invalid_operation_id');
            validateReferenceMetadata(request);
            const prior = await localDoc(`_local/loom-publication-${request.operationId}`);
            if (prior) {
                if (prior.fingerprint !== publicationFingerprint(request, epoch, create, request.sha256)) fail('operation_id_reused');
                return prior.result?.status === 'published' ? { status: 'ready', outcome: 'published' } : hold('publication_outcome_unknown');
            }
        }
        const target = await publicationTarget(request, create);
        if (target.status === 'held') return target;
        if (target.existing) return hold('path_has_history');
        if (request.soleParent) {
            let current;
            try { current = await leaves(target.id); }
            catch (error) { if (error.status === 404) return hold('base_unavailable'); throw error; }
            const live = current.filter(leaf => leaf.kind === 'file' && !leaf.logicalDeleted && !leaf.branchDeleted);
            if (live.length === 0) return hold('base_unavailable');
            if (current.length !== 1 || live[0].revision !== request.baseRevision || live[0].path !== request.path)
                return hold('base_diverged');
        }
        // Navigation only: publish repeats these checks and validates actual bytes.
        return { status: 'ready' };
    }

    async function publish(request, epoch, reference = false, fromFile = false) {
        if (reference) {
            requireControls();
            if (!referencePath(request.path)) fail('invalid_reference_path');
        } else writablePath(request.path);
        if (typeof request.operationId !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(request.operationId)) fail('invalid_operation_id');
        let body, contentHash, size;
        if (fromFile) {
            validateReferenceRequest(request);
            contentHash = request.sha256;
            size = request.size;
        } else {
            const limit = reference ? MAX_INLINE_REFERENCE_BYTES : MAX_CONTENT_BYTES;
            if (typeof request.contentBase64 !== 'string' || request.contentBase64.length > 4 * Math.ceil(limit / 3)) fail('invalid_content');
            const bytes = Buffer.from(request.contentBase64, 'base64');
            if (bytes.toString('base64') !== request.contentBase64) fail('invalid_content');
            if (bytes.length > limit) fail('content_limit');
            body = reference ? createBinaryBlob(new Uint8Array(bytes)) : createTextBlob(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
            contentHash = hash(bytes);
            size = bytes.length;
        }
        const create = request.create === true;
        if (create ? request.baseRevision !== undefined : !validRev(request.baseRevision)) return hold('exact_base_required');
        if (request.adoptIdentical !== undefined && (request.adoptIdentical !== true || !create || reference)) fail('invalid_adoption');
        const fingerprint = publicationFingerprint(request, epoch, create, contentHash);
        const receiptId = `_local/loom-publication-${request.operationId}`;
        let prior;
        try { prior = await db.get(receiptId); } catch (error) { if (error.status !== 404) throw error; }
        if (prior) {
            if (prior.fingerprint !== fingerprint) fail('operation_id_reused');
            return prior.result ?? hold('publication_outcome_unknown');
        }
        const target = await publicationTarget(request, create);
        if (target.status === 'held') return target;
        const { id, existing } = target;
        if (create) {
            if (existing) {
                if (!request.adoptIdentical || existing.length !== 1) return hold('path_has_history');
                const leaf = existing[0];
                if (leaf.kind !== 'file' || leaf.path !== request.path || leaf.logicalDeleted || leaf.branchDeleted)
                    return hold('path_has_history');
                const exact = await read({ id, revision: leaf.revision });
                if (exact.status !== 'available' || exact.sha256 !== contentHash || exact.contentBase64 !== request.contentBase64)
                    return hold('path_has_history');
                const fresh = await leaves(id);
                if (fresh.length !== 1 || fresh[0].revision !== leaf.revision) return hold('adoption_changed');
                // This is a source-export receipt, not acceptance of any old
                // client operation or a reconstruction of missing ancestry.
                const result = { status: 'published', id, revision: leaf.revision, baseRevision: null, sha256: contentHash };
                await db.put({ _id: receiptId, fingerprint, state: 'published', result });
                return result;
            }
        }
        if (fromFile) {
            body = await referenceBlob(request);
            // Replication can change the destination while a large snapshot hashes.
            const current = await publicationTarget(request, create);
            if (current.status === 'held') return current;
            if (create && current.existing) return hold('path_has_history');
        }
        // A pending receipt is deliberately not automatically replayed after a
        // crash: native document and _local receipt writes are not one transaction.
        const pending = { _id: receiptId, fingerprint, state: 'pending' };
        const stored = await db.put(pending);
        const now = Date.now();
        const file = { path: request.path, name: request.path.split('/').pop(),
            body, stat: { type: 'file', size, ctime: now, mtime: now } };
        const revision = create ? await files.storeIndependentRevision(file) :
            await files.storeWithBaseRevision(file, request.baseRevision);
        if (!revision) return hold('publication_outcome_unknown');
        const result = { status: 'published', id, revision, baseRevision: request.baseRevision ?? null, sha256: contentHash };
        await db.put({ ...pending, _rev: stored.rev, state: 'published', result });
        return result;
    }

    const canonical = value => JSON.stringify(value, (_, v) => v && typeof v === 'object' && !Array.isArray(v) ?
        Object.fromEntries(Object.keys(v).sort().map(k => [k, v[k]])) : v);
    const fingerprint = value => hash(canonical(value));
    const sourceKey = b => fingerprint([b.workspace, b.collection, b.fileId, b.entryKind ?? 'file']);
    const ownerId = path => `_local/loom-layout-target-${hash(path)}`;
    const docData = doc => Object.fromEntries(['path', 'ctime', 'mtime', 'size', 'type', 'datatype', 'children', 'eden', 'data', 'deleted']
        .filter(k => doc[k] !== undefined).map(k => [k, doc[k]]));
    async function localDoc(id) {
        try { return await db.get(id); }
        catch (e) { if (e.status !== 404) throw e; return null; }
    }
    async function saveLocal(id, value) {
        const old = await localDoc(id);
        await db.put({ ...value, _id: id, ...(old ? { _rev: old._rev } : {}) });
    }
    async function rawLeaves(path) {
        try {
            const rows = await db.get(await core.services.path.path2id(path), { open_revs: 'all', revs: true });
            if (!Array.isArray(rows) || rows.length > 64) fail('leaf_limit');
            return rows.map(r => r.ok ?? null);
        } catch (e) { if (e.status !== 404) throw e; return []; }
    }
    async function authentic(record) {
        const path = controlPath(record), all = await rawLeaves(path);
        if (all.length !== 1 || !all[0] || all[0].deleted || all[0]._deleted ||
            !all[0]._rev.startsWith('1-')) return false;
        try { return canonical(await controls.get(path, all[0]._rev)) === canonical(record); }
        catch { return false; }
    }
    async function exactSource(b) {
        const all = (await rawLeaves(b.path)).filter(d => !d?._deleted);
        if (all.length !== 1 || !all[0] || all[0]._rev !== b.nativeRevision ||
            !isFile(all[0]) || all[0].path !== b.path || all[0].deleted || all[0]._deleted)
            return null;
        const doc = all[0];
        if (!Number.isSafeInteger(doc.size) || doc.size < 0 || doc.size > MAX_REFERENCE_BYTES) return null;
        if (doc.type === 'plain') {
            if (doc.size > MAX_CONTENT_BYTES) return null;
            const entry = await files.fetchEntry(b.path, b.nativeRevision, false, true, true);
            if (!entry || entry._rev !== b.nativeRevision) return null;
            const text = readContent(entry);
            if (typeof text !== 'string' || 'sha256:' + hash(Buffer.from(text, 'utf8')) !== b.sha256) return null;
        } else if (!Array.isArray(doc.children) || !doc.children.every(c => typeof c === 'string')) return null;
        // A binary hash is certified by the immutable persisted binding, never
        // recomputed by downloading/assembling all PDF chunks during a move.
        if (!await authentic(b)) return null;
        return doc;
    }
    async function sourceDiagnostic(b) {
        const all = await rawLeaves(b.path), expected = all.find(d => d?._rev === b.nativeRevision);
        const active = all.filter(d => d && !d._deleted && !d.deleted);
        return `leaves=${all.length};active=${active.length};expected=${!!expected};` +
            `expectedDeleted=${!!expected && !!(expected._deleted || expected.deleted)};control=${await authentic(b)}`;
    }
    function branchMatches(doc, pending) {
        if (!doc || doc._deleted || fingerprint(docData(doc)) !== pending.document) return false;
        const r = doc._revisions;
        if (!r) return false;
        return pending.baseRevision ? r.ids.length >= 2 && `${r.start - 1}-${r.ids[1]}` === pending.baseRevision : r.start === 1;
    }
    async function rememberOwner(path, key, doc) {
        const old = await localDoc(ownerId(path));
        if (old && old.sourceKey !== key) fail('layout_source_collision');
        await saveLocal(ownerId(path), { sourceKey: key, revision: doc._rev, document: fingerprint(docData(doc)) });
    }
    let layoutSerial = Promise.resolve();
    function serializedLayout(run) {
        const result = layoutSerial.then(run);
        layoutSerial = result.catch(() => {});
        return result;
    }
    async function layoutPublish(request, epoch, preflight = false) {
        requireControls();
        if (Object.keys(request).some(k => !['protocol', 'epoch', 'method', 'operationId', 'previous', 'path'].includes(k))) fail('unknown_field');
        const a = validate(request.previous);
        if (a.kind !== 'binding' || a.entryKind || a.deleted || a.previousPath !== undefined) fail('invalid_layout');
        if (!(preflight && request.operationId === undefined) &&
            (!token(request.operationId) || !request.operationId.startsWith('layout_'))) fail('invalid_operation_id');
        const suffix = a.path.slice(a.collectionRoot.length);
        if (typeof request.path !== 'string' || !request.path.endsWith(suffix) ||
            !(relativePath(request.path) || !a.writable && referencePath(request.path)) ||
            !collectionRoot(request.path.slice(0, -suffix.length)) || request.path === a.path) fail('invalid_layout_path');
        const key = sourceKey(a), id = await core.services.path.path2id(request.path);
        const receiptId = request.operationId ? `_local/loom-layout-publication-${request.operationId}` : null;
        const fp = fingerprint([epoch, a, request.path]);
        let pending = receiptId ? await localDoc(receiptId) : null;
        if (pending && pending.fingerprint !== fp) fail('operation_id_reused');
        if (pending?.result) return preflight ? { status: 'ready', outcome: 'published' } : pending.result;
        let target = await rawLeaves(request.path);
        if (pending && target.length === 1 && branchMatches(target[0], pending)) {
            if (!await exactSource(a)) return hold('layout_previous_changed');
            if (preflight) return { status: 'ready', outcome: 'recoverable' };
            await rememberOwner(request.path, key, target[0]);
            const result = { status: 'published', id, revision: target[0]._rev, sha256: a.sha256.slice(7) };
            await saveLocal(receiptId, { ...pending, result });
            return result;
        }
        const source = await exactSource(a);
        if (!source) return hold('layout_previous_changed');
        const clone = { ...docData(source), path: request.path };
        let baseRevision;
        if (target.length) {
            const owner = await localDoc(ownerId(request.path)), at = target[0];
            if (target.length !== 1 || !at || at._deleted || !owner || owner.sourceKey !== key ||
                at._rev !== owner.revision || fingerprint(docData(at)) !== owner.document)
                return hold('layout_destination_collision');
            baseRevision = at._rev;
        }
        const expected = fingerprint(clone);
        if (pending && (pending.document !== expected || pending.baseRevision !== (baseRevision ?? null)))
            return hold('layout_publication_ambiguous');
        // Read-only refusal/recovery probe. Publication repeats all checks, and
        // canonical source admission remains the Go owner's responsibility.
        if (preflight) return { status: 'ready' };
        if (!pending) {
            pending = { fingerprint: fp, document: expected, baseRevision: baseRevision ?? null };
            await saveLocal(receiptId, pending);
        }
        await rememberOwner(a.path, key, source);
        // Native putRaw passes through the existing codec/encryption transforms.
        // CAS metadata only: preserve children/eden IDs without chunk reads or a
        // forced branch write. A concurrent target write causes a conflict hold.
        let written;
        try { written = await native.putRaw({ ...clone, _id: id, ...(baseRevision ? { _rev: baseRevision } : {}) }); }
        catch (e) { if (e.status === 409) return hold('layout_destination_collision'); throw e; }
        target = await rawLeaves(request.path);
        if (target.length !== 1 || target[0]?._rev !== written.rev || !branchMatches(target[0], pending))
            return hold('layout_publication_ambiguous');
        if (!await exactSource(a)) return hold('layout_previous_changed');
        await rememberOwner(request.path, key, target[0]);
        const result = { status: 'published', id, revision: written.rev, sha256: a.sha256.slice(7) };
        await saveLocal(receiptId, { ...pending, result });
        return result;
    }
    async function layoutRetire(request, epoch) {
        requireControls();
        if (Object.keys(request).some(k => !['protocol', 'epoch', 'method', 'layout'].includes(k))) fail('unknown_field');
        const l = validate(request.layout);
        if (l.kind !== 'layout') fail('invalid_layout');
        const a = l.previous, b = l.binding;
        const receiptId = `_local/loom-layout-retirement-${l.id}`, fp = fingerprint([epoch, l]);
        let pending = await localDoc(receiptId);
        if (pending && pending.fingerprint !== fp) fail('operation_id_reused');
        if (pending?.result) return pending.result;
        if (!await authentic(l) || !await authentic(a) || !await authentic(b)) return hold('layout_controls_pending');
        if (a.entryKind === 'directory') {
            const result = { status: 'published', revision: a.nativeRevision, sha256: a.sha256.slice(7) };
            await saveLocal(receiptId, { fingerprint: fp, result });
            return result;
        }
        let old = (await rawLeaves(a.path)).filter(d => !d?._deleted);
        if (pending && old.length === 1 && branchMatches(old[0], pending)) {
            await rememberOwner(a.path, sourceKey(a), old[0]);
            const result = { status: 'published', revision: old[0]._rev, sha256: a.sha256.slice(7) };
            await saveLocal(receiptId, { ...pending, result });
            return result;
        }
        if (!await exactSource(b)) return hold('layout_target_changed');
        const source = await exactSource(a);
        if (!source) return hold('layout_previous_changed');
        const tombstone = { ...docData(source), deleted: true };
        if (!pending) {
            pending = { fingerprint: fp, baseRevision: a.nativeRevision, document: fingerprint(tombstone) };
            await saveLocal(receiptId, pending);
        }
        // Never physically delete a branch or force a child under a stale base.
        let written;
        try { written = await native.putRaw({ ...tombstone, _id: source._id, _rev: a.nativeRevision }); }
        catch (e) { if (e.status === 409) return hold('layout_previous_changed'); throw e; }
        old = (await rawLeaves(a.path)).filter(d => !d?._deleted);
        if (old.length !== 1 || old[0]?._rev !== written.rev || !branchMatches(old[0], pending)) return hold('layout_retirement_ambiguous');
        await rememberOwner(a.path, sourceKey(a), old[0]);
        const result = { status: 'published', revision: written.rev, sha256: a.sha256.slice(7) };
        await saveLocal(receiptId, { ...pending, result });
        return result;
    }

    async function sourceMoveStatus(request,epoch) {
        requireControls();
        if(Object.keys(request).some(k=>!['protocol','epoch','method','layout'].includes(k)))fail('unknown_field');
        const l=validate(request.layout);
        if(l.kind!=='layout'||l.sourceMove!==true)fail('invalid_source_move');
        const pending=await localDoc(`_local/loom-source-move-${l.id}`);
        if(pending&&pending.fingerprint!==fingerprint([epoch,l]))fail('operation_id_reused');
        if(pending?.result)return pending.result;
        const meta=await files.fetchEntryMeta(controlPath(l),undefined,true);
        if(meta) {
            const actual=await controls.get(controlPath(l),meta._rev);
            if(fingerprint(actual)!==fingerprint(l))fail('control_collision');
            return {status:'pending',phase:'published'};
        }
        return {status:'pending',phase:pending?.phase??'unpublished'};
    }

    async function sourceMoveApply(request, epoch) {
        requireControls();
        if (Object.keys(request).some(k => !['protocol','epoch','method','layout'].includes(k))) fail('unknown_field');
        const l=validate(request.layout);
        if(l.kind!=='layout'||l.sourceMove!==true)fail('invalid_source_move');
        const receiptId=`_local/loom-source-move-${l.id}`, fp=fingerprint([epoch,l]);
        let pending=await localDoc(receiptId);
        if(pending&&pending.fingerprint!==fp)fail('operation_id_reused');
        if(pending?.result)return pending.result;
        const observed=await sourceMoveStatus(request,epoch);
        if(observed.phase!=='published'&&observed.phase!=='retired') {
            if(!await exactSource(l.previous))return {...hold('layout_previous_changed'),phase:'unpublished',detail:await sourceDiagnostic(l.previous)};
            if(!await exactSource(l.binding))return {...hold('layout_target_changed'),phase:'unpublished',detail:await sourceDiagnostic(l.binding)};
        }
        if(!pending) {
            pending={fingerprint:fp,phase:'prepared'};
            await saveLocal(receiptId,pending);
        }
        // Replay the exact accepted control after an uncertain IPC outcome.
        // No new source comparison may silently abandon a published move.
        await controls.put(l);
        if(pending.phase!=='published') {
            pending={...pending,phase:'published'};await saveLocal(receiptId,pending);
        }
        const result=await layoutRetire({protocol:PROTOCOL,epoch,method:'layout.retire',layout:l},epoch);
        if(result.status!=='published')return {...result,phase:'published'};
        const completed={...result,phase:'retired'};
        await saveLocal(receiptId,{...pending,result:completed});
        return completed;
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
                    requireControls();
                    if (!relativePath(request.path) && !referencePath(request.path)) fail('invalid_path');
                    if (!validRev(request.revision)) fail('exact_revision_required');
                    try {
                        const exact = await read({ id: await core.services.path.path2id(request.path), revision: request.revision });
                        if (relativePath(request.path) && !hasExactAncestry(exact)) {
                            const historical = await readRemoteHistory(core, request.path, exact.id, request.revision, metadata);
                            if (historical.status !== 'history') return historical;
                            return { ...exact, ...historical, status: exact.status };
                        }
                        return exact;
                    }
                    catch (error) {
                        if (error.status !== 404) throw error;
                        // Replication retains ancestry even when an intermediate
                        // body was never transferred. Content must come separately
                        // from the immutable per-intent payload, never a newer leaf.
                        const id = await core.services.path.path2id(request.path);
                        let branches;
                        try { branches = await leaves(id); }
                        catch (missing) { if (missing.status === 404) branches = []; else throw missing; }
                        const matches = branches.filter((b) => b.path === request.path && b.ancestors?.includes(request.revision));
                        if (!matches.length) {
                            if (!relativePath(request.path)) return hold('revision_unavailable');
                            return await readRemoteHistory(core, request.path, id, request.revision, metadata);
                        }
                        const chains = matches.map((b) => b.ancestors.slice(b.ancestors.indexOf(request.revision) + 1));
                        if (chains.some((c) => JSON.stringify(c) !== JSON.stringify(chains[0]))) return hold('ancestry_ambiguous');
                        const historical = { id, path: request.path, revision: request.revision, kind: 'file', status: 'history',
                            ancestors: chains[0], ancestryAvailable: true,
                            ancestryCompleteToRoot: matches.every((b) => b.ancestryCompleteToRoot) };
                        if (!hasExactAncestry(historical)) {
                            if (!relativePath(request.path)) return hold('revision_unavailable');
                            return await readRemoteHistory(core, request.path, id, request.revision, metadata);
                        }
                        return historical;
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
                    // Filtering system/chunk rows must not hide a raw continuation.
                    const hasMore = page.results.length >= limit || (typeof page.pending === 'number' && page.pending > 0);
                    const changes = [];
                    for (const row of page.results) {
                        const evidence = await leaves(row.id);
                        if (evidence.some((leaf) => leaf.kind !== 'system')) changes.push({ id: row.id, sequence: row.seq, leaves: evidence });
                    }
                    return { status: 'available', epoch, nextSequence: page.last_seq, hasMore, changes };
                }
                case 'publish': return await publish(request, epoch);
                case 'publish.reference': return await publish(request, epoch, true);
                case 'publish.reference.file': return await publish(request, epoch, true, true);
                case 'publication.preflight': return await publicationPreflight(request, epoch);
                case 'layout.publish': return await serializedLayout(() => layoutPublish(request, epoch));
                case 'layout.preflight': return await serializedLayout(() => layoutPublish(request, epoch, true));
                case 'layout.retire': return await serializedLayout(() => layoutRetire(request, epoch));
                case 'source.move': return await serializedLayout(() => sourceMoveApply(request, epoch));
                case 'source.move.status': return await serializedLayout(() => sourceMoveStatus(request,epoch));
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
