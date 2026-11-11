import test from 'node:test';
import assert from 'node:assert/strict';
import {FileTree} from '../renderer/file-tree.js';

// Same bounded element surface used by the existing tree-selection tests.
function element(tag) {
  const attributes=new Map(),node={tagName:tag.toUpperCase(),dataset:{},style:{},children:[],scrollTop:0,className:'',tabIndex:-1,
    append(...children){for(const child of children){child.parentElement=this;this.children.push(child);}},
    replaceChildren(...children){this.children=[];this.append(...children);},
    setAttribute(name,value){attributes.set(name,String(value));if(name==='tabindex')this.tabIndex=Number(value);},
    getAttribute(name){return attributes.get(name)??null;},
    addEventListener(name,callback){this['listener_'+name]=callback;},
    contains(target){return this===target||this.children.some(child=>child.contains(target));},
    matches(selector){return selector==='[role=treeitem]'?this.getAttribute('role')==='treeitem':selector.startsWith('.')?this.className.split(' ').includes(selector.slice(1)):selector==='input,textarea,select,[contenteditable="true"]'&&['INPUT','TEXTAREA','SELECT'].includes(this.tagName);},
    closest(selector){return this.matches(selector)?this:this.parentElement?.closest(selector);},
    querySelectorAll(selector){return this.children.flatMap(child=>[...(child.matches(selector)?[child]:[]),...child.querySelectorAll(selector)]);},
    querySelector(selector){return this.querySelectorAll(selector)[0]||null;},
    focus(){document.activeElement=this;},scrollIntoView(){},
  };
  node.classList={toggle(name,active){const values=new Set(node.className.split(' ').filter(Boolean));if(active)values.add(name);else values.delete(name);node.className=[...values].join(' ');},add(name){this.toggle(name,true);},remove(name){this.toggle(name,false);}};
  return node;
}
const file=name=>({name,relative_path:name,kind:'file'}),folder=name=>({...file(name),kind:'directory'});
const event=(target,extra={})=>({target,key:'',preventDefault(){this.defaultPrevented=true;},stopPropagation(){},...extra});
function fixture(t,{entries=[],loader=async()=>{throw Error('Filter must not load directories');},hideReadmeAgents=false}={}){
  const previous=globalThis.document;
  globalThis.document={createElement:element,activeElement:element('body')};
  t.after(()=>{if(previous===undefined)delete globalThis.document;else globalThis.document=previous;});
  const container=element('div'),commands=[],opens=[];let selected;
  const tree=new FileTree(container,{loader,roots:()=>[{ref:'box',label:'Personal'}],selected:()=>selected,openFile:(...args)=>opens.push(args),command:(...args)=>commands.push(args),icon:()=>element('span'),icons(){},hideReadmeAgents});
  tree.model.remember('box','',{entries,has_more:false});tree.model.expanded.add(tree.model.key('box',''));tree.render();
  const rows=()=>container.querySelectorAll('[role=treeitem]'),row=path=>rows().find(value=>value.dataset.path===path);
  return {tree,container,rows,row,commands,opens,setSelected(value){selected=value;}};
}

test('filter is opt-in, matches exact file names case-insensitively and retains same-named directories',t=>{
  const entries=['README.md','readme.MD','AGENTS.md','aGeNtS.Md','README.md.bak','MY_README.md','README','AGENTS.markdown',' README.md','README.md ','README.md\n'].map(file);
  entries.push({...folder('README.md'),relative_path:'readme-directory'},{...folder('AGENTS.md'),relative_path:'agents-directory'});
  const f=fixture(t,{entries}),page=f.tree.model.branch('box').page,unfiltered=f.rows().length;
  assert.equal(unfiltered,entries.length+1);
  f.tree.setHideReadmeAgents(true);
  for(const name of ['README.md','readme.MD','AGENTS.md','aGeNtS.Md'])assert.equal(f.row(name),undefined);
  for(const name of ['README.md.bak','MY_README.md','README','AGENTS.markdown',' README.md','README.md ','README.md\n','readme-directory','agents-directory'])assert.ok(f.row(name),name);
  assert.equal(f.tree.model.branch('box').page,page);assert.equal(page.entries.length,entries.length);
  f.tree.setHideReadmeAgents(false);assert.equal(f.rows().length,unfiltered);assert.equal(f.opens.length,0);
});

