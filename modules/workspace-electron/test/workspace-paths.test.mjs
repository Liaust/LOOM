import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, rm, mkdir, lstat, readFile, writeFile, rename} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {NotesSync} from '../src/notes-sync.mjs';
import {WorkspacePaths} from '../src/workspace-paths.mjs';
import {digest} from '../src/local-files.mjs';

const within = (value, parent) => value === parent || value.startsWith(parent + '/');
const from = 'Projects/one';
const to = 'Projects/other/two';
const stopWatchers = notes => { for (const watcher of notes.watchers.values()) watcher.close(); notes.watchers.clear(); };
const row = (notes, id) => JSON.parse(notes.db.prepare('SELECT record FROM operations WHERE id=?').get(id).record);
const connect = (notes, paths) => {
  notes.lifecycle.paths = paths;
};

async function fixture(t, guards = {}) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'loom-workspace-paths-'));
  const calls = [], noteCalls = [], moves = [], events = [];
  const files = new Map([[from + '/a.md', 'kept'], [from + '/asset.pdf', 'remote only']]);
  const reviews = new Map();
  const control = {applyState: 'complete'};
  const remote = {
    async api(endpoint, query, {method, body}) {
      assert.equal(endpoint, '/v1/files/workspace-path'); assert.equal(method, 'POST');
      calls.push(structuredClone(body));
      if (body.action === 'plan') {
        const review = {id: `workspace_path_${reviews.size + 1}`, kind: body.kind, project: body.project, state: 'planned', plan_hash: `hash-${reviews.size + 1}`, from_root: 'box', source: body.from, to_root: body.kind === 'relocate_project' ? 'box' : 'storage', destination: body.to, entries: 3, bytes: 15, qualification: 'Original identity and history; no activation or downloads.'};
        reviews.set(review.id, review);
        return {data: control.alterPlan ? control.alterPlan(structuredClone(review)) : review};
      }
      const review = reviews.get(body.id);
      assert.ok(review); assert.equal(body.plan_hash, review.plan_hash);
      assert.equal(body.kind, review.kind); assert.equal(body.project, review.project);
      assert.equal(body.from, review.source); assert.equal(body.to, review.destination);
      if (body.action === 'apply') {
        assert.equal(body.confirm, true);
        await control.beforeApply?.(body);
        if (control.applyState === 'complete' && review.state !== 'complete') {
          for (const [name, text] of [...files]) if (within(name, review.source)) {
            files.delete(name);
            if (review.kind === 'relocate_project') files.set(review.destination + name.slice(review.source.length), text);
          }
        }
        review.state = control.applyState;
        if (control.loseApply) {
          control.loseApply = false;
          throw Object.assign(Error('Lost apply reply'), {receipt: {...review, state: 'prepared'}});
        }
      } else if (body.action === 'cancel') {
        assert.equal(body.confirm, true);
        if (review.state !== 'complete') review.state = 'cancelled';
        if (control.loseCancel) { control.loseCancel = false; throw Error('Lost cancellation reply'); }
      } else assert.equal(body.action, 'status');
      return {data: control.alterReply ? control.alterReply({...review}) : {...review}};
    },
    async json(endpoint, query, options = {}) {
      noteCalls.push({endpoint, query: {...query}, options});
      assert.equal(endpoint, 'note', 'A workspace placeholder must never send an ordinary tree-move request.');
      assert.notEqual(options.method, 'POST', 'No background edits are expected in this fixture.');
      return {data: {root: 'box', path: query.path, exists: files.has(query.path), text: files.get(query.path) || '', file: {file_id: 'source-' + query.path}, base_id: 'base-' + query.path, generation: 'g', hash: 'sha256:' + digest(Buffer.from(files.get(query.path) || ''))}};
    },
  };
  const moveDirectory = async (local, placement) => {
    moves.push(structuredClone(placement));
    const source = path.join(local.root, placement.from), destination = path.join(local.root, placement.to);
    const before = await lstat(source).catch(error => { if (error.code !== 'ENOENT') throw error; return null; });
    if (before) {
      const collision = await lstat(destination).catch(error => { if (error.code !== 'ENOENT') throw error; return null; });
      if (collision) throw Error('Local destination collision.');
      assert.equal(`${before.dev}:${before.ino}`, placement.identity);
      await mkdir(path.dirname(destination), {recursive: true}); await rename(source, destination);
    }
    const after = await lstat(destination); assert.equal(`${after.dev}:${after.ino}`, placement.identity);
    if (control.loseNative) { control.loseNative = false; throw Error('Lost native mover reply'); }
  };
  const notes = new NotesSync(directory, remote, {moveDirectory, emit: (kind, value) => events.push({kind, value})});
  await notes.init();
  const paths = new WorkspacePaths(notes, remote, guards); connect(notes, paths);
  t.after(async () => { await notes.close(); await rm(directory, {recursive: true, force: true}); });
  return {directory, notes, paths, remote, calls, noteCalls, files, reviews, control, moves, events, moveDirectory};
}

