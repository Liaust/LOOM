const valid=scope=>scope&&typeof scope.root==='string'&&typeof scope.path==='string'&&!scope.path.split('/').some(p=>p==='..'||p==='.')&&!scope.path.startsWith('/')&&!scope.path.includes('\\');
export class ExplorerFocus {
  constructor(storage){
    this.storage=storage;this.mode='explorer';this.scope=null;this.recent=[];this.views={};this.initialized=true;
    try{const saved=JSON.parse(storage?.getItem('explorer-focus')||'{}');this.scope=valid(saved.scope)?saved.scope:null;this.mode=saved.mode==='focus'&&this.scope?'focus':'explorer';this.recent=(saved.recent||[]).filter(valid).slice(0,8);this.views=saved.views&&typeof saved.views==='object'?saved.views:{};if(this.mode==='focus')this.initialized=false;}catch{}
  }
  key(){return this.mode==='focus'?JSON.stringify([this.scope.root,this.scope.path]):'explorer';}
  save(){try{this.storage?.setItem('explorer-focus',JSON.stringify({mode:this.mode,scope:this.scope,recent:this.recent,views:this.views}));}catch{}}
  capture(tree,container){this.views[this.key()]={expanded:[...tree.expanded].slice(0,256),scroll:container.scrollTop};const keep=new Set(['explorer',...this.recent.map(s=>JSON.stringify([s.root,s.path]))]);for(const key of Object.keys(this.views))if(!keep.has(key))delete this.views[key];this.save();}
  switch(mode,tree,container,scope){
    if(scope&&!valid(scope))throw Error('Invalid focus folder.');
    if(this.initialized)this.capture(tree,container);this.initialized=true;
    if(scope){this.scope={root:scope.root,path:scope.path};this.recent=[this.scope,...this.recent.filter(s=>s.root!==scope.root||s.path!==scope.path)].slice(0,8);}
    this.mode=mode==='focus'&&this.scope?'focus':'explorer';
    const view=this.views[this.key()];tree.expanded=new Set((view?.expanded||[]).filter(k=>{try{const [root,path]=JSON.parse(k);return typeof root==='string'&&typeof path==='string';}catch{return false;}}));
    if(this.mode==='focus')tree.expanded.add(tree.key(this.scope.root,this.scope.path));
    container.scrollTop=view?.scroll||0;this.save();return view?.scroll||0;
  }
  roots(roots){if(this.mode!=='focus')return roots;const root=roots.find(r=>r.ref===this.scope.root);return root?[{...root,rootLabel:root.label,path:this.scope.path,label:this.scope.path.split('/').pop()||root.label}]:[];}
  contains(root,path){return this.mode!=='focus'||root===this.scope.root&&(path===this.scope.path||!this.scope.path||path.startsWith(this.scope.path+'/'));}
  moved(root,from,to){
    const move=s=>s.root===root&&(s.path===from||s.path.startsWith(from+'/'))?{...s,path:to+s.path.slice(from.length)}:s;
    if(this.scope)this.scope=move(this.scope);this.recent=this.recent.map(move);this.save();
  }
}
