import path from 'node:path';
import {noteMetadata} from './note-metadata.mjs';
import {referenceParts} from './note-navigation.mjs';
import {isBoxRef} from './box-roots.mjs';

const key=(root,relative)=>JSON.stringify([root,relative]),markdown=relative=>/\.(md|markdown|mdown|mkdn?)$/i.test(relative);
const stem=relative=>relative.replace(/\.(md|markdown|mdown|mkdn?)$/i,''),uniqueItems=(items,kind='link')=>[...new Map(items.map(item=>[kind==='link'?key(item.root,item.path):item.name,item])).values()].slice(0,40);
export class NoteGraph {
  constructor(notes,remote,{online=()=>true,localOnly=false}={}){Object.assign(this,{notes,remote,online,localOnly});this.metadata=new Map();this.epochs=new Map();this.catalogRequests=new Map();}
  invalidate(root){this.epochs.set(root,(this.epochs.get(root)||0)+1);}
  init(){this.notes.db.exec('CREATE TABLE IF NOT EXISTS navigation_cache(key TEXT PRIMARY KEY,record TEXT NOT NULL,at INTEGER NOT NULL,bytes INTEGER NOT NULL)');return this;}
  cached(key){const row=this.notes.db.prepare('SELECT record,at FROM navigation_cache WHERE key=?').get(key);return row?{...JSON.parse(row.record),cached_at:row.at}:null;}
  remember(key,value,prune=true){const raw=JSON.stringify(value),bytes=Buffer.byteLength(raw);if(bytes>1024*1024)return;this.notes.db.prepare('INSERT INTO navigation_cache VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET record=excluded.record,at=excluded.at,bytes=excluded.bytes').run(key,raw,Date.now(),bytes);if(prune)this.prune();}
  prune(){
    const rows=this.notes.db.prepare('SELECT key,bytes FROM navigation_cache ORDER BY at,key').all();let total=rows.reduce((n,r)=>n+r.bytes,0),count=rows.length;const removed=new Set(),del=this.notes.db.prepare('DELETE FROM navigation_cache WHERE key=?');
    for(const row of rows){if(count<=1000&&total<=16*1024*1024)break;if(removed.has(row.key))continue;let parts;try{parts=JSON.parse(row.key);}catch{}
      // A catalog manifest and its pages are one eviction unit.
      const group=parts&&/^catalog(?:-page)?-v[23]$/.test(parts[0])?rows.filter(r=>{try{const p=JSON.parse(r.key);return p[0].replace('-page','')===parts[0].replace('-page','')&&p[1]===parts[1]&&p[2]===parts[2]&&p[3]?.profile===parts[3]?.profile;}catch{return false;}}):[row];
      for(const r of group)if(!removed.has(r.key)){del.run(r.key);removed.add(r.key);total-=r.bytes;count--;}
    }
  }
  local(){
    const documents=[],live=new Set();
    for(const record of this.notes.records.values())if(!record.deleted&&!record.directory&&markdown(record.path)){
      const id=key(record.root,record.path),old=this.metadata.get(id);live.add(id);if(!old||old.text!==record.text)this.metadata.set(id,{text:record.text,meta:noteMetadata(record.text||'')});documents.push({...record,meta:this.metadata.get(id).meta});
    }
    for(const id of this.metadata.keys())if(!live.has(id))this.metadata.delete(id);
    return documents;
  }
  localComplete({root,kind,query=''}){
    const result=[],q=query.toLocaleLowerCase();for(const document of this.local())if(document.root===root){
      const values=kind==='tag'?document.meta.tags:kind==='property'?document.meta.properties:[path.posix.basename(stem(document.path)),...document.meta.aliases];
      for(const name of values)if(name.toLocaleLowerCase().includes(q)||kind==='link'&&document.path.toLocaleLowerCase().includes(q))result.push({root,path:document.path,name,kind});
    }return uniqueItems(result,kind);
  }
  async complete(input){
    const {root,kind,query=''}=input,cacheKey=JSON.stringify(['complete',root,kind,query.toLocaleLowerCase()]);let result;
    try{if(!this.online())throw Error('Main is offline');result=(await this.remote.json('links',{action:'complete',root,kind,query})).data;this.remember(cacheKey,result);return{...result,items:uniqueItems([...result.items.filter(item=>!item.root||item.root===root),...this.localComplete(input)],kind),origin:'main'};}
    catch(error){result=this.cached(cacheKey);return{items:uniqueItems([...(result?.items||[]).filter(item=>!item.root||item.root===root),...this.localComplete(input)],kind),origin:result?'cached':'local',coverage:'Offline/partial working-set suggestions',cached_at:result?.cached_at,error:error.message};}
  }
  index(documents=this.local()){
    const paths=new Map(),names=new Map();for(const document of documents){paths.set(key(document.root,document.path),document);for(const name of new Set([stem(document.path),path.posix.basename(stem(document.path)),...document.meta.aliases])){const k=key(document.root,name.toLocaleLowerCase());if(!names.has(k))names.set(k,[]);names.get(k).push(document);}}return{paths,names};
  }
  localTargets(root,from,reference,index=this.index()){
    const parsed=referenceParts(reference);if(parsed.external)return [];
    const target=parsed.target,direct=new Set();
    for(const candidate of [target?path.posix.join(path.posix.dirname(from),target):from,target]){if(!candidate)continue;direct.add(candidate);if(!path.posix.extname(candidate)){direct.add(candidate+'.md');direct.add(candidate+'.markdown');}}
    for(const candidate of direct){const document=index.paths.get(key(root,candidate));if(document)return[document];}
    return index.names.get(key(root,stem(target).toLocaleLowerCase()))||[];
  }
  async resolve(input){
    const parsed=referenceParts(input.reference);if(parsed.external)return parsed;
    if(parsed.target.startsWith('/')&&this.notes.boxes){
      const owner=[...this.notes.boxes.boxes.values()].filter(box=>{const base=box.root_path||box.source_path;return base&&(parsed.target===base||parsed.target.startsWith(base.replace(/\/$/,'')+'/'));}).sort((a,b)=>(b.root_path||b.source_path).length-(a.root_path||a.source_path).length)[0];
      if(owner&&isBoxRef(input.root)&&owner.ref!==input.root&&!input.allowCrossBox)return {root:owner.ref,path:parsed.target.slice((owner.root_path||owner.source_path).length+1),box_id:owner.box_id,heading:parsed.heading,cross_box:true,requires_navigation:true};
    }
    this.lastError=null;try{if(!this.online())throw Error('Main is offline');const result=(await this.remote.json('links',{root:input.root,reference:input.reference,path:input.from,action:'resolve',...(input.allowCrossBox?{navigate:true}:{})})).data;const choices=result.choices||[];if(choices.length===1)return choices[0];if(choices.length>1)return{choices,heading:parsed.heading};}
    catch(error){this.lastError=error.message;}
    const local=this.localTargets(input.root,input.from,input.reference).map(record=>({root:record.root,path:record.path,heading:parsed.heading}));
    if(local.length===1)return{...local[0],qualification:'Resolved from the downloaded working set'};if(local.length>1)return{choices:local,heading:parsed.heading,qualification:'Downloaded working-set matches'};
    return null;
  }
  async links({root,path:relative}){
    const cacheKey=JSON.stringify(['links',root,relative]);let result,origin='main';
    try{if(!this.online())throw Error('Main is offline');result=(await this.remote.json('links',{root,path:relative,action:'links'})).data;this.remember(cacheKey,result);}
    catch(error){result=this.cached(cacheKey);origin=result?'cached':'local';result||={forward:[],backlinks:[],warnings:[],coverage:'Downloaded working-set only'};result={...result,error:error.message};}
    const local=this.local(),index=this.index(local),active=local.find(record=>record.root===root&&record.path===relative),pending=new Map(local.filter(record=>record.pending||record.held).map(record=>[key(record.root,record.path),record]));
    const pointsTo=record=>record.meta.references.some(reference=>{try{const matches=this.localTargets(record.root,record.path,reference,index);return matches.length===1&&matches[0].root===root&&matches[0].path===relative;}catch{return false;}});
    const backlinks=(result.backlinks||[]).filter(item=>item.root===root&&(!pending.has(key(item.root,item.path))||pointsTo(pending.get(key(item.root,item.path)))));
    for(const record of local)if(record.path!==relative&&pointsTo(record)&&!backlinks.some(item=>item.root===record.root&&item.path===record.path))backlinks.push({root:record.root,path:record.path,name:path.posix.basename(record.path),kind:record.pending?'local_pending':'backlink'});
    return{...result,forward:active?active.meta.references.map(target=>({target,kind:'local'})):result.forward||[],backlinks,origin,qualification:origin==='main'?result.coverage:`${origin==='cached'?'Cached Main graph plus':'Only'} downloaded working-set observations; not current whole-Main coverage`};
  }
  async tagFiles({root,tag}){
    const cacheKey=JSON.stringify(['tag',root,tag]);let result,origin='main';try{if(!this.online())throw Error('Main is offline');result=(await this.remote.json('links',{action:'tag',root,query:tag})).data;this.remember(cacheKey,result);}catch{result=this.cached(cacheKey)||{items:[]};origin='cached/local';}
    const documents=this.local(),matches=record=>record.root===root&&record.meta.tags.some(value=>value.toLocaleLowerCase()===tag.toLocaleLowerCase()),pending=new Map(documents.filter(record=>record.pending||record.held).map(record=>[key(record.root,record.path),record]));
    const local=documents.filter(matches).map(record=>({root,path:record.path,name:path.posix.basename(record.path),kind:'tag'}));
    const current=(result.items||[]).filter(item=>item.root===root&&(!pending.has(key(item.root,item.path))||matches(pending.get(key(item.root,item.path)))));
    return{items:uniqueItems([...current,...local]),origin,coverage:'Known tagged files; local pending changes included'};
  }
  catalog(input={}){const request=JSON.stringify(input);if(!this.catalogRequests.has(request)){const pending=this.readCatalog(input).finally(()=>this.catalogRequests.delete(request));this.catalogRequests.set(request,pending);}return this.catalogRequests.get(request);}
  async readCatalog({root,path:scope='',refresh=false,more=false,compact=false,quick=false,cacheOnly=false}={}){
    const profile=compact?{profile:'compact'}:undefined,version=compact?'v3':'v2',cacheKey=JSON.stringify(['catalog-'+version,root,scope,...(profile?[profile]:[])]),epoch=this.epochs.get(root)||0;let cached=this.cached(cacheKey);
    if(cached?.chunks){const pages=cached.chunks.map(k=>this.cached(k));cached={...cached,items:pages.flatMap(p=>p?.items||[]),cache_incomplete:pages.some(p=>!p),truncated:cached.truncated||pages.some(p=>!p)};}
    let result=cached||{items:[],coverage:'Downloaded working set only',truncated:true},origin=cached?'cached':'local';
    if(!this.localOnly&&!cacheOnly&&(refresh||cached?.epoch!==epoch||!cached||cached.cache_incomplete&&this.online()||more&&cached.next)){
      try{
        if(!this.online())throw Error('Main is offline');
        const continuing=more&&!refresh&&!cached?.cache_incomplete&&cached?.epoch===epoch&&cached.next;
        // Preserve the depth explicitly loaded by the user across refreshes.
        // Cache age alone never starts another corpus query or drops later pages.
        const items=continuing?[...cached.items]:[];let after=continuing?cached.next:'',page,scanned=0,remaining=quick?(more?5:1):continuing?50:cached&&!cached.next?250:Math.max(50,Math.min(250,cached?.scan_pages||50));
        const cursors=new Set([after]);do{page=(await this.remote.json('links',{action:'catalog',root,path:scope,after,limit:200,...(compact?{compact:true}:{})})).data;if(page.items.some(item=>item.root&&item.root!==root))throw Error('Catalog response belongs to another Box root.');scanned++;const room=10000-items.length;items.push(...page.items.slice(0,room));after=page.items.length>room?items.at(-1).id:page.next||'';if(after&&cursors.has(after))throw Error('Metadata cursor did not advance. Existing coverage is preserved; refresh or choose a narrower folder.');cursors.add(after);}while(after&&items.length<10000&&--remaining);
        result={items,coverage:page.coverage,next:after,scan_pages:scanned+(continuing?cached.scan_pages||50:0),can_load_more:Boolean(after)&&items.length<10000,truncated:Boolean(after)||items.some(d=>d.forward_truncated)};
        const chunks=[];let batch=[],bytes=0;this.notes.db.exec('SAVEPOINT catalog_cache');try{for(const old of cached?.chunks||[])this.notes.db.prepare('DELETE FROM navigation_cache WHERE key=?').run(old);const keep=()=>{if(!batch.length)return;const k=JSON.stringify(['catalog-page-'+version,root,scope,...(profile?[profile]:[]),chunks.length]);this.remember(k,{items:batch},false);chunks.push(k);batch=[];bytes=0;};for(const item of items){const size=Buffer.byteLength(JSON.stringify(item));if(bytes+size>512*1024)keep();batch.push(item);bytes+=size;}keep();this.remember(cacheKey,{chunks,coverage:result.coverage,next:result.next,scan_pages:result.scan_pages,can_load_more:result.can_load_more,truncated:result.truncated,epoch},false);this.prune();this.notes.db.exec('RELEASE catalog_cache');}catch(error){this.notes.db.exec('ROLLBACK TO catalog_cache; RELEASE catalog_cache');throw error;}origin='main';
      }catch(error){if(!cached){origin='local';result={items:[],coverage:'Downloaded working set only',truncated:true};}result={...result,error:error.message};}
    }
    const items=new Map((result.items||[]).filter(d=>!d.root||d.root===root).map(d=>[key(root,d.path),{...d,root,values:d.values||{},forward:d.forward||[]}]));
    for(const record of this.notes.byID?.values?.()||this.notes.records.values())if(record.root===root&&record.deleted)items.delete(key(root,record.path));
    for(const record of this.local())if(record.root===root&&(record.path===scope||!scope||record.path.startsWith(scope+'/'))){
      const meta=record.meta;items.set(key(root,record.path),{id:record.fileId||key(root,record.path),root,path:record.path,revision:record.baseId,aliases:meta.aliases,tags:meta.tags,properties:meta.properties,values:meta.values,forward:meta.references.map(target=>({kind:'local',target})),pending:Boolean(record.pending||record.held),metadata_origin:'local',observed_at:record.source?.file?.updated_at});
    }
    return{...result,items:[...items.values()],origin,qualification:this.localOnly?'Opened Mac files and local edits only; not a scan of Mac Box.':`${origin==='main'?'Main-known':origin==='cached'?'Cached Main-known':'Local'} metadata plus working edits. Not a complete filesystem inventory.${result.cache_incomplete?' Some cached metadata pages are no longer available.':result.next?' More catalog candidates remain; '+(result.can_load_more?'load more or choose a folder.':'choose a narrower folder.'):(result.truncated?' Metadata coverage is partial.':'')}`,cached_at:result.cached_at};
  }
}
