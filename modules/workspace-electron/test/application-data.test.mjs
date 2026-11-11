import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,readFile,rm} from 'node:fs/promises';
import {Readable} from 'node:stream';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {crc32} from 'node:zlib';
import {FileCache} from '../src/cache.mjs';
import {applicationRoots,archivePage,archiveCacheKey,cachedArchiveMember,memberRevision} from '../src/application-data.mjs';
import {DocumentTabs} from '../renderer/tabs.js';
import {DirectoryTree} from '../renderer/tree-model.js';
import {sessionPage} from '../src/session-state.mjs';
import {downloadFileCopy} from '../src/file-download.mjs';
import {MainFiles} from '../src/transport.mjs';
import {FileTree} from '../renderer/file-tree.js';

test('project Data navigation reveals its grouped source without scanning other allocations',async()=>{
  const requests=[],source={ref:'appdata_selected',parent_ref:'application-data',label:'Selected project'};
  const model=new DirectoryTree(async input=>{requests.push(input.root);return {entries:[],has_more:input.root==='application-data',next_cursor:'next'};});
  await FileTree.prototype.reveal.call({model,roots:()=>[source],render(){}},source.ref,'');
  assert.deepEqual(requests,['application-data','appdata_selected']);
  assert.equal(model.isExpanded('application-data',''),true);
  assert.equal(model.branch('application-data','').page.entries[0].root_ref,source.ref);
  assert.equal(model.branch('application-data','').page.next_cursor,'next');
});

test('application reads carry the selected view revision without changing ordinary roots',async()=>{
  const remote=new MainFiles('/unused');
  remote.apiRequest=async(route,query)=>({route,query});
  remote.setApplicationViews([{ref:'appdata_a',source_kind:'application_data',view_revision:'view-a'}]);
  const query={root:'appdata_a',path:'paper.pdf'};
  assert.equal((await remote.request('content',query)).query.view_revision,'view-a');
  assert.equal(query.view_revision,undefined);
  assert.equal((await remote.request('content',{root:'box'})).query.view_revision,undefined);
  remote.setApplicationViews([]);
  assert.equal((await remote.request('content',query)).query.view_revision,undefined);
});

test('application catalogue is explicitly read-only',async()=>{
  await assert.rejects(()=>applicationRoots({json:async()=>({data:{roots:[{ref:'box',read_only:false}]}})}));
  const page=await applicationRoots({json:async()=>({data:{roots:[{ref:'appdata_test',read_only:true,source_kind:'application_data'}]}})});
  assert.equal(page.roots[0].parent_ref,'application-data');
});
test('allocation and archive-member tree rows retain distinct identities',()=>{
  const tree=new DirectoryTree(()=>{});
  const group=tree.remember('application-data','',{entries:[{name:'A',root_ref:'appdata_a',relative_path:''},{name:'B',root_ref:'appdata_b',relative_path:''}]});assert.equal(group.entries.length,2);
  const zip=tree.remember('appdata_a','a.zip',{entries:[{name:'One',relative_path:'a.zip',member:{id:'one'}},{name:'Two',relative_path:'a.zip',member:{id:'two'}}]});assert.equal(zip.entries.length,2);
});
test('selected ZIP member is verified before cache publication; corruption is rejected',async()=>{
  const dir=await mkdtemp(path.join(tmpdir(),'loom-app-data-'));
  try{
    const cache=new FileCache(dir),bytes=Buffer.from('%PDF-1.7\nattachment'),revision='W/"archive-a"',selected={id:'zip_0_abc',name:'paper.pdf',archiveRevision:revision};await cache.init();
    let downloads=0;
    const remote={json:async()=>({data:{archive:{revision},members:[{...selected,size:bytes.length,crc32:crc32(bytes)}]}}),request:async()=>{downloads++;const stream=Readable.from([bytes]);stream.headers={etag:memberRevision(revision,selected.id)};return stream;}};
    const result=await cachedArchiveMember(remote,cache,'appdata_a','a.zip',selected);assert.deepEqual(await readFile(result.cached.diskPath),bytes);
    await cachedArchiveMember(remote,cache,'appdata_a','a.zip',selected);assert.equal(downloads,1);
    assert.notEqual(archiveCacheKey('a.zip',selected),archiveCacheKey('b.zip',selected));
    const offline=await cachedArchiveMember(null,cache,'appdata_a','a.zip',selected,{offline:true});assert.equal(offline.offline,true);
    await assert.rejects(()=>cachedArchiveMember(remote,cache,'appdata_a','a.zip',{...selected,archiveRevision:'old'}),/changed/);
    remote.request=async()=>{const stream=Readable.from([Buffer.alloc(bytes.length)]);stream.headers={etag:memberRevision(revision,selected.id)};return stream;};
    await assert.rejects(()=>cachedArchiveMember(remote,cache,'appdata_b','a.zip',selected),/checksum/);assert.equal(cache.get('appdata_b',archiveCacheKey('a.zip',selected)),null);
    const page=await archivePage(remote,'appdata_a','a.zip');assert.equal(page.entries[0].relative_path,'a.zip');assert.equal(page.entries[0].member.name,'paper.pdf');
  }finally{await rm(dir,{recursive:true,force:true});}
});
test('tabs and restored sessions distinguish two members of the same real archive path',()=>{
  const tabs=new DocumentTabs(),a={root:'appdata_a',path:'a.zip',member:{id:'first',name:'one.pdf',archiveRevision:'W/"a"'}},b={...a,member:{...a.member,id:'second',name:'two.pdf'}};
  tabs.open(a);tabs.open(b);assert.equal(tabs.items.size,2);assert.equal(tabs.target(-1).member.id,'first');assert.equal(tabs.get(a.root,a.path,a.member),a);
  assert.deepEqual(sessionPage({kind:'file',...b}).member,b.member);assert.equal(sessionPage({kind:'file',...b,member:{...b.member,id:'../bad'}}),null);
});
test('explicit single-file download preserves existing destinations and rejects partial bytes',async()=>{
  const dir=await mkdtemp(path.join(tmpdir(),'loom-download-'));try{
    const output=path.join(dir,'one.pdf'),metadata={size:4,revision:'test'};
    await downloadFileCopy({output,metadata,load:async()=>Readable.from([Buffer.from('file')])});assert.equal(await readFile(output,'utf8'),'file');
    await assert.rejects(()=>downloadFileCopy({output,metadata,load:async()=>Readable.from([Buffer.from('oops')])}),/already exists/);
    await assert.rejects(()=>downloadFileCopy({output:path.join(dir,'partial'),metadata,load:async()=>Readable.from([Buffer.from('x')])}),/incomplete/);
  }finally{await rm(dir,{recursive:true,force:true});}
});
