import {normalize, digest} from './local-files.mjs';
import {savedRoot,sameBoxRoots} from './box-roots.mjs';

const route = '/v1/files/workspace-path';
const terminal = state => state === 'complete' || state === 'cancelled';
const within = (value, parent) => parent === '' || value === parent || value.startsWith(parent + '/');
const overlaps = (a, b) => within(a, b) || within(b, a);
const reference = value => {
  if (typeof value !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,255}$/.test(value) || value.includes('..')) throw Error('Invalid saved workspace reference.');
  return value;
};
const relative = value => {
  if (typeof value !== 'string' || !value || normalize(value) !== value) throw Error('Choose a clean visible workspace path.');
  return value;
};
const now = () => new Date().toISOString();

// Main owns physical mutation. This journal owns the handover of this device's
// already-present files, never enrollment, download or project activation.
export class WorkspacePaths {
  constructor(notes, remote, {checkDrafts = () => {}, checkUploads = () => {}} = {}) {
    Object.assign(this, {notes, remote, checkDrafts, checkUploads});
    notes.db.exec('CREATE TABLE IF NOT EXISTS workspace_paths (id TEXT PRIMARY KEY, record TEXT NOT NULL)');
  }

  get(id) {
    const row = this.notes.db.prepare('SELECT record FROM workspace_paths WHERE id=?').get(reference(id));
    if (!row) throw Error('Saved workspace path review not found.');
    return JSON.parse(row.record);
  }
  save(op) {
    this.notes.db.prepare('UPDATE workspace_paths SET record=? WHERE id=?').run(JSON.stringify(op), op.id);
  }
  event(op, action, detail = {}) {
    op.updatedAt = now();
    op.history.push({action, at: op.updatedAt, state: op.state, ...detail});
    this.save(op);
  }
  list() {
    return this.notes.db.prepare('SELECT record FROM workspace_paths ORDER BY rowid DESC LIMIT 100').all().map(row => JSON.parse(row.record));
  }
  blocked(relativePath,root='box') {
    for (const row of this.notes.db.prepare('SELECT record FROM workspace_paths').iterate()) {
      const op = JSON.parse(row.record);
      if (op.state !== 'preview' && !terminal(op.state) && this.intersects(op, relativePath,root)) return op;
    }
  }
  intersects(op, value,root='box') {
    return (op.from_root||'box')===root&&overlaps(value, op.source) || op.to_root===root&&overlaps(value, op.destination);
  }
  validate(data, expected, {id, plan_hash, entries, bytes} = {}) {
    if (!data || typeof data !== 'object' || Array.isArray(data)) throw Error('Main returned no workspace path review.');
    reference(data.id);
    if (data.kind !== expected.kind || data.project !== expected.project || data.source !== expected.from || data.destination !== expected.to || data.from_root !== (expected.from_root||'box') || data.to_root !== (expected.to_root||(expected.kind === 'relocate_project' ? 'box' : 'storage')) || typeof data.plan_hash !== 'string' || !data.plan_hash || data.plan_hash.length > 256 || typeof data.state !== 'string' || !/^[a-z_]{1,64}$/.test(data.state) || !Number.isSafeInteger(data.entries) || data.entries < 0 || !Number.isSafeInteger(data.bytes) || data.bytes < 0 || typeof data.qualification !== 'string') throw Error('Main did not return the exact workspace path review.');
    relative(data.source); relative(data.destination);
    if (id !== undefined && (data.id !== id || data.plan_hash !== plan_hash || data.entries !== entries || data.bytes !== bytes)) throw Error('Main receipt does not match the saved workspace ID, plan hash and inventory.');
    return structuredClone(data);
  }
  expected(op) { return {kind: op.kind, project: op.project, from: op.source, to: op.destination,...(op.from_root&&op.from_root!=='box'?{from_root:op.from_root,to_root:op.to_root}:{})}; }
  body(op, action) {
    return {action, ...this.expected(op), id: op.id, plan_hash: op.plan_hash, ...(['apply', 'cancel'].includes(action) ? {confirm: true} : {})};
  }
  async request(op, action) {
    const {data} = await this.remote.api(route, this.notes.boxes.identity(op.from_root||'box',op.box_id), {method: 'POST', body: this.body(op, action)});
    return this.validate(data, this.expected(op), op);
  }
  assertAvailable(op) {
    if (this.notes.suspended || this.notes.closed) throw Error('Local recovery capture is active. No workspace path mutation was submitted.');
    for (const row of this.notes.db.prepare('SELECT record FROM workspace_paths').iterate()) {
      const other = JSON.parse(row.record);
      if (other.id !== op.id && other.state !== 'preview' && !terminal(other.state) && (this.intersects(other, op.source,op.from_root||'box') || this.intersects(other, op.destination,op.to_root))) throw Error('An overlapping workspace path operation needs explicit recovery.');
    }
    // blocked/checkAvailable compose this owner's guard in the integrator, so
    // read only the lifecycle owner's records, with both ancestry directions.
    for (const other of this.notes.lifecycle?.list() || []) {
      if (!['preview', 'complete', 'rejected', 'cancelled'].includes(other.state) && typeof other.boxPath === 'string' && this.intersects(op, other.boxPath,savedRoot(other))) throw Error('An overlapping project lifecycle operation needs review.');
    }
  }
  async checks(op, {recover = false} = {}) {
    this.notes.boxes.identity(op.from_root||'box',op.box_id);
    this.assertAvailable(op);
    // Check endpoints, not whole parent subtrees: siblings remain independent.
    // Pending ancestor folder operations are checked through the outbox below.
    const scopes = new Map();
    for (const [root, value] of [[op.from_root||'box', op.source], [op.to_root, op.destination]]) {
      scopes.set(JSON.stringify([root, value]), {root, scope: value});
    }
    for (const {root, scope} of scopes.values()) {
      await this.checkDrafts(scope, {root, kind: op.kind});
      await this.checkUploads(scope, {root, kind: op.kind});
    }
    for (const r of this.notes.byID.values()) {
      if (r.deleted || !this.intersects(op, r.path,r.root)) continue;
      if (r.pending || r.held || r.pathOperation || r.treeOperation && (!recover || r.treeOperation !== op.id) || r.externalText !== undefined || r.localAbsence || r.retainedProject && r.retainedProject !== op.id) throw Error(`Resolve pending local changes at ${r.path} before changing workspace paths.`);
      if (op.kind === 'retain_child' && within(r.path, op.source) && !r.directory && (await this.notes.local.read(r.localPath)).text !== r.text) throw Error(`An external local edit at ${r.path} is preserved. Save it before retention.`);
    }
    for (const row of this.notes.db.prepare("SELECT record FROM native_operations WHERE state IN ('queued','pending','held','accepted')").iterate()) {
      const other = JSON.parse(row.record);
      if (other.id === op.id) continue;
      if (other.state === 'accepted' && (!other.mainFirst || other.placement?.stage === 'done')) continue;
      if ([other.path, other.target].some(value => typeof value === 'string' && this.intersects(op, value,savedRoot(other)))) throw Error('A pending local outbox operation overlaps a workspace path or its parent.');
    }
  }

