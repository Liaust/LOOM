const names={'image/png':'image.png','image/jpeg':'image.jpg','image/webp':'image.webp','image/gif':'image.gif'};
export async function clipboardImage(clipboard,convert){
  for(const item of await clipboard.read()){
    const type=item.types.find(type=>names[type])||item.types.find(type=>type.startsWith('image/'));
    if(!type)continue;
    const blob=await item.getType(type);
    if(blob.size>64*1024**2)throw Error('Clipboard image exceeds 64 MiB. Attach the original file instead.');
    const source=Buffer.from(await blob.arrayBuffer());
    return names[type]?{bytes:source,name:names[type]}:{bytes:convert(source),name:'image.png'};
  }
  return null;
}
