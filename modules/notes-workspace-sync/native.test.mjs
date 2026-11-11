import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';

const upstream = process.env.LOOM_SYNC_UPSTREAM;
if (!upstream) throw new Error('Set LOOM_SYNC_UPSTREAM to the prepared and built pinned source directory.');
const root = resolve(upstream);
const bundle = join(root, 'src/apps/cli/dist/index.cjs');
const require = createRequire(join(root, 'package.json'));
const { createNewVaultSettings } = await import(join(root, 'node_modules/@vrtmrz/livesync-commonlib/dist/settings.js'));
const b64 = (s) => Buffer.from(s).toString('base64');

function client(db, settings, onHint) {
    const entry = process.env.LOOM_SYNC_SOURCE_TEST === '1' ?
        ['--import', require.resolve('tsx/esm'), join(root, 'src/apps/cli/loom-adapter/entrypoint.ts')] : [bundle];
    const child = spawn(process.execPath, [...entry, db, '--settings', settings], { cwd: root, stdio: ['pipe', 'pipe', 'pipe', ...(onHint ? ['pipe'] : [])], env: {...process.env, ...(onHint ? {LOOM_NOTES_WAKE_FD:'3'} : {})} });
    if (onHint) child.stdio[3].on('data', onHint);
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

test('source export adopts only an identical sole live text leaf without rewriting it', { timeout: 60000 }, async t => {
    const temp = await mkdtemp(join(tmpdir(), 'loom-adopt-native-'));
    t.after(() => rm(temp, { recursive: true, force: true }));
    const dbRoot = join(temp, 'db'); await mkdir(dbRoot);
    const settingsPath = join(temp, 'settings.json');
    await writeFile(settingsPath, JSON.stringify({ ...createNewVaultSettings(), isConfigured: false, useIndexedDBAdapter: false,
        encrypt: true, passphrase: 'ephemeral-adopt-only', liveSync: false, syncOnStart: false, periodicReplication: false }));
    const c = client(dbRoot, settingsPath); t.after(() => c.kill());
    const { epoch } = await c.call({ method: 'status' });
    const call = request => c.call({ epoch, ...request });
    const seed = { method: 'publish', operationId: 'adopt_seed', path: 'Notes/recovered.md', create: true, contentBase64: b64('saved note') };
    const original = await call(seed); assert.equal(original.status, 'published');
    assert.equal((await call({ ...seed, operationId: 'ordinary_create' })).reason, 'path_has_history');
    const request = { ...seed, operationId: 'source_export', adoptIdentical: true };
    const adopted = await call(request);
    assert.equal(adopted.status, 'published', JSON.stringify(adopted));
    assert.equal(adopted.revision, original.revision); assert.equal(adopted.sha256, original.sha256);
    assert.deepEqual(await call(request), adopted);
    assert.equal((await call({ ...request, adoptIdentical: undefined })).code, 'operation_id_reused');
    assert.equal((await call({ ...request, operationId: 'wrong_bytes', contentBase64: b64('different') })).reason, 'path_has_history');
    assert.equal((await call({ ...request, operationId: 'wrong_kind', create: false, baseRevision: original.revision })).code, 'invalid_adoption');
    await call({ method: 'publish', operationId: 'branch_one', path: seed.path, baseRevision: original.revision, contentBase64: b64('left') });
    await call({ method: 'publish', operationId: 'branch_two', path: seed.path, baseRevision: original.revision, contentBase64: b64('right') });
    assert.equal((await call({ ...request, operationId: 'conflicted', contentBase64: b64('left') })).reason, 'path_has_history');
    const leaves = await call({ method: 'leaves', id: original.id });
    assert.equal(leaves.leaves.length, 2);
    await c.close();
});

test('reviewed reference move preserves binary chunks and retires only the exact old leaf', { timeout: 60000 }, async t => {
    const temp=await mkdtemp(join(tmpdir(),'loom-reference-move-native-'));t.after(()=>rm(temp,{recursive:true,force:true}));
    const dbRoot=join(temp,'db');await mkdir(dbRoot);const settings=join(temp,'settings.json');
    await writeFile(settings,JSON.stringify({...createNewVaultSettings(),isConfigured:false,useIndexedDBAdapter:false,
        encrypt:true,passphrase:'ephemeral-reference-move',liveSync:false,syncOnStart:false,periodicReplication:false}));
    const c=client(dbRoot,settings);t.after(()=>c.kill());const {epoch}=await c.call({method:'status'});
    const call=request=>c.call({epoch,...request});
    const a=await call({method:'publish.reference',operationId:'ref_old',path:'Notes/old.pdf',create:true,contentBase64:b64('binary\x00PDF')});
    const b=await call({method:'publish.reference',operationId:'ref_new',path:'Library/new.pdf',create:true,contentBase64:b64('binary\x00PDF')});
    assert.equal(a.status,'published',JSON.stringify(a));assert.equal(b.status,'published',JSON.stringify(b));
    const previous={version:1,kind:'binding',id:'ref_old_binding',workspace:'w',collection:'notes',generation:'g',fileId:'ref_old_file',
        collectionRoot:'Notes',path:'Notes/old.pdf',sourceBase:'ref_old_base',nativeRevision:a.revision,sha256:'sha256:'+a.sha256,writable:false,sourceSequence:1};
    const binding={...previous,id:'ref_new_binding',collection:'library',fileId:'ref_new_file',collectionRoot:'Library',path:'Library/new.pdf',sourceBase:'ref_new_base',nativeRevision:b.revision};
    for(const control of [previous,binding])assert.equal((await call({method:'control.put',contentBase64:b64(JSON.stringify(control))})).status,'published');
    const layout={version:1,kind:'layout',id:'layout_source_move_reference',sourceMove:true,previous,binding};
    const result=await call({method:'source.move',layout});assert.equal(result.phase,'retired',JSON.stringify(result));
    assert.deepEqual(await call({method:'source.move',layout}),result);
    const leaves=await call({method:'leaves',id:a.id});assert.equal(leaves.leaves.length,1);assert.equal(leaves.leaves[0].logicalDeleted,true);
    const next=await call({method:'leaves',id:b.id});assert.equal(next.leaves[0].revision,b.revision);assert.equal(a.sha256,b.sha256);
    await c.close();
});

test('source move publication has durable phases and replays retirement after restart', { timeout: 60000 }, async t => {
    const temp=await mkdtemp(join(tmpdir(),'loom-source-move-native-'));
    t.after(()=>rm(temp,{recursive:true,force:true}));
    const dbRoot=join(temp,'db');await mkdir(dbRoot);
    const settings=join(temp,'settings.json');
    await writeFile(settings,JSON.stringify({...createNewVaultSettings(),isConfigured:false,useIndexedDBAdapter:false,
        encrypt:true,passphrase:'ephemeral-source-move-only',liveSync:false,syncOnStart:false,periodicReplication:false}));
    let c=client(dbRoot,settings);t.after(()=>c.kill());
    const {epoch}=await c.call({method:'status'});
    const call=request=>c.call({epoch,...request});
    const a=await call({method:'publish',operationId:'old_seed',path:'Notes/old.md',create:true,contentBase64:b64('original index')});
    const b=await call({method:'publish',operationId:'new_seed',path:'Projects/Book/Notes/new.md',create:true,contentBase64:b64('corrected links')});
    const previous={version:1,kind:'binding',id:'old_binding',workspace:'w',collection:'personal',generation:'old_generation',fileId:'old_file',
        collectionRoot:'Notes',path:'Notes/old.md',sourceBase:'old_base',nativeRevision:a.revision,sha256:'sha256:'+a.sha256,writable:true,sourceSequence:40};
    const binding={...previous,id:'new_binding',collection:'book',generation:'new_generation',fileId:'new_file',collectionRoot:'Projects/Book/Notes',
        path:'Projects/Book/Notes/new.md',sourceBase:'new_base',nativeRevision:b.revision,sha256:'sha256:'+b.sha256,sourceSequence:1};
    for(const control of [previous,binding])assert.equal((await call({method:'control.put',contentBase64:b64(JSON.stringify(control))})).status,'published');
    const layout={version:1,kind:'layout',id:'layout_source_move_test',sourceMove:true,previous,binding};
    const initial=await call({method:'source.move.status',layout});assert.equal(initial.phase,'unpublished',JSON.stringify(initial));
    const result=await call({method:'source.move',layout});
    assert.equal(result.status,'published',JSON.stringify(result));assert.equal(result.phase,'retired');
    assert.deepEqual(await call({method:'source.move',layout}),result);
    await c.close();
    const PouchDB=require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const databaseDir=(await readdir(dbRoot)).find(v=>v.endsWith('-livesync-v2'));
    const db=new PouchDB(join(dbRoot,databaseDir),{adapter:'leveldb'});
    const receipt=await db.get('_local/loom-source-move-'+layout.id);
    delete receipt.result;receipt.phase='prepared';await db.put(receipt);
    await db.close();
    c=client(dbRoot,settings);await c.call({method:'status'});
    assert.equal((await call({method:'source.move.status',layout})).phase,'published');
    assert.deepEqual(await call({method:'source.move',layout}),result);
    assert.deepEqual(await call({method:'source.move.status',layout}),result);
    assert.deepEqual(await call({method:'source.move',layout}),result);
    const destination=await call({method:'read',id:b.id,revision:b.revision});
    assert.equal(destination.sha256,b.sha256);assert.equal(Buffer.from(destination.contentBase64,'base64').toString(),'corrected links');
    const retired=await call({method:'leaves',id:a.id});assert.equal(retired.leaves.length,1);assert.equal(retired.leaves[0].logicalDeleted,true);
    const old2=await call({method:'publish',operationId:'old_second',path:'Notes/pending.md',create:true,contentBase64:b64('original')});
    const new2=await call({method:'publish',operationId:'new_second',path:'Projects/Book/Notes/pending.md',create:true,contentBase64:b64('original')});
    const previous2={...previous,id:'second_previous',path:'Notes/pending.md',nativeRevision:old2.revision,sha256:'sha256:'+old2.sha256};
    const binding2={...binding,id:'second_destination',path:'Projects/Book/Notes/pending.md',nativeRevision:new2.revision,sha256:'sha256:'+new2.sha256};
    for(const control of [previous2,binding2])await call({method:'control.put',contentBase64:b64(JSON.stringify(control))});
    const second={...layout,id:'layout_source_move_prepared',previous:previous2,binding:binding2};
    await call({method:'publish',operationId:'pending_edit',path:previous2.path,baseRevision:old2.revision,contentBase64:b64('pending device edit')});
    await c.close();
    const database=new PouchDB(join(dbRoot,databaseDir),{adapter:'leveldb'});
    const canonical=value=>JSON.stringify(value,(_,v)=>v&&typeof v==='object'&&!Array.isArray(v)?Object.fromEntries(Object.entries(v).sort(([a],[b])=>a.localeCompare(b))):v);
    await database.put({_id:'_local/loom-source-move-'+second.id,phase:'prepared',fingerprint:createHash('sha256').update(canonical([epoch,second])).digest('hex')});
    await database.close();c=client(dbRoot,settings);await c.call({method:'status'});
    assert.equal((await call({method:'source.move.status',layout:second})).phase,'prepared');
    const refusal=await call({method:'source.move',layout:second});
    assert.equal(refusal.reason,'layout_previous_changed',JSON.stringify(refusal));assert.equal(refusal.phase,'unpublished');
    assert.match(refusal.detail, /^leaves=1;active=1;expected=false;expectedDeleted=false;control=true$/);
    assert.equal((await call({method:'source.move.status',layout:second})).phase,'prepared');
    await c.close();
});

test('source move preserves deleted conflict history beside its sole live branch', { timeout: 60000 }, async t => {
    const temp=await mkdtemp(join(tmpdir(),'loom-source-move-history-'));
    t.after(()=>rm(temp,{recursive:true,force:true}));
    const dbRoot=join(temp,'db');await mkdir(dbRoot);
    const settings=join(temp,'settings.json');
    await writeFile(settings,JSON.stringify({...createNewVaultSettings(),isConfigured:false,useIndexedDBAdapter:false,
        encrypt:true,passphrase:'ephemeral-history-only',liveSync:false,syncOnStart:false,periodicReplication:false}));
    let c=client(dbRoot,settings);t.after(()=>c.kill());
    const {epoch}=await c.call({method:'status'}),call=request=>c.call({epoch,...request});
    const seed=await call({method:'publish',operationId:'history_seed',path:'Notes/history.md',create:true,contentBase64:b64('base')});
    const current=await call({method:'publish',operationId:'history_current',path:'Notes/history.md',baseRevision:seed.revision,contentBase64:b64('reviewed')});
    const other=await call({method:'publish',operationId:'history_other',path:'Notes/history.md',baseRevision:seed.revision,contentBase64:b64('retained alternative')});
    const target=await call({method:'publish',operationId:'history_target',path:'Library/history.md',create:true,contentBase64:b64('reviewed')});
    await c.close();
    const PouchDB=require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const directory=(await readdir(dbRoot)).find(v=>v.endsWith('-livesync-v2'));
    const db=new PouchDB(join(dbRoot,directory),{adapter:'leveldb'});
    await db.remove(seed.id,other.revision);await db.close();
    c=client(dbRoot,settings);await c.call({method:'status'});
    const previous={version:1,kind:'binding',id:'history_previous',workspace:'w',collection:'personal',generation:'g',fileId:'file',
        collectionRoot:'Notes',path:'Notes/history.md',sourceBase:'base',nativeRevision:current.revision,sha256:'sha256:'+current.sha256,writable:true,sourceSequence:3};
    const binding={...previous,id:'history_next',collection:'library',fileId:'next',collectionRoot:'Library',path:'Library/history.md',nativeRevision:target.revision,sourceSequence:1};
    for(const control of [previous,binding])await call({method:'control.put',contentBase64:b64(JSON.stringify(control))});
    const layout={version:1,kind:'layout',id:'layout_history_retirement',sourceMove:true,previous,binding};
    const result=await call({method:'source.move',layout});
    assert.equal(result.phase,'retired',JSON.stringify(result));
    const old=await call({method:'leaves',id:seed.id});
    assert.equal(old.leaves.length,2);
    assert.equal(old.leaves.filter(l=>l.branchDeleted).length,1);
    assert.equal(old.leaves.filter(l=>l.logicalDeleted&&!l.branchDeleted).length,1);
    assert.deepEqual(await call({method:'source.move',layout}),result);
    await c.close();
});

test('native layout metadata clone: exact controls, binary chunks, interrupted replay, collision, retirement and reversal', { timeout: 60000 }, async t => {
    const temp = await mkdtemp(join(tmpdir(), 'loom-layout-native-'));
    t.after(() => rm(temp, { recursive: true, force: true }));
    const dbRoot = join(temp, 'db'); await mkdir(dbRoot);
    const settingsPath = join(temp, 'settings.json');
    await writeFile(settingsPath, JSON.stringify({ ...createNewVaultSettings(), isConfigured: false, useIndexedDBAdapter: false,
        encrypt: true, passphrase: 'ephemeral-layout-only', liveSync: false, syncOnStart: false, periodicReplication: false,
        syncOnSave: false, syncOnEditorSave: false, syncOnFileOpen: false, syncAfterMerge: false,
        deleteMetadataOfDeletedFiles: false, automaticallyDeleteMetadataOfDeletedFiles: 0 }));
    let c = client(dbRoot, settingsPath); t.after(() => c.kill());
    const { epoch } = await c.call({ method: 'status' });
    const call = request => c.call({ epoch, ...request });
    const put = record => call({ method: 'control.put', contentBase64: b64(JSON.stringify(record)) });
    const b = (id, path, result, writable = true) => ({ version: 1, kind: 'binding', id, workspace: 'w', collection: 'c',
        generation: 'g', fileId: id, collectionRoot: 'Projects/Test', path, sourceBase: 'source',
        nativeRevision: result.revision, sha256: 'sha256:' + result.sha256, writable, sourceSequence: 1 });
    const seed = await call({ method: 'publish', operationId: 'layout_seed', path: 'Projects/Test/a.md', create: true, contentBase64: b64('original') });
    const a = b('text', 'Projects/Test/a.md', seed); assert.equal((await put(a)).status, 'published');
    const move = { method: 'layout.publish', operationId: 'layout_text', previous: a, path: 'Projects/_Passive/Test/a.md' };
    const preflight = { ...move, method: 'layout.preflight' };
    assert.deepEqual(await call(preflight), { status: 'ready' });
    assert.deepEqual(await call(preflight), { status: 'ready' });
    assert.equal((await call({ method: 'leaves', id: seed.id })).leaves[0].revision, seed.revision);
    const published = await call(move); assert.equal(published.status, 'published', JSON.stringify(published));
    assert.deepEqual(await call(preflight), { status: 'ready', outcome: 'published' });
    assert.equal(published.sha256, seed.sha256); assert.deepEqual(await call(move), published);
    assert.equal((await call({ ...move, path: 'Projects/_Passive/Test/b.md' })).code, 'invalid_layout_path');
    assert.equal((await call({ ...move, previous: { ...a, sourceBase: 'other' } })).code, 'operation_id_reused');
    const next = { ...a, id: 'layout_text_next', path: move.path, collectionRoot: 'Projects/_Passive/Test', nativeRevision: published.revision, sourceSequence: 2 };
    const l = { version: 1, kind: 'layout', id: move.operationId, previous: a, binding: next };
    assert.equal((await call({ method: 'layout.retire', layout: l })).reason, 'layout_controls_pending');
    const nextControl = await put(next), layoutControl = await put(l);
    assert.equal(nextControl.status, 'published'); assert.equal(layoutControl.status, 'published');
    assert.equal(nextControl.path, 'LOOM-Control-v1/payload/layout/binding/layout_text_next.md');
    assert.equal(layoutControl.path, 'LOOM-Control-v1/payload/layout/layout/layout_text.md');
    const retired = await call({ method: 'layout.retire', layout: l }); assert.equal(retired.status, 'published', JSON.stringify(retired));
    assert.deepEqual(await call({ method: 'layout.retire', layout: l }), retired);
    assert.equal((await call({ method: 'leaves', id: seed.id })).leaves[0].logicalDeleted, true);
    const reverse = { method: 'layout.publish', operationId: 'layout_reverse', previous: next, path: a.path };
    const back = await call(reverse); assert.equal(back.status, 'published', JSON.stringify(back));
    assert.equal(back.sha256, seed.sha256);
    // A different source cannot reclaim a path merely because its bytes match.
    const alien = { ...next, id: 'alien', fileId: 'other' }; await put(alien);
    assert.equal((await call({ ...reverse, operationId: 'layout_alien', previous: alien })).reason, 'layout_destination_collision');
    const collisionSeed = await call({ method: 'publish', operationId: 'unmanaged', path: 'Other/a.md', create: true, contentBase64: b64('original') });
    assert.equal(collisionSeed.status, 'published');
    assert.equal((await call({ ...move, operationId: 'layout_collision', path: 'Other/a.md' })).reason, 'layout_previous_changed');
    assert.equal((await call({ ...reverse, operationId: 'layout_collision_new', path: 'Other/a.md' })).reason, 'layout_destination_collision');
    const bytes = Buffer.alloc(1024 * 1024, 42);
    const pdf = await call({ method: 'publish.reference', operationId: 'layout_pdf_seed', path: 'Projects/Test/book.pdf', create: true, contentBase64: b64(bytes) });
    const pdfBinding = b('pdf', 'Projects/Test/book.pdf', pdf, false); await put(pdfBinding);
    const pdfMove = { method: 'layout.publish', operationId: 'layout_pdf', previous: pdfBinding, path: 'Projects/_Passive/Test/book.pdf' };
    assert.deepEqual(await call({ ...pdfMove, method: 'layout.preflight', operationId: undefined }), { status: 'ready' });
    const pdfResult = await call(pdfMove); assert.equal(pdfResult.status, 'published', JSON.stringify(pdfResult));
    const branchSeed = await call({ method: 'publish', operationId: 'branch_seed', path: 'Projects/Test/branches.md', create: true, contentBase64: b64('base') });
    const branch = b('branched', 'Projects/Test/branches.md', branchSeed); await put(branch);
    await call({ method: 'publish', operationId: 'branch_a', path: branch.path, baseRevision: branch.nativeRevision, contentBase64: b64('first') });
    await call({ method: 'publish', operationId: 'branch_b', path: branch.path, baseRevision: branch.nativeRevision, contentBase64: b64('second') });
    assert.equal((await call({ method: 'layout.publish', operationId: 'layout_branches', previous: branch, path: 'Projects/_Passive/Test/branches.md' })).reason, 'layout_previous_changed');
    assert.equal((await call({ method: 'layout.preflight', previous: branch, path: 'Projects/_Passive/Test/branches.md' })).reason, 'layout_previous_changed');
    assert.equal((await call({ method: 'leaves', id: branchSeed.id })).leaves.length, 2);
    const backBinding = { ...a, id: 'back_binding', nativeRevision: back.revision, sourceSequence: 3 }; await put(backBinding);
    const archiveMove = { method: 'layout.publish', operationId: 'layout_archive', previous: backBinding, path: 'Projects/_Archived/Test/a.md' };
    const archiveResult = await call(archiveMove); assert.equal(archiveResult.status, 'published', JSON.stringify(archiveResult));
    const archiveBinding = { ...backBinding, id: 'archive_binding', nativeRevision: archiveResult.revision, sourceSequence: 4,
        collectionRoot: 'Projects/_Archived/Test', path: archiveMove.path, generation: 'archive', sourceBase: 'archive_base', writable: false };
    const archiveLayout = { version: 1, kind: 'layout', id: archiveMove.operationId, previous: backBinding, binding: archiveBinding };
    await put(archiveBinding); assert.equal((await put(archiveLayout)).status, 'published');
    // Reclaim the original source-owned path across archive/live generations.
    const restored = await call({ method: 'layout.publish', operationId: 'layout_restore', previous: archiveBinding, path: a.path });
    assert.equal(restored.status, 'published', JSON.stringify(restored));
    const dirty = await call({ method: 'publish', operationId: 'dirty_target', path: next.path, baseRevision: next.nativeRevision, contentBase64: b64('dirty') });
    assert.equal(dirty.status, 'published');
    assert.equal((await call({ ...reverse, operationId: 'layout_dirty', path: 'Another/a.md' })).reason, 'layout_previous_changed');
    await c.close();
    const PouchDB = require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const databaseDir = (await readdir(dbRoot)).find(v => v.endsWith('-livesync-v2'));
    const db = new PouchDB(join(dbRoot, databaseDir), { adapter: 'leveldb' });
    const source = await db.get(pdf.id), target = await db.get(pdfResult.id);
    assert.deepEqual(target.children, source.children); assert.deepEqual(target.eden, source.eden);
    assert.equal(target.type, source.type); assert.equal(target.size, source.size);
    // Erase only the result to model the nontransactional native-write/receipt
    // boundary. Resume must use exact metadata and ancestry, not a byte search.
    const pending = await db.get('_local/loom-layout-publication-layout_pdf'); delete pending.result; await db.put(pending);
    const retirement = await db.get('_local/loom-layout-retirement-layout_text'); delete retirement.result; await db.put(retirement);
    await db.close();
    c = client(dbRoot, settingsPath);
    assert.deepEqual(await call(pdfMove), pdfResult);
    // The retired old path was subsequently reclaimed; a stale retirement may
    // not tombstone that new branch, even if its payload bytes are identical.
    assert.equal((await call({ method: 'layout.retire', layout: l })).status, 'held');
    assert.equal((await call({ method: 'read.path', path: a.path, revision: back.revision })).status, 'available');
    await c.close();
});

test('native changes drain a full filtered raw page before an incoming control', { timeout: 30000 }, async t => {
    const temp = await mkdtemp(join(tmpdir(), 'loom-sync-pagination-'));
    t.after(() => rm(temp, { recursive: true, force: true }));
    const dbRoot = join(temp, 'db'); await mkdir(dbRoot);
    const settingsPath = join(temp, 'settings.json');
    await writeFile(settingsPath, JSON.stringify({ ...createNewVaultSettings(), isConfigured: false, useIndexedDBAdapter: false,
        encrypt: true, passphrase: 'ephemeral-pagination-only',
        liveSync: false, syncOnStart: false, periodicReplication: false, syncOnSave: false,
        syncOnEditorSave: false, syncOnFileOpen: false, syncAfterMerge: false,
        deleteMetadataOfDeletedFiles: false, automaticallyDeleteMetadataOfDeletedFiles: 0 }));
    let c = client(dbRoot, settingsPath); t.after(() => c.kill());
    const { epoch } = await c.call({ method: 'status' });
    const call = request => c.call({ epoch, ...request });
    const initial = await call({ method: 'changes', since: 0, limit: 128 });
    assert.deepEqual(initial.changes, []);
    assert.equal(initial.hasMore, false);
    await c.close();

    // Model replicated system/chunk arrivals in this owned, closed replica only.
    const PouchDB = require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const databaseDir = (await readdir(dbRoot)).find(v => v.endsWith('-livesync-v2'));
    assert.ok(databaseDir);
    const db = new PouchDB(join(dbRoot, databaseDir), { adapter: 'leveldb' });
    let rawPage;
    try {
        const written = await db.bulkDocs(Array.from({ length: 128 }, (_, index) => index % 2 ?
            { _id: `h:pagination-${index}`, type: 'leaf', data: `chunk-${index}` } :
            { _id: `pagination-system-${index}`, type: 'system' }));
        assert.ok(written.every(row => row.ok), JSON.stringify(written));
        rawPage = await db.changes({ since: initial.nextSequence, limit: 128, style: 'all_docs', include_docs: false });
        assert.equal(rawPage.results.length, 128);
        assert.ok(rawPage.results.every(row => !('doc' in row)));
    } finally { await db.close(); }

    c = client(dbRoot, settingsPath);
    assert.equal((await c.call({ method: 'status' })).epoch, epoch);
    const full = await call({ method: 'changes', since: initial.nextSequence, limit: 128 });
    assert.deepEqual(full.changes, []);
    assert.deepEqual(full.nextSequence, rawPage.last_seq);
    assert.equal(full.hasMore, true, 'raw fullness must preserve continuation even without pending rows');
    const intent = { version: 1, kind: 'intent', id: 'pagination_incoming', device: 'device', operation: 'create',
        workspace: 'w', collection: 'c', generation: 'g', fileId: 'incoming_file', base: null,
        path: 'Notes/incoming.md', sha256: 'sha256:' + createHash('sha256').update('incoming').digest('hex'), length: 8 };
    const published = await call({ method: 'control.put', contentBase64: b64(JSON.stringify(intent)) });
    assert.equal(published.status, 'published', JSON.stringify(published));
    let page = await call({ method: 'changes', since: initial.nextSequence, limit: 128 });
    assert.equal(page.status, 'available');
    assert.deepEqual(page.changes, []);
    assert.deepEqual(page.nextSequence, rawPage.last_seq);
    assert.equal(page.hasMore, true, 'a full raw page must continue even when every row is filtered');

    let incoming;
    for (let pages = 0; page.hasMore && pages < 4; pages++) {
        page = await call({ method: 'changes', since: page.nextSequence, limit: 128 });
        assert.equal(page.status, 'available');
        incoming ??= page.changes.flatMap(row => row.leaves).find(leaf => leaf.path === published.path);
    }
    assert.equal(page.hasMore, false, 'the durable input must finish draining');
    assert.ok(incoming, 'the incoming control behind the filtered page was stranded');
    assert.equal(incoming.kind, 'control');
    assert.equal(incoming.revision, published.revision);
    const exact = await call({ method: 'control.read', id: incoming.id, revision: incoming.revision });
    assert.equal(exact.status, 'available', JSON.stringify(exact));
    assert.deepEqual(JSON.parse(Buffer.from(exact.contentBase64, 'base64').toString()), intent);
    const idle = await call({ method: 'changes', since: page.nextSequence, limit: 128 });
    assert.deepEqual(idle.changes, []);
    assert.deepEqual(idle.nextSequence, page.nextSequence);
    assert.equal(idle.hasMore, false);
    await c.close();
});

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
    let hints = 0;
    let c = client(dbRoot, settingsPath, () => { hints++; }); t.after(() => c.kill());
    const status = await c.call({ method: 'status' }); assert.equal(status.status, 'ready');
    assert.equal(status.upstreamRevision, '7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b');
    const epoch = status.epoch;
    assert.equal((await c.call({ method: 'publication.preflight', epoch, path: 'Notes/no-encryption.pdf', create: true })).code,
        'encrypted_control_hooks_required');
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
    for (let i=0;i<100 && hints===0;i++) await new Promise(resolve => setTimeout(resolve,10));
    assert.ok(hints>0,'native changes must wake the owner without unsolicited JSON replies');
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

test('encrypted native controls: exact IPC reads, replay, collisions and hook requirement', { timeout: 60000 }, async (t) => {
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
    const beforePreflight = await call({ method: 'changes', since: 0, limit: 128 });
    const preflight = { method: 'publication.preflight', path: 'Notes/preflight.pdf', create: true };
    assert.deepEqual(await call(preflight), { status: 'ready' });
    const absentReceipt = { ...preflight, operationId: 'preflight_absent_receipt', sha256: '0'.repeat(64), size: 0 };
    assert.deepEqual(await call(absentReceipt), { status: 'ready' });
    for (const operationId of ['', '../escape', 'x'.repeat(129), 1, null]) {
        assert.equal((await call({ ...absentReceipt, operationId })).code, 'invalid_operation_id');
    }
    for (const invalid of [{ size: -1 }, { size: 1.5 }, { size: '1' }, { size: 2 * 1024 * 1024 * 1024 + 1 },
        { size: null }, { sha256: 'bad' }, { sha256: 'A'.repeat(64) }, { sha256: null }]) {
        assert.equal((await call({ ...absentReceipt, ...invalid })).code, 'invalid_reference_snapshot');
    }
    assert.equal((await call({ ...preflight, operationId: 'partial_metadata' })).code, 'invalid_reference_snapshot');
    assert.equal((await call({ ...preflight, sha256: absentReceipt.sha256, size: 0 })).code, 'invalid_operation_id');
    assert.equal((await call({ ...absentReceipt, snapshot: '/not-accepted/reference-123' })).code, 'unknown_field');
    assert.deepEqual(await call({ ...preflight, baseRevision: '1-base' }), { status: 'held', reason: 'exact_base_required' });
    assert.deepEqual(await call({ ...preflight, create: false }), { status: 'held', reason: 'exact_base_required' });
    assert.deepEqual(await call({ ...preflight, create: 'true', baseRevision: '1-base' }), { status: 'held', reason: 'exact_base_required' });
    assert.deepEqual(await call({ ...preflight, create: false, baseRevision: 'not-a-revision' }), { status: 'held', reason: 'exact_base_required' });
    assert.deepEqual(await call({ ...preflight, soleParent: true }), { status: 'held', reason: 'exact_base_required' });
    assert.deepEqual(await call({ ...preflight, create: false, baseRevision: '1-base', soleParent: 'true' }),
        { status: 'held', reason: 'exact_base_required' });
    for (const path of ['Notes/text.md', '../outside.pdf', 'LOOM-Control-v1/private.pdf']) {
        assert.equal((await call({ ...preflight, path })).code, 'invalid_reference_path');
    }
    assert.equal((await call({ ...preflight, contentBase64: b64('not accepted here') })).code, 'unknown_field');
    assert.equal((await c.call({ ...preflight, epoch: 'wrong-epoch' })).reason, 'replica_epoch_mismatch');
    const afterPreflight = await call({ method: 'changes', since: beforePreflight.nextSequence, limit: 128 });
    assert.deepEqual(afterPreflight.changes, []);
    assert.deepEqual(afterPreflight.nextSequence, beforePreflight.nextSequence, 'preflight must not write native data');
    assert.equal((await call({ method: 'publish.reference', operationId: 'preflight_race', path: preflight.path,
        create: true, contentBase64: b64('a later arrival') })).status, 'published');
    assert.deepEqual(await call(preflight), { status: 'held', reason: 'path_has_history' });
    assert.deepEqual(await call(absentReceipt), { status: 'held', reason: 'path_has_history' });
    const exportResult = await call({ method:'publish', operationId:'export', path:'Notes/a.md', create:true, contentBase64:b64('A') });
    const largeText = 'a'.repeat(1200000);
    const largeTextResult = await call({ method:'publish', operationId:'large_markdown', path:'Notes/large.md', create:true, contentBase64:b64(largeText) });
    assert.equal(largeTextResult.status,'published',JSON.stringify(largeTextResult));
    const largeTextRead = await call({ method:'read.path', path:'Notes/large.md', revision:largeTextResult.revision });
    assert.equal(largeTextRead.status,'available',JSON.stringify(largeTextRead));
    assert.equal(Buffer.from(largeTextRead.contentBase64,'base64').toString(),largeText);
    const canvasText = '{"nodes":[],"edges":[]}\r\n';
    const canvas = await call({method:'publish',operationId:'canvas_text',path:'Notes/board.canvas',create:true,contentBase64:b64(canvasText)});
    assert.equal(canvas.status,'published',JSON.stringify(canvas));
    const canvasRead=await call({method:'read.path',path:'Notes/board.canvas',revision:canvas.revision});
    assert.equal(Buffer.from(canvasRead.contentBase64,'base64').toString(),canvasText);
    const baseText = 'views:\r\n  - type: table\r\n    name: People\r\n';
    const base = await call({method:'publish',operationId:'base_text',path:'Notes/People.base',create:true,contentBase64:b64(baseText)});
    assert.equal(base.status,'published',JSON.stringify(base));
    const baseRead = await call({method:'read.path',path:'Notes/People.base',revision:base.revision});
    assert.equal(Buffer.from(baseRead.contentBase64,'base64').toString(),baseText);
    for (const extension of ['wav','svg','csv','xlsx','dat','json']) {
        const reference = await call({ method:'publish.reference', operationId:`reference_${extension}`, path:`Notes/attachment.${extension}`, create:true, contentBase64:b64(Buffer.from([0,255,42])) });
        assert.equal(reference.status,'published',JSON.stringify(reference));
        const exactReference = await call({ method:'read.path', path:`Notes/attachment.${extension}`, revision:reference.revision });
        assert.equal(exactReference.status,'held',JSON.stringify(exactReference));
        assert.equal(exactReference.reason,'reference_binary');
        assert.equal(exactReference.revision,reference.revision);
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
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, create: true }),
        { status: 'held', reason: 'path_has_history' });
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: ref.revision }),
        { status: 'ready' });
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: ref.revision, soleParent: true }),
        { status: 'ready' });
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: '99-missing' }),
        { status: 'held', reason: 'base_unavailable' });
    const refChild = await call({ ...refRequest, operationId: 'reference_child', create: false, baseRevision: ref.revision,
        contentBase64: b64('first child') });
    assert.equal(refChild.status, 'published', JSON.stringify(refChild));
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: ref.revision, soleParent: true }),
        { status: 'held', reason: 'base_diverged' });
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: ref.revision, soleParent: true,
        operationId: 'reference_child', sha256: refChild.sha256, size: Buffer.byteLength('first child') }),
        { status: 'ready', outcome: 'published' }, 'completed publication precedes current sole-parent refusal');
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: refChild.revision, soleParent: true }),
        { status: 'ready' });
    assert.equal((await call({ ...refRequest, operationId: 'reference_sibling', create: false, baseRevision: ref.revision,
        contentBase64: b64('second child') })).status, 'published');
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: refChild.revision, soleParent: true }),
        { status: 'held', reason: 'base_diverged' });
    assert.deepEqual(await call({ method: 'publication.preflight', path: refRequest.path, baseRevision: ref.revision, soleParent: false }),
        { status: 'ready' }, 'ordinary exact-base publication still permits retained branches');
    const soleReference = await call({ ...refRequest, operationId: 'sole_reference', path: 'Notes/sole.pdf' });
    assert.equal(soleReference.status, 'published', JSON.stringify(soleReference));
    const refBinding = {...binding,id:'binary_binding',fileId:'binary_file',path:refRequest.path,nativeRevision:ref.revision,sha256:'sha256:'+ref.sha256,writable:false};
    assert.equal((await call({method:'control.put',contentBase64:b64(JSON.stringify(refBinding))})).status,'published');
    assert.notEqual((await call({method:'control.put',contentBase64:b64(JSON.stringify({...refBinding,id:'badbinary',writable:true}))})).status,'published');
    assert.equal((await call({...refRequest,method:'publish'})).code,'invalid_path');
    const large = await call({...refRequest,operationId:'large_reference',path:'Notes/large.pdf',contentBase64:b64(Buffer.alloc(8*1024*1024+1,255))});
    assert.equal(large.status,'published',JSON.stringify(large));
    const largeLeaves = await call({method:'leaves',id:large.id});
    assert.equal(largeLeaves.leaves[0].size,8*1024*1024+1);
    const largeExact = await call({method:'read.path',path:'Notes/large.pdf',revision:large.revision});
    assert.equal(largeExact.reason,'content_limit');
    assert.equal(largeExact.revision,large.revision);
    const spool = join(dbRoot, 'reference-spool');
    await mkdir(spool, { mode: 0o700 });
    const snapshot = join(spool, 'reference-123');
    const snapshotBytes = Buffer.alloc(65 * 1024 * 1024, 42);
    await writeFile(snapshot, snapshotBytes, { mode: 0o600 });
    const fromFile = { method:'publish.reference.file', operationId:'stream_reference', path:'Notes/stream.pdf', create:true,
        snapshot, size:snapshotBytes.length, sha256:createHash('sha256').update(snapshotBytes).digest('hex') };
    assert.equal((await call({...fromFile,sha256:'0'.repeat(64)})).code,'reference_snapshot_changed');
    assert.equal((await call({...fromFile,size:2*1024*1024*1024+1})).code,'invalid_reference_snapshot');
    assert.equal((await call({...fromFile,snapshot:join(temp,'outside')})).code,'invalid_reference_snapshot');
    const streamed = await call(fromFile);
    assert.equal(streamed.status,'published',JSON.stringify(streamed));
    assert.equal(streamed.sha256,fromFile.sha256);
    // No snapshot exists: these replies prove zero snapshot opens/stream reads.
    await rm(snapshot);
    const filePreflight = { method: 'publication.preflight', operationId: fromFile.operationId, path: fromFile.path,
        create: true, sha256: fromFile.sha256, size: fromFile.size };
    for (let retry = 0; retry < 3; retry++) {
        assert.deepEqual(await call(fromFile), streamed);
        assert.deepEqual(await call(filePreflight), { status: 'ready', outcome: 'published' });
        assert.deepEqual(await call({ ...fromFile, operationId: 'stream_history_no_read' }),
            { status: 'held', reason: 'path_has_history' });
        assert.deepEqual(await call({ ...fromFile, operationId: 'stream_base_no_read', path: 'Notes/missing-base.pdf',
            create: false, baseRevision: '99-missing' }), { status: 'held', reason: 'base_unavailable' });
    }
    assert.equal((await call({ ...filePreflight, sha256: '0'.repeat(64) })).code, 'operation_id_reused');
    assert.equal((await call({ ...filePreflight, path: 'Notes/different.pdf' })).code, 'operation_id_reused');
    assert.equal((await call({ ...filePreflight, size: -1 })).code, 'invalid_reference_snapshot');
    for (const invalid of [{ size: -1 }, { size: 1.5 }, { size: '1' }, { size: 2 * 1024 * 1024 * 1024 + 1 },
        { sha256: 'bad' }, { snapshot: join(temp, 'reference-outside') }, { snapshot: join(spool, 'wrong-name') }]) {
        assert.equal((await call({ ...fromFile, ...invalid })).code, 'invalid_reference_snapshot', JSON.stringify(invalid));
    }
    assert.equal((await call({ ...fromFile, sha256: '0'.repeat(64) })).code, 'operation_id_reused');
    assert.equal((await call({ ...fromFile, operationId: 'stream_needs_snapshot', path: 'Notes/needs-snapshot.pdf' })).code,
        'native_operation_failed', 'a fresh publication must still open and validate its snapshot');
    assert.deepEqual(await call({ ...fromFile, operationId: 'preflight_ready_is_not_permission', path: preflight.path }),
        { status: 'held', reason: 'path_has_history' });
    assert.equal((await call({method:'leaves',id:streamed.id})).leaves[0].size,snapshotBytes.length);
    assert.equal((await call({method:'read.path',path:'../outside.pdf',revision:large.revision})).code,'invalid_path');
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
    const streamReceipt = await db.get('_local/loom-publication-stream_reference');
    delete streamReceipt.result; streamReceipt.state = 'pending'; await db.put(streamReceipt);
    for (const operationId of ['stream_history_no_read', 'stream_base_no_read', 'stream_needs_snapshot', 'preflight_ready_is_not_permission',
        absentReceipt.operationId]) {
        await assert.rejects(db.get('_local/loom-publication-' + operationId), error => error.status === 404);
    }
    const binaryDocument = await db.get(ref.id);
    const deletedReference = await db.put({ ...binaryDocument, deleted: true });
    const soleDocument = await db.get(soleReference.id);
    await db.put({ ...soleDocument, _deleted: true });
    const original = await db.get(exportResult.id);
    await db.bulkDocs([
        {...original, _rev:'3-later', _revisions:{start:3,ids:['later','earlier',original._rev.split('-')[1]]}},
        {...original, _id:leaf.id.replace('/binding/b.md','/payload/offline.md'), path:'LOOM-Control-v1/payload/offline.md', _rev:'1-payload', _revisions:{start:1,ids:['payload']}},
    ], {new_edits:false});
    await db.close();
    const resumed = client(dbRoot, settingsPath); t.after(() => resumed.kill());
    const next = r => resumed.call({epoch:status.epoch,...r});
    for (let retry = 0; retry < 3; retry++) {
        assert.deepEqual(await next(fromFile), { status: 'held', reason: 'publication_outcome_unknown' });
        assert.deepEqual(await next(filePreflight), { status: 'held', reason: 'publication_outcome_unknown' });
        assert.deepEqual(await next({ method: 'publication.preflight', path: refRequest.path, baseRevision: deletedReference.rev }),
            { status: 'held', reason: 'base_not_live_file' });
        assert.deepEqual(await next({ ...fromFile, operationId: 'stream_deleted_base_no_read', path: refRequest.path,
            create: false, baseRevision: deletedReference.rev }), { status: 'held', reason: 'base_not_live_file' });
    }
    assert.deepEqual(await next({ method: 'publication.preflight', path: 'Notes/sole.pdf', baseRevision: soleReference.revision, soleParent: true }),
        { status: 'held', reason: 'base_unavailable' });
    assert.deepEqual(await next({ method: 'publication.preflight', path: fromFile.path, baseRevision: streamed.revision, soleParent: true }),
        { status: 'ready' });
    assert.equal((await next({ ...fromFile, size: -1 })).code, 'invalid_reference_snapshot');
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

