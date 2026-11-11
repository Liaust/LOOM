import test from 'node:test';
import assert from 'node:assert/strict';
import {BoxRoots,isWritableBox} from '../src/box-roots.mjs';
import {scopedQuery,queryParameters,MainFiles} from '../src/transport.mjs';
import {Operations} from '../src/operations.mjs';
import {InteractiveSearch} from '../src/interactive-search.mjs';
import {workspaceSession} from '../src/session-state.mjs';
import {resolveReference} from '../src/note-navigation.mjs';
import {quickOpenPath} from '../renderer/file-commands.js';
import {fileDropEffect} from '../renderer/file-tree.js';

const company='box:company',catalog=()=>({schema_version:'box.catalog.v1',default_box:'personal',boxes:[
  {box_id:'personal',box_key:'personal',root_ref:'box',primary:true,available:true,root_path:'/srv/loom/box'},
  {box_id:'company',box_key:'company',root_ref:company,primary:false,available:true,root_path:'/srv/loom/boxes/company'},
  {box_id:'',box_key:'future',root_ref:'',available:false,primary:false},
]}),roots=()=>new BoxRoots(catalog());
const descriptors=[{ref:'box',box_id:'personal',label:'Box',source_path:'/srv/loom/box',writable:true,available:true},
  {ref:company,box_id:'company',label:'Box',source_path:'/srv/loom/boxes/company',writable:true,available:true}];

test('uninitialized unavailable descriptors do not break Personal or invent writable identity',()=>{
  const boxes=roots();assert.equal(boxes.unavailable.length,1);
  assert.equal(boxes.identity('box').box_id,'personal');assert.equal(boxes.identity(company).box_id,'company');
  assert.equal(boxes.localPath('box','Notes/a.md'),'box/Notes/a.md');assert.equal(boxes.localPath(company,'Notes/a.md'),'boxes/company/Notes/a.md');
  assert.throws(()=>boxes.identity('box:future'),/unavailable/);
  assert.throws(()=>boxes.identity(company,'personal'),/conflicts/);
  assert.throws(()=>boxes.identity(company,'company','renamed'),/replica key/);
  assert.equal(isWritableBox(boxes.get(company)),true);
  assert.equal(isWritableBox({...boxes.get(company),writable:false}),false);
  assert.equal(new BoxRoots(descriptors.map(d=>({...d,box_key:d.ref==='box'?'personal':'company'}))).identity('box').box_id,'personal');
});

test('transport opts into roots and binds each immutable body/query before await without global selection',()=>{
  const boxes=roots(),remote=new MainFiles('/unused',{boxes});assert.equal(remote.setBoxes(boxes),boxes);
  assert.deepEqual(scopedQuery(boxes,'/v1/files/roots'),{box_context:'v1'});
  const body={root:company,operation_id:'sealed',text:'original'},captured=scopedQuery(boxes,'/v1/files/notes',{},body);
  assert.deepEqual(captured,{box_id:'company'});assert.equal(body.text,'original');
  assert.deepEqual(scopedQuery(boxes,'/v1/projects',{box:'company'}),{box_id:'company'});
  assert.deepEqual(scopedQuery(boxes,'/v1/projects/overview',{root:company,box_id:'company',box_key:'company',limit:24}),{box_id:'company',limit:24});
  assert.deepEqual(scopedQuery(boxes,'/v1/files/list',{root:company,box_key:'company'}),{root:company,box_id:'company'});
  assert.throws(()=>scopedQuery(boxes,'/v1/projects',{root:company,box_key:'personal'}),/replica key/);
  assert.deepEqual(scopedQuery(boxes,'/v1/health'),{});
  assert.throws(()=>scopedQuery(boxes,'/v1/files/notes',{root:'box'},body),/different Boxes/);
  assert.throws(()=>scopedQuery(boxes,'/v1/projects',{box_id:'unknown'}),/Unknown/);
  assert.throws(()=>scopedQuery(boxes,'/v1/projects',{all_boxes:true,box_id:'company'}),/conflicts/);
  assert.equal(queryParameters({box_id:['personal','company'],cursor:undefined}).toString(),'box_id=personal&box_id=company');
});