  async review(kind, project, from, to,{root='box',box_id,target_root=kind==='relocate_project'?root:'storage'}={}) {
    if (!['relocate_project', 'retain_child'].includes(kind)) throw Error('Choose project relocation or child retention.');
    const binding=this.notes.boxes.identity(root,box_id);
    if(kind==='relocate_project')sameBoxRoots(root,target_root);
    else if(target_root!=='storage')throw Error('Child retention requires the explicit Storage destination.');
    reference(project); relative(from); relative(to);
    if (kind === 'relocate_project' && overlaps(from, to)) throw Error('Choose a separate Box destination.');
    return this.notes.change(async () => {
      const expected = {kind, project, from, to,...(root!=='box'?{from_root:root,to_root:target_root}:{})};
      const candidate = {...expected,...binding,source: from, destination: to,from_root:root,to_root:target_root};
      await this.checks(candidate);
      const {data} = await this.remote.api(route, binding, {method: 'POST', body: {action: 'plan', ...expected}});
      const review = this.validate(data, expected);
      if (!['planned', 'preview'].includes(review.state)) throw Error('Main did not return an unsubmitted workspace review.');
      const existing = this.notes.db.prepare('SELECT record FROM workspace_paths WHERE id=?').get(review.id);
      if (existing) {
        const saved = JSON.parse(existing.record);
        this.validate(review, this.expected(saved), saved);
        return saved; // Never reset an existing journal to a new preview.
      }
      const op = {...review,...binding, state: 'preview', review, at: now(), history: [{action: 'plan', state: 'preview', mainState: review.state, at: now()}]};
      this.notes.db.prepare('INSERT INTO workspace_paths VALUES(?,?)').run(op.id, JSON.stringify(op));
      return op;
    });
  }

