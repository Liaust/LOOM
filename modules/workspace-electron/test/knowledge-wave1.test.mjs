import test from 'node:test';
import assert from 'node:assert/strict';
import {ExplorerFocus} from '../renderer/explorer-focus.js';
import {DirectoryTree} from '../renderer/tree-model.js';
import {CsvModel} from '../src/csv-model.mjs';
import {CanvasModel,canvasPlacement} from '../src/canvas-model.mjs';

test('folder focus retains global state and does not fetch the global root',async()=>{
  const storage=new Map(),owner=new ExplorerFocus({getItem:k=>storage.get(k),setItem:(k,v)=>storage.set(k,v)}),calls=[];
  const tree=new DirectoryTree(async({root,path})=>{calls.push(path);return{entries:[]};}),container={scrollTop:120};tree.expanded.add(tree.key('box','Notes'));
  owner.switch('focus',tree,container,{root:'box',path:'Projects/Physics'});await tree.reveal('box','Projects/Physics/notes','Projects/Physics');
  assert.deepEqual(calls,['Projects/Physics','Projects/Physics/notes']);assert.equal(owner.contains('box','Notes'),false);owner.moved('box','Projects/Physics','Projects/Research');assert.equal(owner.scope.path,'Projects/Research');
  owner.switch('explorer',tree,container);assert.equal(tree.isExpanded('box','Notes'),true);assert.equal(container.scrollTop,120);
});
test('CSV editing preserves strings, quoted newlines, delimiter and row identities under filtering',()=>{
  const m=new CsvModel('name;code;note\r\nAda;001;"a\r\nb"\r\nBob;002;plain\r\n');
  assert.equal(m.delimiter,';');assert.deepEqual(m.indices({header:true,query:'Bob'}),[2]);m.set(2,2,'new; value');
  const round=new CsvModel(m.text());assert.equal(round.rows[1][1],'001');assert.equal(round.rows[1][2],'a\r\nb');assert.equal(round.rows[2][2],'new; value');assert.equal(m.text().endsWith('\r\n'),true);
  assert.equal(m.undo(),true);assert.equal(m.rows[2][2],'plain');assert.equal(m.redo(),true);assert.equal(m.rows[2][2],'new; value');assert.throws(()=>new CsvModel('a,"unclosed'),/CSV/);
  m.setDelimiter(',');assert.equal(new CsvModel(m.text()).rows[2][2],'new; value');m.undo();assert.equal(m.delimiter,';');m.redo();assert.equal(m.delimiter,',');
});
test('Canvas preserves unknown fields and stable IDs, supports undo and validates geometry',()=>{
  const m=new CanvasModel('{"nodes":[],"edges":[],"custom":{"keep":true}}');const a=m.add('text',0,0,'One'),b=m.add('text',400,0,'Two');m.connect(a,b);m.update(a,{x:10});
  assert.equal(m.data.custom.keep,true);assert.equal(m.data.edges[0].fromNode,a);m.remove(a);assert.equal(m.data.edges.length,0);m.undo();assert.equal(m.data.edges.length,1);assert.equal(m.data.nodes.find(n=>n.id===a).x,10);
  assert.throws(()=>m.update(a,{width:-1}),/Invalid/);assert.equal(m.data.nodes.find(n=>n.id===a).width,300);assert.deepEqual(new CanvasModel(m.text()).data,m.data);
});
test('Canvas additions find free space instead of hiding the existing card',()=>{
 const nodes=[{type:'text',x:0,y:0,width:300,height:180},{type:'text',x:324,y:0,width:300,height:180}];
 const p=canvasPlacement(nodes,0,0);assert.ok(nodes.every(n=>p.x+300<=n.x||p.x>=n.x+n.width||p.y+180<=n.y||p.y>=n.y+n.height));
 assert.deepEqual(canvasPlacement([{type:'group',x:0,y:0,width:1000,height:1000}],0,0),{x:0,y:0});
});
test('CSV column extent includes late wider rows and survives edits and undo',()=>{
 const m=new CsvModel('a\n'+'1\n'.repeat(1100)+'2,3,004\n');assert.equal(m.columnCount,3);m.addRow();assert.equal(m.rows.at(-1).length,3);const before=m.revision;m.set(0,4,'late');assert.equal(m.columnCount,5);assert.ok(m.revision>before);m.undo();assert.equal(m.columnCount,3);
 m.rows=Array.from({length:100000},()=>['']);assert.throws(()=>m.addRow(),/100,000/);
});
