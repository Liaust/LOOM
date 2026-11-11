import {constants,createReadStream} from 'node:fs';
import {open,lstat,realpath,link,unlink} from 'node:fs/promises';
import path from 'node:path';
import {randomUUID} from 'node:crypto';

// Explicit exports never become replicas and never replace an existing file.
export async function downloadFileCopy({output,metadata,load,verify,verifyDestination,signal}){
  if(!path.isAbsolute(output)||!Number.isSafeInteger(metadata.size)||metadata.size<0)throw Error('Invalid download destination or file size.');
  const parent=await realpath(path.dirname(output)),destination=path.join(parent,path.basename(output));
  const parentIdentity=await lstat(parent);await verifyDestination?.(destination);
  try{await lstat(destination);throw Error('That destination already exists. Choose a new filename.');}catch(error){if(error.code!=='ENOENT')throw error;}
  const temporary=path.join(parent,`.loom-download-${randomUUID()}.partial`);
  let file,stream,identity;
  try{
    file=await open(temporary,constants.O_WRONLY|constants.O_CREAT|constants.O_EXCL|constants.O_NOFOLLOW,0o600);identity=await file.stat();
    signal?.throwIfAborted();stream=await load();
    if(stream.headers?.etag&&stream.headers.etag!==metadata.revision)throw Error('The source changed before download. Refresh it first.');
    let received=0;
    for await(const chunk of stream){signal?.throwIfAborted();received+=chunk.length;if(received>metadata.size)throw Error('The file changed size during download.');await file.writeFile(chunk);}
    if(received!==metadata.size)throw Error('The download was incomplete.');
    await verify?.();signal?.throwIfAborted();await file.sync();await file.close();file=null;
    const named=await lstat(temporary);if(named.dev!==identity.dev||named.ino!==identity.ino||!named.isFile())throw Error('The private download file changed.');
    const currentParent=await lstat(parent);if(currentParent.isSymbolicLink()||currentParent.dev!==parentIdentity.dev||currentParent.ino!==parentIdentity.ino||await realpath(path.dirname(output))!==parent)throw Error('The destination folder changed. No copy was published.');
    await verifyDestination?.(destination);await link(temporary,destination);return {path:destination,bytes:received};
  }finally{
    stream?.destroy();await file?.close();
    if(identity){const named=await lstat(temporary).catch(()=>null);if(named?.dev===identity.dev&&named?.ino===identity.ino)await unlink(temporary);}
  }
}

export const cachedFileStream=file=>createReadStream(file);
