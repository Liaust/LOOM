import test from 'node:test';
import assert from 'node:assert/strict';
import {documentTemplate,documentName,quickOpenPath} from '../renderer/file-commands.js';
import YAML from 'yaml';

test('empty-view actions create valid Markdown, Base and Canvas source templates',()=>{
  assert.deepEqual(documentTemplate('note'),{name:'Untitled.md',extension:'md',text:''});
  assert.equal(YAML.parse(documentTemplate('base').text).views[0].type,'table');
  assert.deepEqual(JSON.parse(documentTemplate('canvas').text),{nodes:[],edges:[]});
  assert.equal(documentName('Research','base'),'Research.base');assert.equal(documentName('Diagram','canvas'),'Diagram.canvas');
  assert.equal(documentName('explicit.py','note'),'explicit.py');assert.throws(()=>documentTemplate('unknown'));
});
test('quick opening accepts exposed roots or exact paths but never guesses unknown absolute roots',()=>{
  const roots=[{ref:'box',label:'Box',source_path:'/srv/loom/box'},{ref:'storage',label:'Storage',source_path:'/srv/loom/storage'}];
  assert.deepEqual(quickOpenPath('Box/Notes/first.md',roots),{root:'box',path:'Notes/first.md'});
  assert.deepEqual(quickOpenPath('/srv/loom/storage/archive/paper.pdf',roots),{root:'storage',path:'archive/paper.pdf'});
  assert.deepEqual(quickOpenPath('Notes/first.md',roots),{root:'box',path:'Notes/first.md'});
  for(const value of ['/etc/passwd','Notes/../first.md','Notes//first.md','name-only','Notes/'])assert.equal(quickOpenPath(value,roots),null);
});
