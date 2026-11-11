import { DatabaseSync } from 'node:sqlite';
import { mkdir, lstat, open, rename, link, unlink, readFile } from 'node:fs/promises';
import { watch } from 'node:fs';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { LocalFiles, normalize, digest } from './local-files.mjs';
import { placeAcceptedPath } from './local-placement.mjs';
import { isEditableText, defaultFileName } from './file-formats.mjs';
import { TreeMoves } from './tree-moves.mjs';
import { ProjectLifecycle } from './project-lifecycle.mjs';
import {BoxRoots,savedRoot,sameBoxRoots} from './box-roots.mjs';

const MAX_TEXT = 512 * 1024;
const MAX_WORKING_BYTES = 256 * 1024 ** 2;
const editable = isEditableText;
const key = (root, relative) => JSON.stringify([root, relative]);
const operationRootSQL="COALESCE(json_extract(record,'$.root'),json_extract(record,'$.request.root'),json_extract(record,'$.base.root'),'box')";
export const watchDirectories=paths=>paths.map(relative=>relative||'.');
const validateText = text => {
  if (typeof text !== 'string' || Buffer.byteLength(text) > MAX_TEXT || text.includes('\0')) throw Error('Native note editing currently supports UTF-8 files up to 512 KiB. The original and draft are preserved.');
  return text;
};

// This owner has its own ordinary files and record-level journal. It never reads
// or writes the Obsidian database, plugin settings, aliases or pending intents.
export class NotesSync {
  constructor(directory, remote, { emit = () => {}, moveDirectory, workingRoot, boxes = new BoxRoots() } = {}) {
    this.directory = directory; this.remote = remote; this.emit = emit; this.boxes = boxes; this.rootWatches = new Map();
    this.root = workingRoot || path.join(directory, 'working-set'); this.local = new LocalFiles(this.root);
    this.records = new Map(); this.byID = new Map(); this.workingBytes = 0; this.watchers = new Map(); this.reviews=new Map(); this.serial = Promise.resolve(); this.closed = false;
    this.trees = new TreeMoves(this, moveDirectory);
  }

