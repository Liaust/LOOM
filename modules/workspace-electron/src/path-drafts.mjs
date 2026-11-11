export function retargetDrafts(drafts, root, from, to, bindingID) {
  const moves = [];
  for (const [key, value] of drafts) {
    const [entryRoot, relative] = JSON.parse(key);
    if (entryRoot !== root || relative !== from && !relative.startsWith(`${from}/`)) continue;
    if(bindingID&&relative===from&&!value.binding_id)value.binding_id=bindingID;
    moves.push({ key, target: JSON.stringify([root, to + relative.slice(from.length)]), value });
  }
  const collisions = moves.filter(move => drafts.has(move.target) && move.target !== move.key);
  if (collisions.length) return { moved: 0, collisions: collisions.length };
  for (const move of moves) drafts.delete(move.key);
  for (const move of moves) drafts.set(move.target, move.value);
  return { moved: moves.length, collisions: 0 };
}

export function draftFor(drafts, root, relative, bindingID) {
  if(bindingID)for(const [key,value] of drafts){
    if(JSON.parse(key)[0]!==root)continue;
    if(value.binding_id===bindingID)return value;
    const variant=value.variants?.find(candidate=>candidate.binding_id===bindingID);
    if(variant)return variant;
  }
  const value=drafts.get(JSON.stringify([root,relative]));
  if(value&&bindingID&&!value.binding_id)value.binding_id=bindingID;
  return !value?.binding_id||value.binding_id===bindingID?value:undefined;
}

export function storeDraft(drafts, root, relative, value) {
  if(value.binding_id)for(const [key,existing] of drafts){
    if(JSON.parse(key)[0]!==root)continue;
    if(existing.binding_id===value.binding_id){drafts.set(key,{...value,...(existing.variants?{variants:existing.variants}:{})});return;}
    const index=existing.variants?.findIndex(candidate=>candidate.binding_id===value.binding_id);
    if(index>=0){existing.variants[index]=value;return;}
  }
  const key=JSON.stringify([root,relative]),existing=drafts.get(key);
  if(existing&&existing.binding_id!==value.binding_id){
    existing.variants=[...(existing.variants||[]),value];
    return;
  }
  drafts.set(key,value);
}

export function clearSavedDraft(drafts, root, bindingID, text) {
  for(const [key,value] of drafts){
    if(JSON.parse(key)[0]!==root)continue;
    const variants=(value.variants||[]).filter(candidate=>candidate.binding_id!==bindingID||candidate.text!==text);
    if(value.binding_id===bindingID&&value.text===text){
      if(variants.length){const [first,...rest]=variants;drafts.set(key,{...first,...(rest.length?{variants:rest}:{})});}
      else drafts.delete(key);
    }else if(value.variants)value.variants=variants;
  }
}
