import path from 'node:path';
import { lstat, mkdir } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { normalize, digest } from './local-files.mjs';
import {savedRoot,sameBoxRoots} from './box-roots.mjs';

const inside = (value, parent) => value === parent || value.startsWith(`${parent}/`);
const identity = info => `${info.dev}:${info.ino}`;
const infoAt = file => lstat(file, { bigint: true }).catch(error => { if (error.code !== 'ENOENT') throw error; return null; });

export function nativeDirectoryMover(executable) {
  return async (local, placement) => {
    let parent = local.root;
    for (const part of normalize(placement.to).split('/').slice(0, -1)) {
      parent = path.join(parent, part);
      await mkdir(parent, { mode: 0o700 }).catch(error => { if (error.code !== 'EEXIST') throw error; });
      const info = await lstat(parent);
      if (info.isSymbolicLink() || !info.isDirectory()) throw Error('The local destination parent is unsafe.');
    }
    await promisify(execFile)(executable, ['move', local.root, placement.from, placement.to, placement.identity], { timeout: 15000, maxBuffer: 4096 });
  };
}

// A folder operation uses the existing durable outbox, but never checks out its
// remote descendants. Only this device's already-present working files follow.
export class TreeMoves {
  constructor(owner, moveDirectory) { this.owner = owner; this.moveDirectory = moveDirectory; }
  get(id) {
    const row = this.owner.db.prepare('SELECT record FROM native_operations WHERE id=?').get(id);
    const value = row && JSON.parse(row.record);
    if (value?.kind !== 'tree_move') throw Error('The saved folder move is missing.');
    return value;
  }
  affected(op) { return [...this.owner.byID.values()].filter(r => r.root === savedRoot(op) && !r.deleted && inside(r.path, op.path)); }
  assertAvailable(relative,root='box') {
    for (const row of this.owner.db.prepare("SELECT record FROM native_operations WHERE state IN ('queued','pending','held','accepted') AND json_extract(record,'$.kind')='tree_move' AND json_extract(record,'$.placement.stage')<>'done'").all()) {
      const op = JSON.parse(row.record);
      if(savedRoot(op)!==root)continue;
      if (inside(relative, op.path) || inside(relative, op.target) || inside(op.path, relative) || inside(op.target, relative)) throw Error('A folder move is awaiting Main or local recovery. Your draft remains preserved.');
    }
  }
  async checkLocal(op) {
    const owner = this.owner;
    const root=savedRoot(op);owner.boxes.identity(root,op.box_id,op.box_key);
    if (!this.moveDirectory) throw Error('The native folder-move helper is not installed.');
    for (const r of owner.byID.values()) {
      if (r.root !== root||r.deleted) continue;
      if (inside(r.path, op.target)) throw Error('The local destination contains a retained working-file record. No move was made.');
      if (!inside(r.path, op.path)) continue;
      if (r.pending || r.held || r.pathOperation || r.treeOperation) throw Error('Save or resolve the pending edits inside this folder before moving it.');
      if (!r.directory && !r.deleted && (await owner.local.read(r.localPath)).text !== r.text) throw Error('An external local edit needs saving before moving this folder.');
    }
    const source = await infoAt(path.join(owner.root, op.placement.from));
    if (source && (source.isSymbolicLink() || !source.isDirectory())) throw Error('The local folder is not a real directory.');
    if (await infoAt(path.join(owner.root, op.placement.to))) throw Error('The local destination exists. No move was made.');
    return source && identity(source);
  }
  async preview(from, to,{root='box',target_root=root,box_id}={}) {
    sameBoxRoots(root,target_root);
    from = normalize(from); to = normalize(to);
    if (inside(from, to) || inside(to, from)) throw Error('Choose a separate destination folder.');
    const owner = this.owner;
    const binding=owner.boxes.identity(root,box_id);this.assertAvailable(from,root);this.assertAvailable(to,root);
    const op = { id: `desktop_tree_${randomUUID().replaceAll('-', '')}`, ...binding, kind: 'tree_move', fileKey: 'tree', path: from, target: to,
      state: 'preview', mainFirst: true, directory: true, at: new Date().toISOString(), placement: { from: owner.localPath(root,from), to: owner.localPath(root,to), stage: 'waiting' } };
    await owner.change(async () => {
      await this.checkLocal(op);
      owner.insertOp(op);
    });
    const { data } = await owner.remote.json('tree-move', binding, { method: 'POST', body: { ...(root!=='box'?{root}:{}),operation_id: op.id, from, to } });
    if (data.operation_id !== op.id || data.state !== 'preview' || !data.plan_hash) throw Error('Main did not return the exact folder preview.');
    op.preview = data; op.request = { ...(root!=='box'?{root}:{}),operation_id: op.id, from, to, plan_hash: data.plan_hash, confirm: true }; owner.saveOp(op);
    return { ...data, status: 'preview', local_files: this.affected(op).length };
  }
  async confirm(id) {
    const owner = this.owner;
    let op;
    await owner.change(async () => {
      op = this.get(id);
      if (op.state === 'accepted') { await this.finish(op); return; }
      if (op.state !== 'preview') return;
      if (!op.request) throw Error('Preview this folder move again.');
      const inode = await this.checkLocal(op);
      if (inode) op.placement.identity = inode;
      else op.placement.localAbsent = true;
      op.localFiles = this.affected(op).map(r => ({ id: r.id, from: r.path, hash: digest(Buffer.from(r.text || '')) }));
      op.state = 'queued';
      owner.transaction(() => {
        owner.saveOp(op);
        for (const item of op.localFiles) { const r = owner.byID.get(item.id); r.treeOperation = id; owner.persist(r); }
      });
    });
    owner.pump();
    return { operation_id: id, root: savedRoot(op),box_id:op.box_id, path: op.path, target: op.target, status: 'pending_main' };
  }
  async send(op) {
    const owner = this.owner;
    let result;
    try { const binding=owner.boxes.identity(savedRoot(op),op.box_id);result = (await owner.remote.json('tree-move', binding, { method: 'POST', body: op.request })).data; }
    catch (error) {
      if (error.status >= 400 && error.status < 500) {
        op.state = 'held'; op.reason = error.message; owner.saveOp(op);
        owner.emit('sync-error', { error: `Folder move needs review: ${error.message}` }); return;
      }
      throw error;
    }
    if (result.operation_id !== op.id || !['accepted', 'conflict', 'held', 'prepared'].includes(result.state)) throw Error('Main returned an invalid folder receipt.');
    if(result.root&&result.root!==savedRoot(op)||result.plan_hash&&result.plan_hash!==op.request.plan_hash)throw Error('Main returned an unrelated folder receipt.');
    await owner.change(async () => {
      op.receipt = result; op.state = result.state === 'prepared' ? 'pending' : result.state; op.reason = result.reason; owner.saveOp(op);
      if (op.state === 'accepted') await this.finish(op);
      else if (op.state === 'conflict') {
        for (const item of op.localFiles || []) { const r = owner.byID.get(item.id); if (r?.treeOperation === op.id) { delete r.treeOperation; owner.persist(r); } }
        owner.emit('sync-error', { error: `Folder was not moved: ${op.reason || 'source changed'}` });
      } else if (op.state === 'pending') owner.retry();
    });
  }
  async finish(op) {
    const owner = this.owner;
    if (op.state !== 'accepted' || op.placement.stage === 'done') return;
    try {
      const root=savedRoot(op);owner.boxes.identity(root,op.box_id,op.box_key);
      if (!op.placement.mapped) {
        const source = await infoAt(path.join(owner.root, op.placement.from));
        const destination = await infoAt(path.join(owner.root, op.placement.to));
        if (op.placement.localAbsent && source) throw Error('A new local folder appeared after confirmation. It remains preserved at its original path.');
        if (op.placement.identity) await this.moveDirectory(owner.local, op.placement);
        else if (destination || op.localFiles?.length) throw Error('Local folder placement is unverified. All files and records are preserved.');
        for (const [parent, watcher] of owner.watchers) if (inside(parent, op.placement.from)) { watcher.close(); owner.watchers.delete(parent); }
        const updates = (op.localFiles || []).map(item => {
          const r = owner.byID.get(item.id); if (!r||r.root!==root) throw Error('A retained local file record is missing or belongs to another Box.');
          const to = op.target + item.from.slice(op.path.length);
          const collision = owner.records.get(JSON.stringify([root, to]));
          if (collision && collision.id !== r.id) throw Error('A different working record occupies the local destination.');
          return { r, next: { ...r, path: to, localPath: owner.localPath(root,to) } };
        });
        owner.transaction(() => {
          for (const { next } of updates) owner.persist(next);
          op.placement.mapped = true; owner.saveOp(op);
        });
        for (const { r, next } of updates) {
          owner.records.delete(JSON.stringify([root, r.path])); Object.assign(r, next); owner.records.set(JSON.stringify([root, r.path]), r);
        }
      }
      for (const item of op.localFiles || []) {
        const r = owner.byID.get(item.id); if (!r || r.deleted) continue;
        const { data: source } = await owner.remote.json('note', { ...owner.boxes.identity(root,op.box_id), path: r.path, ...(r.directory ? { directory: 'true' } : {}) });
        if (!source.exists || !source.base_id || source.root!==root || source.path !== r.path) throw Error('The moved source base is unavailable; the local copy remains preserved.');
        r.source = source;
        if (!r.directory) {
          const disk = await owner.local.read(r.localPath);
          if (typeof source.text !== 'string' || digest(Buffer.from(source.text)) !== item.hash || disk.base !== item.hash) { r.externalText = disk.text; r.held = true; r.treeReview = 'File changed during the folder move; review the local and Main versions.'; }
        }
        owner.persist(r);
      }
      await owner.emit('note-sync', { root,box_id:op.box_id, previousPath: op.path, path: op.target, operation_id: op.id, pathAccepted: true, pending: false });
      op.placement.stage = 'done'; delete op.placement.error;
      owner.transaction(() => {
        owner.saveOp(op);
        for (const item of op.localFiles || []) { const r = owner.byID.get(item.id); if (r?.treeOperation === op.id) { delete r.treeOperation; owner.persist(r); } }
      });
      for (const item of op.localFiles || []) { const r = owner.byID.get(item.id); if (r && !r.deleted) { owner.watchLocal(r); owner.notify(r); } }
      owner.startWatch();
    } catch (error) {
      // A failed SQLite transaction may leave this in-memory flag ahead of the
      // retained outbox. Reload it before any explicit recovery attempt.
      const saved = this.get(op.id);
      if (!saved.placement.mapped) op.placement.mapped = false;
      op.placement.error = error.message; owner.saveOp(op);
      owner.emit('sync-error', { error: `Main accepted the folder move; local recovery needed: ${error.message}` });
    }
  }
}
