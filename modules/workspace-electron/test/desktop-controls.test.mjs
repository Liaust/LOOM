import test from 'node:test';
import assert from 'node:assert/strict';
import {convertProperty} from '../renderer/property-types.js';
import {frontmatter,changeProperty} from '../renderer/frontmatter.js';
import {relativeTime} from '../renderer/sections.js';

test('property conversion rejects ambiguous, destructive and out-of-range values',()=>{
  for(const value of ['',null,true,'false','2px','Infinity','9007199254740993'])assert.throws(()=>convertProperty(value,'number'));
  for(const value of ['yes','TRUE',0,1,null])assert.throws(()=>convertProperty(value,'boolean'));
  assert.throws(()=>convertProperty(['one','two'],'text'));
  assert.equal(convertProperty('12.5','number'),12.5);assert.equal(convertProperty('0','number'),0);
  assert.equal(convertProperty('false','boolean'),false);assert.equal(convertProperty('true','boolean'),true);
  assert.deepEqual(convertProperty('one','list'),['one']);assert.deepEqual(convertProperty('','list'),[]);
});
test('typed property changes preserve comments, body, line endings and exact YAML types',()=>{
  const original='---\r\n# Keep this explanation\r\nstatus: "true" # field comment\r\ntags: [a, b]\r\n---\r\n\r\nExact body [[Link]] #tag\r\n';
  const result=changeProperty(original,'status',convertProperty('true','boolean')).text;
  assert.equal(frontmatter(result).fields.find(field=>field.key==='status').type,'boolean');
  assert.equal(frontmatter(result).body,frontmatter(original).body);assert.match(result,/# Keep this explanation/);assert.match(result,/# field comment/);assert.doesNotMatch(result,/(?<!\r)\n/);
});
test('relative presentation retains useful past/future meaning without an idle timer',()=>{
  const now=Date.parse('2026-11-02T12:00:00Z');assert.match(relativeTime('2026-11-02T11:58:00Z',now),/2 minutes ago/);assert.match(relativeTime('2026-11-02T12:05:00Z',now),/in 5 minutes/);assert.equal(relativeTime(null,now),'—');assert.equal(relativeTime('not-a-date',now),'—');
});