test('workspace review, status, list and history use the exact durable SQLite request', async t => {
  const {notes, paths, calls} = await fixture(t);
  const op = await paths.review('relocate_project', 'one', from, to);
  assert.equal(op.state, 'preview'); assert.equal(op.history[0].action, 'plan');
  assert.deepEqual(calls, [{action: 'plan', kind: 'relocate_project', project: 'one', from, to}]);
  assert.deepEqual(JSON.parse(notes.db.prepare('SELECT record FROM workspace_paths WHERE id=?').get(op.id).record), op);
  const inspected = await paths.inspect(op.id);
  assert.equal(inspected.state, 'preview'); assert.equal(inspected.history.at(-1).mainState, 'planned');
  assert.deepEqual(calls.at(-1), {action: 'status', kind: op.kind, project: 'one', from, to, id: op.id, plan_hash: op.plan_hash});
  assert.deepEqual(paths.list()[0], inspected);
  assert.equal(paths.blocked(from + '/a.md'), undefined);
});

test('Main mutation has a held durable tree outbox and fenced records; only existing descendants move', async t => {
  const {notes, paths, control, calls, noteCalls, moves} = await fixture(t);
  const r = await notes.adopt('box', from + '/a.md');
  const op = await paths.review('relocate_project', 'one', from, to);
  control.beforeApply = async () => {
    assert.equal(row(notes, op.id).state, 'held'); assert.equal(row(notes, op.id).request, undefined);
    assert.equal(r.treeOperation, op.id);
    assert.equal(JSON.parse(notes.db.prepare('SELECT record FROM files WHERE id=?').get(r.id).record).treeOperation, op.id);
    assert.equal(paths.get(op.id).state, 'submitted');
    await notes.pump();
    assert.ok(paths.blocked(from)); assert.ok(paths.blocked(to)); assert.ok(paths.blocked('Projects'));
    assert.equal(paths.blocked('Projects/unrelated'), undefined);
  };
  const result = await paths.apply(op.id);
  assert.equal(result.state, 'complete'); assert.equal(r.path, to + '/a.md');
  assert.equal(r.localPath, 'box/' + to + '/a.md'); assert.equal(r.treeOperation, undefined);
  assert.equal(notes.get('box', to + '/a.md').id, r.id); assert.equal(r.source.path, r.path);
  assert.equal(await readFile(path.join(notes.root, r.localPath), 'utf8'), 'kept');
  await assert.rejects(lstat(path.join(notes.root, 'box/' + from)), /ENOENT/);
  await assert.rejects(lstat(path.join(notes.root, 'box/' + to + '/asset.pdf')), /ENOENT/);
  assert.equal(noteCalls.some(call => call.query.path.endsWith('asset.pdf')), false);
  assert.equal(moves.length, 1); assert.equal(row(notes, op.id).placement.stage, 'done');
  assert.equal(row(notes, op.id).state, 'accepted');
  assert.deepEqual(calls.at(-1), {action: 'apply', kind: op.kind, project: 'one', from, to, id: op.id, plan_hash: op.plan_hash, confirm: true});
  await paths.apply(op.id); assert.equal(calls.length, 2);
  assert.equal(paths.blocked(from), undefined);
});

