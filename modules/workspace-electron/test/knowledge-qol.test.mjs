import test from 'node:test';
import assert from 'node:assert/strict';
import {categories,filterKnowledge,knowledgeTypes,propertyValues,observedProperties,propertyCategories,knowledgeClass,KNOWLEDGE_CLASSES,knowledgeGraph,knowledgeOpenOptions,graphCamera,graphViewKey} from '../src/knowledge-model.mjs';

const note=(path,values={},extra={})=>({root:'box',path,values,tags:[],forward:[],...extra});
test('type falls back to kind without merging classifications or changing metadata',()=>{
 const items=[note('Project.md',{kind:'project'}),note('Book.md',{type:'book',kind:'project'}),note('Empty.md',{type:['',' '],kind:['concept','concept']}),note('Unknown.md')],before=structuredClone(items);
 assert.deepEqual(knowledgeTypes(items[0]),['project']);assert.deepEqual(knowledgeTypes(items[1]),['book']);assert.deepEqual(knowledgeTypes(items[2]),['concept']);
 assert.deepEqual(categories(items).type,[{value:'book',count:1},{value:'concept',count:1},{value:'project',count:1}]);
 assert.deepEqual(filterKnowledge(items,{type:'project'}).map(n=>n.path),['Project.md']);
 assert.deepEqual(filterKnowledge(items,{category:'reading'}).map(n=>n.path),['Book.md']);
 assert.deepEqual(filterKnowledge(items,{category:'concepts'}).map(n=>n.path),['Empty.md']);
 assert.deepEqual(filterKnowledge([note('Person.md',{kind:['note','person']})],{category:'people'}).map(n=>n.path),['Person.md']);assert.deepEqual(items,before);
});
test('observed custom properties keep scalar types, deduplicate lists, and qualify unknown values',()=>{
 const items=[note('One.md',{rank:[0,0,false,'0',null,{},['nested']],area:['a','b','a']},{observed_at:'2026-11-07'}),note('Two.md',{rank:false,area:'a'},{metadata_version:1}),note('Three.md',{rank:{not:'a scalar'},blank:' ',nothing:null}),note('Four.md',{rank:0},{metadata_origin:'local'})],before=structuredClone(items);
 assert.deepEqual(propertyValues(items[0],'rank'),[0,false,'0']);assert.deepEqual(observedProperties(items),['area','rank']);
 const groups=propertyCategories(items,'rank');assert.equal(groups.missing,1);assert.equal(groups.observed,2);assert.equal(groups.hints,1);
 assert.deepEqual(new Map(groups.rows.map(r=>[JSON.stringify(r.value),r.count])),new Map([['0',2],['false',2],['"0"',1]]));
 assert.deepEqual(filterKnowledge(items,{property:'rank',propertyValue:0}).map(n=>n.path),['One.md','Four.md']);
 assert.deepEqual(filterKnowledge(items,{property:'rank',propertyValue:false}).map(n=>n.path),['One.md','Two.md']);
 assert.deepEqual(filterKnowledge(items,{property:'rank',propertyValue:'0'}).map(n=>n.path),['One.md']);
 assert.deepEqual(filterKnowledge(items,{property:'rank',propertyMissing:true}).map(n=>n.path),['Three.md']);
 assert.deepEqual(filterKnowledge(items,{property:'area',propertyValue:'a',query:'Two'}).map(n=>n.path),['Two.md']);assert.deepEqual(items,before);
});
test('custom grouping has no magic missing-value sentinel or inherited property matches',()=>{
 const items=[note('Literal.md',{category:'Unclassified / unknown'}),note('Absent.md'),note('Empty.md',{category:[]})];
 assert.deepEqual(filterKnowledge(items,{property:'category',propertyValue:'Unclassified / unknown'}).map(n=>n.path),['Literal.md']);
 assert.deepEqual(filterKnowledge(items,{property:'category',propertyMissing:true}).map(n=>n.path),['Absent.md','Empty.md']);
 assert.deepEqual(propertyValues(items[1],'toString'),[]);assert.deepEqual(propertyValues(note('bad.md',{bad:[Infinity,NaN,null,{},[]]}),'bad'),[]);
});
test('graph classifications are semantic, not ordering-dependent; scope and graph bounds remain intact',()=>{
 const items=[note('Person.md',{type:'person'}),note('Reading.md',{kind:'book'}),note('Concept.md',{type:'concept'}),note('Project.md',{kind:'project'}),note('Other.md',{type:'journal'}),note('Unknown.md'),note('_History/Old.md',{type:'person'})];
 assert.deepEqual(items.map(knowledgeClass),['person','reading','concept','project','other','unclassified','history']);
 assert.equal(knowledgeClass(note('Personal.md',{type:'personal'})),'other');assert.equal(knowledgeClass(note('Ledger.md',{type:'bookkeeping'})),'other');assert.equal(knowledgeClass(note('Plan.md',{kind:'project_plan'})),'project');
 const a=knowledgeGraph(items),b=knowledgeGraph([...items].reverse());assert.deepEqual(new Map(a.nodes.map(n=>[n.id,n.classification])),new Map(b.nodes.map(n=>[n.id,n.classification])));
 for(const node of a.nodes)assert.match(KNOWLEDGE_CLASSES[node.classification].color,/^#[0-9a-f]{6}$/i);
 assert.equal(knowledgeGraph(items,{limit:3}).nodes.length,3);assert.equal(knowledgeGraph(items,{limit:3}).truncated,true);
 assert.ok(knowledgeGraph(items,{grouped:true}).nodes.every(n=>n.classification==='folder'));
 const scoped=[note('A.md',{}, {forward:[{target:'B'}]}),note('B.md'),note('Unrelated.md'),{...note('B.md'),root:'mac'}];assert.deepEqual(knowledgeGraph(scoped,{center:{root:'box',path:'A.md'}}).nodes.map(n=>[n.root,n.path]),[['box','A.md'],['box','B.md']]);
});
test('knowledge navigation retains path/root signature and derives active/background tab intent',()=>{
 for(const [event,expected]of [[undefined,{newTab:false,background:false}],[{shiftKey:true},{newTab:false,background:false}],[{metaKey:true},{newTab:true,background:false}],[{ctrlKey:true},{newTab:true,background:false}],[{ctrlKey:true,shiftKey:true},{newTab:true,background:true}],[{button:1},{newTab:true,background:true}],[{original:{metaKey:true,shiftKey:true}},{newTab:true,background:true}]])assert.deepEqual(knowledgeOpenOptions(event),expected);
});
test('camera snapshots are detached and reject invalid states; graph keys follow navigation intent',()=>{
 const state={x:0.4,y:0.6,angle:0.3,ratio:0.2,unrelated:'not persisted'},copy=graphCamera(state);assert.deepEqual(copy,{x:0.4,y:0.6,angle:0.3,ratio:0.2});copy.x=4;assert.equal(state.x,0.4);
 for(const invalid of [null,{}, {...state,ratio:0},{...state,ratio:-1},{...state,x:Infinity},{...state,angle:NaN}])assert.equal(graphCamera(invalid),null);
 const scope={root:'box',path:'Notes'},key=graphViewKey(scope,'notes',null,{history:true});assert.equal(key,graphViewKey({...scope},'notes',{root:'box',path:'ignored.md'},{history:true,query:''}));
 assert.notEqual(key,graphViewKey(scope,'folders',null,{history:true}));assert.notEqual(key,graphViewKey({...scope,root:'mac'},'notes',null,{history:true}));assert.notEqual(key,graphViewKey(scope,'notes',null,{history:false}));
 assert.notEqual(graphViewKey(scope,'notes',null,{property:'rank',propertyValue:0}),graphViewKey(scope,'notes',null,{property:'rank',propertyValue:'0'}));
 assert.equal(graphViewKey(scope,'local',{root:'box',path:'A.md'}),graphViewKey(scope,'local',{path:'A.md',root:'box'}));
});