test('constructor flag hides nested files without hiding folders or altering branch membership',t=>{
  const f=fixture(t,{entries:[folder('Notes')],hideReadmeAgents:true});
  f.tree.model.remember('box','Notes',{entries:[{...file('rEaDmE.Md'),relative_path:'Notes/rEaDmE.Md'},{...file('kept.md'),relative_path:'Notes/kept.md'}],has_more:false});
  f.tree.model.expanded.add(f.tree.model.key('box','Notes'));f.tree.render();
  assert.ok(f.row('Notes'));assert.ok(f.row('Notes/kept.md'));assert.equal(f.row('Notes/rEaDmE.Md'),undefined);
  assert.equal(f.tree.model.branch('box','Notes').page.entries.length,2);
});

test('hidden-only pages retain their cursor and Load more reaches visible later entries',async t=>{
  const calls=[],f=fixture(t,{hideReadmeAgents:true,loader:async input=>{calls.push(input);return input.cursor?{entries:[file('kept.md')],has_more:false}:{entries:[file('README.md')],has_more:true,next_cursor:'exact-page-two'};}});
  await f.tree.load('box','',{refresh:true});
  const page=f.tree.model.branch('box').page;
  assert.equal(page.entries.length,1);assert.equal(page.next_cursor,'exact-page-two');
  assert.ok(f.container.querySelector('.tree-more'));
  assert.equal(f.container.querySelectorAll('.tree-message').some(node=>['Empty folder','No visible files'].includes(node.textContent)),false);
  await f.container.querySelector('.tree-more').onclick();
  assert.equal(calls[1].cursor,'exact-page-two');assert.ok(f.row('kept.md'));assert.equal(f.row('README.md'),undefined);
  assert.equal(f.tree.model.branch('box').page.entries.length,2);
  f.tree.setHideReadmeAgents(false);assert.ok(f.row('README.md'));assert.equal(calls.length,2);
});

test('filter prunes selection and keyboard ranges/commands use only visible rows without closing the current file',t=>{
  const f=fixture(t,{entries:[file('a.md'),file('README.md'),file('z.md')]});
  f.setSelected({root:'box',path:'README.md',directory:false});f.tree.render();
  f.tree.selectRow(f.row('a.md'),{});f.tree.selectRow(f.row('z.md'),{shiftKey:true});
  assert.equal(f.tree.selectedEntries().length,3);
  const old=f.row('README.md');old.focus();f.tree.setHideReadmeAgents(true);
  assert.equal(f.tree.selectedEntries().some(entry=>entry.path==='README.md'),false);
  assert.notEqual(document.activeElement,old);assert.equal(f.container.contains(document.activeElement),true);
  f.tree.selectRow(f.row('a.md'),{});f.tree.keydown(event(f.row('a.md'),{key:'ArrowDown',shiftKey:true}));
  assert.deepEqual(f.tree.selectedEntries().map(entry=>entry.path),['a.md','z.md']);
  assert.equal(document.activeElement.dataset.path,'z.md');
  f.tree.keydown(event(f.row('z.md'),{key:'c',ctrlKey:true}));
  assert.deepEqual(f.commands[0][1].map(entry=>entry.path),['a.md','z.md']);
  f.tree.keydown(event(old,{key:'Enter'}));assert.deepEqual(f.opens,[]);
  f.tree.setHideReadmeAgents(false);assert.ok(f.row('README.md'));assert.deepEqual(f.opens,[]);
});

test('hidden-only completed folders are not described as empty, while actual empty folders remain empty',t=>{
  const f=fixture(t,{entries:[file('README.md'),file('AGENTS.md')],hideReadmeAgents:true});
  assert.equal(f.container.querySelector('.tree-message').textContent,'No visible files');
  f.tree.model.remember('box','',{entries:[],has_more:false});f.tree.render();
  assert.equal(f.container.querySelector('.tree-message').textContent,'Empty folder');
});
