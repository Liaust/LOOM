import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { createRequire } from 'node:module';

const upstream = process.env.LOOM_SYNC_UPSTREAM;
if (!upstream) throw new Error('Set LOOM_SYNC_UPSTREAM to the prepared and built pinned source directory.');
const root = resolve(upstream);
const bundle = join(root, 'src/apps/cli/dist/index.cjs');
const require = createRequire(join(root, 'package.json'));
const { createNewVaultSettings } = await import(join(root, 'node_modules/@vrtmrz/livesync-commonlib/dist/settings.js'));
const b64 = (s) => Buffer.from(s).toString('base64');

function client(db, settings) {
    const child = spawn(process.execPath, [bundle, db, '--settings', settings], { stdio: ['pipe', 'pipe', 'pipe'] });
    const waiting = new Map();
    let count = 0, stderr = '';
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    createInterface({ input: child.stdout }).on('line', (line) => {
        let message;
        try { message = JSON.parse(line); } catch { throw new Error('Non-JSON native stdout: ' + line); }
        waiting.get(message.id)?.resolve(message.result); waiting.delete(message.id);
    });
    const exited = once(child, 'exit');
    child.on('exit', (code) => {
        for (const pending of waiting.values()) pending.reject(new Error(`Native process exited ${code}: ${stderr}`));
        waiting.clear();
    });
    return {
        async call(request) {
            const id = ++count;
            return new Promise((resolve, reject) => {
                waiting.set(id, { resolve, reject });
                child.stdin.write(JSON.stringify({ id, request: { protocol: 1, ...request } }) + '\n');
            });
        },
        async close() { child.stdin.end(); const [code] = await exited; assert.equal(code, 0, stderr); },
        kill() { child.kill('SIGTERM'); },
    };
}