  async init() {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    for (const target of [this.directory, this.root]) {
      await mkdir(target, { mode: 0o700 }).catch(e => { if (e.code !== 'EEXIST') throw e; });
      if ((await lstat(target)).isSymbolicLink()) throw Error('Client state cannot be a symbolic link.');
    }
    const database = path.join(this.directory, 'notes.sqlite');
    const info = await lstat(database).catch(e => { if (e.code !== 'ENOENT') throw e; });
    if (info && (!info.isFile() || info.isSymbolicLink())) throw Error('Invalid client journal.');
    this.db = new DatabaseSync(database);
    this.db.exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
      CREATE TABLE IF NOT EXISTS files (id TEXT PRIMARY KEY, path TEXT UNIQUE NOT NULL, record TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS operations (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, file_id TEXT NOT NULL, state TEXT NOT NULL, record TEXT NOT NULL);
      CREATE INDEX IF NOT EXISTS operations_pending ON operations(state,seq);
      CREATE TABLE IF NOT EXISTS box_operations (seq INTEGER PRIMARY KEY, id TEXT UNIQUE NOT NULL, file_id TEXT NOT NULL, state TEXT NOT NULL, record TEXT NOT NULL);
      CREATE TEMP VIEW IF NOT EXISTS native_operations AS SELECT * FROM operations UNION ALL SELECT * FROM box_operations;`);
    this.lifecycle=new ProjectLifecycle(this,this.remote);
    for (const row of this.db.prepare('SELECT record FROM files').all()) {
      const r = JSON.parse(row.record); normalize(r.path); if(!r.deleted)this.records.set(key(r.root, r.path), r);else this.persist(r);this.byID.set(r.id,r);this.workingBytes+=Buffer.byteLength(r.text||'');
    }
    for (const row of this.db.prepare("SELECT record FROM native_operations WHERE state='accepted' AND json_extract(record,'$.kind')='tree_move' AND json_extract(record,'$.placement.stage')<>'done'").all()) await this.trees.finish(JSON.parse(row.record));
    for (const r of [...this.records.values()]) {
      if (!this.recordAvailable(r) || r.treeOperation || r.retainedProject || this.lifecycle.blocked(r.path,r.root)) continue;
      const pathOperation = r.pathOperation && JSON.parse(this.db.prepare('SELECT record FROM native_operations WHERE id=?').get(r.pathOperation.id)?.record || 'null');
      if (pathOperation?.state === 'accepted') {
        await this.finishPath(r, pathOperation);
        if (!r.deleted) this.watchLocal(r);
        continue;
      }
      if (!r.deleted) {
        if(r.observation||r.localAbsence)continue;
        const saved=r.pathOperation&&JSON.parse(this.db.prepare('SELECT record FROM native_operations WHERE id=?').get(r.pathOperation.id)?.record||'null');
        if(saved?.placement?.external)continue;
        const disk = await this.local.read(r.localPath).catch(() => null);
        if (disk && disk.text !== r.text && disk.base !== r.diskHash) {
          // An external editor may have saved after our last checkpoint. Preserve
          // its bytes instead of rematerializing older journal content over them.
          if(r.pending){r.externalText=disk.text;r.held=true;this.persist(r);}
          else this.queue(r, 'edit', disk.text);
        } else await this.materialize(r);
        this.watchLocal(r);
      }
    }
  }

  get(root, relative) { const r = this.records.get(key(root, relative)); return r && !r.deleted ? r : undefined; }
  available(root) { try { this.boxes.identity(root); return true; } catch { return false; } }
  recordAvailable(r) { try { this.boxes.identity(r.root,r.box_id,r.box_key);return r.localPath===this.localPath(r.root,r.path); } catch { return false; } }
  localPath(root, relative) { return this.boxes.localPath(root,relative); }
  relativePath(root, localPath) { return this.boxes.relative(root,localPath); }
  change(run) {
    const result = this.serial.catch(() => {}).then(run); this.serial = result; return result;
  }
  transaction(run) {
    this.db.exec('BEGIN IMMEDIATE');
    try { const value = run(); this.db.exec('COMMIT'); return value; } catch (e) { this.db.exec('ROLLBACK'); throw e; }
  }
  persist(r) { this.db.prepare('INSERT INTO files VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET path=excluded.path,record=excluded.record').run(r.id, r.deleted?JSON.stringify([r.root,'retired',r.id]):key(r.root,r.path), JSON.stringify(r)); }
  insertOp(op) {
    const table=savedRoot(op)==='box'?'operations':'box_operations';
    const seq=this.db.prepare('SELECT COALESCE(MAX(seq),0)+1 AS seq FROM native_operations').get().seq;
    this.db.prepare(`INSERT INTO ${table}(seq,id,file_id,state,record) VALUES(?,?,?,?,?)`).run(seq,op.id,op.fileKey,op.state,JSON.stringify(op));
  }
  saveOp(op) { for(const table of ['operations','box_operations'])this.db.prepare(`UPDATE ${table} SET state=?,record=? WHERE id=?`).run(op.state, JSON.stringify(op), op.id); }
  activitySummary(root) {
    const counts = Object.fromEntries(this.db.prepare(`SELECT state,COUNT(*) AS count FROM native_operations ${root?'WHERE '+operationRootSQL+'=?':''} GROUP BY state`).all(...root?[root]:[]).map(r=>[r.state,Number(r.count)]));
    let placement=0,recovery=0;
    // Local placement qualifiers are not the same as Main acceptance.
    for(const r of this.records.values()){if(root&&r.root!==root)continue;if(r.pathOperation)placement++;if(r.localRecoveryNeeded)recovery++;}
    return {counts,placement_pending:placement,local_recovery:recovery,observed_at:new Date().toISOString(),complete:true};
  }
  activity(root) {
    return this.db.prepare(`SELECT record FROM native_operations ${root?'WHERE '+operationRootSQL+'=?':''} ORDER BY seq DESC LIMIT 50`).all(...root?[root]:[]).map(row => {
      const op = JSON.parse(row.record);
      const r=this.byID.get(op.fileKey);
      if(op.kind==='metadata_action')return {id:op.id,name:op.target||op.path,...(op.state==='accepted'&&op.action!=='delete'?{root:op.root||op.request?.root||'box',box_id:op.box_id,path:op.target||op.path}:{}),owner:'local',status:op.placement?.error?'local_recovery':op.state,description:op.placement?.error||op.reason||`${op.action} · remote asset`,at:op.at};
      return { id: op.id, name: op.target || op.path, ...(!r?.deleted?{path:r?.path||op.target||op.path,root:r?.root||op.root||'box',box_id:r?.box_id||op.box_id}:{}), owner: 'local',
        status: op.placement?.error ? 'local_recovery' : op.mainFirst&&op.state==='accepted'&&(r?.pathOperation?.id===op.id||op.kind==='tree_move'&&op.placement.stage!=='done') ? 'local_pending' : op.state, description: op.placement?.error || op.reason || `${op.kind} · native client`, at: op.at };
    });
  }
  pendingHistory({root,path:scope='',from,to,kind}){
    const rows=this.db.prepare("SELECT id,file_id,state,json_extract(record,'$.at') AS at,json_extract(record,'$.path') AS path,json_extract(record,'$.target') AS target,json_extract(record,'$.kind') AS kind FROM native_operations WHERE state<>'accepted' AND file_id IN (SELECT id FROM files WHERE json_extract(record,'$.root')=?) ORDER BY seq DESC LIMIT 100").all(root);
    return rows.filter(row=>{const r=this.byID.get(row.file_id);return r?.root===root&&(!scope||row.path===scope||row.path?.startsWith(scope+'/'))&&(!from||row.at>=from)&&(!to||row.at<to)&&(!kind||row.kind===kind);}).map(row=>({id:row.id,file_id:row.file_id,at:row.at,path:row.path,target:row.target,current_path:this.byID.get(row.file_id)?.path,kind:row.kind,state:'local_'+row.state,reason:'Recent local operation; not source acceptance.'}));
  }
  inspect(id){
    const row=this.db.prepare('SELECT record FROM native_operations WHERE id=?').get(id);if(!row)throw Error('No local operation found.');
    const op=JSON.parse(row.record);
    const localPending=op.mainFirst&&(this.byID.get(op.fileKey)?.pathOperation?.id===op.id||op.kind==='tree_move'&&op.placement.stage!=='done');
    return {summary:[`Operation: ${op.id}`,`Path: ${op.path}`,op.target?`Destination: ${op.target}`:'',`Action: ${op.kind}`,`State: ${op.state}`,`Source receipt: ${op.receipt?.state||'Not received'}`,op.mainFirst?`Local placement: ${op.placement?.stage||'waiting for Main'}`:'',op.mainFirst?`Local handover: ${localPending?'pending':'complete'}`:'',op.placement?.error?`Local recovery: ${op.placement.error}`:'',op.reason?`Reason: ${op.reason}`:'',op.resolvedBy?`Resolved by: ${op.resolvedBy}`:''].filter(Boolean).join('\n')};
  }
  notify(r,extra={}) { return this.emit('note-sync', { root:r.root, path:r.path, binding_id:r.id, pending:r.pending, held:r.held, deleted:r.deleted, pendingTarget:r.pathOperation?.target, localRecoveryNeeded:r.localRecoveryNeeded, ...extra }); }

  async finishPath(r, op) {
    const previousPath = op.path;
    try {
      if (op.directory && op.placement.stage !== 'done') {
        const children = await this.local.list(op.placement.from).catch(error => { if (error.code !== 'ENOENT') throw error; return { entries: [] }; });
        if (children.entries.some(entry => !this.records.get(key(r.root, this.relativePath(r.root,entry.relative_path)))?.deleted)) throw Error('New local children need review. Main accepted; the local folder was preserved.');
      }
      await placeAcceptedPath(this.local, path.join(this.directory, 'local-recovery'), op, async () => this.saveOp(op));
      if (op.kind === 'rename') {
        this.records.delete(key(r.root, r.path));
        r.path = op.target; r.localPath = op.placement.to;
        this.records.set(key(r.root, r.path), r);
        const disk = r.directory ? null : await this.local.read(r.localPath);
        if (disk && disk.text !== r.text) {
          if(disk.base===r.diskHash)await this.materialize(r);
          else {
            r.externalText = disk.text;
            throw Error('Main accepted the move; a concurrent local edit is preserved at its new path and needs review.');
          }
        }
        this.watchLocal(r);
      } else r.deleted = true;
      if(r.deleted)this.records.delete(key(r.root,r.path));
      // Keep pathOperation durable until the external draft owner acknowledges
      // handover. Restart can replay this even after the working path changed.
      await this.notify(r, { previousPath, operation_id: op.id, pathAccepted: true });
      delete op.placement.error;
      delete r.pathOperation; delete r.localRecoveryNeeded;delete r.localAbsence;
      r.pending = r.lastOperation !== op.id; r.held = false;
      this.transaction(() => { this.saveOp(op); this.persist(r); });
      this.notify(r, { operation_id: op.id, pathAccepted: true });
      this.startWatch();
    } catch (error) {
      op.placement.error = error.message; r.localRecoveryNeeded = error.message; r.held = true; r.pending = true;
      this.transaction(() => { this.saveOp(op); this.persist(r); });
      this.notify(r, { operation_id: op.id, pathAccepted: true });
    }
  }

  async materialize(r) {
    if (r.deleted) return; // Retained bytes/history are not silently unlinked.
    const parts = normalize(r.localPath).split('/'); let directory = this.root;
    for (const part of parts.slice(0,-1)) {
      directory = path.join(directory,part);
      await mkdir(directory,{mode:0o700}).catch(e=>{if(e.code!=='EEXIST')throw e;});
      if (!(await lstat(directory)).isDirectory() || (await lstat(directory)).isSymbolicLink()) throw Error('Unsafe local parent.');
    }
    if (r.directory) {
      const target=path.join(directory,parts.at(-1)); await mkdir(target,{mode:0o700}).catch(e=>{if(e.code!=='EEXIST')throw e;});
      if (!(await lstat(target)).isDirectory() || (await lstat(target)).isSymbolicLink()) throw Error('Unsafe local folder.');
      const info=await lstat(target);r.diskIdentity=`${info.dev}:${info.ino}`;this.persist(r);return;
    }
    const destination=path.join(directory,parts.at(-1)), temporary=path.join(directory,`.loom-${randomUUID()}.tmp`);
    const info=await lstat(destination).catch(e=>{if(e.code!=='ENOENT')throw e;});
    if(info && (!info.isFile()||info.isSymbolicLink()))throw Error('Unsafe local note.');
    const before=info?await this.local.read(r.localPath):null;
    if(before?.text===r.text){r.diskHash=before.base;const info=await lstat(destination);r.diskIdentity=`${info.dev}:${info.ino}`;this.persist(r);return;}
    if(before&&before.text!==r.text&&before.base!==r.diskHash){r.externalText=before.text;r.held=true;this.persist(r);throw Error('An external local edit was preserved. Review it before replacing either version.');}
    const file=await open(temporary,'wx',0o600);
    try { await file.writeFile(r.text,'utf8'); await file.sync(); } finally { await file.close(); }
    if(before && (await this.local.read(r.localPath)).base!==before.base)throw Error('The local note changed during materialization. Both the journal and local bytes are preserved.');
    if(!before){
      // link is a no-replace publication; a newly arrived external destination
      // must not be overwritten by an earlier absent-path observation.
      await link(temporary,destination);await unlink(temporary);
    }else{
      const retained=path.join(this.directory,'local-recovery');await mkdir(retained,{mode:0o700,recursive:true});
      const saved=path.join(retained,`${r.id}-${randomUUID()}.txt`);
      // Atomically displace, retain and inspect the actual current inode. Never
      // use an unconditional rename over a destination an external editor can
      // replace between checks. Publication then uses no-replace link.
      await rename(destination,saved);
      const captured=await readFile(saved,'utf8');
      if(digest(Buffer.from(captured))!==before.base){
        await link(saved,destination).catch(e=>{if(e.code!=='EEXIST')throw e;});
        r.externalText=captured;r.held=true;this.persist(r);throw Error('A racing external edit was retained. Review it before saving over either version.');
      }
      await link(temporary,destination);await unlink(temporary);
    }
    r.diskHash=digest(Buffer.from(r.text));const placed=await lstat(destination);r.diskIdentity=`${placed.dev}:${placed.ino}`;this.persist(r);
  }

  watchLocal(r) {
    if (r.directory || !this.recordAvailable(r)) return;
    const parent = path.posix.dirname(r.localPath);
    if (this.watchers.has(parent)) return;
    if (this.watchers.size >= 1024) {this.emit('sync-error',{error:'Local watch capacity reached. Additional files remain saved; refresh them explicitly.'});return;}
    const watcher=watch(path.join(this.root,parent),{persistent:false},(_event,name)=>{
      if(!name||name.startsWith('.'))return;
      if(this.suspended)return;
      const target=this.get(r.root,this.relativePath(r.root,path.posix.join(parent,name)));
      if(!target||target.treeOperation||target.retainedProject)return;
      void this.change(async()=>{
        const disk=await this.local.read(target.localPath).catch(()=>null);
        if(disk && disk.text!==target.text&&!target.held&&!target.pathOperation&&!target.retainedProject){this.queue(target,'edit',disk.text);this.notify(target);this.pump();}
      }).catch(e=>this.emit('sync-error',{error:e.message}));
    });
    watcher.on('error',()=>{watcher.close();this.watchers.delete(parent);});this.watchers.set(parent,watcher);
  }

  async adopt(root, relative, { absent = false, directory = false } = {}) {
    relative=normalize(relative);
    const binding=this.boxes.identity(root);
    if(!directory&&!editable(relative))throw Error('This is not a supported writable note.');
    const existing=this.get(root,relative);if(existing)return existing;
    if(this.records.size>=100000)throw Error('Local working-set entry capacity reached. Existing files and pending edits remain preserved.');
    const {data:source}=await this.remote.json('note',{...binding,path:relative,...(directory?{directory:'true'}:{})});
    if(!source?.file?.file_id||!source.base_id||!source.generation||source.path!==relative||source.root!==root)throw Error('Main returned an incomplete source base.');
    validateText(source.text);
    if(absent===source.exists)throw Error(absent?'The destination already exists.':'The source note is missing.');
    const record={id:randomUUID(),...binding,path:relative,localPath:this.localPath(root,relative),text:source.text,directory,source,pending:false,held:false};
    return this.change(async()=>{
      if(this.suspended)throw Error('Recovery capture is in progress. Reopen the file shortly.');
      const concurrent=this.get(root,relative);if(concurrent)return concurrent;
      if(this.records.size>=100000)throw Error('Local working-set entry capacity reached. Existing files are preserved.');
      if(this.workingBytes+Buffer.byteLength(record.text)>MAX_WORKING_BYTES)throw Error('Local working-set text budget reached (256 MiB). Existing files and pending edits remain preserved.');
      this.persist(record);this.records.set(key(root,relative),record);this.byID.set(record.id,record);this.workingBytes+=Buffer.byteLength(record.text);
      await this.materialize(record);this.watchLocal(record);this.startWatch();return record;
    });
  }

  queue(r, kind, text='', target, { mainFirst = false } = {}) {
    const binding=this.boxes.identity(r.root,r.box_id,r.box_key);
    if(!this.recordAvailable(r))throw Error('Saved local replica does not match its registered Box. Existing bytes are preserved.');
    this.lifecycle?.checkAvailable(r.path,r.root);
    if(r.retainedProject)throw Error(r.retainedProjectKind==='retain_child'?'This folder was retained in Storage. Its local copy and history remain preserved; make a separate working copy to edit it.':'This local copy belongs to an archived project. Restore it or explicitly make a working copy; the retained bytes are preserved.');
    validateText(text);
    if(r.held)throw Error('This note has a conflict or held operation. Your draft is preserved; inspect its source before retrying.');
    const op={id:`desktop_${randomUUID().replaceAll('-','')}`,...binding,fileKey:r.id,kind,path:r.path,target,
      text,base:r.source,predecessor:r.pending?r.lastOperation:undefined,state:'queued',at:new Date().toISOString(),directory:r.directory};
    if (mainFirst) {
      op.mainFirst = true;
      op.placement = { from: r.localPath, ...(target ? { to: this.localPath(r.root,target) } : {}), stage: 'waiting' };
    }
    const before=structuredClone(r);
    try {
      this.transaction(()=>{
        this.workingBytes+=Buffer.byteLength(text)-Buffer.byteLength(r.text);r.text=text;r.pending=true;r.lastOperation=op.id;
        this.insertOp(op);
        if(mainFirst)r.pathOperation={id:op.id,kind,target};
        else {
          if(kind==='rename'){this.records.delete(key(r.root,r.path));r.path=target;r.localPath=this.localPath(r.root,target);this.records.set(key(r.root,target),r);}
          if(kind==='delete')r.deleted=true;
        }
        this.persist(r);
      });
    }catch(e){this.workingBytes+=Buffer.byteLength(before.text)-Buffer.byteLength(r.text);this.records.delete(key(r.root,r.path));Object.assign(r,before);this.records.set(key(r.root,r.path),r);throw e;}
    return op;
  }

  async read(r) {
    const read=await this.local.read(r.localPath);
    return {...read,editable:!r.directory&&!r.retainedProject&&r.pathOperation?.kind!=='delete',pending:r.pending,held:r.held,pendingTarget:r.pathOperation?.target,localRecoveryNeeded:r.localRecoveryNeeded,binding_id:r.id};
  }
  async save(root, relative, text, expected) {
    if(this.suspended)throw Error('Recovery capture is in progress. Your editor draft is preserved; save again shortly.');
    const r=this.get(root,normalize(relative));if(!r)throw Error('Open this native local note before saving.');
    await this.change(async()=>{
      if(r.treeOperation)throw Error('This folder is moving. Your editor draft is preserved; save it again after the move completes.');
      if(r.pathOperation?.kind==='delete')throw Error('Removal is awaiting Main. Your editor draft remains preserved.');
      if(digest(Buffer.from(r.text))!==expected)throw Error('The note changed locally; your draft is preserved. Reopen it before saving.');
      const disk=await this.local.read(r.localPath);
      if(disk.base!==expected)throw Error('An external editor changed this note. Your draft is preserved.');
      this.queue(r,'edit',text);await this.materialize(r);this.notify(r);
    });
    this.pump();return {...await this.read(r),status:'saved_locally_pending_source'};
  }

  async mutate({root,box_id,target_root=root,path:relative,operation,name,target,text='',directory=false}) {
    if(this.suspended)throw Error('Recovery capture is in progress. Try the file action again shortly.');
    relative=normalize(relative);
    sameBoxRoots(root,target_root);this.boxes.identity(root,box_id);
    this.trees.assertAvailable(relative,root);
    this.lifecycle?.checkAvailable(relative,root);
    if (directory && operation === 'rename') {
      const destination = normalize(target || path.posix.join(path.posix.dirname(relative), name || ''));
      this.trees.assertAvailable(destination,root);
      const { data: children } = await this.remote.json('entries', { root, path: relative, limit: 1 });
      const localChildren = await this.local.list(this.localPath(root,relative)).catch(error => { if (error.code !== 'ENOENT') throw error; return { entries: [] }; });
      if (children.entries?.length || children.has_more || localChildren.entries.length) return this.trees.preview(relative, destination,{root});
    }
    let r;
    if(operation.startsWith('create_')) {
      validateText(text);
      if(operation==='create_note')name=defaultFileName(name||'');
      if(!name||name.includes('/')||name.includes('\\')||name.startsWith('.'))throw Error('Use one visible name.');
      const destination=normalize(path.posix.join(relative,name));
      this.trees.assertAvailable(destination,root);
      try{r=await this.adopt(root,destination,{absent:true,directory:operation==='create_folder'});}
      catch(error){
        if(Number.isInteger(error.status)||!/network|connect|SSH|timed out|did not respond|ECONN|socket|unavailable/i.test(error.message))throw error;
        r=await this.stageLocal(destination,{root,directory:operation==='create_folder',text:operation==='create_folder'?'':text});this.startWatch();this.pump();
        return {root,path:r.path,local_path:r.localPath,operation_id:r.lastOperation,status:'saved_locally_pending_source'};
      }
      await this.change(async()=>{this.queue(r,'edit',r.directory?'':text);await this.materialize(r);this.notify(r);});
    } else {
      r=this.get(root,relative)||await this.adopt(root,relative,{directory});
      if(r.pathOperation)throw Error('A path change is already awaiting Main or local recovery. Existing content and the request are preserved.');
      if(r.directory){
        if(r.pending)throw Error('Wait for this folder’s source receipt before changing it again.');
        const {data:children}=await this.remote.json('entries',{root,path:relative,limit:1});
        const localChildren=await this.local.list(r.localPath);
        const liveLocal=localChildren.entries.some(entry=>{
          const retained=this.records.get(key(root,this.relativePath(root,entry.relative_path)));
          return !retained?.deleted;
        });
        if(children.entries?.length||children.has_more)throw Error('Nonempty folder moves need the existing reviewed child-operation workflow. Main still reports children; no files were moved.');
        if(liveLocal)throw Error('Nonempty folder moves need the existing reviewed child-operation workflow. Local child placement or untracked content still needs attention; no files were moved.');
      }
      const destination=operation==='rename'?normalize(target||path.posix.join(path.posix.dirname(relative),name||'')):undefined;
      if(operation!=='rename'&&operation!=='trash')throw Error('Unsupported note operation.');
      if(destination===relative)throw Error('Choose a different destination.');
      if(destination&&this.records.has(key(root,destination)))throw Error('The destination is already in the local working set.');
      await this.change(async()=>{
        if(r.pathOperation)throw Error('A path change is already pending.');
        if(destination&&[...this.records.values()].some(value=>value.root===root&&value.pathOperation?.target===destination))throw Error('The destination has another pending move.');
        if(destination){
          const existing=await this.local.stat(this.localPath(root,destination)).catch(error=>{if(error.code!=='ENOENT')throw error;return null;});
          if(existing)throw Error('The local destination exists. It was preserved; no move was requested.');
        }
        const op=this.queue(r,operation==='trash'?'delete':'rename',r.text,destination,{mainFirst:true});
        if(r.localAbsence&&operation==='trash'){op.placement.external=true;op.placement.identity=r.diskIdentity;this.saveOp(op);}
        this.notify(r);
      });
    }
    this.startWatch();this.pump();
    return {root,path:r.path,local_path:r.localPath,target:r.pathOperation?.target,operation_id:r.lastOperation,status:r.pathOperation?'pending_main':'saved_locally_pending_source'};
  }

  pump() {
    if(this.closed||this.suspended)return this.running;
    this.requested=true;
    if(this.running)return this.running;
    this.running=(async()=>{do{this.requested=false;await this.send();}while(this.requested&&!this.closed&&!this.suspended)})()
      .catch(e=>{this.emit('sync-error',{error:e.message});this.retry();}).finally(()=>{this.running=null;});
    return this.running;
  }
  async send() {
    let cursor=0;
    for(;;){
      const rows=this.db.prepare("SELECT seq,record FROM native_operations WHERE state IN ('queued','pending') AND seq>? ORDER BY seq LIMIT 100").all(cursor);
      if(!rows.length)return;
      for(const row of rows) {
      if(this.suspended||this.closed)return;
      cursor=row.seq;
      const op=JSON.parse(row.record);
      let root,binding;
      try{root=savedRoot(op);binding=this.boxes.identity(root,op.box_id,op.box_key);const file=this.byID.get(op.fileKey);if(file&&(file.root!==root||!this.recordAvailable(file)))throw Error('Saved request and local file have conflicting Box replica identity.');}
      catch(error){op.state='held';op.reason=error.message;this.saveOp(op);this.emit('sync-error',{error:error.message});continue;}
      if (op.kind === 'tree_move') { await this.trees.send(op); continue; }
      if (op.kind === 'metadata_action') { await this.binary.send(op); continue; }
      const r=this.byID.get(op.fileKey);if(!r||r.held||r.treeOperation)continue;
      let source=op.base;
      if(op.predecessor){
        const predecessor=JSON.parse(this.db.prepare('SELECT record FROM native_operations WHERE id=?').get(op.predecessor)?.record||'null');
        if(predecessor?.state!=='accepted')continue;
        source=predecessor.receipt.note;
      }
      if(!source?.base_id&&op.unbound){
        const parent=this.get(root,path.posix.dirname(op.path));
        if(parent?.pending||parent?.held){
          if(parent.held){op.state='held';op.reason='The offline parent folder needs source-owner review. This child remains saved locally.';r.held=true;this.transaction(()=>{this.saveOp(op);this.persist(r);});this.notify(r);}
          continue;
        }
        try{source=(await this.remote.json('note',{...binding,path:op.path,...(op.directory?{directory:'true'}:{})})).data;}
        catch(error){
          if(!(error.status>=400&&error.status<500)){this.emit('sync-error',{root,error:error.message});this.retry();continue;}
          op.state='held';op.reason=error.message;r.held=true;this.transaction(()=>{this.saveOp(op);this.persist(r);});this.notify(r);continue;
        }
        if(!source?.base_id||!source.file?.file_id||source.exists||source.root!==root||source.path!==op.path){
          op.state='held';op.reason='The offline destination is now occupied or its absent base is unavailable. Local content is preserved.';r.held=true;this.transaction(()=>{this.saveOp(op);this.persist(r);});this.notify(r);continue;
        }
        op.base=source;r.source=source;this.transaction(()=>{this.saveOp(op);this.persist(r);});
      }
      if(!source?.base_id)throw Error('The preceding source receipt is unavailable. Local content is retained.');
      if(!op.request){
        op.request={root,path:op.predecessor?source.path:op.path,operation_id:op.id,file_id:source.file.file_id,base_id:source.base_id,
          generation:source.generation,kind:op.kind,text:op.kind==='edit'?op.text:'',...(op.target?{target:op.target}:{}),directory:op.directory};
        this.saveOp(op);
      }
      let result;
      try { result=(await this.remote.json('note',binding, {method:'POST',body:op.request})).data; }
      catch(e){if(e.status>=400&&e.status<500){op.state='held';op.reason=e.message;this.saveOp(op);r.held=true;this.persist(r);this.notify(r);continue;}this.emit('sync-error',{root,error:e.message});this.retry();continue;}
      if(result.operation_id!==op.id||!['accepted','conflict','held','pending'].includes(result.state))throw Error('Main returned an invalid edit receipt.');
      await this.change(async()=>{
        op.receipt=result;op.state=result.state;op.reason=result.reason;
        if(result.state==='accepted') {
          if(!result.note?.base_id)throw Error('The accepted edit is missing its exact source base.');
          const expectedPath=op.kind==='rename'?op.target:op.request.path;
          if(result.note.root!==root||result.note.path!==expectedPath||result.note.file?.file_id!==op.request.file_id||result.note.box_id&&binding.box_id&&result.note.box_id!==binding.box_id)throw Error('Main returned an unrelated source receipt. The saved request is unchanged.');
          r.source=result.note;r.pending=op.mainFirst||r.lastOperation!==op.id;r.held=false;
        } else {r.held=result.state==='conflict'||result.state==='held';r.pending=true;}
        this.transaction(()=>{this.saveOp(op);this.persist(r);});this.notify(r);
        if(result.state==='accepted'&&op.mainFirst)await this.finishPath(r,op);
        if(result.state==='pending')this.retry();
      });
      }
    }
  }
  retry() {
    if(this.closed||this.retryTimer)return;
    this.retryDelay=Math.min((this.retryDelay||500)*2,30000);
    this.retryTimer=setTimeout(()=>{this.retryTimer=null;this.pump();this.startWatch();},this.retryDelay);this.retryTimer.unref?.();
  }

  async refresh(relative,root='box') {
    const binding=this.boxes.identity(root);
    const r=this.get(root,relative);if(!r||r.pending||r.treeOperation||this.suspended)return;
    if(r.retainedProject&&!r.restoreReady)return;
    this.lifecycle?.checkAvailable(relative,root);
    const version=JSON.stringify([r.path,r.source?.base_id,r.lastOperation,r.text]);
    let source;
    try{source=(await this.remote.json('note',{...binding,path:relative,...(r.directory?{directory:'true'}:{})})).data;}
    catch(error){
      if(error.status!==404)throw error;
      // A missing parent prevents a Note snapshot. Verify absence through Files
      // rather than treating an unavailable membership or network as deletion.
      try{await this.remote.json('stat',{...binding,path:relative});throw error;}
      catch(statError){if(statError.status!==404||statError===error)throw error;}
      source={root,path:relative,exists:false,observation:{kind:'source_path_absent',path:relative,at:new Date().toISOString()}};
    }
    if(source.root!==root||source.path!==relative)throw Error('Main returned an unrelated refresh source.');
    await this.change(async()=>{
      if(this.lifecycle?.blocked(relative,root)||r.retainedProject&&!r.restoreReady)return;
      if(r.pending||r.treeOperation||this.suspended||JSON.stringify([r.path,r.source?.base_id,r.lastOperation,r.text])!==version)return;
      const disk=r.directory?null:await this.local.read(r.localPath).catch(()=>null);
      if(r.retainedProject){
        if(!r.restoreReady||!source.exists||!source.file?.file_id||!source.base_id||!source.generation||!r.directory&&(!disk||disk.text!==r.text||source.text!==r.text))throw Error('Restored source and retained local copy need review. Both versions remain preserved.');
        r.source=source;delete r.retainedProject;delete r.restoreReady;this.persist(r);this.watchLocal(r);this.notify(r);return;
      }
      if(!source.exists){await this.retireObserved(r,source);return;}
      if(disk&&disk.text!==r.text){this.queue(r,'edit',disk.text);this.notify(r);this.pump();return;}
      if(r.directory)return;
      validateText(source.text);
      if(source.hash!==r.source.hash||source.base_id!==r.source.base_id){this.workingBytes+=Buffer.byteLength(source.text)-Buffer.byteLength(r.text);r.source=source;r.text=source.text;this.persist(r);await this.materialize(r);this.notify(r,{contentChanged:true});}
    });
  }

  async review(root,relative) {
    const r=this.get(root,normalize(relative));if(!r?.held||r.directory)throw Error('This note has no reviewable text conflict.');
    const operations=this.db.prepare("SELECT record FROM native_operations WHERE file_id=? AND state<>'accepted' AND state<>'resolved' ORDER BY seq").all(r.id).map(row=>JSON.parse(row.record));
    if(operations.some(op=>op.kind!=='edit'))throw Error('Path-operation conflicts require source-owner review; no rename or removal was retried.');
    const {data:source}=await this.remote.json('note',{root,path:r.path});validateText(source.text);
    const disk=await this.local.read(r.localPath);
    if(source.root!==root||source.path!==r.path)throw Error('Main returned an unrelated conflict source.');
    const token=randomUUID(),review={at:Date.now(),id:r.id,root,path:r.path,last:r.lastOperation,hash:digest(Buffer.from(r.text)),diskHash:disk.base,diskText:disk.text,source,covered:operations.map(op=>op.id)};
    this.reviews.set(token,review);while(this.reviews.size>8)this.reviews.delete(this.reviews.keys().next().value);
    return {token,path:r.path,local:r.text,source:source.text,sourceExists:source.exists,...(disk.text!==r.text?{external:disk.text}:{})};
  }

  // New local files have no authority to replace a remote destination. Their
  // absent base is acquired only on reconnect, before sealing the first request.
  async stageLocal(relative,{root='box',box_id,directory=false,text=''}={}) {
    const binding=this.boxes.identity(root,box_id);
    relative=normalize(relative);validateText(text);
    if(!relative||!directory&&!editable(relative))throw Error('Only supported text files and folders can be created locally.');
    return this.change(async()=>{
      if(this.suspended)throw Error('Recovery capture is in progress.');
      const existing=this.get(root,relative);if(existing)return existing;
      if(this.records.size>=100000||this.workingBytes+Buffer.byteLength(text)>MAX_WORKING_BYTES)throw Error('Local working-set capacity reached. The external file remains preserved.');
      const r={id:randomUUID(),...binding,path:relative,localPath:this.localPath(root,relative),directory,text,source:{...binding,path:relative},pending:false,held:false};
      this.persist(r);this.records.set(key(root,relative),r);this.byID.set(r.id,r);this.workingBytes+=Buffer.byteLength(text);
      const op=this.queue(r,'edit',directory?'':text);op.unbound=true;this.saveOp(op);
      await this.materialize(r);this.watchLocal(r);this.notify(r);this.startWatch();return r;
    });
  }

  async externalPath(r,target) {
    if(r.pending||r.held||r.treeOperation||r.pathOperation||r.retainedProject)return;
    if([...this.records.values()].some(parent=>parent.root===r.root&&parent.directory&&parent.held&&r.path.startsWith(parent.path+'/')))throw Error('An external parent-folder change needs review. Descendant source files are preserved.');
    await this.change(async()=>{
      if(r.pending||r.held||this.suspended)return;
      const old=await this.local.stat(r.localPath).catch(e=>{if(e.code!=='ENOENT')throw e;return null;});
      if(old)return;
      if(!target){
        r.localAbsence=true;r.localRecoveryNeeded='The local path is missing; an external move or removal needs review. Main was not changed. Choose Remove explicitly to confirm deletion.';this.persist(r);this.notify(r);return;
      }
      if(target){
        target=normalize(target);
        if(r.directory||this.get(r.root,target))throw Error('The external folder move or destination needs review. All local content is preserved.');
        const info=await lstat(await this.local.resolve(this.localPath(r.root,target)));
        const read=await this.local.read(this.localPath(r.root,target));
        if(`${info.dev}:${info.ino}`!==r.diskIdentity||read.base!==r.diskHash)throw Error('External move identity is not established. Local files are preserved.');
      }
      const op=this.queue(r,target?'rename':'delete',r.text,target,{mainFirst:true});
      delete r.localAbsence;
      op.placement.external=true;op.placement.identity=r.diskIdentity;this.saveOp(op);this.notify(r);
    });
    this.pump();
  }

  async retireObserved(r,source) {
    // Absence is a source observation, not an invented accepted edit. Save a
    // replayable placement before retiring any unchanged local projection.
    const childrenHold='The source folder is absent, but local children remain. They were preserved.';
    if(r.directory&&r.held&&r.localRecoveryNeeded===childrenHold&&!(await this.local.list(r.localPath).catch(()=>({entries:[]}))).entries.length){r.held=false;delete r.localRecoveryNeeded;}
    if(r.pending||r.held)return;
    if(!r.directory){const disk=await this.local.read(r.localPath).catch(error=>{if(error.code!=='ENOENT')throw error;return null;});if(disk&&disk.text!==r.text){r.held=true;r.localRecoveryNeeded='Main removed this file, but the local copy has changed. Both the local edit and Main recovery history are preserved.';this.persist(r);this.notify(r);return;}}
    if(r.directory&&(await this.local.list(r.localPath).catch(()=>({entries:[]}))).entries.length){r.held=true;r.localRecoveryNeeded=childrenHold;this.persist(r);this.notify(r);return;}
    const id=r.observation?.id||randomUUID();
    const op=r.observation||{id,kind:'delete',mainFirst:true,state:'accepted',directory:r.directory,placement:{from:r.localPath,stage:'waiting'},sourceAbsence:source.base_id};
    r.observation=op;this.persist(r);
    try{
      await placeAcceptedPath(this.local,path.join(this.directory,'local-recovery'),op,async()=>this.persist(r));
      r.deleted=true;if(source.base_id)r.source=source;else r.observedAbsent=source.observation;this.records.delete(key(r.root,r.path));this.persist(r);this.notify(r,{sourceAbsent:true});this.startWatch();
    }catch(error){r.held=true;r.localRecoveryNeeded=error.message;this.persist(r);this.notify(r);}
  }

  async resolve(token,text) {
    validateText(text);const review=this.reviews.get(token);
    if(!review||Date.now()-review.at>300000)throw Error('Conflict review expired. Review the current versions again.');
    const r=this.get(review.root||'box',review.path);if(!r||r.id!==review.id)throw Error('The note moved. Review it again.');
    const binding=this.boxes.identity(r.root,r.box_id);
    await this.change(async()=>{
      const disk=await this.local.read(r.localPath);
      if(r.lastOperation!==review.last||digest(Buffer.from(r.text))!==review.hash||disk.base!==review.diskHash)throw Error('The local note changed after review. No version was replaced.');
      const op={id:`desktop_${randomUUID().replaceAll('-','')}`,...binding,fileKey:r.id,kind:'edit',path:r.path,text,base:review.source,state:'queued',at:new Date().toISOString(),directory:false,
        resolution:{covered:review.covered,previousLocal:r.text,externalLocal:review.diskText,reviewedSourceBase:review.source.base_id}};
      this.transaction(()=>{
        for(const id of review.covered){const previous=JSON.parse(this.db.prepare('SELECT record FROM native_operations WHERE id=?').get(id).record);previous.originalState=previous.state;previous.state='resolved';previous.resolvedBy=op.id;this.saveOp(previous);}
        this.insertOp(op);
        this.workingBytes+=Buffer.byteLength(text)-Buffer.byteLength(r.text);r.source=review.source;r.text=text;r.pending=true;r.held=false;r.diskHash=disk.base;r.lastOperation=op.id;delete r.externalText;this.persist(r);
      });
      await this.materialize(r);this.reviews.delete(token);this.notify(r);
    });
    this.pump();return {status:'saved_locally_pending_source'};
  }

  watchScopes() {
    const scopes=new Map();
    const add=(root,relative)=>{if(!this.available(root))return;if(!scopes.has(root))scopes.set(root,new Set());scopes.get(root).add(relative);};
    for(const r of this.records.values())if(!r.deleted&&!r.retainedProject&&!this.selections?.sourceMissing(path.posix.dirname(r.path),r.root))add(r.root,path.posix.dirname(r.path));
    for(const {root,path:relative} of this.selections?.watchScopes()||[])add(root,relative);
    return new Map([...scopes].map(([root,paths])=>[root,[...paths].sort()]));
  }
  stopWatches() {
    clearTimeout(this.watchTimer);
    for(const state of this.rootWatches.values())state.controller?.abort();
    this.rootWatches.clear();
  }
  startWatch() {
    if(this.closed||this.suspended||!this.remote.request)return;
    const scopes=this.watchScopes();
    if([...scopes.values()].reduce((n,paths)=>n+paths.length,0)>4096){this.stopWatches();this.emit('sync-error',{error:'Live sync exceeds its 4,096-folder watch capacity. No partial coverage is claimed; pause a selection or reduce its scope.'});return;}
    for(const [root,state] of this.rootWatches)if(!scopes.has(root)){state.controller?.abort();this.rootWatches.delete(root);}
    if([...scopes].every(([root,paths])=>{const state=this.rootWatches.get(root);return state?.active&&!state.controller.signal.aborted&&state.signature===JSON.stringify(paths);}))return;
    clearTimeout(this.watchTimer);this.watchTimer=setTimeout(()=>{void this.openWatch();},100);this.watchTimer.unref?.();
  }
  async openWatch() {
    if(this.closed||this.suspended||!this.remote.request)return;
    const scopes=this.watchScopes();
    if([...scopes.values()].reduce((n,paths)=>n+paths.length,0)>4096)return;
    await Promise.all([...scopes].map(([root,paths])=>this.openRootWatch(root,paths)));
  }
  async openRootWatch(root,paths) {
    const old=this.rootWatches.get(root),signature=JSON.stringify(paths);
    if(old?.active&&!old.controller.signal.aborted&&old.signature===signature)return;
    old?.controller?.abort();
    const controller=new AbortController(),state={controller,signature,active:true,connected:old?.connected||false};this.rootWatches.set(root,state);
    try {
      const reconnect=!state.connected,previous=new Set(JSON.parse(old?.signature||'[]')),binding=this.boxes.identity(root);
      const response=await this.remote.request('watch',binding, {method:'POST',body:{root,paths},lane:'watch',signal:controller.signal});
      state.connected=true;
      let buffer='';const decoder=new TextDecoder();
      for await(const chunk of response){
        buffer+=decoder.decode(chunk,{stream:true});if(buffer.length>1024*1024)throw Error('Oversized watch event.');
        let end;while((end=buffer.indexOf('\n\n'))>=0){
          const frame=buffer.slice(0,end);buffer=buffer.slice(end+2);const line=frame.split('\n').find(l=>l.startsWith('data: '));if(!line)continue;
          const event=JSON.parse(line.slice(6));if(!['ready','changed'].includes(event.type))continue;
          if(event.root&&event.root!==root)throw Error('Watch notification belongs to another Box root.');
          this.retryDelay=0;
          const observed=event.type==='ready'&&!reconnect?paths.filter(p=>!previous.has(p)):watchDirectories(event.paths||paths);
          if(observed.length)this.selections?.sourceChanged(observed,event.type==='ready'&&reconnect,root);
          const selected=[...this.records.values()].filter(r=>r.root===root&&!r.deleted&&!r.retainedProject&&!this.selections?.covers(r.path,root)&&observed.includes(path.posix.dirname(r.path))).sort((a,b)=>b.path.split('/').length-a.path.split('/').length);
          for(const r of selected)await this.refresh(r.path,root).catch(e=>this.emit('sync-error',{error:e.message}));
          this.pump();this.emit('source-notification',{...binding,paths:(event.paths||paths).map(p=>p==='.'?'':p)});
        }
      }
      if(!controller.signal.aborted){state.connected=false;this.retry();}
    }catch(e){if(!controller.signal.aborted){state.connected=false;this.emit('sync-error',{error:e.message});if(!(e.status>=400&&e.status<500&&![404,408,429].includes(e.status)))this.retry();}}
    finally {state.active=false;}
  }

  async close() {
    if(this.closed)return;
    this.closed=true;clearTimeout(this.retryTimer);this.stopWatches();
    for(const watcher of this.watchers.values())watcher.close();
    await this.running;await this.serial.catch(()=>{});this.db?.close();
  }
}
