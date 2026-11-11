import test from 'node:test';
import assert from 'node:assert/strict';
import {Dashboard,chronological} from '../src/dashboard.mjs';

test('independent blocks publish before a slower owner, with at most three reads',async()=>{
 let finish;const slow=new Promise(r=>finish=r),events=[],calls=[];
 const d=new Dashboard({api:async route=>{calls.push(route);if(route.endsWith('/protection'))await slow;return {data:{observed_at:'2026-11-02T10:00:00Z'}};}});
 const work=d.read({section:'system',token:'one'},e=>events.push(e));await new Promise(r=>setImmediate(r));
 assert.equal(calls.length,3);assert.equal(events.length,2);finish();assert.equal(Object.keys((await work).blocks).length,3);
});
test('cached sources retain original evidence and failures back off without becoming fresh',async()=>{
 let calls=0,fail=false,now=1_000;const real=Date.now;Date.now=()=>now;
 try{const d=new Dashboard({api:async()=>{calls++;if(fail)throw Error('private failure');return {data:{observed_at:'2026-11-02T10:00:00Z'}};}});
 const first=await d.read({section:'activity',token:'one'});now+=100;assert.equal((await d.read({section:'activity',token:'two'})).blocks.jobs.cached,true);assert.equal(calls,1);
 now+=16000;fail=true;const failed=await d.read({section:'activity',token:'three'});assert.equal(failed.blocks.jobs.stale,true);assert.equal(failed.blocks.jobs.observedAt,first.blocks.jobs.observedAt);
 await d.read({section:'activity',token:'four'});assert.equal(calls,2);assert.ok(!JSON.stringify(failed).includes('private failure'));
 }finally{Date.now=real;}
});
test('cancelled late results never publish into a new page',async()=>{
 let finish;const d=new Dashboard({api:async()=>{await new Promise(r=>finish=r);return {data:{}};}}),events=[];
 const work=d.read({section:'activity',token:'one'},e=>events.push(e));d.cancel();finish();assert.deepEqual((await work).blocks,{});assert.equal(events.length,0);
});
test('filter paging has independent cache keys and chronological merge is owner stable',async()=>{
 const calls=[];const d=new Dashboard({api:async(route,q)=>{calls.push({route,q});return {data:{}};}});
 await d.read({section:'projects',token:'one',state:'passive'});await d.read({section:'projects',token:'two',state:'working'});
 assert.deepEqual(calls.filter(c=>c.route.includes('projects')).map(c=>c.q.state),['passive','working']);
 assert.deepEqual(chronological([{owner:'local',id:'a',at:'2026-01-01'},{owner:'jobs',id:'b',at:'2026-11-01'}]).map(i=>i.id),['b','a']);
 await assert.rejects(d.read({section:'unknown',token:'x'}),/Invalid/);
});
