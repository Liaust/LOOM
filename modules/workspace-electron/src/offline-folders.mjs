import { randomUUID } from 'node:crypto';
import path from 'node:path';
import { normalize } from './local-files.mjs';
import {isBoxRef} from './box-roots.mjs';

const note = name => /\.(md|markdown|txt|canvas|base)$/i.test(name);
const MAX_BYTES = 256 * 1024 ** 2;
const identity = (root, relative, cursor = '') => JSON.stringify([root, relative, cursor]);

// One bounded download lane, with durable page and entry cursors. Downloading
// never submits a save or creates a Notes enrollment on Main.
export class OfflineFolders {
  constructor(notes, cache, remote, {emit = () => {}} = {}) {
    this.notes=notes;this.db=notes.db;this.cache=cache;this.remote=remote;this.emit=emit;this.closed=false;
    this.db.exec(`CREATE TABLE IF NOT EXISTS offline_jobs (id TEXT PRIMARY KEY,record TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS offline_pages (id TEXT PRIMARY KEY,record TEXT NOT NULL);`);
    for(const job of this.jobs())if(job.state==='running'){job.state='paused';job.reason='Application restarted; resume the retained traversal.';this.persist(job);}
  }
  jobs(){return this.db.prepare('SELECT record FROM offline_jobs ORDER BY rowid DESC LIMIT 50').all().map(r=>JSON.parse(r.record));}
  persist(job){this.db.prepare('INSERT INTO offline_jobs VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record').run(job.id,JSON.stringify(job));}
  remember(root,relative,cursor,page){this.db.prepare('INSERT INTO offline_pages VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record').run(identity(root,relative,cursor),JSON.stringify({...page,observedAt:Date.now()}));this.db.exec('DELETE FROM offline_pages WHERE rowid IN (SELECT rowid FROM offline_pages ORDER BY rowid DESC LIMIT -1 OFFSET 8192)');}
  page(root,relative,cursor=''){const row=this.db.prepare('SELECT record FROM offline_pages WHERE id=?').get(identity(root,relative,cursor));return row?{...JSON.parse(row.record),cached:true,offline:true,ms:0}:null;}
  async preview({root,box_id,path:relative,assets=false}){
    if(root!=='storage')this.notes.boxes.identity(root,box_id);relative=normalize(relative,{allowHidden:true});
    const queue=[{path:relative,cursor:''}],seen=new Set();let entries=0,files=0,bytes=0,pages=0,unsupported=0;
    while(queue.length&&pages<10&&entries<500){
      const current=queue.shift();const key=identity(root,current.path,current.cursor);if(seen.has(key))break;seen.add(key);
      const {data}=await this.remote.json('entries',{root,path:current.path,limit:100,...current.cursor?{cursor:current.cursor}:{}});pages++;
      for(const entry of data.entries||[]){entries++;if(entry.name.startsWith('.'))continue;
        if(entry.kind==='directory')queue.push({path:entry.relative_path,cursor:''});
        else if(note(entry.name)||assets){files++;bytes+=entry.size||0;}else unsupported++;
      }
      if(data.has_more)queue.unshift({path:current.path,cursor:data.next_cursor});
    }
    return {root,path:relative,assets,files,bytes,unsupported,limited:queue.length>0,budget:MAX_BYTES,
      qualification:'At most 500 entries / 10 pages previewed. Download stops at 256 MiB or 100,000 entries. Notes retain existing source permissions; other files are pinned read-only copies.'};
  }
  start({root,box_id,path:relative,assets=false}){
    const binding=root==='storage'?{root}:this.notes.boxes.identity(root,box_id);
    if(typeof assets!=='boolean')throw Error('Invalid offline selection.');relative=normalize(relative,{allowHidden:true});
    const jobs=this.jobs();if(jobs.length>=50)throw Error('Offline operation history reached 50 selections. No history was removed.');
    if(jobs.some(j=>j.root===root&&j.path===relative&&j.state!=='complete'))throw Error('This folder already has a retained download. Resume that operation.');
    const job={id:randomUUID(),...binding,path:relative,assets,state:'running',queue:[{path:relative,cursor:'',index:0}],seen:[],entries:0,files:0,bytes:0,skipped:0,at:new Date().toISOString()};
    this.persist(job);this.pump();return job;
  }
  setState(id,state){
    const job=this.jobs().find(j=>j.id===id);if(!job)throw Error('Unknown offline operation.');
    if(!['running','paused'].includes(state)||job.state==='complete')throw Error('This offline operation cannot change state.');
    job.state=state;delete job.reason;this.persist(job);if(state==='running')this.pump();return job;
  }
  pump(){if(this.closed||this.suspended)return this.running;this.requested=true;if(this.running)return this.running;this.running=(async()=>{do{this.requested=false;await this.run();}while(this.requested&&!this.closed&&!this.suspended);})().finally(()=>{this.running=null;});return this.running;}
  async run(){
    while(!this.closed&&!this.suspended){
      const job=this.jobs().find(j=>j.state==='running');if(!job)return;
      try{await this.step(job);}catch(error){const current=this.jobs().find(j=>j.id===job.id);if(current?.state==='running'){current.state='paused';current.reason=error.message;this.persist(current);this.emit('offline-progress',current);}}
    }
  }
  async step(job){
    if(job.root!=='storage')this.notes.boxes.identity(job.root,job.box_id);
    if(!job.queue.length){job.state='complete';this.persist(job);this.emit('offline-progress',job);return;}
    const current=job.queue[0];
    if(!current.page){
      const {data}=await this.remote.json('entries',{root:job.root,path:current.path,limit:100,...current.cursor?{cursor:current.cursor}:{}});
      current.page=data;this.remember(job.root,current.path,current.cursor,data);
      const live=this.jobs().find(j=>j.id===job.id);if(live?.state!=='running')job.state=live?.state||'paused';this.persist(job);if(job.state!=='running')return;
    }
    const page=current.page;
    while(current.index<(page.entries||[]).length){
      if(this.closed||this.suspended)return;const live=this.jobs().find(j=>j.id===job.id);if(live?.state!=='running')return;
      const entry=page.entries[current.index];const target=normalize(entry.relative_path,{allowHidden:true});
      if(target!==path.posix.join(current.path,entry.name)||entry.name.includes('/'))throw Error('The server returned an entry outside its folder.');
      if(job.entries>=100000)throw Error('Offline traversal reached 100,000 entries. Retained progress is paused.');
      if(entry.name.startsWith('.'))job.skipped++;
      else if(entry.kind==='directory'){
        if(target.split('/').length>64)throw Error('Offline folder depth exceeded 64.');
        if(!job.seen.includes(target)){job.seen.push(target);job.queue.push({path:target,cursor:'',index:0});}
      }else if(entry.kind==='file'&&(note(entry.name)||job.assets)){
        if(!Number.isSafeInteger(entry.size)||entry.size<0)throw Error('Invalid source file size.');
        if(entry.size>this.cache.maxFileBytes){job.skipped++;}
        else{
          if(job.bytes+entry.size>MAX_BYTES)throw Error('Offline download reached its 256 MiB budget. Existing downloads are preserved.');
          let replica=this.notes.get(job.root,target);
          if(!replica&&isBoxRef(job.root)&&note(entry.name)&&entry.size<=512*1024){
            try{replica=await this.notes.adopt(job.root,target);}
            catch(error){if(![400,403,404,409,422].includes(error.status)&&!/supported writable|source|admission|custody|budget|capacity|exceeds/i.test(error.message))throw error;}
          }
          if(!replica){const {data:metadata}=await this.remote.json('stat',{root:job.root,path:target});if(metadata.size!==entry.size)throw Error('The file changed since its directory observation. Resume after reviewing the source.');await this.cache.fetch(job.root,target,metadata,()=>this.remote.content(job.root,target));await this.cache.pin(job.root,target,true);}
          job.files++;job.bytes+=entry.size;
        }
      }else job.skipped++;
      current.index++;job.entries++;
      const after=this.jobs().find(j=>j.id===job.id);if(after?.state!=='running'){job.state=after?.state||'paused';}
      this.persist(job);if(job.state!=='running')return;
    }
    if(page.has_more){if(!page.next_cursor||page.next_cursor===current.cursor)throw Error('The folder cursor did not advance. Refresh requires review; no walk was restarted.');current.cursor=page.next_cursor;current.index=0;delete current.page;}
    else job.queue.shift();
    this.persist(job);this.emit('offline-progress',job);
  }
  async close(){this.closed=true;await this.running;}
}