test('overview snapshots and contextual reads never reuse another Box cache',async()=>{
  const calls=[],remote={async api(route,q){calls.push({route,q});return {data:route==='/v1/projects'?[{project_id:q.box_id,name:q.box_id}]:[]};}};
  const operations=new Operations(remote,{boxes:roots()});
  const personal=await operations.overview({section:'projects',activeBox:'box'});
  const companyOverview=await operations.overview({section:'projects',activeBox:company});
  assert.equal(personal.items[0].name,'personal');assert.equal(companyOverview.items[0].name,'company');
  assert.equal((await operations.overview({section:'projects',box_id:'personal'})).cached,true);assert.equal(calls.length,2);
  assert.equal(calls[1].q.box_id,'company');assert.equal(operations.cache.size,2);
});

test('late Personal overview is cancelled when Company opens even if transport ignores abort',async()=>{
  let release;
  const remote={async api(_route,q){if(q.box_id==='personal')return new Promise(r=>release=()=>r({data:[{project_id:'personal',name:'old'}]}));return {data:[{project_id:'company',name:'new'}]};}};
  const operations=new Operations(remote,{boxes:roots()}),old=assert.rejects(operations.overview({section:'projects',activeBox:'box'}),/cancelled/);
  const current=await operations.overview({section:'projects',activeBox:company});release();await old;
  assert.equal(current.items[0].name,'new');assert.equal(operations.cache.size,1);
});

test('Company job and schedule models bind every read, without global index fallback',async()=>{
  const calls=[],operations=new Operations({api:async(route,query)=>{calls.push({route,query});return {data:route==='/v1/schedules/hermes'?{availability:'available',jobs:[]}:[]};}},{boxes:roots()});
  await operations.overview({section:'activity',box_id:'company'});
  await operations.overview({section:'schedules',box_id:'company'});
  assert.equal(calls.length,4);assert.equal(calls.every(c=>c.query.box_id==='company'),true);
  assert.equal(calls.some(c=>c.route.startsWith('/v1/indexes')),false);
  await assert.rejects(operations.inspect({kind:'artifact',ref:'artifact',box_id:'company'}),/no Box-scoped backend/);
});

test('Main-wide read-only inspectors remain available from Company without Company query fields',async()=>{
  const calls=[],operations=new Operations({api:async(route,query)=>{
    calls.push({route,query});return {data:route.startsWith('/v1/workers/')?{instance:{display_name:'Indexer',enabled:true,paused:true}}:route.startsWith('/v1/services/')?{display_name:'Hermes',health_status:'ok'}:[]};
  }},{boxes:roots()});
  for(const kind of ['worker','service']){
    const result=await operations.inspect({kind,ref:'main_test',box_id:'company'});
    assert.equal(result.scope,'system');assert.equal(result.scopeLabel,'Main-wide');assert.match(result.summary,/Scope: Main-wide/);
  }
  await operations.inspect({kind:'protection',box_id:'company'});
  assert.equal(calls.length,4);assert.equal(calls.every(c=>!c.query.box_id&&!c.query.root),true);
});

test('Personal schedule inventory also uses its explicit Box selection',async()=>{
  const calls=[],operations=new Operations({api:async(route,query)=>{calls.push({route,query});return {data:route==='/v1/schedules/hermes'?{availability:'available',jobs:[]}:[]};}},{boxes:roots()});
  await operations.overview({section:'schedules',activeBox:'box'});
  const native=calls.find(c=>c.route==='/v1/schedules/hermes');
  assert.ok(native);assert.equal(native.query.box_id,'personal');
});

