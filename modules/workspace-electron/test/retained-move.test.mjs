import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {Operations} from '../src/operations.mjs';

test('retained move saves the exact review and recovers a lost reply without replanning',async t=>{
  const db=new DatabaseSync(':memory:');t.after(()=>db.close());const calls=[];let lose=true;
  const review={id:'retained_one',plan_hash:'exact',source:'imports/old/material',destination:'Projects/material',inventory:'sha256:inventory',state:'planned',entries:2,bytes:3};
  const remote={async api(route,q,{body}){calls.push(body);if(body.action==='plan')return {data:review};if(lose){lose=false;throw Object.assign(Error('lost reply'),{receipt:{...review,state:'prepared'}});}return {data:{...review,state:'complete'}};}};
  const ops=new Operations(remote,{db}),plan=await ops.action({kind:'retained-preview',from:review.source,to:review.destination});
  assert.equal(plan.id,'retained_one');assert.equal(calls.length,1);
  const held=await ops.action({kind:'retained-apply',id:plan.id});assert.equal(held.state,'needs_review');assert.equal(held.receipt.state,'prepared');
  const restarted=new Operations(remote,{db}),result=await restarted.action({kind:'retained-apply',id:plan.id});assert.equal(result.state,'complete');assert.deepEqual(calls[1],calls[2]);
  await restarted.action({kind:'retained-apply',id:plan.id});assert.equal(calls.length,3);assert.equal(calls.filter(c=>c.action==='plan').length,1);
});

test('cancellation recovers a lost reply, preserves the review and never revives a cancelled move',async t=>{
  const db=new DatabaseSync(':memory:');t.after(()=>db.close());const calls=[];let lost=true;
  const review={id:'cancel_one',plan_hash:'exact',source:'imports/material',destination:'Projects/material',inventory:'digest',state:'planned'};
  const remote={async api(route,q,{body}){calls.push(body);if(body.action==='plan')return {data:review};if(body.action==='cancel'&&lost){lost=false;throw Error('lost cancellation reply');}return {data:{...review,state:'cancelled'}};}};
  const ops=new Operations(remote,{db});await ops.action({kind:'retained-preview',from:review.source,to:review.destination});
  assert.equal((await ops.action({kind:'retained-cancel',id:review.id})).state,'needs_review');
  const restarted=new Operations(remote,{db});assert.equal((await restarted.action({kind:'retained-inspect',id:review.id})).state,'cancelled');
  await restarted.action({kind:'retained-apply',id:review.id});assert.equal(calls.length,3);assert.equal(calls[2].action,'status');assert.equal(calls.filter(c=>c.action==='apply').length,0);
  assert.equal((await restarted.action({kind:'retained-list'}))[0].plan_hash,'exact');
});