  async prepare(op) {
    await this.checks(op);
    const notes = this.notes;
    const root=op.from_root||'box',binding=notes.boxes.identity(root,op.box_id);
    const affected = [...notes.byID.values()].filter(r => !r.deleted && r.root === root && within(r.path, op.source));
    op.localFiles = affected.map(r => ({id: r.id, from: r.path, hash: digest(Buffer.from(r.text || ''))}));
    let tree;
    if (op.kind === 'relocate_project') {
      tree = {id: op.id,...binding, kind: 'tree_move', fileKey: 'tree', path: op.source, target: op.destination, state: 'held', mainFirst: true, directory: true, at: now(), workspacePath: {id: op.id, plan_hash: op.plan_hash}, placement: {from: notes.localPath(root,op.source), to: notes.localPath(root,op.destination), stage: 'waiting'}, localFiles: op.localFiles};
      const identity = await notes.trees.checkLocal(tree);
      if (identity) tree.placement.identity = identity;
      else tree.placement.localAbsent = true;
      if (notes.db.prepare('SELECT id FROM native_operations WHERE id=?').get(tree.id)) throw Error('An unrelated outbox operation already uses this workspace ID.');
    }
    op.state = 'submitted'; op.prepared = true;
    notes.transaction(() => {
      if (tree) notes.insertOp(tree);
      for (const r of affected) notes.persist({...r, treeOperation: op.id});
      this.event(op, 'submit', {request: this.body(op, 'apply')});
    });
    for (const r of affected) r.treeOperation = op.id;
  }
  release(op) {
    const notes = this.notes;
    const affected = [...notes.byID.values()].filter(r => r.treeOperation === op.id);
    notes.transaction(() => {
      if (op.kind === 'relocate_project' && op.prepared) {
        const tree = this.tree(op);
        if (tree.placement.mapped || tree.placement.stage === 'done') throw Error('A locally moved folder cannot be cancelled.');
        tree.state = 'cancelled'; tree.placement.stage = 'cancelled'; notes.saveOp(tree);
      }
      for (const r of affected) { const next = {...r}; delete next.treeOperation; notes.persist(next); }
      op.state = 'cancelled'; this.event(op, 'cancelled');
    });
    for (const r of affected) delete r.treeOperation;
    notes.startWatch();
  }
  tree(op) {
    const tree = this.notes.trees.get(op.id);
    if (tree.workspacePath?.id !== op.id || tree.workspacePath?.plan_hash !== op.plan_hash || tree.path !== op.source || tree.target !== op.destination || JSON.stringify(tree.localFiles) !== JSON.stringify(op.localFiles)) throw Error('The exact local workspace handover is missing. Files remain held.');
    return tree;
  }
  async finish(op) {
    if (!op.prepared || op.receipt?.state !== 'complete') throw Error('Main completion and the original local handover are required.');
    if (op.kind === 'relocate_project') {
      const tree = this.tree(op);
      tree.state = 'accepted'; tree.receipt = op.receipt;
      this.notes.saveOp(tree);
      await this.notes.trees.finish(tree);
      const saved = this.tree(op);
      if (saved.placement.stage !== 'done') throw Error(saved.placement.error || 'The existing local folder still needs exact recovery.');
    } else {
      const updates = op.localFiles.map(item => {
        const r = this.notes.byID.get(item.id);
        if (!r || r.deleted || r.root !== (op.from_root||'box') || r.path !== item.from || r.treeOperation !== op.id && r.retainedProject !== op.id) throw Error('A retained local record changed during handover.');
        const next = {...r, retainedProject: op.id, retainedProjectKind: 'retain_child', retainedProjectReason: 'Project child retained in Storage; this local copy remains at its original Box path.'};
        delete next.treeOperation;
        return {r, next};
      });
      this.notes.transaction(() => { for (const {next} of updates) this.notes.persist(next); });
      for (const {r, next} of updates) { delete r.treeOperation; Object.assign(r, next); await this.notes.notify(r); }
    }
    op.state = 'complete'; delete op.error; this.event(op, 'local_complete');
    this.notes.emit('workspace-paths', {id: op.id, kind: op.kind, project: op.project, source: op.source, destination: op.destination, state: op.state});
    this.notes.startWatch();
  }
  observe(op, receipt) {
    op.receipt = receipt;
    this.event(op, 'status', {mainState: receipt.state});
    if (receipt.state === 'cancelled') this.release(op);
    else if (!terminal(op.state) && (op.state !== 'preview' || !['planned', 'preview'].includes(receipt.state))) {
      op.state = 'needs_review'; this.event(op, 'held', {mainState: receipt.state});
    }
  }
  fail(op, error) {
    op.state = 'needs_review'; op.error = error.message;
    if (error.receipt) {
      try { op.receipt = this.validate(error.receipt, this.expected(op), op); } catch { /* An unrelated partial receipt grants no authority. */ }
    }
    this.event(op, 'error', {error: op.error});
    return op;
  }

