import test from 'node:test';
import assert from 'node:assert/strict';
import {Dashboard} from '../src/dashboard.mjs';
import {BoxRoots} from '../src/box-roots.mjs';

const company='box:company',boxes=()=>new BoxRoots([
  {box_id:'personal',box_key:'personal',root_ref:'box',primary:true,available:true},
  {box_id:'company',box_key:'company',root_ref:company,available:true},
]);
test('Company projects binds query/cache and labels shared runtime Main-wide',async()=>{
  const calls=[],d=new Dashboard({api:async(route,q)=>{calls.push({route,q});return {data:{items:[{name:q.box_id||'global'}]}};}},{boxes:boxes()});
  await d.read({section:'projects',token:'personal',activeBox:'box'});
  const events=[],result=await d.read({section:'projects',token:'company',activeBox:company},e=>events.push(e));
  assert.equal(calls.filter(c=>c.route==='/v1/system/runtime').length,2);
  assert.equal(calls.find(c=>c.q.box_id==='company').route,'/v1/projects/overview');assert.equal(result.blocks.projects.data.items[0].name,'company');
  assert.equal(result.blocks.runtime.available,true);assert.equal(result.blocks.runtime.scope,'system');
  assert.equal(result.blocks.runtime.scopeLabel,'Main-wide');
  assert.equal(calls.filter(c=>c.route==='/v1/system/runtime').every(c=>!c.q.box_id&&!c.q.root),true);
  assert.equal(result.activeBox,company);assert.equal(events.every(e=>e.activeBox===company),true);
  assert.equal((await d.read({section:'projects',token:'again',box_id:'company'})).blocks.projects.cached,true);
  assert.equal((await d.read({section:'projects',token:'back',box_id:'personal'})).blocks.projects.data.items[0].name,'personal');
});
test('Company activity and schedules bind every owner read to the selected Box',async()=>{
  const calls=[];const d=new Dashboard({api:async(route,query,options)=>{calls.push({route,query,options});return {data:{items:[]}};}},{boxes:boxes()});
  for(const section of ['activity','schedules']){
    const result=await d.read({section,token:section,box_id:'company'});
    assert.equal(Object.values(result.blocks).every(v=>v.available===true&&v.scope==='box'),true);
  }
  assert.equal(calls.length,4);assert.equal(calls.every(c=>c.query.box_id==='company'),true);
  assert.equal(calls.find(c=>c.route.endsWith('/workspace')).options.body.profile,'');
});
test('System remains truly Main-wide with explicit scope even from Company',async()=>{
  const calls=[],d=new Dashboard({api:async(route,q)=>{calls.push(q);return {data:{observed_at:'2026-11-08T10:00:00Z'}};}},{boxes:boxes()});
  const result=await d.read({section:'system',token:'main',activeBox:company});
  assert.equal(result.scope,'system');assert.equal(calls.length,3);assert.equal(calls.every(q=>!q.box_id&&!q.root),true);
  assert.equal(Object.values(result.blocks).every(v=>v.scope==='system'&&v.scopeLabel==='Main-wide'),true);
});

test('explicit Main-wide schedule view includes global inventory but no ambiguous native controls',async()=>{
  const calls=[],d=new Dashboard({api:async(route,query)=>{calls.push({route,query});return {data:{items:[]}};}},{boxes:boxes()});
  const result=await d.read({section:'schedules',token:'global',all_boxes:true});
  assert.equal(calls.length,2);assert.equal(calls.every(c=>c.query.all_boxes===true&&!c.query.box_id),true);
  assert.equal(result.scope,'all_boxes');assert.equal(result.blocks.hermesManagement.reason,'single_box_required');
});
test('late Personal dashboard never publishes or enters Company cache after switch',async()=>{
  let release;const events=[],d=new Dashboard({api:async(route,q)=>{if(route.includes('/projects/')&&q.box_id==='personal')return new Promise(r=>release=()=>r({data:{items:[{name:'private'}]}}));return {data:{items:[{name:'company'}]}};}},{boxes:boxes()});
  const old=d.read({section:'projects',token:'old',activeBox:'box'},e=>events.push(e));
  await d.read({section:'projects',token:'new',activeBox:company},e=>events.push(e));release();await old;
  assert.equal(events.some(e=>e.token==='old'&&e.name==='projects'),false);
  assert.equal([...d.cache.values()].some(v=>v.data.items?.some(i=>i.name==='private')),false);
});
test('Company failures never reuse Personal snapshots and cancellation is scope-qualified',async()=>{
  const d=new Dashboard({api:async(_r,q)=>{if(q.box_id==='company')throw Error('offline');return {data:{items:['Personal only']}};}},{boxes:boxes()});
  await d.read({section:'projects',token:'old',box_id:'personal'});
  const failed=await d.read({section:'projects',token:'new',box_id:'company'});
  assert.equal(failed.blocks.projects.available,false);assert.equal(failed.blocks.projects.data,undefined);
  d.controller=new AbortController();d.controllerScope=JSON.stringify(boxes().selection({root:company}));
  d.cancel({activeBox:'box'});assert.equal(d.controller.signal.aborted,false);d.cancel({activeBox:company});assert.equal(d.controller,null);
});
