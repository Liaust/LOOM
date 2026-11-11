import path from 'node:path';
import {lstat,readdir,realpath} from 'node:fs/promises';
import {randomUUID} from 'node:crypto';

export const sourceFingerprint=s=>[s.dev,s.ino,s.size,s.mtimeNs,s.ctimeNs].map(String).join(':');
export const excludedCopyName=name=>name.startsWith('.')||['node_modules','vendor','__pycache__','target','dist','build','result'].includes(name);
export async function copySnapshot(source){
 const entries={};let count=0,bytes=0;
 const visit=async file=>{
  if(++count>10000)throw Error('This copy exceeds 10,000 entries. Choose a smaller folder.');
  const s=await lstat(file,{bigint:true});if(s.isSymbolicLink()||await realpath(file)!==file||!s.isFile()&&!s.isDirectory())throw Error('Copies cannot include symbolic links or special files.');
  if(s.isFile()&&s.size>2n*1024n**3n)throw Error('This file exceeds the 2 GiB upload limit.');
  entries[file]=sourceFingerprint(s);if(s.isFile())bytes+=Number(s.size);
  if(s.isDirectory())for(const name of (await readdir(file)).sort())if(!excludedCopyName(name))await visit(path.join(file,name));
 };
 await visit(source);return {entries,count,bytes};
}
export class CopyReviews{
 constructor({now=Date.now}={}){this.now=now;this.reviews=new Map();}
 create(value){for(const [id,v]of this.reviews)if(v.expires<=this.now())this.reviews.delete(id);if(this.reviews.size>=20)throw Error('Close earlier copy reviews before creating another.');const id=randomUUID();this.reviews.set(id,{...value,expires:this.now()+300000});return id;}
 take(id){const value=this.reviews.get(id);this.reviews.delete(id);if(!value||value.expires<=this.now())throw Error('Copy review expired or already used. Review the copy again.');return value;}
}
export async function checkCopyDrafts(drafts,root,relative,readSaved){
 for(const [key,draft]of drafts){const [r,p]=JSON.parse(key);if(r!==root||!(p===relative||!relative||p.startsWith(relative+'/')))continue;
  const saved=root==='mac'?await readSaved(p).catch(()=>null):null;
  if(saved&&[draft,...draft.variants||[]].every(v=>v.text===saved.text&&(!v.binding_id||v.binding_id===saved.binding_id)))continue;
  throw Error('Save the source and destination drafts before copying. All drafts are preserved.');
 }
}
export async function guardCopyDestination(output,forbidden=[],{source=false}={}){
 const parent=await realpath(path.dirname(output)),destination=path.join(parent,path.basename(output));
 for(const root of forbidden){if(!root)continue;const actual=await realpath(root).catch(()=>path.resolve(root));if(destination===actual||destination.startsWith(actual+path.sep)||source&&actual.startsWith(destination+path.sep))throw Error('Choose Mac Box, Downloads or another ordinary folder, not managed Main replicas or private application state.');}
 return destination;
}