  async inspect(id) {
    return this.notes.change(async () => {
      const op = this.get(id);
      if (terminal(op.state)) return op;
      try { this.observe(op, await this.request(op, 'status')); } catch (error) { this.fail(op, error); }
      return op;
    });
  }
  async apply(id, {recover = false} = {}) {
    return this.notes.change(async () => {
      const op = this.get(id);
      if (terminal(op.state)) return op;
      if (!recover && op.state !== 'preview') throw Error('Inspect this saved operation and explicitly recover the same ID/hash; do not submit another plan.');
      if (recover && op.state === 'preview') throw Error('This workspace review has not been submitted.');
      if (!recover) await this.prepare(op);
      try {
        if (recover) {
          this.observe(op, await this.request(op, 'status'));
          if (op.state === 'cancelled') return op;
          if (!op.prepared) throw Error('The original local handover is missing. Files remain held.');
          if (op.receipt.state !== 'complete') {
            if (op.cancelRequested) throw Error('Inspect or finish the saved cancellation before applying again.');
          }
          await this.checks(op, {recover: true});
        }
        if (!recover || op.receipt.state !== 'complete') {
          op.state = 'submitted'; this.event(op, recover ? 'recover' : 'apply', {request: this.body(op, 'apply')});
          op.receipt = await this.request(op, 'apply');
          this.event(op, 'receipt', {mainState: op.receipt.state});
        }
        if (op.receipt.state === 'cancelled') this.release(op);
        else if (op.receipt.state === 'complete') await this.finish(op);
        else { op.state = 'needs_review'; this.event(op, 'held', {mainState: op.receipt.state}); }
      } catch (error) { this.fail(op, error); }
      return op;
    });
  }
  async cancel(id) {
    return this.notes.change(async () => {
      const op = this.get(id);
      if (terminal(op.state)) return op;
      op.cancelRequested = true; op.state = 'cancelling';
      this.event(op, 'cancel', {request: this.body(op, 'cancel')});
      try { this.observe(op, await this.request(op, 'cancel')); } catch (error) { this.fail(op, error); }
      return op;
    });
  }
}
