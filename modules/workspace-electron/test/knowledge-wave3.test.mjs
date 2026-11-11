import test from 'node:test';
import assert from 'node:assert/strict';
import {WorkspaceHistory,historyRange} from '../src/history.mjs';
import {diffLines} from 'diff';
test('history keeps local pending separate, reuses bounded cache and never performs writes',async()=>{
 const cache=new Map(),calls=[];let offline=false;const history=new WorkspaceHistory({async json(endpoint,input){calls.push(input);if(offline)throw Error('offline');assert.equal(endpoint,'history');return{data:{items:[{id:'one',state:'accepted'}],coverage:'Source receipts',next:'next'}};}},{pendingHistory:()=>[{id:'local',state:'local_queued'}]},{remember:(k,v)=>cache.set(k,v),cached:k=>cache.get(k)});
 const result=await history.read({root:'box',path:'Notes'});assert.deepEqual(result.items.map(i=>i.state),['local_queued','accepted']);offline=true;const cached=await history.read({root:'box',path:'Notes'});assert.equal(cached.origin,'cached');assert.equal(cached.error,'offline');offline=false;
 const next=await history.read({root:'box',path:'Notes',after:'next'});assert.equal(next.items.length,1);assert.equal(calls[2].limit,100);assert.equal(calls[2].after,'next');
});
test('bounded retained text diff reports exact additions and removals',()=>{const diff=diffLines('one\ntwo\n','one\nthree\n',{timeout:100,maxEditLength:2000});assert.equal(diff.find(p=>p.removed).value,'two\n');assert.equal(diff.find(p=>p.added).value,'three\n');});
test('inclusive timeline days follow local calendar boundaries through daylight saving',()=>{
 const previous=process.env.TZ;try{process.env.TZ='Europe/Amsterdam';const range=historyRange('2026-03-29','2026-03-29');assert.equal(Date.parse(range.to)-Date.parse(range.from),23*3600000);assert.throws(()=>historyRange('2026-03-30','2026-03-29'),/end date/);assert.throws(()=>historyRange('2026-02-30'),/valid/);}finally{if(previous===undefined)delete process.env.TZ;else process.env.TZ=previous;}
});