test('a lost Main reply survives restart; status does not release holds or resubmit an ordinary tree move', async t => {
  const f = await fixture(t), {notes, paths, control, calls} = f;
  const r = await notes.adopt('box', from + '/a.md');
  const op = await paths.review('relocate_project', 'one', from, to);
  control.loseApply = true;
  const held = await paths.apply(op.id);
  assert.equal(held.state, 'needs_review'); assert.equal(held.receipt.state, 'prepared');
  assert.equal(r.treeOperation, op.id); assert.equal(row(notes, op.id).state, 'held');
  await notes.pump(); assert.equal(calls.length, 2);
  assert.throws(() => notes.queue(r, 'edit', 'unsafe'), /needs review|exact recovery/);
  await assert.rejects(paths.apply(op.id), /explicitly recover/);
  await notes.close();
  const restarted = new NotesSync(f.directory, f.remote, {moveDirectory: f.moveDirectory});
  await restarted.init(); t.after(() => restarted.close());
  const recovered = new WorkspacePaths(restarted, f.remote); connect(restarted, recovered);
  assert.equal(recovered.list()[0].id, op.id);
  await restarted.pump(); assert.equal(calls.length, 2);
  assert.equal((await recovered.inspect(op.id)).state, 'needs_review');
  assert.equal(restarted.byID.get(r.id).treeOperation, op.id);
  assert.equal((await recovered.apply(op.id, {recover: true})).state, 'complete');
  assert.equal(restarted.byID.get(r.id).path, to + '/a.md');
  assert.equal(calls.filter(call => call.action === 'plan').length, 1);
  assert.equal(calls.filter(call => call.action === 'apply').length, 1);
  for (const call of calls.slice(1)) { assert.equal(call.id, op.id); assert.equal(call.plan_hash, op.plan_hash); }
});

test('incomplete Main states stay held and explicit recovery repeats only the saved apply body', async t => {
  const {notes, paths, control, calls} = await fixture(t);
  const r = await notes.adopt('box', from + '/a.md');
  const op = await paths.review('relocate_project', 'one', from, to);
  control.applyState = 'prepared';
  assert.equal((await paths.apply(op.id)).state, 'needs_review');
  assert.equal(row(notes, op.id).state, 'held'); assert.equal(r.treeOperation, op.id);
  assert.equal((await paths.inspect(op.id)).state, 'needs_review');
  await notes.pump(); assert.equal(calls.filter(c => c.action === 'apply').length, 1);
  control.applyState = 'complete';
  assert.equal((await paths.apply(op.id, {recover: true})).state, 'complete');
  const applies = calls.filter(c => c.action === 'apply'); assert.deepEqual(applies[0], applies[1]);
});

test('child retention preserves local paths and bytes, marks them read-only, and never maps into Storage', async t => {
  const {notes, paths, moves, noteCalls} = await fixture(t);
  const r = await notes.adopt('box', from + '/a.md');
  const oldPath = r.localPath, oldInode = (await lstat(path.join(notes.root, oldPath))).ino;
  const op = await paths.review('retain_child', 'one', from, 'Library/retained-one');
  const count = noteCalls.length;
  assert.equal((await paths.apply(op.id)).state, 'complete');
  assert.equal(moves.length, 0); assert.equal(noteCalls.length, count);
  assert.equal(r.localPath, oldPath); assert.equal(r.path, from + '/a.md');
  assert.equal(r.retainedProject, op.id); assert.equal(r.retainedProjectKind, 'retain_child'); assert.match(r.retainedProjectReason, /original Box path/);
  assert.equal(r.treeOperation, undefined); assert.equal((await lstat(path.join(notes.root, oldPath))).ino, oldInode);
  assert.equal(await readFile(path.join(notes.root, oldPath), 'utf8'), 'kept');
  assert.throws(() => notes.queue(r, 'edit', 'overwrite'), /retained in Storage/);
  await assert.rejects(lstat(path.join(notes.root, 'storage')), /ENOENT/);
});

test('pending records on either endpoint or parent, external edits and outbox aliases refuse before Main', async t => {
  for (const flag of ['pending', 'held', 'pathOperation', 'treeOperation', 'externalText', 'localAbsence', 'parent', 'target', 'external', 'outbox']) {
    await t.test(flag, async t => {
      const {notes, paths, calls} = await fixture(t);
      const r = await notes.adopt('box', from + '/a.md');
      const op = await paths.review('relocate_project', 'one', from, to);
      stopWatchers(notes);
      if (flag === 'external') await writeFile(path.join(notes.root, r.localPath), 'external edit');
      else if (flag === 'parent' || flag === 'target') {
        const extra = {id: 'other-record', root: 'box', path: flag === 'parent' ? 'Projects' : to + '/pending.md', localPath: 'box/other', directory: flag === 'parent', pending: true};
        notes.byID.set(extra.id, extra); notes.persist(extra);
      } else if (flag === 'outbox') {
        const other = {id: 'other-operation', kind: 'rename', path: 'Elsewhere/a.md', target: from, state: 'held'};
        notes.db.prepare('INSERT INTO operations(id,file_id,state,record) VALUES(?,?,?,?)').run(other.id, 'missing', 'held', JSON.stringify(other));
      } else { r[flag] = flag === 'externalText' ? 'new draft' : true; notes.persist(r); }
      await assert.rejects(paths.apply(op.id), /pending|external|outbox/i);
      assert.equal(paths.get(op.id).state, 'preview'); assert.equal(calls.filter(c => c.action === 'apply').length, 0);
      assert.equal(notes.db.prepare('SELECT id FROM operations WHERE id=?').get(op.id), undefined);
    });
  }
});

