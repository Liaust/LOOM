const raster=/\.(png|jpe?g|gif|webp|avif|bmp)$/i;
export function attachmentReference(input){
  if(typeof input!=='string'||input.length>8192||/[\0\r\n\\]/.test(input))throw Error('Invalid attachment reference.');
  const raw=input.trim().replace(/^!\[\[|\]\]$/g,''),[destination,option,...extra]=raw.split('|');
  if(extra.length)throw Error('Invalid attachment dimensions.');
  const at=destination.indexOf('#'),target=at<0?destination:destination.slice(0,at),fragment=at<0?'':destination.slice(at+1);
  if(!target)throw Error('Choose an attachment file.');
  let decoded;try{decoded=decodeURIComponent(target);}catch{throw Error('Invalid attachment path encoding.');}
  const external=/^https?:\/\//i.test(decoded);
  if(!external&&/^[a-z][\w+.-]*:/i.test(decoded)||/[\x00-\x1f\\]/.test(decoded))throw Error('This attachment scheme is not allowed.');
  const kind=raster.test(decoded)?'image':/\.pdf$/i.test(decoded)?'pdf':'file';
  let width,height,page=1;
  const dimension=(v,label)=>{const n=Number(v);if(!/^\d+$/.test(v)||!Number.isSafeInteger(n)||n<1||n>10000)throw Error(`Invalid attachment ${label}.`);return n;};
  if(option&&/^\d/.test(option)){
    if(!/^\d+(?:x\d+)?$/.test(option))throw Error('Invalid attachment dimensions.');
    const [w,h]=option.split('x');width=dimension(w,'width');if(h)height=dimension(h,'height');
  }
  if(kind==='pdf'&&fragment){
    const parameters=new URLSearchParams(fragment);
    if(parameters.has('page'))page=dimension(parameters.get('page'),'page');
    if(parameters.has('height'))height=dimension(parameters.get('height'),'height');
  }
  return {raw,target,kind,external,width,height,page,label:option&&!/^\d/.test(option)?option:decoded.split('/').at(-1)};
}

export function attachmentLink(from,to){
  const origin=from.split('/').slice(0,-1),destination=to.split('/');
  while(origin.length&&destination.length&&origin[0]===destination[0]){origin.shift();destination.shift();}
  const relative=[...origin.map(()=>'..'),...destination].join('/');
  // Encode syntax delimiters, not filesystem separators or readable names.
  return relative.replace(/[%#|\[\]\r\n]/g,c=>encodeURIComponent(c));
}
