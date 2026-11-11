import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,mkdir,rm,writeFile,readFile,lstat} from 'node:fs/promises';
import {DatabaseSync} from 'node:sqlite';
import path from 'node:path';
import os from 'node:os';
import {stageNativeRecovery} from '../src/native-recovery.mjs';
import {BoxRoots} from '../src/box-roots.mjs';

test('native capture preserves committed WAL, pending history, drafts and empty directories without the cache',async t=>{
  const root=await mkdtemp(path.join(os.tmpdir(),'loom-native-recovery-'));await mkdir(path.join(root,'native-client/working-set/box/empty'),{recursive:true});await mkdir(path.join(root,'file-cache'));
  const db=new DatabaseSync(path.join(root,'native-client/notes.sqlite'));db.exec("PRAGMA journal_mode=WAL;CREATE TABLE operations(id TEXT,state TEXT);INSERT INTO operations VALUES('pending_1','queued');");
  t.after(async()=>{db.close();await rm(root,{recursive:true,force:true});});
  await writeFile(path.join(root,'drafts.json'),'[["local","unsent draft"]]');await writeFile(path.join(root,'file-cache/rebuildable.bin'),'cache');
  const catalog=[{box_id:'box_personal',box_key:'personal',root_ref:'box',primary:true},{box_id:'box_company',box_key:'company',root_ref:'box:box_company'}];
  await writeFile(path.join(root,'boxes.json'),JSON.stringify(catalog));
  await mkdir(path.join(root,'native-client/working-set/boxes/company'),{recursive:true});
  await writeFile(path.join(root,'native-client/working-set/boxes/company/note.md'),'Company unsent bytes');
  db.exec("INSERT INTO operations VALUES('pending_company','queued');");
  const staged=await stageNativeRecovery(root,db);t.after(()=>rm(staged.temporary,{recursive:true,force:true}));
  const restored=new DatabaseSync(path.join(staged.workspace,'native-client/notes.sqlite'));assert.equal(restored.prepare('SELECT state FROM operations').get().state,'queued');assert.equal(restored.prepare('SELECT COUNT(*) AS n FROM operations').get().n,2);assert.equal(restored.prepare('PRAGMA journal_mode').get().journal_mode,'delete');restored.close();
  const boxes=new BoxRoots(JSON.parse(await readFile(path.join(staged.workspace,'boxes.json'),'utf8')));
  assert.equal(boxes.localPath('box:box_company','note.md'),'boxes/company/note.md');
  assert.equal(await readFile(path.join(staged.workspace,'native-client/working-set/boxes/company/note.md'),'utf8'),'Company unsent bytes');
  assert.match(await readFile(path.join(staged.workspace,'drafts.json'),'utf8'),/unsent/);
  assert.equal((await lstat(path.join(staged.workspace,'native-client/working-set/box/empty'))).isDirectory(),true);
  await assert.rejects(()=>lstat(path.join(staged.workspace,'file-cache')),/ENOENT/);assert.ok((await lstat(await staged.archive())).size>0);
});
