import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,mkdir,rm,readFile,writeFile,realpath,readdir,symlink} from 'node:fs/promises';
import {DatabaseSync} from 'node:sqlite';
import path from 'node:path';
import os from 'node:os';
import {MacBox} from '../src/mac-box.mjs';

const helper=path.resolve('../../.loom-acceptance/workspace-desktop-v1/loom-workspace-fs');
test('Mac Box edits, folder moves and removal are entirely local and retain originals',async t=>{
 const dir=await mkdtemp(path.join(await realpath(os.tmpdir()),'loom-mac-')),root=path.join(dir,'box');await mkdir(root);
 const db=new DatabaseSync(path.join(dir,'state.sqlite')),events=[];t.after(async()=>{db.close();await rm(dir,{recursive:true,force:true});});
 const owner=await new MacBox(root,helper,{db,emit:(type,data)=>events.push({type,...data})}).init();
 await owner.mutate({path:'',operation:'create_folder',name:'local'});
 await owner.mutate({path:'local',operation:'create_note',name:'note.md',text:'Mac-only'});
 const first=await owner.read('local/note.md');await owner.save('local/note.md','new local text',first.base,first.binding_id);
 await owner.mutate({path:'local',operation:'rename',target:'moved'});
 assert.equal((await owner.read('moved/note.md')).binding_id,first.binding_id);
 assert.equal(await readFile(path.join(root,'moved/note.md'),'utf8'),'new local text');
 const reopened=await new MacBox(root,helper,{db}).init();assert.equal((await reopened.read('moved/note.md')).text,'new local text');
 await owner.mutate({path:'moved',operation:'trash'});assert.ok(events.some(e=>e.type==='mac-path'&&e.deleted));
 const journals=await readdir(root);assert.equal(journals.includes('moved'),false);
 let found=false;for(const name of journals)try{if(await readFile(path.join(root,name,'removed/note.md'),'utf8')==='new local text')found=true;}catch{}
 assert.equal(found,true);
});
test('Mac external edits, occupied destinations and links cannot be overwritten',async t=>{
 const root=await mkdtemp(path.join(await realpath(os.tmpdir()),'loom-mac-')),db=new DatabaseSync(':memory:');t.after(async()=>{db.close();await rm(root,{recursive:true,force:true});});
 const owner=await new MacBox(root,helper,{db}).init();await writeFile(path.join(root,'one.py'),'old');const initial=await owner.read('one.py');await writeFile(path.join(root,'one.py'),'external');
 await assert.rejects(()=>owner.save('one.py','draft',initial.base,initial.binding_id),/changed/);
 await assert.rejects(()=>owner.mutate({path:'',operation:'create_note',name:'one.py',text:'overwrite'}),/exists/);
 await symlink(root,path.join(root,'linked'));await assert.rejects(()=>owner.mutate({path:'linked',operation:'create_note',name:'bad.md'}),/links/);
 assert.equal(await readFile(path.join(root,'one.py'),'utf8'),'external');
});
test('interrupted local move bookkeeping rolls back all bindings and recovers the moved inode on restart',async t=>{
 const root=await mkdtemp(path.join(await realpath(os.tmpdir()),'loom-mac-')),db=new DatabaseSync(':memory:');t.after(async()=>{db.close();await rm(root,{recursive:true,force:true});});
 const owner=await new MacBox(root,helper,{db}).init();t.after(()=>owner.close());
 await owner.mutate({path:'',operation:'create_folder',name:'source'});
 await owner.mutate({path:'source',operation:'create_note',name:'a.md',text:'a'});await owner.mutate({path:'source',operation:'create_note',name:'b.md',text:'b'});
 const a=await owner.read('source/a.md'),b=await owner.read('source/b.md');
 db.exec("CREATE TRIGGER fail_local_handover BEFORE INSERT ON mac_files WHEN NEW.path='target/b.md' BEGIN SELECT RAISE(ABORT,'interrupted handover'); END;");
 await assert.rejects(()=>owner.mutate({path:'source',operation:'rename',target:'target'}),/interrupted handover/);
 assert.ok(db.prepare('SELECT record FROM mac_files WHERE path=?').get('source/a.md'));assert.equal(db.prepare('SELECT record FROM mac_files WHERE path=?').get('target/a.md'),undefined);
 db.exec('DROP TRIGGER fail_local_handover');const restarted=await new MacBox(root,helper,{db}).init();t.after(()=>restarted.close());
 assert.equal((await restarted.read('target/a.md')).binding_id,a.binding_id);assert.equal((await restarted.read('target/b.md')).binding_id,b.binding_id);
});