test('native database entry: divergent exact-base writes, ancestry, replay, restart and holds', { timeout: 30000 }, async (t) => {
    const temp = await mkdtemp(join(tmpdir(), 'loom-sync-native-'));
    t.after(() => rm(temp, { recursive: true, force: true }));
    const dbRoot = join(temp, 'db'); await mkdir(dbRoot);
    const settingsPath = join(temp, 'settings.json');
    const settings = { ...createNewVaultSettings(), isConfigured: false, useIndexedDBAdapter: false,
        liveSync: false, syncOnStart: false, periodicReplication: false, syncOnSave: false,
        syncOnEditorSave: false, syncOnFileOpen: false, syncAfterMerge: false,
        deleteMetadataOfDeletedFiles: false, automaticallyDeleteMetadataOfDeletedFiles: 0 };
    await writeFile(settingsPath, JSON.stringify(settings));
    const originalSettings = await readFile(settingsPath, 'utf8');
    // A file in the database directory must not be scanned or mirrored.
    await writeFile(join(dbRoot, 'untouched.md'), 'not enrolled');
    let c = client(dbRoot, settingsPath); t.after(() => c.kill());
    const status = await c.call({ method: 'status' }); assert.equal(status.status, 'ready');
    assert.equal(status.upstreamRevision, '7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b');
    const epoch = status.epoch;
    const sync = await c.call({ method: 'sync', epoch });
    assert.equal(sync.status, 'replication');
    assert.notEqual(sync.outcome, 'completed');
    const call = (r) => c.call({ epoch, ...r });
    const initial = await call({ method: 'changes', since: 0 });
    assert.deepEqual(initial.changes, []);
    assert.equal((await call({ method: 'changes.wait', since: initial.nextSequence, timeoutMs: 10 })).status, 'idle');
    assert.equal((await call({ method: 'changes.wait', since: 'now', timeoutMs: 10 })).code, 'explicit_sequence_required');
    assert.equal((await call({ method: 'changes.wait', since: 0, timeoutMs: 6000 })).code, 'invalid_wait');
    const seed = { method: 'publish', operationId: 'seed', path: 'personal/note.md', create: true, contentBase64: b64('A') };
    const a = await call(seed); assert.equal(a.status, 'published', JSON.stringify(a));
    assert.equal((await call({ method: 'changes.wait', since: initial.nextSequence, timeoutMs: 100 })).status, 'changed');
    const b = await call({ method: 'publish', operationId: 'agent', path: seed.path, baseRevision: a.revision, contentBase64: b64('B') });
    const edit = { method: 'publish', operationId: 'offline', path: seed.path, baseRevision: a.revision, contentBase64: b64('C') };
    const cEdit = await call(edit); assert.equal(cEdit.status, 'published', JSON.stringify(cEdit));
    assert.notEqual(b.revision, cEdit.revision);
    const branches = await call({ method: 'leaves', id: a.id });
    assert.deepEqual(new Set(branches.leaves.map((v) => v.revision)), new Set([b.revision, cEdit.revision]));
    for (const [leaf, bytes] of [[b, 'B'], [cEdit, 'C']]) {
        const read = await call({ method: 'read', id: a.id, revision: leaf.revision });
        assert.equal(read.status, 'available', JSON.stringify(read));
        assert.equal(Buffer.from(read.contentBase64, 'base64').toString(), bytes);
        assert.ok(read.ancestors.includes(a.revision));
    }
    assert.deepEqual(await call(edit), cEdit);
    assert.equal((await call({ ...edit, contentBase64: b64('D') })).code, 'operation_id_reused');
    assert.equal((await call({ ...edit, operationId: 'missing', baseRevision: undefined })).reason, 'exact_base_required');
    assert.equal((await call({ ...edit, operationId: 'unknown', baseRevision: '99-missing' })).reason, 'base_unavailable');
    assert.equal((await call({ ...seed, operationId: 'again' })).reason, 'path_has_history');
    assert.equal((await call({ ...seed, operationId: 'traversal', path: '../x.md' })).code, 'invalid_path');
    for (const method of ['delete', 'rename']) assert.equal((await call({ method })).reason, 'client_intent_not_proven');
    assert.equal((await c.call({ method: 'changes', epoch: 'old', since: 0 })).reason, 'replica_epoch_mismatch');
    const changes = await call({ method: 'changes', since: initial.nextSequence, limit: 128 });
    assert.ok(changes.changes.some((row) => row.id === a.id && row.leaves.length === 2));
    await c.close();

    // Insert only into this owned, closed native replica to model a crash receipt
    // and a native logical tombstone; no extra service or replication engine.
    const PouchDB = require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const entries = await readdir(dbRoot);
    const databaseDir = entries.find((v) => v.endsWith('-livesync-v2'));
    assert.ok(databaseDir, entries.join(','));
    const db = new PouchDB(join(dbRoot, databaseDir), { adapter: 'leveldb' });
    const receipt = await db.get('_local/loom-publication-offline');
    delete receipt.result; receipt.state = 'pending'; await db.put(receipt);
    const doc = await db.get(a.id, { rev: cEdit.revision });
    const deletion = await db.put({ ...doc, deleted: true }, { force: true });
    const other = await db.get(a.id, { rev: b.revision });
    const branchDeletion = await db.put({ ...other, _deleted: true });
    await db.close();

    c = client(dbRoot, settingsPath);
    assert.equal((await c.call({ method: 'status' })).epoch, epoch);
    assert.equal((await call(edit)).reason, 'publication_outcome_unknown');
    const tombstone = await call({ method: 'read', id: a.id, revision: deletion.rev });
    assert.equal(tombstone.logicalDeleted, true);
    assert.equal(tombstone.disposition, 'held_ambiguous_deletion');
    assert.equal(tombstone.status, 'held');
    const branchLeaves = await call({ method: 'leaves', id: a.id });
    assert.ok(branchLeaves.leaves.some((v) => v.revision === branchDeletion.rev && v.branchDeleted));
    const resumed = await call({ method: 'changes', since: changes.nextSequence });
    assert.ok(resumed.changes.some((row) => row.leaves.some((v) => v.revision === deletion.rev)));
    await c.close();
    assert.equal(await readFile(join(dbRoot, 'untouched.md'), 'utf8'), 'not enrolled');
    assert.equal(await readFile(settingsPath, 'utf8'), originalSettings);
    assert.ok(!(await readdir(dbRoot)).includes('personal'));
});

