const identifier = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
export const isBoxRef = root => root === 'box' || typeof root === 'string' && /^box:[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(root);
export const isWritableBox = descriptor => Boolean(descriptor && isBoxRef(descriptor.ref || descriptor.root_ref) && descriptor.available !== false && descriptor.read_only !== true && (descriptor.writable === true || (descriptor.ref || descriptor.root_ref) === 'box' && descriptor.writable !== false));

// The legacy alias is immutable. A catalog never derives identity from the
// sidebar, and an unknown saved root is not a Personal destination.
export class BoxRoots {
  constructor(boxes) { this.replace(boxes); }
  replace(boxes) {
    if (boxes === undefined) { this.boxes = new Map([['box', {ref: 'box', writable: true, available: true, replica: 'box'}]]); this.unavailable=[];return; }
    const next = new Map(), replicas = new Set(), ids = new Set(),unavailable=[];
    for (const value of boxes.boxes || boxes) {
      const ref = value.root_ref || value.ref, id = value.box_id, key = value.box_key;
      if(value.available===false&&(!id||!ref)){unavailable.push({...value});continue;}
      if (!identifier(id) || !identifier(key) || !isBoxRef(ref) || ref !== 'box' && ref !== `box:${id}` || ref === 'box' && value.primary === false) throw Error('Invalid registered Box descriptor.');
      const replica = ref === 'box' ? 'box' : `boxes/${key}`;
      if (next.has(ref) || ids.has(id) || replicas.has(replica)) throw Error('Conflicting registered Box identity or replica.');
      ids.add(id); replicas.add(replica);
      next.set(ref, {...value, ref, replica, writable: value.writable !== false && value.read_only !== true});
    }
    this.boxes = next;this.unavailable=unavailable;
  }
  get(root) { return this.boxes.get(root); }
  lookup(value) { return this.get(value)||[...this.boxes.values()].find(box=>box.box_id===value||box.box_key===value); }
  selection({root,box,box_id,all_boxes,activeBox}={}) {
    const explicit=[root,box,box_id,activeBox].filter(value=>value!==undefined&&value!==''&&value!==null);
    if(all_boxes===true||all_boxes==='true'){if(explicit.length)throw Error('All Boxes conflicts with a specific Box selection.');return {all_boxes:true};}
    if(!explicit.length)return {};
    const selected=explicit.map(value=>this.lookup(value));
    if(selected.some(value=>!value)||selected.some(value=>value.ref!==selected[0].ref))throw Error('Unknown or conflicting Box selection.');
    return this.identity(selected[0].ref,box_id);
  }
  identity(root, expected,expectedKey) {
    const box = this.get(root);
    if (!box || !isWritableBox(box)) throw Error(`Box root ${root} is unavailable. Saved work remains bound to its original root.`);
    if (expected && box.box_id && expected !== box.box_id) throw Error('Saved Box identity conflicts with its registered root.');
    if (expectedKey && box.box_key && expectedKey !== box.box_key) throw Error('Saved Box replica key conflicts with its registered root.');
    return {root, ...(box.box_id ? {box_id: box.box_id, box_key: box.box_key} : {})};
  }
  localPath(root, relative) {
    this.identity(root);
    return `${this.get(root).replica}/${relative}`.replace(/\/$/, '');
  }
  relative(root, localPath) {
    const prefix = this.get(root)?.replica;
    if (!prefix || !localPath.startsWith(prefix + '/')) throw Error('Local replica does not match its saved Box root.');
    return localPath.slice(prefix.length + 1);
  }
}

export function savedRoot(record, fallback = 'box') {
  const roots = [record.root, record.request?.root, record.base?.root].filter(value => value !== undefined);
  const root = roots[0] || fallback;
  if (!isBoxRef(root) || roots.some(value => value !== root) || !roots.length && record.box_id) throw Error('Saved operation has conflicting or missing Box root identity.');
  return root;
}
export function sameBoxRoots(root, targetRoot = root) {
  if (!isBoxRef(root) || root !== targetRoot) throw Error('Cross-Box transfer is not supported here. No files were changed.');
}
