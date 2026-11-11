import test from 'node:test';
import assert from 'node:assert/strict';
import {fetchPdfRange} from '../src/pdf-range.mjs';

test('PDF requests exact source-bound ranges, never a whole-file fallback',async()=>{
  let request;
  const bytes=await fetchPdfRange('loom-file://preview/identity/file.pdf',4,8,20,{fetcher:async(url,options)=>{
    request={url,...options};return new Response(new Uint8Array([1,2,3,4]),{status:206,headers:{'Content-Range':'bytes 4-7/20','Content-Length':'4'}});
  }});
  assert.deepEqual([...bytes],[1,2,3,4]);assert.equal(request.headers.Range,'bytes=4-7');assert.equal(request.cache,'no-store');
});
test('PDF rejects stale, unbounded, partial or changed sources',async()=>{
  for(const [status,range,length,body]of [[200,'bytes 4-7/20','4','file'],[206,'bytes 4-7/21','4','file'],[206,'bytes 4-7/20','5','file'],[206,'bytes 4-7/20','4','fi'],[206,'bytes 4-7/20','4','files'],[409,'','0','']])
    await assert.rejects(()=>fetchPdfRange('url',4,8,20,{fetcher:async()=>new Response(body,{status,headers:{'Content-Range':range,'Content-Length':length}})}));
  let called=false;await assert.rejects(()=>fetchPdfRange('url',0,21,20,{fetcher:async()=>{called=true;}}));assert.equal(called,false);
});
test('PDF cancellation reaches the source reader',async()=>{
  const controller=new AbortController();controller.abort();
  await assert.rejects(()=>fetchPdfRange('url',0,4,4,{signal:controller.signal,fetcher:async(_url,{signal})=>{signal.throwIfAborted();}}),/abort/i);
});
