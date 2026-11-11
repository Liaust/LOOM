export class DirectoryTree {
  constructor(loader) {
    this.loader = loader;
    this.branches = new Map();
    this.expanded = new Set();
  }

  key(root, relative = '') { return JSON.stringify([root, relative]); }

  branch(root, relative = '') {
    const key = this.key(root, relative);
    if (!this.branches.has(key)) this.branches.set(key, { root, path: relative, page: null, pending: null, error: null, revision: 0 });
    return this.branches.get(key);
  }

  remember(root, relative, page, append = false) {
    const branch = this.branch(root, relative);
    const key=entry=>entry.root_ref||JSON.stringify([entry.relative_path,entry.member?.id]);
    const entries = new Map((append ? branch.page?.entries || [] : []).map(entry => [key(entry), entry]));
    for (const entry of page.entries) entries.set(key(entry), entry);
    branch.page = { ...page, entries: [...entries.values()].sort((a, b) => (b.kind === 'directory') - (a.kind === 'directory') || a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' })) };
    branch.error = null;
    return branch.page;
  }

  async load(root, relative = '', { more = false, refresh = false } = {}) {
    const branch = this.branch(root, relative);
    if (branch.pending) {if(refresh)branch.revision++;return branch.pending;}
    if (branch.page && !more && !refresh) return branch.page;
    if (more && !branch.page?.has_more) return branch.page;
    const operation = Promise.resolve().then(async () => {
      for(;;){
        const revision=branch.revision;
        const page=await this.loader({root,path:relative,...(more?{cursor:branch.page.next_cursor}:{})});
        if(this.branches.get(this.key(root,relative))!==branch)return page;
        // A source-membership event invalidated this read; never publish its old snapshot.
        if(revision!==branch.revision){more=false;continue;}
        return this.remember(root,relative,page,more);
      }
    }).catch(error => {
      branch.error = error.message;
      throw error;
    }).finally(() => { if (branch.pending === operation) branch.pending = null; });
    branch.pending = operation;
    return operation;
  }

  async expand(root, relative = '') {
    const key=this.key(root,relative),branch=this.branch(root,relative);
    const refresh=branch.opened&&!this.expanded.has(key);
    branch.opened=true;this.expanded.add(key);
    return this.load(root, relative,{refresh});
  }

  collapse(root, relative = '') { this.expanded.delete(this.key(root, relative)); }
  isExpanded(root, relative = '') { return this.expanded.has(this.key(root, relative)); }
  collapseAll() { this.expanded.clear(); }

  async sourceChanged(root,paths){
    const requests=[];
    for(const relative of new Set(paths)){
      const key=this.key(root,relative),branch=this.branches.get(key);if(!branch)continue;
      if(this.expanded.has(key))requests.push(this.load(root,relative,{refresh:true}));
      else{branch.revision++;branch.page=null;branch.error=null;}
    }
    await Promise.allSettled(requests);
  }

  remove(root,relative){
    if(!relative)return;
    const inside=value=>value===relative||value.startsWith(`${relative}/`);
    for(const [key,branch] of this.branches){
      if(branch.root!==root)continue;
      if(inside(branch.path)){this.branches.delete(key);this.expanded.delete(key);continue;}
      if(branch.path===relative.split('/').slice(0,-1).join('/'))branch.revision++;
      if(branch.page)branch.page={...branch.page,entries:branch.page.entries.filter(entry=>!inside(entry.relative_path))};
    }
  }

  resetRoot(root) {
    for (const [key, branch] of this.branches) if (branch.root === root) { this.branches.delete(key); this.expanded.delete(key); }
  }

  async reveal(root, directory = '', base = '') {
    const parts = directory.split('/').filter(Boolean);
    // Only ancestors of the selected location, never a recursive subtree walk.
    for (let depth = base.split('/').filter(Boolean).length; depth <= parts.length; depth++) await this.expand(root, parts.slice(0, depth).join('/'));
  }
}