test('reference preflight recovers completed publication with a compacted base before metadata refusal', { timeout: 60000 }, async t => {
    const temp = await mkdtemp(join(tmpdir(), 'loom-reference-receipt-'));
    t.after(() => rm(temp, { recursive: true, force: true }));
    const dbRoot = join(temp, 'db'); await mkdir(dbRoot);
    const settingsPath = join(temp, 'settings.json');
    await writeFile(settingsPath, JSON.stringify({ ...createNewVaultSettings(), isConfigured: false, useIndexedDBAdapter: false,
        encrypt: true, passphrase: 'ephemeral-receipt-only', liveSync: false, syncOnStart: false, periodicReplication: false }));
    let c = client(dbRoot, settingsPath); t.after(() => c.kill());
    const { epoch } = await c.call({ method: 'status' });
    const call = request => c.call({ epoch, ...request });
    const path = 'Notes/recovered.pdf';
    const seed = await call({ method: 'publish.reference', operationId: 'receipt_seed', path, create: true, contentBase64: b64('base') });
    assert.equal(seed.status, 'published', JSON.stringify(seed));
    const update = { method: 'publish.reference', operationId: 'receipt_update', path, baseRevision: seed.revision, contentBase64: b64('updated') };
    const published = await call(update);
    assert.equal(published.status, 'published', JSON.stringify(published));
    await c.close();
    const PouchDB = require('pouchdb-core').plugin(require('pouchdb-adapter-leveldb'));
    const directory = (await readdir(dbRoot)).find(v => v.endsWith('-livesync-v2'));
    const db = new PouchDB(join(dbRoot, directory), { adapter: 'leveldb' });
    await db.compact();
    await assert.rejects(db.get(seed.id, { rev: seed.revision }), error => error.status === 404);
    await db.close();
    c = client(dbRoot, settingsPath); await c.call({ method: 'status' });
    const preflight = { method: 'publication.preflight', path, baseRevision: seed.revision, soleParent: true };
    const probe = { ...preflight, operationId: update.operationId, sha256: published.sha256, size: Buffer.byteLength('updated') };
    for (let retry = 0; retry < 3; retry++) {
        assert.deepEqual(await call(preflight), { status: 'held', reason: 'base_unavailable' });
        assert.deepEqual(await call(probe), { status: 'ready', outcome: 'published' });
        assert.deepEqual(await call({ ...probe, operationId: 'receipt_absent' }), { status: 'held', reason: 'base_unavailable' });
        assert.deepEqual(await call(update), published);
    }
    assert.equal((await call({ ...probe, baseRevision: published.revision })).code, 'operation_id_reused');
    await c.close();
    const pending = new PouchDB(join(dbRoot, directory), { adapter: 'leveldb' });
    const receipt = await pending.get('_local/loom-publication-' + update.operationId);
    delete receipt.result; receipt.state = 'pending'; await pending.put(receipt);
    await assert.rejects(pending.get('_local/loom-publication-receipt_absent'), error => error.status === 404);
    await pending.close();
    c = client(dbRoot, settingsPath); await c.call({ method: 'status' });
    assert.deepEqual(await call(probe), { status: 'held', reason: 'publication_outcome_unknown' });
    assert.equal((await call({ ...probe, sha256: '0'.repeat(64) })).code, 'operation_id_reused');
    await c.close();
});
