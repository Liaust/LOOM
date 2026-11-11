import test from 'node:test';
import assert from 'node:assert/strict';
import {graphViewKey} from '../src/knowledge-model.mjs';

// Sigma builds GPU declarations at import time; these tests exercise DOM and
// snapshot contracts only, not WebGL rendering.
const previousGL=globalThis.WebGL2RenderingContext;
globalThis.WebGL2RenderingContext={};
const {KnowledgeView}=await import('../renderer/knowledge-view.js');
const {Camera}=await import('sigma');
if(previousGL===undefined)delete globalThis.WebGL2RenderingContext;else globalThis.WebGL2RenderingContext=previousGL;

function element(tag){
 const attributes=new Map();
 return{tagName:tag.toUpperCase(),children:[],dataset:{},style:{},className:'',value:'',textContent:'',
  append(...children){for(const child of children){child.parentElement=this;this.children.push(child);}},
  replaceChildren(...children){this.children=[];this.append(...children);},
  insertBefore(child,before){child.parentElement=this;this.children.splice(this.children.indexOf(before),0,child);},
  setAttribute(name,value){attributes.set(name,String(value));},getAttribute(name){return attributes.get(name)??null;},
  addEventListener(name,fn){this['on_'+name]=fn;},
  contains(target){return this===target||this.children.some(child=>child.contains(target));},
  matches(selector){return selector.startsWith('.')?this.className.split(' ').includes(selector.slice(1)):selector.startsWith('[aria-label=')?this.getAttribute('aria-label')===selector.slice(12,-2):this.tagName===selector.toUpperCase();},
  querySelectorAll(selector){return this.children.flatMap(child=>[...(child.matches(selector)?[child]:[]),...child.querySelectorAll(selector)]);},
  querySelector(selector){return this.querySelectorAll(selector)[0]||null;},
  focus(){document.activeElement=this;},setSelectionRange(start,end){this.selectionStart=start;this.selectionEnd=end;}
 };
}
function fixture(t,{items=[],restore,mode='table',canLoadMore=false}={}){
 const old=Object.fromEntries(['document','window','requestAnimationFrame','cancelAnimationFrame'].map(k=>[k,globalThis[k]])),listeners=new Map(),frames=new Map(),calls=[],opens=[],folders=[];let sequence=0;
 globalThis.document={createElement:element,createTextNode:text=>Object.assign(element('text'),{textContent:text}),activeElement:element('body'),hidden:false};
 globalThis.window={addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:name=>listeners.delete(name)};
 globalThis.requestAnimationFrame=fn=>{frames.set(++sequence,fn);return sequence;};globalThis.cancelAnimationFrame=id=>frames.delete(id);
 const api={async knowledgeCatalog(input){calls.push(input);return{items:input.cacheOnly?[]:items,qualification:'Loaded fixture metadata only.',can_load_more:!input.cacheOnly&&canLoadMore};}};
 const view=new KnowledgeView({api,scope:{root:'box',path:'Notes'},mode,restore,open:(...args)=>opens.push(args),folder:(...args)=>folders.push(args),icon:()=>element('span')});
 t.after(()=>{view.destroy();for(const [key,value]of Object.entries(old))if(value===undefined)delete globalThis[key];else globalThis[key]=value;});
 return{view,calls,opens,folders,listeners,frames};
}
const note=(path,values={})=>({root:'box',path,values,tags:[],forward:[]});
const event=(extra={})=>({button:0,preventDefault(){this.defaultPrevented=true;},...extra});
const tick=()=>new Promise(resolve=>setImmediate(resolve));

