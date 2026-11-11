import test from 'node:test';
import assert from 'node:assert/strict';
import { resolveReference, referenceParts } from '../src/note-navigation.mjs';
import { queryBase, compileExpression, yamlData } from '../src/base-model.mjs';

const records = [
  {root:'box',path:'Notes/one.md',text:'---\ntags: [physics]\nkind: person\nrank: 2\n---\n# One'},
  {root:'box',path:'Projects/two.md',text:'---\ntags: [physics/qm]\nkind: person\nrank: 1\n---\n[[one]]'},
];
const owner = {roots:[{ref:'box',source_path:'/srv/loom/box'}],records,stat:async(root,p)=>{
  if(!records.some(r=>r.path===p)) throw Object.assign(Error('Missing'),{status:404});
  return {kind:'file',relative_path:p};
}};
test('references use exact siblings, aliases, headings and exposed absolute paths',async()=>{
  assert.deepEqual(await resolveReference({root:'box',from:'Notes/readme.md',reference:'[[one#One|label]]'},owner),{root:'box',path:'Notes/one.md',heading:'One'});
  assert.equal((await resolveReference({root:'box',from:'Notes/readme.md',reference:'/srv/loom/box/Projects/two.md'},owner)).path,'Projects/two.md');
  assert.equal(referenceParts('https://example.com').external,'https://example.com/');
  await assert.rejects(resolveReference({root:'box',from:'Notes/readme.md',reference:'../../private.md'},owner),/leaves/);
  assert.throws(()=>referenceParts('javascript:alert(1)'),/scheme/);
});
test('ambiguous titles require explicit choice; unavailable owner is not replaced by search',async()=>{
  const ambiguous={...owner,records:[...records,{...records[0],path:'Other/one.md'}]};
  assert.equal((await resolveReference({root:'box',from:'Library/readme.md',reference:'one'},ambiguous)).choices.length,2);
  await assert.rejects(resolveReference({root:'box',from:'Notes/x.md',reference:'one'},{...owner,stat:async()=>{throw Object.assign(Error('Offline'),{status:503});}}),/Offline/);
});
test('Bases filter, formulas, hidden sort columns, paging and partial inventory qualification',()=>{
  const result=queryBase(`filters:\n  and:\n    - 'file.hasTag("physics")'\n    - 'kind == "person"'\nformulas:\n  score: rank * 2\nviews:\n  - type: table\n    name: People\n    order: [file.name, formula.score]\n    sort:\n      - property: rank\n        direction: ASC\n`,records,{limit:1});
  assert.equal(result.total,2);assert.equal(result.rows[0].path,'Projects/two.md');
  assert.equal(result.rows[0].values['formula.score'],2);assert.equal(result.hasMore,true);
  assert.match(result.qualification,/not a complete inventory/);
});
test('Base source never executes JavaScript or object prototypes',()=>{
  assert.throws(()=>compileExpression('file.constructor("return process")()')({file:{},note:{}}),/Unsafe|unsupported/);
  assert.throws(()=>compileExpression('fetch("https://example.com")')({file:{},note:{}}),/unsupported/);
  assert.throws(()=>yamlData('x: 1\nx: 2'),/unique/);
  assert.throws(()=>queryBase('views: [{type: map}]',records),/not supported/);
});
