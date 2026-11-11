export function mergeSearchHits(working,indexed){
  const result=new Map();for(const hit of [...working,...indexed]){const key=JSON.stringify([hit.root,hit.relative_path]);if(!result.has(key))result.set(key,hit);}return [...result.values()];
}
