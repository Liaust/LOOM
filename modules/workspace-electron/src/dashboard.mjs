import {BoxRoots,isBoxRef} from './box-roots.mjs';

const specs = {
  projects: [['projects','/v1/projects/overview',30_000],['runtime','/v1/system/runtime',15_000]],
  activity: [['jobs','/v1/jobs/overview',15_000]],
  schedules: [['schedules','/v1/schedules/overview',60_000],['hermes','/v1/schedules/hermes',60_000],['hermesManagement','/v1/schedules/hermes/workspace',60_000]],
  system: [['host','/v1/system/host',5_000],['runtime','/v1/system/runtime',15_000],['protection','/v1/system/protection',60_000]],
};
export const chronological = items => [...items].sort((a,b)=>(b.at||'').localeCompare(a.at||'')||`${a.owner}:${a.id}`.localeCompare(`${b.owner}:${b.id}`));

export class Dashboard {
  constructor(remote,{boxes=new BoxRoots()}={}){this.remote=remote;this.boxes=boxes;this.cache=new Map();this.failures=new Map();}
  setBoxes(boxes){this.boxes=boxes instanceof BoxRoots?boxes:new BoxRoots(boxes);}
  selection(input={}){return this.boxes.selection({...input,root:isBoxRef(input.root)?input.root:undefined});}
  cancel(input){if(input&&JSON.stringify(this.selection(input))!==this.controllerScope)return;this.controller?.abort();this.controller=null;}
  async read(input={},publish=()=>{}){
    const {section,token,refresh=false,block}=input;
    if(!specs[section]||typeof token!=='string'||token.length>100)throw Error('Invalid dashboard request');
    if(block&&!specs[section].some(s=>s[0]===block))throw Error('Invalid dashboard block');
    const capturedSelection=this.selection(input),selection=section==='system'?{}:capturedSelection;
    const query={};
    if(section==='projects'){
      if(input.state&&!['all','working','passive','archived','unclassified'].includes(input.state))throw Error('Invalid work state');
      if(input.query&& (typeof input.query!=='string'||input.query.length>160))throw Error('Invalid project query');
      if(input.cursor&&(typeof input.cursor!=='string'||input.cursor.length>1024))throw Error('Invalid cursor');
      Object.assign(query,{limit:24,...input.state&&input.state!=='all'?{state:input.state}:{},...input.query?{query:input.query}:{},...input.cursor?{cursor:input.cursor}:{}});
    }
    if(section==='activity'){
      query.hours=input.hours===1?1:24;
      if(input.state&&!['all','running','queued','attention','completed'].includes(input.state))throw Error('Invalid job state');
      if(input.state)query.state=input.state;
      if(input.since||input.until){if(!Number.isFinite(Date.parse(input.since))||!Number.isFinite(Date.parse(input.until)))throw Error('Invalid job window');query.since=input.since;query.until=input.until;}
    }
    if(section==='schedules'){query.days=input.days===1?1:7;query.limit=100;if(input.cursor)query.cursor=input.cursor;if(input.status){if(!['active','paused','disabled'].includes(input.status))throw Error('Invalid schedule state');query.status=input.status;}}
    this.cancel();const controller=new AbortController();this.controller=controller;this.controllerScope=JSON.stringify(capturedSelection);
    const context={...selection,scope:section==='system'?'system':selection.all_boxes?'all_boxes':'box',...(capturedSelection.root?{activeBox:capturedSelection.root}:{})};
    const result={section,token,...context,blocks:{}};
    await Promise.all(specs[section].filter(s=>!block||block===s[0]).map(async([name,route,ttl])=>{
      const scoped=['projects','jobs','schedules','hermes','hermesManagement'].includes(name),scope=scoped?(selection.all_boxes?'all_boxes':'box'):'system';
      if(name==='hermesManagement'&&selection.all_boxes){const value={available:false,scope,reason:'single_box_required'};result.blocks[name]=value;publish({kind:'dashboard',token,section,...context,name,value});return;}
      const q={...(['projects','jobs','schedules'].includes(name)?query:{}),...(scoped?selection:{})};
      const key=JSON.stringify([route,q,selection]),previous=this.cache.get(key);
      let value;
      const failure=this.failures.get(key);
      if(!refresh&&failure&&Date.now()<failure.retryAt)value=structuredClone(failure.value);
      else if(!refresh&&previous&&Date.now()-previous.savedAt<ttl)value={...structuredClone(previous),cached:true};
      else {
        try{
          const data=(await this.remote.api(route,q,{signal:controller.signal,...name==='hermesManagement'?{method:'POST',body:{operation:'workspace_info',profile:selection.root&&selection.root!=='box'?'':'mina'}}:{}})).data;
          if(controller.signal.aborted)return;
          value={data,available:true,scope,scopeLabel:scope==='system'?'Main-wide':scope==='all_boxes'?'All Boxes':'Selected Box',observedAt:data.observed_at||data.host?.generated_at||new Date().toISOString(),savedAt:Date.now(),cached:false};
          this.cache.set(key,structuredClone(value));if(this.cache.size>32)this.cache.delete(this.cache.keys().next().value);
          this.failures.delete(key);
        }catch(e){
          if(controller.signal.aborted)return;
          value=previous?{...structuredClone(previous),cached:true,stale:true,error:'Owner unavailable; showing the previous snapshot'}:{available:false,scope,error:Number.isInteger(e.status)?`Owner unavailable (HTTP ${e.status})`:'Main unavailable'};
          this.failures.set(key,{value:structuredClone(value),retryAt:Date.now()+Math.min(120_000,ttl*2**Math.min(3,failure?.attempts||0)),attempts:(failure?.attempts||0)+1});
          if(this.failures.size>32)this.failures.delete(this.failures.keys().next().value);
        }
      }
      if(!controller.signal.aborted){result.blocks[name]=value;publish({kind:'dashboard',token,section,...context,name,value});}
    }));
    if(this.controller===controller)this.controller=null;
    return result;
  }
}
