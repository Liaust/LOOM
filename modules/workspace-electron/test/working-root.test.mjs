import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,mkdir,writeFile,readFile,rename,lstat,rm,symlink} from 'node:fs/promises';
import {DatabaseSync} from 'node:sqlite';
import path from 'node:path';
import os from 'node:os';
import {relocateWorkingRoot,recoveryWorkingRoot,locationFile} from '../src/working-root.mjs';
import {stageNativeRecovery} from '../src/native-recovery.mjs';

async function fixture(t){const home=await mkdtemp(path.join(await import('node:fs/promises').then(m=>m.realpath(os.tmpdir())),'loom-root-')),data=path.join(home,'app'),old=path.join(data,'native-client/working-set'),target=path.join(home,'loom-main');await mkdir(path.join(old,'box/Notes/empty'),{recursive:true});await writeFile(path.join(old,'box/Notes/note.md'),'unsent exact bytes');await writeFile(path.join(data,'drafts.json'),'pending draft');t.after(()=>rm(home,{recursive:true,force:true}));return{home,data,old,target,move:async(root,from,to)=>rename(path.join(root,from),path.join(root,to))};}
test('root relocation preserves inode, logical paths, drafts and replay identity',async t=>{
 const f=await fixture(t),before=await lstat(f.old);assert.equal(await relocateWorkingRoot(f.data,f.target,f),f.target);assert.equal((await lstat(f.target)).ino,before.ino);
 assert.equal(await readFile(path.join(f.target,'box/Notes/note.md'),'utf8'),'unsent exact bytes');assert.equal(await readFile(path.join(f.data,'drafts.json'),'utf8'),'pending draft');
 assert.equal(await relocateWorkingRoot(f.data,f.target,f),f.target);assert.equal(await recoveryWorkingRoot(f.data),f.target);assert.equal((await lstat(f.old)).isFile(),true);
});
test('occupied or symlink destination never merges or rewrites either root',async t=>{
 const f=await fixture(t);await mkdir(f.target);await assert.rejects(()=>relocateWorkingRoot(f.data,f.target,f),/already exists/);await rm(f.target,{recursive:true});await symlink(f.old,f.target);await assert.rejects(()=>relocateWorkingRoot(f.data,f.target,f),/already exists/);assert.equal(await readFile(path.join(f.old,'box/Notes/note.md'),'utf8'),'unsent exact bytes');
});
test('interrupted prepared move replays only the exact moved inode',async t=>{
 const f=await fixture(t);await assert.rejects(()=>relocateWorkingRoot(f.data,f.target,{...f,move:async(...args)=>{await f.move(...args);throw Error('interrupted')}}),/interrupted/);
 assert.equal(JSON.parse(await readFile(path.join(f.data,locationFile),'utf8')).state,'prepared');assert.equal(await relocateWorkingRoot(f.data,f.target,f),f.target);
 await rename(f.target,f.target+'-held');await mkdir(f.target);await assert.rejects(()=>relocateWorkingRoot(f.data,f.target,f),/replaced/);
});
test('mapped native capture preserves pending SQLite and reads external files into the old archive namespace',async t=>{
 const f=await fixture(t);await relocateWorkingRoot(f.data,f.target,f);
 const db=new DatabaseSync(path.join(f.data,'native-client/notes.sqlite'));db.exec("CREATE TABLE operations(id TEXT,state TEXT);INSERT INTO operations VALUES('exact-id','queued');");t.after(()=>db.close());
 const staged=await stageNativeRecovery(f.data,db);t.after(()=>rm(staged.temporary,{recursive:true,force:true}));
 assert.equal(await readFile(path.join(staged.workspace,'native-client/working-set/box/Notes/note.md'),'utf8'),'unsent exact bytes');
 assert.equal(JSON.parse(await readFile(path.join(staged.workspace,locationFile),'utf8')).path,f.target);
 const saved=new DatabaseSync(path.join(staged.workspace,'native-client/notes.sqlite'));assert.equal(saved.prepare('SELECT id FROM operations').get().id,'exact-id');saved.close();
});