test('draft and upload callbacks cover exact endpoints without holding sibling project drafts', async t => {
  for (const which of ['checkDrafts', 'checkUploads']) await t.test(which, async t => {
    let armed = false; const scopes = [];
    const f = await fixture(t, {[which]: async (relative, context) => {
      scopes.push({relative, ...context});
      if (armed && relative === to) throw Error('Resolve endpoint draft/upload first.');
    }});
    const op = await f.paths.review('relocate_project', 'one', from, to); armed = true;
    await assert.rejects(f.paths.apply(op.id), /endpoint draft\/upload/);
    assert.ok(scopes.some(s => s.relative === from && s.root === 'box'));
    assert.ok(scopes.some(s => s.relative === to && s.root === 'box'));
    assert.equal(scopes.some(s => ['Projects', 'Projects/other'].includes(s.relative)), false);
    assert.equal(f.calls.filter(c => c.action === 'apply').length, 0);
  });
});

test('lifecycle guards compose workspace paths without self-recursion and bind either endpoint ancestry', async t => {
  for (const boxPath of ['Projects', from + '/nested', 'Projects/other', to + '/nested']) await t.test(boxPath, async t => {
    const {notes, paths, calls} = await fixture(t);
    const op = await paths.review('relocate_project', 'one', from, to);
    notes.lifecycle.save({id: 'lifecycle_other', state: 'needs_review', boxPath});
    await assert.rejects(paths.apply(op.id), /overlapping project lifecycle/);
    assert.equal(calls.filter(c => c.action === 'apply').length, 0);
  });
  await t.test('unrelated sibling', async t => {
    const {notes, paths} = await fixture(t, {checkDrafts: relative => {
      if (within('Projects/unrelated/draft.md', relative)) throw Error('Unrelated draft was incorrectly fenced.');
    }});
    notes.lifecycle.save({id: 'lifecycle_other', state: 'needs_review', boxPath: 'Projects/unrelated'});
    const op = await paths.review('relocate_project', 'one', from, to);
    assert.equal((await paths.apply(op.id)).state, 'complete');
  });
});

test('destination collisions and a failed SQLite checkpoint cannot submit Main or leave in-memory fences', async t => {
  for (const failure of ['collision', 'sqlite']) await t.test(failure, async t => {
    const {notes, paths, calls} = await fixture(t);
    const r = await notes.adopt('box', from + '/a.md');
    const op = await paths.review('relocate_project', 'one', from, to);
    if (failure === 'collision') await mkdir(path.join(notes.root, 'box/' + to), {recursive: true});
    else notes.db.exec("CREATE TRIGGER refuse_handover BEFORE UPDATE ON workspace_paths BEGIN SELECT RAISE(ABORT,'checkpoint refused'); END");
    await assert.rejects(paths.apply(op.id), failure === 'collision' ? /destination exists/ : /checkpoint refused/);
    assert.equal(r.treeOperation, undefined); assert.equal(paths.get(op.id).state, 'preview');
    assert.equal(notes.db.prepare('SELECT id FROM operations WHERE id=?').get(op.id), undefined);
    assert.equal(calls.filter(c => c.action === 'apply').length, 0);
  });
});

test('cancellation preserves exact reviews and recovers a lost reply without ever applying', async t => {
  const {notes, paths, calls, control} = await fixture(t);
  const op = await paths.review('relocate_project', 'one', from, to);
  control.loseCancel = true;
  assert.equal((await paths.cancel(op.id)).state, 'needs_review');
  assert.ok(paths.blocked(from));
  const reopened = new WorkspacePaths(notes, paths.remote);
  assert.equal((await reopened.inspect(op.id)).state, 'cancelled');
  assert.equal(reopened.blocked(from), undefined);
  await reopened.apply(op.id, {recover: true}); assert.equal(calls.filter(c => c.action === 'apply').length, 0);
  assert.deepEqual(calls[1], {action: 'cancel', kind: op.kind, project: 'one', from, to, id: op.id, plan_hash: op.plan_hash, confirm: true});
});

