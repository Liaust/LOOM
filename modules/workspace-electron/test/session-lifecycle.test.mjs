import test from 'node:test';
import assert from 'node:assert/strict';
import {EventEmitter} from 'node:events';
import {workspaceSession,saveWorkspaceSession,loadWorkspaceSession} from '../src/session-state.mjs';
import {DocumentTabs} from '../renderer/tabs.js';
import {WindowLifecycle} from '../src/window-lifecycle.mjs';

test('sessions preserve safe references/history/selection but no bodies or receipts',()=>{
 const tabs=new DocumentTabs();tabs.visit({kind:'file',root:'box',path:'Notes/A.md',selection:[2,4,80,'forward'],text:'private content'});tabs.visit({kind:'timeline',root:'box',path:'Notes',saved:{kind:'edit',from:'2026-11-01',pages:[{secret:'not session state'}],result:{body:'not state'},scroll:50}});
 const s=workspaceSession({version:1,tabs:[...tabs.slots].map(([id,slot])=>({id,...slot})),active:tabs.activeTab,section:'files'}),text=JSON.stringify(s);assert.doesNotMatch(text,/private|secret|body|receipt/);assert.equal(s.tabs[0].history[1].selection[2],80);assert.equal(s.tabs[0].history[2].saved.kind,'edit');
 const restored=new DocumentTabs();restored.restore(s);assert.equal(restored.page().kind,'timeline');assert.equal(restored.target(-1).path,'Notes/A.md');assert.equal(restored.items.size,0);assert.notEqual(restored.newTab(),s.active);
 assert.equal(workspaceSession({version:1,tabs:[{id:'tab_1',history:[{kind:'file',root:'box',path:'../escape'}],cursor:0}]}),null);
 const storage=new Map(),api={getItem:k=>storage.get(k),setItem:(k,v)=>storage.set(k,v)};assert.equal(saveWorkspaceSession(api,s),true);assert.equal(loadWorkspaceSession(api).tabs.length,1);
});
class FakeWindow extends EventEmitter{
 isDestroyed(){return !!this.destroyed;}show(){this.shown=true;}focus(){this.focused=true;}
 close(){let stopped=false;this.emit('close',{preventDefault(){stopped=true;}});if(!stopped){this.destroyed=true;this.emit('closed');}}
}
test('Mac window reopening reuses the runtime and coalesces concurrent activation',async()=>{
 let created=0,flushed=0;const owner=new WindowLifecycle({create:()=>{created++;return new FakeWindow();},flush:async()=>{flushed++;}});
 const [a,b]=await Promise.all([owner.open(),owner.open()]);assert.equal(a,b);assert.equal(created,1);await owner.open();assert.equal(a.focused,true);a.close();await new Promise(setImmediate);assert.equal(a.destroyed,true);assert.equal(flushed,1);
 const reopened=await owner.open();assert.notEqual(reopened,a);assert.equal(created,2);
});
test('failed local flush prevents window loss and can retry without duplicating close work',async()=>{
 let fail=true,errors=0;const owner=new WindowLifecycle({create:()=>new FakeWindow(),flush:async()=>{if(fail)throw Error('disk unavailable');},failed:()=>errors++});const w=await owner.open();w.close();w.close();await new Promise(setImmediate);assert.equal(w.isDestroyed(),false);assert.equal(errors,1);fail=false;w.close();await new Promise(setImmediate);assert.equal(w.isDestroyed(),true);
});
