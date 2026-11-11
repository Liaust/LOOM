import {watch} from 'node:fs';
import {stat} from 'node:fs/promises';
import path from 'node:path';

const relative = value => {
  if(typeof value!=='string'||value.includes('\\')||value.includes('\0')||value.startsWith('/')||value.split('/').some(p=>p==='.'||p==='..'))throw Error('Invalid directory subscription.');
  return value.replace(/\/+$/,'');
};

// Explorer membership is independent of the downloaded Notes working set.
// Subscriptions read no payloads and never enroll, index or adopt files.
export class DirectoryWatch {
  constructor(remote,{localRoot,emit=()=>{},retryMs=500}={}){
    Object.assign(this,{remote,localRoot,emit,retryMs});
    this.groups=new Map();this.closed=false;
  }
  setScopes(scopes){
    if(this.closed)return;
    if(!Array.isArray(scopes)||scopes.length>4096)throw Error('Too many explorer directories.');
    const grouped=new Map();
    for(const scope of scopes){
      if(typeof scope.root!=='string'||!scope.root)throw Error('Invalid explorer root.');
      if(!grouped.has(scope.root))grouped.set(scope.root,new Set());
      grouped.get(scope.root).add(relative(scope.path));
    }
    if(grouped.size>16)throw Error('Too many explorer roots.');
    for(const [root,state] of this.groups)if(!grouped.has(root)){this.stop(state);this.groups.delete(root);}
    for(const [root,values]of grouped){
      const paths=[...values].sort(),signature=JSON.stringify(paths),prior=this.groups.get(root);
      if(prior?.signature===signature)continue;
      if(prior)this.stop(prior);
      const state={root,paths,signature,failures:0};this.groups.set(root,state);void this.open(state);
    }
  }
  current(state){return !this.closed&&this.groups.get(state.root)===state;}
  stop(state){state.controller?.abort();clearTimeout(state.retry);clearTimeout(state.debounce);state.retry=null;state.debounce=null;state.dirty?.clear();for(const watcher of state.watchers||[])watcher.close();state.watchers=[];}
  reconnect(){
    for(const state of this.groups.values()){this.stop(state);state.failures=0;void this.open(state);}
  }
  changed(state,paths){
    if(!this.current(state))return;
    state.dirty??=new Set();for(const value of paths)state.dirty.add(value);
    if(!state.debounce)state.debounce=setTimeout(()=>{
      state.debounce=null;if(!this.current(state))return;
      const paths=[...state.dirty];state.dirty.clear();this.emit('explorer-changed',{root:state.root,paths});
    },50);
  }
  async open(state){
    if(!this.current(state))return;
    const controller=state.controller=new AbortController();
    const current=()=>this.current(state)&&state.controller===controller&&!controller.signal.aborted;
    try{
      if(['mac','local'].includes(state.root)){
        const watchers=[];state.watchers=watchers;
        // Resolve the original authority for each shallow watch, including its
        // parent so replacement/rename cannot strand an old inode subscription.
        for(const relative of state.paths){
          let candidate=relative,absolute;
          for(;;){
            try{absolute=await this.localRoot(state.root,candidate);if(!(await stat(absolute)).isDirectory())throw Error('Not a directory.');break;}
            catch(error){if(!['ENOENT','ENOTDIR'].includes(error.code)||!candidate)throw error;candidate=path.posix.dirname(candidate);if(candidate==='.')candidate='';}
          }
          if(!current())return;
          const onChange=(kind,name)=>{
            if(!current())return;this.changed(state,[relative]);
            // Re-resolve the source on membership changes, not on every keystroke.
            if(kind==='rename'&&!state.retry){state.retry=setTimeout(()=>{state.retry=null;if(!current())return;this.stop(state);void this.open(state);},100);}
          };
          for(const directory of new Set([absolute,...(candidate?[path.dirname(absolute)]:[])])){
            const watcher=watch(directory,{persistent:false},onChange);watchers.push(watcher);
            watcher.on('error',error=>{if(current()){this.stop(state);this.failed(state,error);}});
          }
        }
        if(current()){state.failures=0;this.changed(state,state.paths);}return;
      }
      const response=await this.remote.request('watch',{root:state.root},{method:'POST',body:{root:state.root,paths:state.paths},lane:'explorer-watch',signal:controller.signal});
      if(!current()){response.destroy();return;}
      state.failures=0;
      let buffer='';const decoder=new TextDecoder();
      for await(const chunk of response){
        if(!current())break;
        buffer+=decoder.decode(chunk,{stream:true});
        if(buffer.length>1024*1024)throw Error('Oversized explorer notification.');
        let end;
        while((end=buffer.indexOf('\n\n'))>=0){
          const block=buffer.slice(0,end);buffer=buffer.slice(end+2);
          const data=block.split('\n').filter(line=>line.startsWith('data: ')).map(line=>line.slice(6)).join('\n');if(!data)continue;
          const event=JSON.parse(data);
          if(event.root&&event.root!==state.root)throw Error('Explorer notification changed root.');
          if(!['ready','changed'].includes(event.type)||!Array.isArray(event.paths))continue;
          const paths=event.paths.map(p=>p==='.'?'':relative(p));
          if(paths.some(p=>!state.paths.includes(p)))throw Error('Explorer notification exceeded its scope.');
          this.changed(state,paths);
        }
      }
      if(current())this.failed(state,new Error('Explorer notification stream disconnected.'));
    }catch(error){if(current()){this.stop(state);this.failed(state,error);}}
  }
  failed(state,error){
    if(!this.current(state))return;
    this.emit('explorer-watch-status',{root:state.root,error:error.message});
    if([400,401,403,404].includes(error.status))return;
    const delay=Math.min(30000,this.retryMs*2**Math.min(state.failures++,6));
    state.retry=setTimeout(()=>{state.retry=null;void this.open(state);},delay);state.retry.unref?.();
  }
  close(){this.closed=true;for(const state of this.groups.values())this.stop(state);this.groups.clear();}
}
