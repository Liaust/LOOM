import path from 'node:path';

export const applicationRoot = ref => /^appdata_[A-Za-z0-9_-]{1,768}$/.test(ref || '');
export const applicationGroup = {ref:'application-data',label:'Application data',node:'main',read_only:true,available:true,source_kind:'application_data_group',source_path:''};

export async function applicationRoots(remote,{project='',cursor=0,selection={}}={}){
  const {data}=await remote.json('application-data',{...selection,project_id:project,cursor,limit:100});
  if(!Array.isArray(data.roots)||data.roots.length>500)throw Error('Invalid application data catalogue.');
  return {...data,roots:data.roots.map(root=>{
    if(!applicationRoot(root.ref)||root.read_only!==true||root.source_kind!=='application_data')throw Error('Invalid application data source.');
    return {...root,parent_ref:applicationGroup.ref};
  })};
}

export function archiveMember(value){
  if(!value||typeof value.id!=='string'||!/^[A-Za-z0-9_-]{1,160}$/.test(value.id)||typeof value.name!=='string'||!value.name||value.name.length>4096||typeof value.archiveRevision!=='string'||value.archiveRevision.length>512)throw Error('Choose a listed archive member.');
  return {id:value.id,name:value.name,archiveRevision:value.archiveRevision};
}
export const archiveCacheKey=(archive,member)=>JSON.stringify({archive,member:archiveMember(member).id,revision:member.archiveRevision});
export const memberRevision=(revision,id)=>revision.replace(/"$/,'')+'-'+id+'"';

export async function archivePage(remote,root,relative){
  const {data,ms}=await remote.json('archive-members',{root,path:relative});
  return {root,path:relative,entries:data.members.map(member=>({name:member.name,relative_path:relative,kind:'file',size:member.size,member:{id:member.id,name:member.name,archiveRevision:data.archive.revision}})),has_more:false,order:'archive',ms};
}

export async function cachedArchiveMember(remote,cache,root,archive,selection,{offline=false}={}){
  const selected=archiveMember(selection),key=archiveCacheKey(archive,selected);
  let cached=cache.get(root,key);
  if(offline){if(!cached)throw Error('This attachment is not cached. Reconnect to Main to open it.');return {cached,metadata:{name:path.posix.basename(selected.name),relative_path:archive,size:cached.size,revision:cached.revision},member:selected,cacheKey:key,offline:true};}
  const {data}=await remote.json('archive-members',{root,path:archive});
  if(data.archive.revision!==selected.archiveRevision)throw Error('The source archive changed. Refresh its attachment list before opening.');
  const member=data.members.find(item=>item.id===selected.id);
  if(!member||member.name!==selected.name)throw Error('The selected attachment is no longer available.');
  const metadata={name:path.posix.basename(member.name),relative_path:archive,size:member.size,revision:memberRevision(data.archive.revision,member.id),crc32:member.crc32,modified_at:data.archive.modified_at};
  if(metadata.size>cache.maxFileBytes||metadata.size>cache.maxBytes)throw Error('This attachment exceeds the preview cache limit. Download its original ZIP instead.');
  cached ||= await cache.fetch(root,key,metadata,()=>remote.request('archive-content',{root,path:archive,member:member.id,revision:data.archive.revision},{lane:'content'}));
  return {cached,metadata,member:selected,cacheKey:key,offline:false};
}
