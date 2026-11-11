import test from 'node:test';
import assert from 'node:assert/strict';
import {frontmatter,changeProperty} from '../renderer/frontmatter.js';
import {documentMetrics} from '../renderer/document-metrics.js';
import {sidebarBounds,sidebarWidth} from '../renderer/sidebar-sizing.js';

test('property edits preserve body, unrelated comments, nested YAML and scalar types',()=>{
  const body='\r\n# Research\r\n\r\nExact **body** with [[a link]].\r\n';
  const text='\uFEFF---\r\n# retain this\r\ntype: reference # typed\r\ntags: [physics, study]\r\ncomplete: false\r\nweight: 2\r\nstructured:\r\n  nested: yes\r\n---\r\n'+body;
  const initial=frontmatter(text);assert.equal(initial.fields.length,5);assert.equal(initial.fields.find(f=>f.key==='structured').editable,false);
  let change=changeProperty(text,'complete',true);assert.equal(frontmatter(change.text).body,body);assert.match(change.text,/# retain this/);assert.match(change.text,/# typed/);assert.equal(frontmatter(change.text).fields.find(f=>f.key==='complete').value,true);
  change=changeProperty(change.text,'tags',['physics','[[Topic]]']);assert.equal(frontmatter(change.text).body,body);assert.deepEqual(frontmatter(change.text).fields.find(f=>f.key==='tags').value,['physics','[[Topic]]']);assert.match(change.text,/nested: yes/);
  change=changeProperty(change.text,'type',null,{remove:true});assert.equal(frontmatter(change.text).fields.length,4);assert.equal(frontmatter(change.text).body,body);assert.ok(change.text.startsWith('\uFEFF---\r\n'));
});
test('invalid metadata and aliases are never silently rewritten as simple fields',()=>{
  for(const text of ['---\na: [\n---\nbody','---\na: 1\na: 2\n---\nbody','---\n- a\n---\nbody','---\na: 1'])assert.throws(()=>changeProperty(text,'x','new'));
  const alias='---\na: &a [one]\nb: *a\n---\nbody';assert.equal(frontmatter(alias).fields.find(f=>f.key==='b').editable,false);assert.match(changeProperty(alias,'x','new').text,/b: \*a/);
  assert.throws(()=>changeProperty(alias,'a',['other']));assert.throws(()=>changeProperty(alias,'a',null,{remove:true}));
  assert.throws(()=>changeProperty('plain','x',Infinity));assert.throws(()=>changeProperty('plain','x',{}));
  const added=changeProperty('plain\n','type','note',{add:true});assert.equal(frontmatter(added.text).body,'plain\n');assert.throws(()=>changeProperty(added.text,'type','other',{add:true}));
});
test('document counts exclude frontmatter, links include metadata and ignore literal code',()=>{
  const text='---\nrelated: ["[[Previous]]"]\n---\n# Title\n\n[[Topic|Alias]] [Paper](paper.pdf) `[[literal]]`\n\n```md\n[[not a link]]\n```\n';
  const result=documentMetrics(text);assert.equal(result.properties,1);assert.deepEqual(result.references,['Topic','paper.pdf','Previous']);assert.equal(result.characters,frontmatter(text).body.length);assert.ok(result.words>0);
  assert.deepEqual(documentMetrics(text,{links:false}).references,[]);
});
test('sidebar has an explicit compact minimum and a usable window-relative maximum',()=>{
  assert.equal(sidebarWidth(0,1280),166);assert.equal(sidebarWidth(246,1280),246);assert.equal(sidebarWidth(2000,1280),960);
  assert.equal(sidebarWidth(2000,800,240),336);assert.equal(sidebarBounds(1280).max,960);assert.equal(sidebarWidth(NaN,1280),246);
});
