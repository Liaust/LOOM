import {lstat,open,readdir,realpath} from 'node:fs/promises';
import {constants} from 'node:fs';
import path from 'node:path';
import {createHash,randomUUID} from 'node:crypto';
import {sourceFingerprint,excludedCopyName} from './copy-review.mjs';
import {BoxRoots,savedRoot} from './box-roots.mjs';

const CHUNK=1024*1024,MAX_FILE=2*1024*1024*1024;
const excluded=excludedCopyName,fingerprint=sourceFingerprint;
const digest=body=>'sha256:'+createHash('sha256').update(body).digest('hex');
export class UploadQueue {
  constructor(db,remote,{emit=()=>{},ensureDirectory,beforeAttachment,accepted=()=>{},boxes=new BoxRoots()}={}) {
    Object.assign(this,{db,remote,emit,ensureDirectory,beforeAttachment,accepted,boxes});this.stopped=false;this.suspended=false;
    db.exec(`CREATE TABLE IF NOT EXISTS desktop_uploads(id TEXT PRIMARY KEY,record TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS desktop_box_uploads(id TEXT PRIMARY KEY,record TEXT NOT NULL);
      CREATE TEMP VIEW IF NOT EXISTS desktop_upload_queue AS SELECT rowid AS position,* FROM desktop_uploads UNION ALL SELECT rowid AS position,* FROM desktop_box_uploads;`);
    this.put=db.prepare('INSERT INTO desktop_uploads(id,record) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record');
    this.putBox=db.prepare('INSERT INTO desktop_box_uploads(id,record) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record');
    for(const row of db.prepare('SELECT record FROM desktop_upload_queue').all()){const v=JSON.parse(row.record);if(['queued','uploading','preparing'].includes(v.state)){v.state='paused';if(v.attachmentID)v.resumeOnConnect=true;v.error='Resume this saved upload when ready.';this.save(v);}}
  }
  save(v){(v.root&&v.root!=='box'?this.putBox:this.put).run(v.id,JSON.stringify(v));this.emit('uploads-changed',{item:this.public(v)});return v;}
  get(id){const row=this.db.prepare('SELECT record FROM desktop_upload_queue WHERE id=?').get(id);if(!row)throw Error('Upload not found.');return JSON.parse(row.record);}
  async enqueueAttachment(attachment){
    const binding=this.boxes.identity(attachment.root,attachment.box_id),info=await lstat(attachment.stagePath,{bigint:true});
    if(!info.isFile()||info.isSymbolicLink()||await realpath(attachment.stagePath)!==attachment.stagePath||Number(info.size)!==attachment.size)throw Error('Staged attachment changed.');
    const v={id:'desktop_upload_'+attachment.id,...binding,source:attachment.stagePath,destination:attachment.destination,attachmentID:attachment.id,kind:'file',state:'queued',fingerprint:fingerprint(info),size:attachment.size,offset:0,collision:'keep_both',hash:attachment.hash};
    this.save(v);this.pump();return this.get(v.id);
  }
  public(v){return {id:v.id,root:v.root||'box',box_id:v.box_id,path:v.destination,kind:v.kind,name:path.basename(v.source),state:v.state,size:v.size||0,offset:v.offset||0,error:v.error||'',excluded:v.excluded||[],exclusionCount:v.exclusionCount||0};}
  list(){return this.db.prepare("SELECT record FROM desktop_upload_queue ORDER BY CASE WHEN json_extract(record,'$.state') IN ('queued','uploading','paused','held') THEN 0 ELSE 1 END,position DESC LIMIT 100").all().map(row=>this.public(JSON.parse(row.record)));}
  async enqueue(sources,{root,box_id,path:destination='',collision='keep_both',expected}) {
    const binding=this.boxes.identity(root,box_id);
    if(typeof destination!=='string'||destination.startsWith('/')||destination.includes('\\')||/[\x00-\x1f]/.test(destination)||destination.split('/').some(p=>p==='..'||p.startsWith('.'))||!['skip','keep_both'].includes(collision))throw Error('Choose a visible Box folder.');
    if(!Array.isArray(sources)||!sources.length||sources.length>100)throw Error('Select up to 100 source entries.');
    let count=0,exclusionCount=0;const staged=[],seen=new Set(),exclusions=[];const saveExcluded=(name,reason)=>{exclusionCount++;if(exclusions.length<20)exclusions.push(`${name}: ${reason}`);};
    const add=async(source,target,parentID)=>{
      if(++count>10000)throw Error('This import reached 10,000 entries; remaining entries were not queued.');
      const name=path.basename(source);
      if(excluded(name)){saveExcluded(name,'hidden or rebuildable');return;}
      const info=await lstat(source,{bigint:true});
      if(expected&&expected[source]!==fingerprint(info))throw Error('The reviewed source changed. No copy was queued; review it again.');seen.add(source);
      if(info.isSymbolicLink()||(!info.isDirectory()&&!info.isFile())){saveExcluded(name,'link or special file');return;}
      if(await realpath(source)!==source){saveExcluded(name,'symbolic-link ancestor');return;}
      if(info.isFile()&&info.size>BigInt(MAX_FILE)){saveExcluded(name,'exceeds 2 GiB per-file limit');return;}
      const v={id:'desktop_upload_'+randomUUID(),...binding,source,destination:target.normalize('NFC'),parentID,kind:info.isDirectory()?'directory':'file',state:'queued',fingerprint:fingerprint(info),size:Number(info.size),offset:0,collision};staged.push(v);
      if(info.isDirectory()) {
        const children=await readdir(source);children.sort();
        for(const child of children)await add(path.join(source,child),[target,child].filter(Boolean).join('/'),v.id);
      }
    };
    let failure;
    try{for(const source of sources){if(typeof source!=='string'||!path.isAbsolute(source))throw Error('A native file selection is required.');await add(source,[destination,path.basename(source)].filter(Boolean).join('/'));}}catch(error){failure=error.message;}
    if(expected&&(failure||Object.keys(expected).some(p=>!seen.has(p))))throw Error(failure||'The reviewed folder changed. No copy was queued.');
    if(!failure){this.db.exec('SAVEPOINT enqueue_upload');try{for(const v of staged)this.save(v);this.db.exec('RELEASE enqueue_upload');}catch(error){this.db.exec('ROLLBACK TO enqueue_upload; RELEASE enqueue_upload');throw error;}}
    if(exclusions.length||failure){const v={id:'desktop_upload_'+randomUUID(),...binding,source:'Import details',destination,state:'notice',kind:'notice',excluded:exclusions,exclusionCount,error:failure||''};this.save(v);}
    this.pump();return this.list();
  }
  async source(v){
    if(await realpath(v.source)!==v.source)throw Error('Source path now passes through a symbolic link.');
    const handle=await open(v.source,constants.O_RDONLY|constants.O_NOFOLLOW);
    try{const info=await handle.stat({bigint:true});if(!info.isFile()||fingerprint(info)!==v.fingerprint)throw Error('Source changed; choose the current file as a new import.');return handle;}catch(error){await handle.close();throw error;}
  }
  async verify(v,handle){const info=await handle.stat({bigint:true});if(fingerprint(info)!==v.fingerprint)throw Error('Source changed during transfer; original preserved.');}
  async prepare(v){
    const handle=await this.source(v);const hash=createHash('sha256'),buffer=Buffer.alloc(CHUNK);let offset=0;
    try{while(offset<v.size){if(this.stopped||this.suspended||this.get(v.id).state!=='uploading')return false;const {bytesRead}=await handle.read(buffer,0,Math.min(buffer.length,v.size-offset),offset);if(!bytesRead)throw Error('Source ended early.');hash.update(buffer.subarray(0,bytesRead));offset+=bytesRead;}await this.verify(v,handle);}finally{await handle.close();}
    v.hash='sha256:'+hash.digest('hex');return true;
  }
  async request(action,query={},options={}){const response=await this.remote.request('uploads',{action,...query},{lane:'upload',...options});const result=await responseJSON(response),data=result.data,root=query.root||options.body?.root,id=query.id||options.body?.id;if(data.intent&&(data.intent.root!==root||data.intent.id!==id))throw Error('Main returned an upload receipt for another saved root or operation.');return data;}
  pump(){if(this.stopped||this.suspended)return this.running;this.requested=true;if(this.running)return this.running;this.running=(async()=>{do{this.requested=false;await this.run();}while(this.requested&&!this.stopped&&!this.suspended);})().finally(()=>{this.running=null;});return this.running;}
  async run(){
    while(!this.stopped&&!this.suspended){
      const row=this.db.prepare("SELECT record FROM desktop_upload_queue WHERE json_extract(record,'$.state')='queued' ORDER BY position LIMIT 1").get();if(!row)break;
      const v=JSON.parse(row.record);v.state='uploading';v.error='';this.save(v);
      try {
        const binding=this.boxes.identity(savedRoot(v),v.box_id,v.box_key);
        if(v.attachmentID)await this.beforeAttachment?.(v,binding);
        if(v.parentID){const parent=this.get(v.parentID);if(savedRoot(parent)!==binding.root||parent.box_id!==v.box_id)throw Error('Saved upload parent belongs to another Box.');if(parent.state==='skipped'){v.state='skipped';this.save(v);continue;}if(parent.state!=='accepted')throw Error('Parent folder has not been accepted; resume it first.');v.destination=path.posix.join(parent.destination,path.basename(v.source).normalize('NFC'));this.save(v);}
        if(v.kind==='directory'){const current=await lstat(v.source,{bigint:true});if(!current.isDirectory()||await realpath(v.source)!==v.source||fingerprint(current)!==v.fingerprint)throw Error('Source folder changed; review a new copy. Mac original preserved.');const result=await this.ensureDirectory(v.destination,v.collision,v.id,binding);v.destination=result.path;v.state=result.state;this.save(v);await this.accepted({...v,...binding});continue;}
        if(!v.hash&&!await this.prepare(v))continue;
        if(this.get(v.id).state!=='uploading')continue;
        let server=await this.request('begin',binding, {method:'POST',body:{id:v.id,root:binding.root,path:v.destination,hash:v.hash,size:v.size,collision:v.collision}});
        v.finalPath=server.path;v.offset=server.offset;v.state=this.get(v.id).state;this.save(v);
        if(server.state==='skipped'){v.state='skipped';this.save(v);continue;}
        if(server.state!=='accepted'){
          const handle=await this.source(v);const buffer=Buffer.alloc(CHUNK);
          try{while(v.offset<v.size){if(this.stopped||this.suspended||this.get(v.id).state!=='uploading')break;await this.verify(v,handle);const {bytesRead}=await handle.read(buffer,0,Math.min(CHUNK,v.size-v.offset),v.offset);if(!bytesRead)throw Error('Source ended early.');const body=buffer.subarray(0,bytesRead);server=await this.request('chunk',{...binding,id:v.id,offset:v.offset},{method:'PUT',body,headers:{'X-LOOM-SHA256':digest(body)}});v.offset=server.offset;v.state=this.get(v.id).state;this.save(v);}await this.verify(v,handle);}finally{await handle.close();}
          if(this.stopped||this.suspended||this.get(v.id).state!=='uploading')continue;
        }
        server=await this.request('complete',{...binding,id:v.id},{method:'POST'});
        v.state=server.state;v.destination=server.path;this.save(v);await this.accepted({...v,...binding});
      }catch(error){if(this.get(v.id).state==='uploading'){v.state='held';v.error=error.message;this.save(v);}}
      finally{if((this.suspended||this.stopped)&&this.get(v.id).state==='uploading'){v.state=this.stopped?'paused':'queued';if(v.attachmentID&&this.stopped)v.resumeOnConnect=true;this.save(v);}}
    }
  }
  async control({id,action}) {
    if(action==='pause_all'||action==='resume_all'){for(const row of this.db.prepare('SELECT record FROM desktop_upload_queue').all()){const v=JSON.parse(row.record);if((action==='pause_all'?['queued','uploading']:['paused']).includes(v.state)){v.state=action==='pause_all'?'paused':'queued';v.resumeOnConnect=false;this.save(v);}}this.emit('uploads-changed',{});if(action==='resume_all')this.pump();return this.list();}
    const v=this.get(id);if(!['pause','resume','cancel'].includes(action))throw Error('Unknown upload action.');
    if(['accepted','skipped','notice','cancelled'].includes(v.state))return this.list();
    if(action==='pause'){v.state='paused';v.resumeOnConnect=false;this.save(v);}
    if(action==='resume'){v.state='queued';v.resumeOnConnect=false;v.error='';this.save(v);this.pump();}
    if(action==='cancel'){v.state='paused';v.resumeOnConnect=false;this.save(v);try{const binding=this.boxes.identity(savedRoot(v),v.box_id);const server=await this.request('cancel',{...binding,id},{method:'POST'});v.state=server.state;v.offset=server.offset;v.destination=server.path;this.save(v);}catch(error){v.state=error.status===404?'cancelled':'held';v.error=error.status===404?'':error.message;this.save(v);}}
    return this.list();
  }
  async close(){this.stopped=true;await this.running;for(const row of this.db.prepare("SELECT record FROM desktop_upload_queue WHERE json_extract(record,'$.state') IN ('queued','uploading')").all()){const v=JSON.parse(row.record);v.state='paused';if(v.attachmentID)v.resumeOnConnect=true;this.save(v);}}
}
export function interruptedAttachment(upload){return upload.attachmentID&&(upload.state==='paused'&&upload.resumeOnConnect===true||upload.state==='held'&&/connect|SSH|timed out|ECONN|socket|unavailable/i.test(upload.error));}
async function responseJSON(response){let raw='';for await(const part of response){raw+=part;if(raw.length>65536)throw Error('Oversized upload response.');}return JSON.parse(raw);}
