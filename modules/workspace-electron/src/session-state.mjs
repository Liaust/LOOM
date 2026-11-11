import {isBoxRef} from './box-roots.mjs';
const roots=new Set(['box','storage','archive','local','mac']),kinds=new Set(['empty','file','directory','knowledge','timeline']);
const validRoot=root=>roots.has(root)||isBoxRef(root)||root==='application-data'||/^appdata_[A-Za-z0-9_-]{1,768}$/.test(root||'');
const relative=value=>typeof value==='string'&&value.length<=4096&&!value.startsWith('/')&&!value.includes('\\')&&!/[\x00-\x1f]/.test(value)&&value.split('/').every(p=>p!=='.'&&p!=='..');
const bounded=(value,max=200)=>typeof value==='string'&&value.length<=max?value:undefined;
const position=value=>Array.isArray(value)&&value.length<=5&&value.slice(0,3).every(v=>Number.isFinite(v)&&v>=0)?[value[0],value[1],value[2],value[3]==='backward'?'backward':'forward',Number.isFinite(value[4])?Math.max(0,value[4]):0]:undefined;
export function sessionPage(page){
 if(!page||!kinds.has(page.kind))return null;if(page.kind==='empty')return{kind:'empty'};
 if(!validRoot(page.root)||!relative(page.path))return null;
 const result={kind:page.kind,root:page.root,path:page.path};
 if(typeof page.box_id==='string'&&/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(page.box_id))result.box_id=page.box_id;
 if(page.kind==='file'){result.selection=position(page.selection);result.sourceMode=page.sourceMode===true;if(bounded(page.heading,512))result.heading=page.heading;}
 if(page.member){const m=page.member;if(page.kind!=='file'||!/^appdata_/.test(page.root)||typeof m.id!=='string'||!/^[A-Za-z0-9_-]{1,160}$/.test(m.id)||!bounded(m.name,4096)||!bounded(m.archiveRevision,512))return null;result.member={id:m.id,name:m.name,archiveRevision:m.archiveRevision};}
 const saved=page.saved||{};result.saved={scroll:Number.isFinite(saved.scroll)?Math.max(0,Math.min(1e9,saved.scroll)):0};
 if(page.kind==='knowledge'){
  result.view=['categories','table','graph'].includes(page.view)?page.view:'categories';
  if(page.center&&validRoot(page.center.root)&&relative(page.center.path))result.center={root:page.center.root,path:page.center.path};
  const filters=saved.filters||{};result.saved.filters={history:filters.history!==false};for(const key of ['query','tag','type','people','category','property','propertyValue'])if(bounded(filters[key]))result.saved.filters[key]=filters[key];
  if(bounded(saved.groupBy))result.saved.groupBy=saved.groupBy;
  if(saved.graph&&bounded(saved.graph.key,8192)&&saved.graph.camera&&['x','y','ratio','angle'].every(k=>Number.isFinite(saved.graph.camera[k])))result.saved.graph={key:saved.graph.key,camera:{x:saved.graph.camera.x,y:saved.graph.camera.y,ratio:Math.max(.01,Math.min(100,saved.graph.camera.ratio)),angle:saved.graph.camera.angle},focus:bounded(saved.graph.focus,8192)||null};
  if(['string','number','boolean'].includes(typeof filters.propertyValue)&&(!Number.isFinite(filters.propertyValue)&&typeof filters.propertyValue==='number'?false:typeof filters.propertyValue!=='string'||filters.propertyValue.length<=512))result.saved.filters.propertyValue=filters.propertyValue;
  if(filters.propertyMissing===true)result.saved.filters.propertyMissing=true;
  if(Number.isSafeInteger(saved.page))result.saved.page=Math.max(0,Math.min(100,saved.page));
  result.saved.graphMode=['local','two','notes','folders'].includes(saved.graphMode)?saved.graphMode:undefined;
 }
 if(page.kind==='timeline'){result.saved.kind=['create','edit','rename','delete'].includes(saved.kind)?saved.kind:'';for(const key of ['from','to'])if(/^\d{4}-\d{2}-\d{2}$/.test(saved[key]||''))result.saved[key]=saved[key];}
 return result;
}
export function workspaceSession(input){
 if(!input||input.version!==1||!Array.isArray(input.tabs))return null;
 const seen=new Set(),tabs=[];for(const slot of input.tabs.slice(0,24)){
  if(!slot||!/^tab_\d{1,8}$/.test(slot.id)||seen.has(slot.id)||!Array.isArray(slot.history))continue;
  const cursor=Number.isInteger(slot.cursor)?Math.max(0,Math.min(slot.history.length-1,slot.cursor)):slot.history.length-1,start=Math.max(0,cursor-31),history=slot.history.slice(start,cursor+33).map(sessionPage);
  if(!history.length||history.some(p=>!p))continue;tabs.push({id:slot.id,history,cursor:cursor-start,pinned:slot.pinned===true});seen.add(slot.id);
 }
 if(!tabs.length)return null;return{version:1,tabs,active:seen.has(input.active)?input.active:tabs[0].id,section:['files','projects','activity','schedules','system'].includes(input.section)?input.section:'files',sidebarKnowledge:input.sidebarKnowledge===true,...(isBoxRef(input.activeBox)?{activeBox:input.activeBox}:{})};
}
export function loadWorkspaceSession(storage){try{const text=storage.getItem('workspace-session');return text&&text.length<=256*1024?workspaceSession(JSON.parse(text)):null;}catch{return null;}}
export function saveWorkspaceSession(storage,value){const session=workspaceSession(value);if(!session)return false;const text=JSON.stringify(session);if(text.length>256*1024)return false;try{if(storage.getItem('workspace-session')!==text)storage.setItem('workspace-session',text);return true;}catch{return false;}}
