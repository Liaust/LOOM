import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,symlink,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {navigationIntent} from '../renderer/navigation-intent.js';
import {clipboardEntries,pasteTarget} from '../renderer/file-clipboard.js';
import {editorPreferences,loadEditorPreferences} from '../renderer/editor-preferences.js';
import {workspaceSession} from '../src/session-state.mjs';
import {LocalFiles} from '../src/local-files.mjs';
test('all navigation modifiers share one intent contract',()=>{
 assert.deepEqual(navigationIntent(),{newTab:false,background:false});assert.deepEqual(navigationIntent({metaKey:true}),{newTab:true,background:false});assert.deepEqual(navigationIntent({ctrlKey:true,shiftKey:true}),{newTab:true,background:true});assert.deepEqual(navigationIntent({button:1}),{newTab:true,background:true});
});
test('clipboard refuses root, overlapping and cross-machine cuts before mutation',()=>{
 assert.throws(()=>clipboardEntries([{root:'box',path:''}]));assert.throws(()=>clipboardEntries([{root:'mac',path:'one',directory:true},{root:'mac',path:'one/two.md'}]));assert.throws(()=>pasteTarget({root:'mac',path:'a.md'},{root:'box',path:'Notes'},true));assert.throws(()=>pasteTarget({root:'box',path:'one',directory:true},{root:'box',path:'one/two'},false));assert.deepEqual(pasteTarget({root:'mac',path:'one/a.py'},{root:'mac',path:'one'},false),{path:'one/a copy.py',name:'a copy.py'});
});
test('preferences are bounded and corrupted storage falls back',()=>{
 assert.deepEqual(editorPreferences({fontSize:999,lineHeight:-5,width:1,propertiesCollapsed:'yes'}),{fontSize:26,lineHeight:1.3,width:540,propertiesCollapsed:false});assert.equal(loadEditorPreferences({getItem:()=>'{'}).fontSize,16);
});
test('session preserves pin, typed property grouping and graph camera without arbitrary fields',()=>{
 const result=workspaceSession({version:1,active:'tab_1',tabs:[{id:'tab_1',pinned:true,cursor:0,history:[{kind:'knowledge',root:'box',path:'Notes',view:'graph',saved:{groupBy:'kind',filters:{property:'enabled',propertyValue:false,propertyMissing:true},graph:{key:'same scope',camera:{x:.5,y:.6,ratio:2,angle:0},focus:'node'},page:2}}]}]});assert.equal(result.tabs[0].pinned,true);const page=result.tabs[0].history[0];assert.equal(page.saved.filters.propertyValue,false);assert.equal(page.saved.filters.propertyMissing,true);assert.equal(page.saved.graph.camera.ratio,2);assert.equal(page.saved.groupBy,'kind');
});
test('local hover preview is bounded, tolerates a cut UTF-8 codepoint and refuses links/binary',async()=>{
 const root=await mkdtemp(path.join(os.tmpdir(),'loom-preview-'));try{const files=new LocalFiles(root);await writeFile(path.join(root,'note.md'),'a'.repeat(16383)+'é'+'b'.repeat(20000));const result=await files.preview('note.md');assert.equal(result.text,'a'.repeat(16383));assert.equal(result.truncated,true);await symlink('note.md',path.join(root,'link.md'));await assert.rejects(files.preview('link.md'));await writeFile(path.join(root,'bad.md'),Buffer.from([0,1]));await assert.rejects(files.preview('bad.md'));}finally{await rm(root,{recursive:true,force:true});}
});