test('installed Sigma 4 camera implements the graph control and restoration APIs',async()=>{
 const camera=new Camera(),saved={x:.3,y:.7,angle:.25,ratio:.4};let updated=0;camera.on('updated',()=>updated++);
 camera.setState(saved);assert.deepEqual(camera.getState(),saved);
 await camera.zoomIn({duration:0});assert.ok(camera.getState().ratio<saved.ratio);
 await camera.zoomOut({duration:0});assert.equal(camera.getState().ratio,saved.ratio);
 await camera.reset({duration:0});assert.deepEqual(camera.getState(),{x:.5,y:.5,angle:0,ratio:1});assert.equal(camera.isAnimating(),false);assert.equal(updated,4);
});
test('table opens preserve path/root and forward ordinary, modifier and middle intents',async t=>{
 const f=fixture(t,{items:[note('Notes/A.md',{kind:'project'})]});await f.view.ready;
 const button=f.view.dom.querySelector('.knowledge-note');assert.equal(button.children.at(-1).textContent,'project');
 for(const [click,expected]of [[event(),{newTab:false,background:false}],[event({metaKey:true}),{newTab:true,background:false}],[event({ctrlKey:true,shiftKey:true}),{newTab:true,background:true}]]){button.onclick(click);await tick();assert.deepEqual(f.opens.pop(),['Notes/A.md','box',expected]);assert.equal(click.defaultPrevented,true);}
 button.onauxclick(event({button:1}));await tick();assert.deepEqual(f.opens.pop(),['Notes/A.md','box',{newTab:true,background:true}]);
 button.onauxclick(event({button:2}));await tick();assert.equal(f.opens.length,0);
});
test('custom category drilldown preserves existing filters, typed values and saved grouping',async t=>{
 const f=fixture(t,{mode:'categories',restore:{groupBy:'rank',filters:{history:false,category:'concepts'}},items:[note('Notes/A.md',{kind:'concept',rank:0}),note('Notes/B.md',{kind:'concept',rank:'0'}),note('Notes/C.md',{kind:'concept'}),note('Notes/D.md',{kind:'person',rank:0})]});await f.view.ready;
 const rows=f.view.dom.querySelectorAll('.category-row');assert.equal(rows.length,3);const zero=rows.find(row=>row.children[0].textContent==='0');zero.onclick();
 assert.equal(f.view.mode,'table');assert.deepEqual(f.view.filters,{history:false,category:'concepts',property:'rank',propertyMissing:false,propertyValue:0});assert.equal(f.view.dom.querySelectorAll('.knowledge-note').length,1);assert.equal(f.view.snapshot().groupBy,'rank');
 const clear=f.view.dom.querySelectorAll('button').find(b=>b.getAttribute('aria-label')?.startsWith('Clear filter:'));await clear.onclick(event());assert.deepEqual(f.view.filters,{history:false,category:'concepts'});
});
test('empty cached first paint does not reset a restored table page',async t=>{
 const f=fixture(t,{items:Array.from({length:230},(_,i)=>note(`Notes/${i}.md`)),restore:{page:2}});assert.equal(f.view.page,2);await f.view.ready;assert.equal(f.view.page,2);assert.equal(f.view.dom.querySelectorAll('.knowledge-note').length,30);
});
test('metadata continuation stays bounded and keeps an explicit load-more action',async t=>{
 const f=fixture(t,{items:[note('Notes/A.md')],canLoadMore:true});await f.view.ready;assert.equal(f.calls.length,12);assert.equal(f.calls.filter(c=>c.more).length,10);assert.ok(f.calls.every(c=>c.quick&&c.compact));
 const more=f.view.dom.querySelectorAll('button').find(b=>b.textContent==='Load more known files');assert.ok(more);assert.equal(more.disabled,false);
});
test('camera state is captured before renderer disposal and restored through empty graph paint',async t=>{
 const scope={root:'box',path:'Notes'},key=graphViewKey(scope,'notes',null,{history:true}),camera={x:.3,y:.7,angle:.25,ratio:.4},focus='["box","Notes/A.md"]';
 const f=fixture(t,{restore:{graph:{key,camera,focus},graphMode:'notes'}});await f.view.ready;
 let killed=0;f.view.activeGraphKey=key;f.view.neighborFocus=focus;f.view.renderer={getCamera:()=>({getState:()=>camera}),kill:()=>killed++};f.view.render();assert.equal(killed,1);
 const saved=f.view.snapshot();assert.deepEqual(saved.graph,{key,camera,focus});saved.graph.camera.x=9;assert.equal(f.view.snapshot().graph.camera.x,.3);
 f.view.mode='graph';f.view.render();assert.deepEqual(f.view.snapshot().graph.camera,camera);assert.equal(f.view.dom.querySelectorAll('button').filter(b=>['Fit graph','Reset graph','Zoom in','Zoom out'].includes(b.getAttribute('aria-label'))).every(b=>b.disabled),true);
 f.view.destroy();assert.equal(f.listeners.size,0);assert.equal(f.frames.size,0);
});
test('leaving a graph cancels pending initialization rather than retaining a render loop',async t=>{
 const f=fixture(t,{mode:'graph',items:[note('Notes/A.md')]});await f.view.ready;assert.equal(f.frames.size,1);f.view.render();assert.equal(f.frames.size,1);f.view.destroy();assert.equal(f.frames.size,0);
});
