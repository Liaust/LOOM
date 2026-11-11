import path from 'node:path';
import {isBoxRef} from './box-roots.mjs';

const within=(file,scope)=>!scope||file===scope||file.startsWith(scope+'/');
export function workingTextResults(records,{root,path:scope='',query},limit=60){
  const needle=query.trim().toLocaleLowerCase(),out=[];
  if(needle.length<2)return out;
  for(const record of records){
    if(record.root!==root||record.deleted||record.directory||typeof record.text!=='string'||!within(record.path,scope))continue;
    const at=record.text.toLocaleLowerCase().indexOf(needle);
    if(at<0)continue;
    const start=Math.max(record.text.lastIndexOf('\n',at)+1,at-70);
    out.push({root,relative_path:record.path,name:path.posix.basename(record.path),kind:'file',origin:'working',pending:Boolean(record.pending||record.held),excerpt:record.text.slice(start,at+needle.length+100).replace(/\s+/g,' ').trim()});
    if(out.length===limit)break;
  }return out;
}
export class InteractiveSearch{
  constructor(remote,{records,roots}){Object.assign(this,{remote,records,roots});this.controller=null;}
  cancel(){this.controller?.abort();this.controller=null;}
  local(input){return {results:workingTextResults(this.records(),input),notice:'Current working copies only'};}
  async indexed({root,path:scope='',query}){
    this.cancel();const descriptor=this.roots().find(value=>value.ref===root);
    if(root==='mac')return {results:[],notice:'Current Mac working copies only; no Main index for Mac Box.'};
    if(!descriptor||descriptor.available===false||root==='local'||descriptor.source_kind?.startsWith('application_data'))throw Error('This source has no indexed content search.');
    const selection=descriptor.box_id?{box_id:descriptor.box_id}:{},request={query,mode:'lexical',source_node_key:'main',limit:30,source_lifecycle:isBoxRef(root)?'active':'archived'};
    if(isBoxRef(root)&&root!=='box'&&!descriptor.box_id)throw Error('This Box has no registered search identity.');
    if(isBoxRef(root)&&scope.startsWith('Projects/'))request.project_ref=scope.split('/')[1];
    const controller=this.controller=new AbortController(),started=performance.now();
    try{
      const {data}=await this.remote.api('/v1/knowledge/notes/files/search',selection, {method:'POST',body:request,signal:controller.signal,lane:'search'});
      if(controller.signal.aborted)throw Error('Search cancelled.');
      const base=descriptor.source_path.replace(/\/$/,''),suppressed=new Set();
      for(const value of this.records())if(value.root===root&&(value.deleted||value.pending||value.held)){suppressed.add(value.path);if(value.source?.path)suppressed.add(value.source.path);}
      const results=(data.results||[]).filter(hit=>hit.path?.startsWith(base+'/')).map(hit=>({...hit,root,relative_path:hit.path.slice(base.length+1),name:hit.title||path.posix.basename(hit.path),kind:'file',origin:'indexed'})).filter(hit=>within(hit.relative_path,scope)&&!suppressed.has(hit.relative_path));
      return {results,ms:Math.round(performance.now()-started),notice:data.notice||'Published Main index; unindexed or newer text may be absent.'};
    }finally{if(this.controller===controller)this.controller=null;}
  }
}
