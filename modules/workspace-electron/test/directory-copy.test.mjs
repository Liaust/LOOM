import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,realpath,readdir,rm,readFile} from 'node:fs/promises';
import {Readable} from 'node:stream';
import path from 'node:path';
import os from 'node:os';
import {createHash} from 'node:crypto';
import {makeDirectoryCopy} from '../src/directory-copy.mjs';

const source={root:'box',path:'Projects/example',revision:'exact'};
const payload=Buffer.from('verified archive fixture');
const summary={schema_version:'loom.directory_export.v1',mode:'portable',project_slug:'directory-snapshot',source_root:source.root,source_path:source.path,source_revision:source.revision,archive_bytes:payload.length,archive_checksum:`sha256:${createHash('sha256').update(payload).digest('hex')}`,included:{count:1,bytes:5},ignored:{count:0,bytes:0}};
async function fixture(t,changes={}){
 const directory=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-copy-')));t.after(()=>rm(directory,{recursive:true,force:true}));
 const remote={async apiRequest(route,query,options){assert.equal(route,'/v1/files/snapshot');assert.equal(options.body.plan_hash,'sha256:'+'a'.repeat(64));const response=Readable.from([payload]);response.statusCode=200;response.headers={'content-length':String(payload.length),'x-loom-project-export-summary':Buffer.from(JSON.stringify({...summary,...changes})).toString('base64url')};return response;}};
 return {directory,options:{remote,helper:'unused',root:directory,rootIdentity:'1:2',target:'new-copy',source,planHash:'sha256:'+'a'.repeat(64),temporaryRoot:directory,verifyDestination:async()=>{}}};
}
test('working copy binds exact source and saves native request before import',async t=>{
 const {directory,options}=await fixture(t);let saved;
 const result=await makeDirectoryCopy({...options,prepared:async value=>{saved=value;assert.deepEqual(await readFile(value.request.archive),payload);},importer:async(helper,root,request)=>{assert.deepEqual(request,saved.request);assert.equal(request.root_identity,'1:2');assert.equal(request.target,'new-copy');return {ok:true};}});
 assert.equal(result.summary.source_path,source.path);assert.deepEqual(await readdir(directory),[]);
});
test('uncertain native import retains the verified snapshot for exact recovery',async t=>{
 const {directory,options}=await fixture(t);let saved;
 await assert.rejects(makeDirectoryCopy({...options,prepared:value=>saved=value,importer:async()=>{throw Error('reply lost');}}),/reply lost/);
 assert.deepEqual(await readFile(saved.request.archive),payload);assert.equal((await readdir(directory)).length,1);
});
test('wrong source identity is rejected before importer or request publication',async t=>{
 const {directory,options}=await fixture(t,{source_path:'another'});let imported=false;
 await assert.rejects(makeDirectoryCopy({...options,importer:async()=>imported=true}));assert.equal(imported,false);assert.deepEqual(await readdir(directory),[]);
});
test('destination drift aborts before native publication',async t=>{
 const {directory,options}=await fixture(t);let checks=0;
 await assert.rejects(makeDirectoryCopy({...options,verifyDestination:async()=>{if(++checks===2)throw Error('parent changed');},importer:async()=>assert.fail('must not import')}),/parent changed/);
 assert.deepEqual(await readdir(directory),[]);
});
