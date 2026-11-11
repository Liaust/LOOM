import path from 'node:path';
import {constants} from 'node:fs';
import {mkdir,lstat,realpath,open} from 'node:fs/promises';
import {createHash,randomUUID} from 'node:crypto';
import {sourceFingerprint} from './copy-review.mjs';
import {attachmentLink} from './attachment-reference.mjs';

const LIMIT=2*1024**3,CHUNK=1024**2;
// A staged payload belongs to one explicit import. It is not a disposable cache:
// failures, undo and closing the editor never destroy the user's only copy.
export class Attachments{
  constructor(db,directory,{context,enqueue,importLocal,emit=()=>{}}){
    Object.assign(this,{db,directory,context,enqueue,importLocal,emit});
    db.exec('CREATE TABLE IF NOT EXISTS desktop_attachments(id TEXT PRIMARY KEY,record TEXT NOT NULL)');
    this.put=db.prepare('INSERT INTO desktop_attachments VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record');
  }
  rows(){return this.db.prepare('SELECT record FROM desktop_attachments').all().map(row=>JSON.parse(row.record));}
  save(v){this.put.run(v.id,JSON.stringify(v));this.emit('attachments-changed',{root:v.root,binding_id:v.binding,path:v.notePath,id:v.id});return v;}
  async init(){await mkdir(this.directory,{recursive:true,mode:0o700});if(await realpath(this.directory)!==this.directory||(await lstat(this.directory)).isSymbolicLink())throw Error('Attachment staging must be a real private directory.');return this;}
  async stage(input,{source,bytes,name}){
    const owner=this.context(input),id=randomUUID();
    if(!owner||!owner.editable||!owner.binding||!/\.(md|markdown|mdown)$/i.test(owner.path))throw Error('Choose a writable Markdown note first.');
    name=path.basename(name||source||'image.png').normalize('NFC');
    if(!name||name.startsWith('.')||/[\x00-\x1f\\]/.test(name)||name.length>200)throw Error('Choose a visible attachment filename.');
    const ext=path.extname(name),stem=path.basename(name,ext),filename=`${stem}-${id.slice(0,8)}${ext}`;
    const stagePath=path.join(this.directory,filename);let incoming,before;
    if(source){if(!path.isAbsolute(source)||await realpath(source)!==source)throw Error('Attachment sources cannot contain symbolic links.');const named=await lstat(source,{bigint:true});if(!named.isFile()||named.size>BigInt(LIMIT))throw Error('Attachments must be regular files up to 2 GiB.');incoming=await open(source,constants.O_RDONLY|constants.O_NOFOLLOW|constants.O_NONBLOCK);before=await incoming.stat({bigint:true});if(!before.isFile()||sourceFingerprint(named)!==sourceFingerprint(before)){await incoming.close();throw Error('Attachment source changed before import.');}}
    else if(!Buffer.isBuffer(bytes)||bytes.length>LIMIT)throw Error('Invalid clipboard attachment.');
    const output=await open(stagePath,'wx',0o600),hash=createHash('sha256');let size=0;
    try{
      if(incoming){const buffer=Buffer.alloc(CHUNK);for(;;){const {bytesRead}=await incoming.read(buffer);if(!bytesRead)break;size+=bytesRead;if(size>LIMIT)throw Error('Attachment exceeds 2 GiB.');const part=buffer.subarray(0,bytesRead);hash.update(part);await output.writeFile(part);}if(await realpath(source)!==source||sourceFingerprint(before)!==sourceFingerprint(await lstat(source,{bigint:true}))||sourceFingerprint(before)!==sourceFingerprint(await incoming.stat({bigint:true}))||BigInt(size)!==before.size)throw Error('The attachment changed during import. Original preserved.');}
      else{size=bytes.length;hash.update(bytes);await output.writeFile(bytes);}
      await output.sync();
    }finally{await output.close();await incoming?.close();}
    const current=this.context({...input,binding:owner.binding});
    if(!current?.editable||current.binding!==owner.binding)throw Error('The note changed during import. The staged attachment is preserved.');
    const destination=path.posix.join(path.posix.dirname(current.path),'Attachments',filename);
    const v={id,root:current.root,box_id:current.box_id,binding:current.binding,notePath:current.path,stagePath,size,hash:'sha256:'+hash.digest('hex'),destination,reference:attachmentLink(current.path,destination),state:'staged'};
    this.save(v);
    try{
      if(v.root==='mac'){await this.importLocal(v);v.state='accepted';this.save(v);}
      else{const upload=await this.enqueue(v);v.uploadID=upload.id;v.state=upload.state==='accepted'?'accepted':'pending';if(upload.state==='accepted')v.destination=upload.destination;this.save(v);}
    }catch(error){v.state='held';v.error=error.message;this.save(v);}
    return this.public(v,current.path);
  }
  public(v,from=v.notePath){return{id:v.id,root:v.root,binding:v.binding,state:v.state,error:v.error,path:v.destination,reference:attachmentLink(from,v.destination),insert:`![[${attachmentLink(from,v.destination)}]]`};}
  accepted(upload){for(const v of this.rows())if(v.uploadID===upload.id){v.destination=upload.destination;v.state='accepted';v.error='';this.save(v);}}
  async pending(root,reference,from,binding){
    return this.rows().find(v=>v.root===root&&v.binding===binding&&v.state!=='accepted'&&[v.reference,attachmentLink(from,v.destination)].includes(reference));
  }
  list(input){const owner=this.context(input);return this.rows().filter(v=>v.root===owner.root&&v.binding===owner.binding).map(v=>({...this.public(v,owner.path),original:`![[${v.reference}]]`}));}
  linked(input){const owner=this.context(input),v=this.rows().find(v=>v.id===input.id&&v.root===owner.root&&v.binding===owner.binding);if(!v||input.reference!==attachmentLink(owner.path,v.destination))throw Error('Attachment link does not match its saved import.');v.reference=input.reference;this.put.run(v.id,JSON.stringify(v));return true;}
  async retryLocal(input){const owner=this.context(input);if(owner.root!=='mac'||!owner.editable)throw Error('Choose the original writable Mac note.');for(const v of this.rows().filter(v=>v.root===owner.root&&v.binding===owner.binding&&v.state==='held')){await this.importLocal(v);v.state='accepted';v.error='';this.save(v);}return this.list(input);}
}
