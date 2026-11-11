import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {NoteGraph} from '../src/note-graph.mjs';
import {queryBase,editBaseDefinition,noteProperties,baseCatalogScope} from '../src/base-model.mjs';
import {categories,knowledgeGraph,filterKnowledge} from '../src/knowledge-model.mjs';
const notes=[{root:'box',path:'Notes/A.md',values:{type:'concept',rank:1},tags:['subject/physics'],forward:[{target:'B'}]},{root:'box',path:'Notes/B.md',values:{type:'person',people:['Ada']},aliases:['Bee'],tags:[],forward:[]},{root:'box',path:'Notes/_History/C.md',values:{type:'concept'},forward:[]}];
test('overview first paint is local, requests coalesce and compact reads are page bounded',async()=>{
 const db=new DatabaseSync(':memory:');let calls=0;
 const graph=new NoteGraph({db,records:new Map()},{async json(_,input){calls++;assert.equal(input.compact,true);await new Promise(resolve=>setTimeout(resolve,5));return {data:{items:notes,next:'workspace_note_next',coverage:'known'}};}}).init();
 assert.equal((await graph.catalog({root:'box',compact:true,quick:true,cacheOnly:true})).items.length,0);assert.equal(calls,0);
 const [a,b]=await Promise.all([graph.catalog({root:'box',compact:true,quick:true}),graph.catalog({root:'box',compact:true,quick:true})]);assert.equal(calls,1);assert.deepEqual(a,b);assert.equal(a.next,'workspace_note_next');
 graph.invalidate('box');assert.equal((await graph.catalog({root:'box',compact:true,quick:true,cacheOnly:true})).items.length,3);assert.equal(calls,1);
 db.close();
});
test('cache pressure evicts catalog manifests and pages together',async()=>{
 const db=new DatabaseSync(':memory:'),graph=new NoteGraph({db,records:new Map()},{async json(){return {data:{items:notes}};}}).init();await graph.catalog({root:'box',compact:true,quick:true});
 for(let i=0;i<1001;i++)graph.remember(JSON.stringify(['other',i]),{a:i});
 const manifest=graph.cached(JSON.stringify(['catalog-v3','box','',{profile:'compact'}]));assert.equal(manifest,null);
 assert.equal(db.prepare("SELECT count(*) AS n FROM navigation_cache WHERE key LIKE '%catalog-page-v3%'").get().n,0);db.close();
});
test('metadata observation time is never presented as file modification time',()=>{const row={root:'box',path:'A.md',values:{},observed_at:'2026-11-03T12:00:00Z'};assert.equal(noteProperties(row).file.mtime,null);assert.equal(noteProperties(row).file.size,null);assert.equal(noteProperties({...row,modified_at:'2020-01-01T00:00:00Z',size:10}).file.mtime,'2020-01-01T00:00:00Z');});
test('local Mac knowledge never reports a Main outage or requests remote metadata',async()=>{const db=new DatabaseSync(':memory:'),graph=new NoteGraph({db,records:new Map()},null,{localOnly:true}).init();const result=await graph.catalog({root:'mac',quick:true,compact:true});assert.equal(result.error,undefined);assert.match(result.qualification,/Opened Mac files/);db.close();});
test('metadata Bases preserve unknown YAML and support remote-only typed rows and grouping',()=>{
 const text='# preserve\ncustom: keep\nviews:\n - type: table\n   order: [file.name, type]\n';const updated=editBaseDefinition(text,0,{groupBy:{property:'type'},type:'cards'});assert.match(updated,/# preserve/);assert.match(updated,/custom: keep/);
 const result=queryBase(updated,notes,{qualification:'Known metadata'});assert.equal(result.total,3);assert.equal(result.type,'cards');assert.equal(result.rows[2].group,'person');assert.equal(result.qualification,'Known metadata');assert.throws(()=>editBaseDefinition(text,0,{unsafe:true}));
});
test('categories and graph retain scope, ambiguity and explicit historical qualification',()=>{
 const graph=knowledgeGraph(notes,{center:{root:'box',path:'Notes/A.md'},hops:1});assert.equal(graph.nodes.length,2);assert.equal(graph.edges.length,1);assert.equal(categories(notes).people[0].value,'Ada');assert.equal(filterKnowledge(notes,{history:false}).length,2);
 const ambiguous=knowledgeGraph([...notes,{...notes[1],path:'Else/B.md'}]);assert.equal(ambiguous.ambiguous.length,0); // same-folder exact name wins
 const unresolved=knowledgeGraph([{...notes[0],forward:[{target:'missing'}]}]);assert.equal(unresolved.unresolved.length,1);
});
test('catalog overlays pending local properties and retains remote-only metadata offline',async()=>{
 const db=new DatabaseSync(':memory:'),records=new Map([['a',{root:'box',path:'Notes/A.md',text:'---\ntype: edited\n---\n[[B]]',pending:true}]]);let online=true;
 const graph=new NoteGraph({db,records},{async json(){return{data:{items:notes,coverage:'known'}};}},{online:()=>online}).init();
 const result=await graph.catalog({root:'box'});assert.equal(result.items.length,3);assert.equal(result.items[0].values.type,'edited');online=false;const offline=await graph.catalog({root:'box',refresh:true});assert.equal(offline.items.length,3);assert.equal(offline.origin,'cached');assert.match(offline.error,/offline/);db.close();
});
test('bounded catalog scans retain a resumable cursor even through missing old paths',async()=>{
 const db=new DatabaseSync(':memory:');let calls=0;
 const graph=new NoteGraph({db,records:new Map()},{async json(_endpoint,input){calls++;const page=Number(input.after||0);return{data:{items:page===0?[{...notes[0],id:'a'}]:page===50?[{...notes[1],id:'b'}]:[],next:page<50?String(page+1):'',coverage:'known'}};}}).init();
 const first=await graph.catalog({root:'box'});assert.equal(calls,50);assert.equal(first.items.length,1);assert.equal(first.next,'50');assert.equal(first.can_load_more,true);
 const next=await graph.catalog({root:'box',more:true});assert.equal(next.items.length,2);assert.equal(next.can_load_more,false);assert.equal(calls,51);
 assert.equal((await graph.catalog({root:'box'})).items.length,2);assert.equal(calls,51);
 db.prepare('UPDATE navigation_cache SET at=0').run();assert.equal((await graph.catalog({root:'box'})).items.length,2);assert.equal(calls,51);
 graph.invalidate('box');const refreshed=await graph.catalog({root:'box'});assert.equal(refreshed.items.length,2);assert.equal(refreshed.next,'');assert.equal(calls,102);db.close();
});
test('Base catalog scoping only narrows on mandatory literal folder conditions',()=>{
 const base=filters=>`filters: ${JSON.stringify(filters)}\nviews: [{type: table}]\n`;
 assert.equal(baseCatalogScope(base('file.inFolder("Projects/example/notes") && type == "concept"')),'Projects/example/notes');
 assert.equal(baseCatalogScope(base({and:['file.inFolder("Notes")',{or:['type == "note"','type == "book"']}]})),'Notes');
 for(const filter of ['file.inFolder("Notes") || type == "book"','!file.inFolder("Notes")','file.inFolder(this.file.folder)','file.inFolder("../escape")'])assert.equal(baseCatalogScope(base(filter)),'');
 assert.equal(baseCatalogScope(base({or:['file.inFolder("Notes")','file.inFolder("Topics")']})),'');
});
test('evicted metadata pages are qualified offline and rebuilt on the next requested read',async()=>{
 const db=new DatabaseSync(':memory:');let online=true,calls=0;
 const graph=new NoteGraph({db,records:new Map()},{async json(){calls++;return{data:{items:notes,coverage:'known'}};}},{online:()=>online}).init();await graph.catalog({root:'box'});
 db.prepare('DELETE FROM navigation_cache WHERE key=?').run(JSON.stringify(['catalog-page-v2','box','',0]));online=false;
 const cached=await graph.catalog({root:'box'});assert.equal(cached.cache_incomplete,true);assert.match(cached.qualification,/no longer available/);assert.equal(calls,1);
 online=true;assert.equal((await graph.catalog({root:'box'})).items.length,3);assert.equal(calls,2);db.close();
});
