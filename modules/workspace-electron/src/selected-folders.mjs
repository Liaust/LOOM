import { watch } from 'node:fs';
import { lstat } from 'node:fs/promises';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { normalize } from './local-files.mjs';
import { isEditableText } from './file-formats.mjs';

const within=(value,parent)=>value===parent||value.startsWith(parent+'/');
const id=(selection,relative)=>JSON.stringify([selection,relative]);
const MAX_BYTES=256*1024**2;

// Selections, membership and dirty directories are individual durable records.
// No timer scans the working set: fs events or reconnect enqueue shallow work.
export class SelectedFolders {
  constructor(notes,cache,remote,{emit=()=>{},remember=()=>{}}={}){
    this.notes=notes;this.db=notes.db;this.cache=cache;this.remote=remote;this.emit=emit;this.rememberPage=remember;this.watchers=new Map();this.localDirty=new Set();
    this.db.exec(`CREATE TABLE IF NOT EXISTS sync_selections(id TEXT PRIMARY KEY,record TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS box_sync_selections(id TEXT PRIMARY KEY,record TEXT NOT NULL);
      CREATE TEMP VIEW IF NOT EXISTS native_sync_selections AS SELECT rowid AS position,* FROM sync_selections UNION ALL SELECT rowid AS position,* FROM box_sync_selections;
      CREATE TABLE IF NOT EXISTS sync_members(id TEXT PRIMARY KEY,selection_id TEXT NOT NULL,path TEXT NOT NULL,record TEXT NOT NULL);
      CREATE INDEX IF NOT EXISTS sync_members_selection ON sync_members(selection_id,path);
      CREATE TABLE IF NOT EXISTS sync_dirty(id TEXT PRIMARY KEY,selection_id TEXT NOT NULL,path TEXT NOT NULL,generation INTEGER NOT NULL,state TEXT NOT NULL,record TEXT NOT NULL);`);
    notes.selections=this;
  }
  list(){return this.db.prepare('SELECT record FROM native_sync_selections ORDER BY position').all().map(x=>JSON.parse(x.record));}
  persist(s){const table=s.root==='box'?'sync_selections':'box_sync_selections';this.db.prepare(`INSERT INTO ${table} VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record`).run(s.id,JSON.stringify(s));}
  active(s){return !this.suspended&&!this.closed&&this.list().find(current=>current.id===s.id)?.state==='active';}
  progress(s){const live=this.list().find(current=>current.id===s.id);if(live){live.bytes=s.bytes;this.persist(live);}}
  members(s){return this.db.prepare('SELECT record FROM sync_members WHERE selection_id=?').all(s.id).map(x=>JSON.parse(x.record));}
  member(s,relative){const row=this.db.prepare('SELECT record FROM sync_members WHERE id=?').get(id(s.id,relative));return row&&JSON.parse(row.record);}
  remember(s,entry,epoch){const value={...entry,epoch};this.db.prepare('INSERT INTO sync_members VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record').run(id(s.id,entry.relative_path),s.id,entry.relative_path,JSON.stringify(value));}
  covers(relative,root='box'){return this.list().some(s=>s.root===root&&s.state!=='paused'&&within(relative,s.path));}
  sourceMissing(relative,root='box'){return this.list().some(s=>s.root===root&&s.sourceMissing&&within(relative,s.path));}
  watchPaths(root='box'){const paths=new Set();for(const s of this.list().filter(s=>s.root===root&&s.state!=='paused'&&this.notes.available(s.root))){paths.add(path.posix.dirname(s.path));if(s.sourceMissing)continue;paths.add(s.path);for(const e of this.members(s))if(e.kind==='directory')paths.add(e.relative_path);}return [...paths];}
  watchScopes(){return [...new Set(this.list().map(s=>s.root))].flatMap(root=>this.watchPaths(root).map(relative=>({root,path:relative})));}
  mark(s,relative){
    const previous=this.db.prepare('SELECT generation FROM sync_dirty WHERE id=?').get(id(s.id,relative));
    this.db.prepare('INSERT INTO sync_dirty VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,state=excluded.state,record=excluded.record').run(id(s.id,relative),s.id,relative,(previous?.generation||0)+1,'pending',JSON.stringify({root:s.root,box_id:s.box_id,cursor:'',epoch:randomUUID(),count:0}));
  }
  start({root,box_id,path:relative,assets=false}){
    const binding=this.notes.boxes.identity(root,box_id);
    if(!normalize(relative)||relative.split('/').length>64)throw Error('Choose a visible Box subfolder for continuous sync.');relative=normalize(relative);
    if(this.list().some(s=>s.root===root&&(within(relative,s.path)||within(s.path,relative))))throw Error('This selection overlaps an existing continuous selection. Resume that selection instead.');
    if(this.list().length>=32)throw Error('Continuous sync supports at most 32 explicit selections.');
    const s={id:randomUUID(),...binding,path:relative,assets:assets===true,state:'active',bytes:0,at:new Date().toISOString()};this.persist(s);this.mark(s,relative);this.notes.startWatch();this.pump();return s;
  }
  state(selection,state){
    const s=this.list().find(s=>s.id===selection);if(!s||!['active','paused'].includes(state))throw Error('Unknown continuous selection or state.');
    s.state=state;delete s.reason;this.persist(s);
    if(state==='active'){this.mark(s,s.path);for(const e of this.members(s))if(e.kind==='directory')this.mark(s,e.relative_path);}
    this.syncWatchers();this.notes.startWatch();this.pump();return s;
  }
  sourceChanged(paths,reconnect=false,root='box'){
    if(this.suspended||this.closed)return;
    for(const s of this.list().filter(s=>s.root===root&&s.state!=='paused')){
      const directories=[s.path,...this.members(s).filter(e=>e.kind==='directory').map(e=>e.relative_path)];
      for(const directory of directories)if(reconnect||paths.includes(directory)||paths.includes(path.posix.dirname(directory))&&directory===s.path)this.mark(s,directory);
    }
    this.pump();
  }
  pump(){
    if(this.closed||this.suspended)return this.running;if(this.running){this.requested=true;return this.running;}
    this.running=(async()=>{do{this.requested=false;await this.drain();}while(this.requested&&!this.closed&&!this.suspended);})().catch(e=>this.emit('sync-error',{error:e.message})).finally(()=>{this.running=null;});return this.running;
  }
  async drain(){
    while(!this.closed&&!this.suspended){
      const active=new Map(this.list().filter(s=>s.state!=='paused').map(s=>[s.id,s]));
      const row=this.db.prepare("SELECT * FROM sync_dirty WHERE state='pending' ORDER BY rowid LIMIT 1").get();if(!row)return;
      const s=active.get(row.selection_id);if(!s){this.db.prepare("UPDATE sync_dirty SET state='paused' WHERE id=?").run(row.id);continue;}
      try{await this.page(s,row);}catch(error){
        this.db.prepare("UPDATE sync_dirty SET state='held' WHERE id=? AND generation=?").run(row.id,row.generation);
        const live=this.list().find(current=>current.id===s.id);if(live){live.reason=error.message;this.persist(live);this.emit('sync-selection',{...live,pending:true});}
      }
    }
    this.syncWatchers();
  }
  async page(s,row){
    this.notes.boxes.identity(s.root,s.box_id);
    const work=JSON.parse(row.record);let page;
    if(work.root&&work.root!==s.root||work.box_id&&work.box_id!==s.box_id)throw Error('Saved selection cursor belongs to another Box.');
    try{page=(await this.remote.json('entries',{root:s.root,box_id:s.box_id,path:row.path,limit:100,...(work.cursor?{cursor:work.cursor}:{})})).data;}
    catch(error){if(error.status===404){if(!this.active(s))return;if(row.path===s.path){const live=this.list().find(current=>current.id===s.id);live.sourceMissing=true;this.persist(live);}await this.missing(s,row.path);this.db.prepare('DELETE FROM sync_dirty WHERE id=? AND generation=?').run(row.id,row.generation);this.notes.startWatch();return;}throw error;}
    if(!this.active(s))return;
    if(row.path===s.path&&s.sourceMissing){s.sourceMissing=false;const live=this.list().find(current=>current.id===s.id);delete live.sourceMissing;this.persist(live);}
    if(!Array.isArray(page.entries))throw Error('Invalid selected-folder inventory.');
    this.rememberPage(s.root,row.path,work.cursor,page);
    let members=this.db.prepare('SELECT count(*) AS total FROM sync_members WHERE selection_id=?').get(s.id).total;
    let directories=this.watchScopes().length;
    for(const entry of page.entries){
      if(!this.active(s))return;
      if(entry.name?.startsWith('.')||['node_modules','credentials','private_no_index'].includes(entry.name))continue;
      const target=normalize(entry.relative_path);
      if(target!==path.posix.join(row.path,entry.name)||target.split('/').length>64)throw Error('Invalid selected-folder child.');
      if(++work.count>100000)throw Error('A selected folder exceeded 100,000 entries; progress is retained.');
      const previous=this.member(s,target);
      if(!previous&&++members>100000)throw Error('Continuous selection reached 100,000 entries. Existing content and progress remain preserved.');
      if(entry.kind==='directory'&&!previous&&++directories>4096)throw Error('Continuous sync reached its 4,096-folder capacity. Narrow this selection; no partial watch coverage is claimed.');
      this.remember(s,entry,work.epoch);
      if(entry.kind==='directory'){
        if(!this.notes.get(s.root,target))await this.notes.adopt(s.root,target,{directory:true});
        if(!previous)this.mark(s,target);
      }else if(entry.kind==='file'){
        if(isEditableText(entry.name)&&entry.size<=512*1024){
          const r=this.notes.get(s.root,target);
          if(r)await this.notes.refresh(target,s.root);
          else{if(s.bytes+entry.size>MAX_BYTES)throw Error('Selected-folder text budget reached (256 MiB). Existing files remain saved.');await this.notes.adopt(s.root,target);s.bytes+=entry.size;this.progress(s);}
        }else if(s.assets&&(!previous||previous.revision!==entry.revision||!this.cache.get(s.root,target,entry.revision)?.pinned)){
          if(entry.size>this.cache.maxFileBytes||s.bytes+entry.size>MAX_BYTES)throw Error('Selected assets exceed the explicit 256 MiB download budget.');
          const {data:metadata}=await this.remote.json('stat',{root:s.root,box_id:s.box_id,path:target});if(!this.active(s))return;await this.cache.fetch(s.root,target,metadata,()=>this.remote.content(s.root,target));await this.cache.pin(s.root,target,true);s.bytes+=entry.size;this.progress(s);
        }
      }
    }
    if(!this.active(s))return;
    if(page.has_more){if(!page.next_cursor||page.next_cursor===work.cursor)throw Error('Selected-folder cursor did not advance.');work.cursor=page.next_cursor;
      this.db.prepare('UPDATE sync_dirty SET record=? WHERE id=? AND generation=?').run(JSON.stringify(work),row.id,row.generation);return;}
    // Only a completed directory observation establishes absence. Partial pages
    // and interrupted/replaced generations can never authorize local retirement.
    if(this.db.prepare('SELECT generation FROM sync_dirty WHERE id=?').get(row.id)?.generation!==row.generation)return;
    for(const old of this.members(s).filter(e=>path.posix.dirname(e.relative_path)===row.path&&e.epoch!==work.epoch))await this.missing(s,old.relative_path);
    this.db.prepare('DELETE FROM sync_dirty WHERE id=? AND generation=?').run(row.id,row.generation);
    if(row.path===s.path&&!this.notes.get(s.root,s.path))await this.notes.adopt(s.root,s.path,{directory:true});
    this.syncWatchers();this.notes.startWatch();this.emit('sync-selection',{...s,pending:false});
  }
  async missing(s,relative){
    const records=[...this.notes.records.values()].filter(r=>r.root===s.root&&within(r.path,relative)).sort((a,b)=>b.path.length-a.path.length);
    for(const r of records){
      try{await this.notes.refresh(r.path,s.root);}catch(error){
        if(error.status!==404)throw error;
        await this.notes.change(()=>this.notes.retireObserved(r,{exists:false,observation:{kind:'source_path_absent',path:relative,at:new Date().toISOString()}}));
      }
    }
    for(const e of this.members(s).filter(e=>within(e.relative_path,relative))){this.db.prepare('DELETE FROM sync_members WHERE id=?').run(id(s.id,e.relative_path));this.db.prepare('DELETE FROM sync_dirty WHERE id=?').run(id(s.id,e.relative_path));}
  }
  syncWatchers(){
    if(this.closed)return;
    const wanted=new Map(this.watchScopes().filter(s=>this.covers(s.path,s.root)).map(s=>[id(s.root,s.path),s]));
    for(const r of this.notes.records.values())if(r.directory&&!r.deleted&&this.notes.available(r.root)&&this.covers(r.path,r.root))wanted.set(id(r.root,r.path),{root:r.root,path:r.path});
    for(const [key,w] of this.watchers)if(!wanted.has(key)){w.close();this.watchers.delete(key);}
    for(const [key,{root,path:relative}] of wanted)if(!this.watchers.has(key)){
      if(this.watchers.size>=4096)throw Error('Local selection watch capacity reached.');
      try{const w=watch(path.join(this.notes.root,this.notes.localPath(root,relative)),{persistent:false},(_event,name)=>{if(name?.startsWith('.'))return;this.localDirty.add(key);if(this.suspended)return;clearTimeout(this.localTimer);this.localTimer=setTimeout(()=>this.flushLocal(),200);this.localTimer.unref?.();});w.on('error',()=>{w.close();this.watchers.delete(key);});this.watchers.set(key,w);}catch(error){if(error.code!=='ENOENT')throw error;}
    }
  }
  followMove(from,to,root='box'){
    this.notes.transaction(()=>{
      for(const s of this.list()){
        if(s.root!==root)continue;
        if(!within(from,s.path)&&!within(s.path,from)&&!within(to,s.path))continue;
        if(within(s.path,from)){s.path=to+s.path.slice(from.length);delete s.sourceMissing;this.persist(s);}
        const affected=this.members(s).filter(e=>within(e.relative_path,from));
        for(const entry of affected){
          this.db.prepare('DELETE FROM sync_members WHERE id=?').run(id(s.id,entry.relative_path));
          this.db.prepare('DELETE FROM sync_dirty WHERE id=?').run(id(s.id,entry.relative_path));
          const target=to+entry.relative_path.slice(from.length);if(within(target,s.path))this.remember(s,{...entry,name:path.posix.basename(target),relative_path:target},entry.epoch);
        }
        if(s.state==='active'){this.mark(s,s.path);for(const entry of this.members(s))if(entry.kind==='directory')this.mark(s,entry.relative_path);}
      }
    });
    this.syncWatchers();this.notes.startWatch();this.pump();
  }
  async flushLocal(){
    if(this.closed||this.suspended)return;
    if(this.localRunning){this.localRequested=true;return this.localRunning;}
    this.localRunning=(async()=>{do{
      this.localRequested=false;const paths=[...this.localDirty];this.localDirty.clear();const missing=[];
      // Observe all event-dirty destinations before interpreting source absence;
      // a cross-folder rename must not become an early delete plus new create.
      for(const key of paths){const [root,relative]=JSON.parse(key);missing.push(...await this.scanLocal(relative,{root,deferMissing:true}));}
      for(const r of missing)if(!r.directory)await this.notes.externalPath(r);
    }while((this.localRequested||this.localDirty.size)&&!this.closed&&!this.suspended);})().catch(e=>this.emit('sync-error',{error:e.message})).finally(()=>{this.localRunning=null;});return this.localRunning;
  }
  async scanLocal(relative,{root='box',deferMissing=false}={}){
    if(!this.notes.available(root)||!this.covers(relative,root))return [];
    const page=await this.notes.local.list(this.notes.localPath(root,relative)).catch(e=>{if(e.code!=='ENOENT')throw e;return {entries:[]};});
    const entries=new Map(page.entries.map(e=>[this.notes.relativePath(root,e.relative_path),e]));
    const records=[...this.notes.records.values()].filter(r=>r.root===root&&this.covers(r.path,root));
    for(const [target,entry] of entries){
      if(this.notes.get(root,target))continue;
      const info=await lstat(await this.notes.local.resolve(entry.relative_path));
      const moved=records.find(r=>r.diskIdentity===`${info.dev}:${info.ino}`&&r.path!==target);
      if(moved){
        if(moved.directory){moved.held=true;moved.localRecoveryNeeded=`External folder move to ${target} needs source-owner review; no source files were removed.`;this.notes.persist(moved);this.notes.notify(moved);throw Error(moved.localRecoveryNeeded);}
        await this.notes.externalPath(moved,target);continue;
      }
      if(entry.kind==='directory'||isEditableText(target)&&entry.size<=512*1024){
        let exists;
        try{exists=(await this.remote.json('note',{...this.notes.boxes.identity(root),path:target,...(entry.kind==='directory'?{directory:'true'}:{})})).data.exists;}catch(e){if(Number.isInteger(e.status))throw e;}
        if(exists)await this.notes.adopt(root,target,{directory:entry.kind==='directory'});
        else await this.notes.stageLocal(target,{root,directory:entry.kind==='directory',text:entry.kind==='directory'?'':(await this.notes.local.read(entry.relative_path)).text});
        if(entry.kind==='directory'){this.localDirty.add(id(root,target));this.notes.startWatch();}
      }
    }
    const missing=records.filter(r=>path.posix.dirname(r.path)===relative&&!entries.has(r.path)).sort((a,b)=>b.path.length-a.path.length);
    for(const r of missing){
      if(r.directory){this.emit('sync-error',{error:`External folder removal or move needs review: ${r.path}. Main files and local history are preserved; use the folder Move/Remove action for a verified change.`});continue;}
      // Look for a proven move anywhere already observed before treating absence
      // as deletion. A moved inode with new content is deliberately held.
      if(!deferMissing)await this.notes.externalPath(r);
    }
    this.syncWatchers();this.notes.pump();return missing;
  }
  async close(){this.closed=true;clearTimeout(this.localTimer);for(const w of this.watchers.values())w.close();await this.localRunning;await this.running;}
}
