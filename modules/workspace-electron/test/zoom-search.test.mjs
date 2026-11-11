import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm,readFile} from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {InterfaceZoom,zoomFactor,zoomShortcut} from '../src/interface-zoom.mjs';
import {workingTextResults,InteractiveSearch} from '../src/interactive-search.mjs';
import {mergeSearchHits} from '../src/search-results.mjs';

test('native app zoom handles equals/shifted/numpad plus, minus and reset without intercepting ordinary typing',()=>{
  for(const key of ['+','='])assert.equal(zoomShortcut({type:'keyDown',meta:true,key},'darwin'),'in');
  assert.equal(zoomShortcut({type:'keyDown',meta:true,shift:true,key:'+'},'darwin'),'in');
  assert.equal(zoomShortcut({type:'keyDown',meta:true,key:'-'},'darwin'),'out');
  assert.equal(zoomShortcut({type:'keyDown',control:true,key:'0'},'linux'),'reset');
  assert.equal(zoomShortcut({type:'keyDown',control:true,code:'NumpadAdd'},'linux'),'in');
  for(const value of [{type:'keyUp',meta:true,key:'+'},{type:'keyDown',key:'='},{type:'keyDown',meta:true,alt:true,key:'+'}])assert.equal(zoomShortcut(value,'darwin'),null);
  assert.equal(zoomFactor(NaN),1);assert.equal(zoomFactor(100),2);assert.equal(zoomFactor(-1),.5);
});
test('interface zoom changes native page scale and persists independently of notes and editor font',async()=>{
  const dir=await mkdtemp(path.join(os.tmpdir(),'loom-zoom-'));try{
    const file=path.join(dir,'interface.json'),set=[],zoom=await new InterfaceZoom(file).init();
    await Promise.all([zoom.change('in',{setZoomFactor:v=>set.push(v)}),zoom.change('in',{setZoomFactor:v=>set.push(v)})]);
    assert.deepEqual(set,[1.1,1.2]);assert.equal((await new InterfaceZoom(file).init()).factor,1.2);
    await zoom.change('reset');assert.deepEqual(JSON.parse(await readFile(file,'utf8')),{zoom:1});
    await assert.rejects(zoom.change('unknown'));
  }finally{await rm(dir,{recursive:true,force:true});}
});
test('working text search finds unindexed current text without opening files and respects scope/deletions',()=>{
  const docs=[{root:'box',path:'Projects/atlas/current.md',text:'An Opportunity cost'},
    {root:'box',path:'Projects/atlas-old/other.md',text:'opportunity'},
    {root:'box',path:'Projects/atlas/gone.md',text:'opportunity',deleted:true},
    {root:'mac',path:'local.md',text:'opportunity'}];
  const hits=workingTextResults(docs,{root:'box',path:'Projects/atlas',query:'opportunity'});
  assert.equal(hits.length,1);assert.equal(hits[0].origin,'working');assert.match(hits[0].excerpt,/Opportunity/);
  assert.equal(mergeSearchHits(hits,[{...hits[0],origin:'indexed'}])[0].origin,'working');
});
test('indexed search cancels superseded HTTP/SQL work and uses a separate interactive lane',async()=>{
  const calls=[],remote={api:async(route,query,options)=>{calls.push(options);if(calls.length===1)return new Promise((resolve,reject)=>options.signal.addEventListener('abort',()=>reject(Error('cancelled'))));return {data:{results:[]}};}};
  const search=new InteractiveSearch(remote,{roots:()=>[{ref:'box',source_path:'/srv/loom/box'}],records:()=>[]});
  const old=assert.rejects(search.indexed({root:'box',query:'first'}),/cancelled/);await search.indexed({root:'box',query:'second'});await old;
  assert.equal(calls[0].signal.aborted,true);assert.equal(calls[1].lane,'search');assert.equal(calls[1].body.project_ref,undefined);
});
test('indexed results do not cross source boundaries or resurrect pending/deleted paths',async()=>{
  const remote={api:async()=>({data:{results:['/srv/loom/box/Projects/a/current.md','/srv/loom/box/Projects/a/old.md','/srv/loom/box-copy/secret.md'].map(path=>({path}))}})};
  const search=new InteractiveSearch(remote,{roots:()=>[{ref:'box',source_path:'/srv/loom/box'}],records:()=>[{root:'box',path:'Projects/a/new.md',pending:true,source:{path:'Projects/a/old.md'}}]});
  const result=await search.indexed({root:'box',path:'Projects/a',query:'some query'});assert.equal(result.results.length,1);assert.equal(result.results[0].relative_path,'Projects/a/current.md');
});
