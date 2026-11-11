export async function fetchPdfRange(url,begin,end,length,{fetcher=fetch,signal}={}) {
  if(!Number.isSafeInteger(length)||length<=0||!Number.isSafeInteger(begin)||!Number.isSafeInteger(end)||begin<0||end<=begin||end>length)
    throw Error('Invalid PDF byte range.');
  const response=await fetcher(url,{headers:{Range:`bytes=${begin}-${end-1}`},signal,cache:'no-store'});
  const expected=`bytes ${begin}-${end-1}/${length}`;
  if(response.status!==206||response.headers.get('Content-Range')!==expected||Number(response.headers.get('Content-Length'))!==end-begin){
    await response.body?.cancel();throw Error(response.status===409?'PDF changed on Main. Reopen it.':'The PDF source did not return the requested byte range.');
  }
  const reader=response.body.getReader(),bytes=new Uint8Array(end-begin);let offset=0;
  try {
    for(;;){const {done,value}=await reader.read();if(done)break;
      if(offset+value.length>bytes.length)throw Error('PDF range exceeds its declared size.');
      bytes.set(value,offset);offset+=value.length;
    }
    if(offset!==bytes.length)throw Error('PDF range was incomplete.');
    return bytes;
  }finally{await reader.cancel().catch(()=>{});reader.releaseLock();}
}