test('a completed movement cannot be undone by cancellation', async t => {
  const {paths, calls} = await fixture(t);
  const op = await paths.review('relocate_project', 'one', from, to);
  await paths.apply(op.id);
  assert.equal((await paths.cancel(op.id)).state, 'complete');
  assert.equal(calls.filter(call => call.action === 'cancel').length, 0);
});

test('a cancellation after preparation only releases its own fence when Main proves no movement', async t => {
  const {notes, paths, control} = await fixture(t);
  const r = await notes.adopt('box', from + '/a.md');
  const op = await paths.review('relocate_project', 'one', from, to);
  control.applyState = 'prepared'; await paths.apply(op.id);
  assert.equal(r.treeOperation, op.id);
  assert.equal((await paths.cancel(op.id)).state, 'cancelled');
  assert.equal(r.treeOperation, undefined); assert.equal(row(notes, op.id).state, 'cancelled');
  assert.equal(await readFile(path.join(notes.root, r.localPath), 'utf8'), 'kept');
});

test('lost native mover replies use the original inode and manifest; later local edits are never overwritten', async t => {
  const {notes, paths, control, calls, moves} = await fixture(t);
  const r = await notes.adopt('box', from + '/a.md'); stopWatchers(notes);
  const op = await paths.review('relocate_project', 'one', from, to);
  control.loseNative = true;
  assert.equal((await paths.apply(op.id)).state, 'needs_review');
  assert.equal(row(notes, op.id).state, 'accepted'); assert.equal(r.treeOperation, op.id);
  await writeFile(path.join(notes.root, 'box/' + to + '/a.md'), 'user edit during recovery');
  assert.equal((await paths.apply(op.id, {recover: true})).state, 'complete');
  assert.equal(moves.length, 2);
  for (const key of ['from', 'to', 'identity']) assert.equal(moves[0][key], moves[1][key]);
  assert.equal(calls.filter(c => c.action === 'apply').length, 1);
  assert.equal(await readFile(path.join(notes.root, r.localPath), 'utf8'), 'user edit during recovery');
  assert.equal(r.held, true); assert.equal(r.externalText, 'user edit during recovery');
});

test('mismatched Main receipts keep the original outbox and both paths held', async t => {
  for (const field of ['id', 'plan_hash', 'project', 'kind', 'source', 'destination', 'from_root', 'to_root', 'entries']) await t.test(field, async t => {
    const {notes, paths, control, moves} = await fixture(t);
    const r = await notes.adopt('box', from + '/a.md');
    const op = await paths.review('relocate_project', 'one', from, to);
    control.alterReply = value => ({...value, [field]: field === 'entries' ? 99 : 'wrong'});
    assert.equal((await paths.apply(op.id)).state, 'needs_review');
    assert.equal(moves.length, 0); assert.equal(r.treeOperation, op.id); assert.equal(row(notes, op.id).state, 'held');
    assert.ok(paths.blocked(from)); assert.ok(paths.blocked(to));
  });
});

test('invalid inputs and mismatched plans never create a durable mutation intent', async t => {
  const {paths, control, calls} = await fixture(t);
  for (const input of [['unknown', 'one', from, to], ['relocate_project', '../bad', from, to], ['relocate_project', 'one', from + '//a', to], ['relocate_project', 'one', from, from + '/nested'], ['retain_child', 'one', '../bad', to]]) await assert.rejects(paths.review(...input));
  assert.equal(calls.length, 0);
  control.alterPlan = review => ({...review, destination: 'Different/place'});
  await assert.rejects(paths.review('relocate_project', 'one', from, to), /exact workspace path review/);
  assert.deepEqual(paths.list(), []);
});

test('an absent local subtree never downloads or creates a new local project during relocation', async t => {
  const {notes, paths, moves, noteCalls} = await fixture(t);
  const op = await paths.review('relocate_project', 'one', from, to);
  assert.equal((await paths.apply(op.id)).state, 'complete');
  assert.equal(moves.length, 0); assert.equal(noteCalls.length, 0); assert.equal(notes.byID.size, 0);
  await assert.rejects(lstat(path.join(notes.root, 'box/' + to)), /ENOENT/);
});
