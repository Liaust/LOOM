const samePage=(a,b)=>a?.kind===b?.kind&&a?.root===b?.root&&a?.path===b?.path&&a?.view===b?.view&&JSON.stringify(a?.center)===JSON.stringify(b?.center)&&JSON.stringify(a?.member)===JSON.stringify(b?.member);
const pageKinds=new Set(['empty','file','directory','knowledge','timeline']);
const boolean=(value,name)=>{if(typeof value!=='boolean')throw TypeError(`${name} must be a boolean.`);};
const validPage=page=>{
  if(!page||!pageKinds.has(page.kind)||(page.kind!=='empty'&&(typeof page.root!=='string'||!page.root||typeof page.path!=='string')))
    throw TypeError('A tab page needs a supported kind and source reference.');
};
const navigationArgs=(step,record,activate)=>{
  if(!Number.isSafeInteger(step))throw TypeError('Navigation step must be a safe integer.');
  boolean(record,'record');boolean(activate,'activate');
};

export class DocumentTabs {
  constructor(limit=24,cacheLimit=64,historyLimit=128,closedLimit=24){
    for(const [name,value,min]of [['limit',limit,1],['cacheLimit',cacheLimit,0],['historyLimit',historyLimit,1],['closedLimit',closedLimit,0]])
      if(!Number.isSafeInteger(value)||value<min)throw RangeError(`${name} must be a safe integer of at least ${min}.`);
    this.items=new Map();this.slots=new Map();this.closed=[];this.activeTab=null;this.serial=0;
    Object.assign(this,{limit,cacheLimit,historyLimit,closedLimit});this.newTab();
  }
  restore(session){
    if(this.items.size)throw Error('Restore tabs only before opening documents.');
    if(!session||!Array.isArray(session.tabs)||!session.tabs.length||session.tabs.length>this.limit)throw TypeError('Invalid tab session.');
    const slots=new Map();let serial=0;
    for(const slot of session.tabs){
      if(!slot||typeof slot.id!=='string'||!/^tab_\d{1,8}$/.test(slot.id)||slots.has(slot.id)||!Array.isArray(slot.history)||!slot.history.length||!Number.isInteger(slot.cursor)||slot.cursor<0||slot.cursor>=slot.history.length)
        throw TypeError('Invalid tab session slot.');
      if(slot.pinned!==undefined)boolean(slot.pinned,'pinned');
      for(const page of slot.history)validPage(page);
      const start=Math.max(0,slot.cursor-this.historyLimit+1);
      slots.set(slot.id,{history:slot.history.slice(start,start+this.historyLimit).map(p=>({...p})),cursor:slot.cursor-start,pinned:slot.pinned===true});
      serial=Math.max(serial,Number(slot.id.slice(4)));
    }
    if(!slots.has(session.active))throw TypeError('Invalid active tab in session.');
    this.slots=slots;this.activeTab=session.active;this.serial=serial;this.closed=[];
  }
  key(root,path,member){return JSON.stringify(member?[root,path,member.id,member.archiveRevision]:[root,path]);}
  get(root,path,member){return this.items.get(this.key(root,path,member));}
  page(id=this.activeTab){const slot=this.slots.get(id);return slot?.history[slot.cursor];}
  get active(){const page=this.page();return page?.kind==='file'?this.key(page.root,page.path,page.member):null;}
  visible(){return [...this.slots].map(([id,slot])=>{const page=this.page(id);return{id,page,value:page.kind==='file'?this.get(page.root,page.path,page.member):null,pinned:slot.pinned===true};});}
  newTab({activate=true,reuseEmpty=true}={}){
    boolean(activate,'activate');boolean(reuseEmpty,'reuseEmpty');
    if(activate&&reuseEmpty&&this.page()?.kind==='empty')return this.activeTab;
    if(this.slots.size>=this.limit)throw Error('Close an open file tab before opening another.');
    const id=`tab_${++this.serial}`;this.slots.set(id,{history:[{kind:'empty'}],cursor:0,pinned:false});if(activate)this.activeTab=id;return id;
  }
  target(step,id=this.activeTab){const slot=this.slots.get(id);return slot?.history[slot.cursor+step];}
  visit(page,{tab=this.activeTab,step=0,record=true,activate=true}={}){
    validPage(page);navigationArgs(step,record,activate);
    if(!tab){if(step)throw Error('Navigation changed. Try Back or Forward again.');tab=this.newTab({activate});}
    const slot=this.slots.get(tab);if(!slot)throw Error('That tab has been closed.');
    if(step){
      if(!samePage(slot.history[slot.cursor+step],page))throw Error('Navigation changed. Try Back or Forward again.');
      slot.cursor+=step;
    }else if(record&&!samePage(this.page(tab),page)){
      slot.history.splice(slot.cursor+1);slot.history.push({...page});slot.cursor=slot.history.length-1;
      if(slot.history.length>this.historyLimit){slot.history.shift();slot.cursor--;}
    }else if(!record)slot.history[slot.cursor]={...page};
    if(activate)this.activeTab=tab;return page;
  }
  open(value,{newTab=false,background=false,activate=!background,tab=this.activeTab,step=0,record=true}={}){
    const page={kind:'file',root:value?.root,path:value?.path,...value?.member?{member:value.member}:{}};
    validPage(page);navigationArgs(step,record,activate);boolean(newTab,'newTab');boolean(background,'background');
    if(background&&activate)throw TypeError('A background open cannot activate a tab.');
    const create=newTab||background;
    if(create){
      if(step)throw Error('Navigation changed. Try Back or Forward again.');
      tab=this.newTab({activate,reuseEmpty:!background});
    }
    this.visit(page,{tab,step,record,activate});
    const key=this.key(value.root,value.path,value.member);this.items.delete(key);this.items.set(key,value);return value;
  }
  move(root,from,to,localPath){
    const affected=[...this.items].filter(([,value])=>value.root===root&&(value.path===from||value.path.startsWith(`${from}/`)));
    if(affected.some(([,value])=>this.items.has(this.key(root,to+value.path.slice(from.length)))))return false;
    for(const [key]of affected)this.items.delete(key);
    for(const [,value]of affected){
      const suffix=value.path.slice(from.length);value.path=to+suffix;
      value.entry={...value.entry,name:value.path.split('/').pop(),relative_path:value.path};value.local_path=localPath===undefined?undefined:localPath+suffix;
      this.items.set(this.key(root,value.path),value);
    }
    for(const slot of [...this.slots.values(),...this.closed.map(entry=>entry.slot)])for(const page of slot.history)if(page.root===root&&(page.path===from||page.path?.startsWith(`${from}/`)))page.path=to+page.path.slice(from.length);
    return true;
  }
  close(id=this.activeTab){
    const order=[...this.slots.keys()],index=order.indexOf(id),slot=this.slots.get(id);
    if(!slot)return this.page()||null;
    this.slots.delete(id);
    if(this.closedLimit){this.closed.push({id,slot,index});if(this.closed.length>this.closedLimit)this.closed.shift();}
    if(this.activeTab===id)this.activeTab=order[index-1]||order[index+1]||null;
    return this.page()||null;
  }
  reopenClosed(){
    const entry=this.closed.at(-1);if(!entry)return null;
    if(this.slots.size>=this.limit)throw Error('Close an open file tab before opening another.');
    const order=[...this.slots];order.splice(Math.min(entry.index,order.length),0,[entry.id,entry.slot]);
    this.slots=new Map(order);this.closed.pop();this.activeTab=entry.id;return entry.id;
  }
  reorder(id,beforeId=null){
    if(!this.slots.has(id)||(beforeId!==null&&!this.slots.has(beforeId)))return false;
    if(id===beforeId)return true;
    const order=[...this.slots].filter(([key])=>key!==id),index=beforeId===null?order.length:order.findIndex(([key])=>key===beforeId);
    order.splice(index,0,[id,this.slots.get(id)]);this.slots=new Map(order);return true;
  }
  pin(id,pinned=true){
    boolean(pinned,'pinned');const slot=this.slots.get(id);if(!slot)return false;
    slot.pinned=pinned;return true;
  }
  cycle(step=1){
    if(!Number.isSafeInteger(step))throw TypeError('Cycle step must be a safe integer.');
    const order=[...this.slots.keys()];if(!order.length)return null;
    let index=order.indexOf(this.activeTab);if(!step)return index<0?null:this.activeTab;
    if(index<0)index=step>0?-1:0;
    this.activeTab=order[(index+step%order.length+order.length)%order.length];return this.activeTab;
  }
  prune(protectedDocument=()=>false){
    const visible=new Set(this.visible().filter(slot=>slot.page.kind==='file').map(slot=>this.key(slot.page.root,slot.page.path,slot.page.member))),removed=[];
    for(const [key,value]of this.items){
      if(this.items.size<=this.cacheLimit)break;
      if(visible.has(key)||value.dirty||value.held||protectedDocument(value))continue;
      this.items.delete(key);removed.push(value);
    }
    return removed;
  }
}