test('encrypted native controls: exact IPC reads, replay, collisions and hook requirement', { timeout: 30000 }, async (t) => {
    const temp = await mkdtemp(join(tmpdir(), 'loom-sync-controls-'));
    t.after(() => rm(temp, { recursive: true, force: true }));
    const dbRoot = join(temp, 'db'); await mkdir(dbRoot);
    const settingsPath = join(temp, 'settings.json');
    const settings = { ...createNewVaultSettings(), isConfigured: false, useIndexedDBAdapter: false,
        encrypt: true, passphrase: 'ephemeral-native-test-only',
        liveSync: false, syncOnStart: false, periodicReplication: false, syncOnSave: false,
        syncOnEditorSave: false, syncOnFileOpen: false, syncAfterMerge: false,
        deleteMetadataOfDeletedFiles: false, automaticallyDeleteMetadataOfDeletedFiles: 0 };
    await writeFile(settingsPath, JSON.stringify(settings));
    const c = client(dbRoot, settingsPath); t.after(() => c.kill());
    const status = await c.call({ method: 'status' });
    assert.equal(status.encryptionEnabled, true); assert.equal(status.clientIntentVersion, 1);
    const call = r => c.call({ epoch: status.epoch, ...r });
    const exportResult = await call({ method:'publish', operationId:'export', path:'Notes/a.md', create:true, contentBase64:b64('A') });
    const largeText = 'a'.repeat(1200000);
    const largeTextResult = await call({ method:'publish', operationId:'large_markdown', path:'Notes/large.md', create:true, contentBase64:b64(largeText) });
    assert.equal(largeTextResult.status,'published',JSON.stringify(largeTextResult));
    const largeTextRead = await call({ method:'read.path', path:'Notes/large.md', revision:largeTextResult.revision });
    assert.equal(largeTextRead.status,'available',JSON.stringify(largeTextRead));
    assert.equal(Buffer.from(largeTextRead.contentBase64,'base64').toString(),largeText);
    for (const extension of ['canvas','wav','svg','csv','xlsx','dat','json']) {
        const reference = await call({ method:'publish.reference', operationId:`reference_${extension}`, path:`Notes/attachment.${extension}`, create:true, contentBase64:b64(Buffer.from([0,255,42])) });
        assert.equal(reference.status,'published',JSON.stringify(reference));
        assert.equal((await call({ method:'publish', operationId:`write_${extension}`, path:`Notes/attachment.${extension}`, baseRevision:reference.revision, contentBase64:b64('changed') })).code,'invalid_path');
    }
    const binding = {version:1,kind:'binding',id:'b',workspace:'w',collection:'c',generation:'g',fileId:'file',collectionRoot:'Notes',path:'Notes/a.md',sourceBase:'exact_source_base',nativeRevision:exportResult.revision,sha256:'sha256:'+exportResult.sha256,writable:true};
    const put = await call({method:'control.put',contentBase64:b64(JSON.stringify(binding))});
    const resolutionPayload = {method:'payload.put',intentId:'operator-choice',contentBase64:b64('reviewed operator text')};
    const payloadReceipt = await call(resolutionPayload);
    assert.equal(payloadReceipt.status,'published');
    assert.deepEqual(await call(resolutionPayload),payloadReceipt);
    assert.notEqual((await call({...resolutionPayload,contentBase64:b64('different')})).status,'published');
    const payloadRead = await call({method:'payload.read',intentId:'operator-choice'});
    assert.equal(Buffer.from(payloadRead.contentBase64,'base64').toString(),'reviewed operator text');
    assert.equal((await call({...resolutionPayload,intentId:'../escape'})).code,'invalid_intent_id');
    assert.equal(put.status,'published',JSON.stringify(put));
    assert.deepEqual(await call({method:'control.put',contentBase64:b64(JSON.stringify(binding))}),put);
    assert.notEqual((await call({method:'control.put',contentBase64:b64(JSON.stringify({...binding,sourceBase:'other'}))})).status,'published');
    const changes = await call({method:'changes',since:0,limit:128});
    const leaf = changes.changes.flatMap(r=>r.leaves).find(l=>l.path===put.path);
    assert.equal(leaf.kind,'control');
    const read = await call({method:'control.read',id:leaf.id,revision:put.revision});
    assert.equal(read.status,'available',JSON.stringify(read));assert.deepEqual(JSON.parse(Buffer.from(read.contentBase64,'base64').toString()),binding);
    const exact = await call({method:'read.path',path:'Notes/a.md',revision:exportResult.revision});
    assert.equal((await call({method:'read.path',path:'Notes/a.md',revision:'99-missing'})).reason,'revision_unavailable');
    assert.equal(exact.status,'available');assert.equal(Buffer.from(exact.contentBase64,'base64').toString(),'A');
    const binary = Buffer.from([137,80,78,71,0,255]);
    const refRequest = { method:'publish.reference', operationId:'binary', path:'Notes/image.png', create:true, contentBase64:b64(binary) };
    const ref = await call(refRequest);
    assert.equal(ref.status,'published',JSON.stringify(ref));
    assert.deepEqual(await call(refRequest),ref);
    const refBinding = {...binding,id:'binary_binding',fileId:'binary_file',path:refRequest.path,nativeRevision:ref.revision,sha256:'sha256:'+ref.sha256,writable:false};
    assert.equal((await call({method:'control.put',contentBase64:b64(JSON.stringify(refBinding))})).status,'published');
    assert.notEqual((await call({method:'control.put',contentBase64:b64(JSON.stringify({...refBinding,id:'badbinary',writable:true}))})).status,'published');
    assert.equal((await call({...refRequest,method:'publish'})).code,'invalid_path');
    const large = await call({...refRequest,operationId:'large_reference',path:'Notes/large.pdf',contentBase64:b64(Buffer.alloc(8*1024*1024+1,255))});
    assert.equal(large.status,'published',JSON.stringify(large));
    const largeLeaves = await call({method:'leaves',id:large.id});
    assert.equal(largeLeaves.leaves[0].size,8*1024*1024+1);
    for (const contentBase64 of ['!!!!', 'YWJj\n', 'Zh==', 'Zg']) {
        assert.equal((await call({...refRequest,operationId:'invalid_base64',contentBase64})).code,'invalid_content');
    }
    assert.equal((await call({method:'read.path',path:put.path,revision:put.revision})).code,'invalid_path');
    await c.close();assert.ok(!(await readdir(dbRoot)).includes('LOOM-Control-v1'));
    // Model the actual replication result: a leaf with a known but absent
    // ancestor body, plus the immutable payload retained as a separate leaf.
    const PouchDB = require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const databaseDir = (await readdir(dbRoot)).find(v => v.endsWith('-livesync-v2'));
    const db = new PouchDB(join(dbRoot, databaseDir), { adapter:'leveldb' });
    const original = await db.get(exportResult.id);
    await db.bulkDocs([
        {...original, _rev:'3-later', _revisions:{start:3,ids:['later','earlier',original._rev.split('-')[1]]}},
        {...original, _id:leaf.id.replace('/binding/b.md','/payload/offline.md'), path:'LOOM-Control-v1/payload/offline.md', _rev:'1-payload', _revisions:{start:1,ids:['payload']}},
    ], {new_edits:false});
    await db.close();
    const resumed = client(dbRoot, settingsPath); t.after(() => resumed.kill());
    const next = r => resumed.call({epoch:status.epoch,...r});
    const history = await next({method:'read.path',path:'Notes/a.md',revision:'2-earlier'});
    assert.equal(history.status,'history',JSON.stringify(history));
    assert.deepEqual(history.ancestors,[exportResult.revision]); assert.equal(history.ancestryCompleteToRoot,true);
    assert.equal((await next({method:'read.path',path:'Notes/a.md',revision:'1-unrelated'})).reason,'revision_unavailable');
    const retained = await next({method:'payload.read',intentId:'offline'});
    assert.equal(retained.status,'available',JSON.stringify(retained));
    assert.equal(Buffer.from(retained.contentBase64,'base64').toString(),'A');
    assert.equal((await next({method:'payload.read',intentId:'missing'})).reason,'payload_unavailable');
    assert.equal((await next({method:'payload.read',intentId:'../escape'})).code,'invalid_intent_id');
    await resumed.close();
});