test('Company native options and queued mutations keep their original Box/profile',async()=>{
  const calls=[],remote={api:async(route,query,options)=>{
    calls.push({route,query,options});
    return {data:options.body.operation==='workspace_info'?{profile:'tyler',profiles:[{id:'tyler',name:'Tyler'}],timezone:'Europe/Amsterdam',items:[]}:{profile:'tyler',box_id:'company',job:{id:'native_company',paused:true}}};
  }},operations=new Operations(remote,{boxes:roots()});
  const options=await operations.inspect({kind:'hermes-options',box_id:'company'});
  assert.equal(options.profile,'tyler');assert.equal(calls[0].query.box_id,'company');assert.equal(calls[0].options.body.profile,'');
  let release;operations.pendingAction=new Promise(resolve=>release=resolve);
  const input={kind:'schedule-create',owner:'hermes',host:'main',profile:'tyler',name:'Owned acceptance',prompt:'Read-only',expression:'0 12 * * 3',box_id:'company'};
  const pending=operations.action(input);input.box_id='personal';operations.setBoxes(roots());release();await pending;
  assert.equal(calls[1].query.box_id,'company');assert.equal(calls[1].options.body.profile,'tyler');
  await assert.rejects(operations.action({...input,profile:'tyler'}),/selected Box/);
  await assert.rejects(operations.action({...input,box_id:'company',profile:'mina'}),/selected Box/);
  assert.equal(calls.length,2);
});

test('queued contextual actions retain their captured Box and saved cancellation is Box-qualified',async()=>{
  let release;const calls=[];
  const remote={async api(route,q,o){calls.push({route,q,o});return {data:{ok:true,validation:o.body.dry_run?{state:'planned_only',errors:0}:{state:'passed',ok:true},project_root:'/root/'+q.box_id,context_state:'registered_refresh_pending'}};}};
  const operations=new Operations(remote,{boxes:roots()});operations.pendingAction=new Promise(r=>release=r);
  const pending=operations.action({kind:'project-create',name:'Company',slug:'company',box_id:'company'});
  operations.setBoxes(roots());release();await pending;
  assert.equal(calls.length,2);assert.equal(calls.every(call=>call.q.box_id==='company'),true);
  operations.controller=new AbortController();operations.controllerScope=JSON.stringify(roots().selection({root:company}));
  operations.cancel({root:'box'});assert.equal(operations.controller.signal.aborted,false);operations.cancel({root:company});assert.equal(operations.controller,null);
});

test('Company indexed content uses active lifecycle and Box filtering before ranking',async()=>{
  let call;const search=new InteractiveSearch({api:async(...args)=>{call=args;return {data:{results:[]}};}},{roots:()=>descriptors,records:()=>[]});
  await search.indexed({root:company,query:'company',path:'Projects/acme'});
  assert.equal(call[1].box_id,'company');assert.equal(call[2].body.source_lifecycle,'active');assert.equal(call[2].body.project_ref,'acme');
});

test('session restore preserves unavailable Box tabs, focus, pinned history and graph centers',()=>{
  const session=workspaceSession({version:1,activeBox:company,active:'tab_2',tabs:[
    {id:'tab_1',cursor:0,pinned:true,history:[{kind:'file',root:'box',path:'Notes/a.md'}]},
    {id:'tab_2',cursor:0,pinned:true,history:[{kind:'knowledge',root:company,box_id:'company',path:'Notes',center:{root:'box:retired',path:'old.md'}}]},
  ]});
  assert.equal(session.activeBox,company);assert.equal(session.tabs.length,2);assert.equal(session.tabs[1].history[0].center.root,'box:retired');
  assert.equal(session.tabs[1].pinned,true);
});

test('cross-Box absolute links expose a navigation label without preloading target content',async()=>{
  let reads=0;const result=await resolveReference({root:'box',from:'Notes/a.md',reference:'/srv/loom/boxes/company/Notes/a.md'},{roots:descriptors,records:[],stat:async()=>{reads++;}});
  assert.equal(result.root,company);assert.equal(result.requires_navigation,true);assert.equal(reads,0);
});

test('duplicate root labels require a choice and drag/drop rejects cross-Box before mutation',()=>{
  assert.equal(quickOpenPath('Box/Notes/a.md',descriptors).choices.length,2);
  assert.equal(quickOpenPath('Box/../a.md',descriptors),null);
  assert.equal(fileDropEffect(company,company,descriptors),'move');
  assert.equal(fileDropEffect('mac',company,descriptors),'copy');
  assert.equal(fileDropEffect('box',company,descriptors),null);
  assert.equal(fileDropEffect(company,'box',descriptors),null);
  assert.equal(fileDropEffect('mac','box:unknown',descriptors),null);
});
