import {referenceParts} from './reference-parts.mjs';

export const isHistory=path=>path.split('/').some(p=>/^_?history$|^archive$|^_archived$/i.test(p));
const stem=path=>path.replace(/\.(?:md|markdown)$/i,''),key=d=>JSON.stringify([d.root,d.path]);
const scalar=value=>typeof value==='string'?Boolean(value.trim()):typeof value==='boolean'||typeof value==='number'&&Number.isFinite(value);
export const propertyValues=(item,property)=>[...new Set([].concat(Object.hasOwn(item.values||{},property)?item.values[property]:[]).filter(scalar))];
export function knowledgeTypes(item){const types=propertyValues(item,'type').filter(v=>typeof v==='string');return types.length?types:propertyValues(item,'kind').filter(v=>typeof v==='string');}
export function observedProperties(items){
 const names=new Set();for(const item of items)for(const name of Object.keys(item.values||{}))if(propertyValues(item,name).length)names.add(name);
 return [...names].sort((a,b)=>a.localeCompare(b));
}
export function propertyCategories(items,property){
 const groups=new Map();let missing=0,observed=0,hints=0;
 for(const item of items){const values=propertyValues(item,property);if(!values.length){missing++;continue;}if(item.observed_at||item.metadata_origin==='local')observed++;else hints++;
  for(const value of values){const id=JSON.stringify(value),row=groups.get(id)||{value,count:0};row.count++;groups.set(id,row);}}
 return{rows:[...groups.values()].sort((a,b)=>b.count-a.count||String(a.value).localeCompare(String(b.value))||(typeof a.value).localeCompare(typeof b.value)),missing,observed,hints};
}
export function categories(items){
 const groups={type:new Map(),subject:new Map(),people:new Map()};
 const add=(kind,value)=>{if(typeof value==='string'&&value.trim())groups[kind].set(value,(groups[kind].get(value)||0)+1);};
 for(const d of items){for(const v of knowledgeTypes(d))add('type',v);for(const tag of new Set(d.tags||[]))if(tag.startsWith('subject/'))add('subject',tag.slice(8));for(const v of propertyValues(d,'people'))add('people',v);}
 return Object.fromEntries(Object.entries(groups).map(([k,v])=>[k,[...v].sort((a,b)=>b[1]-a[1]||a[0].localeCompare(b[0])).map(([value,count])=>({value,count}))]));
}
export function filterKnowledge(items,{query='',tag='',type='',history=true,people='',category='',property='',propertyValue,propertyMissing=false}={}){
 const q=query.toLocaleLowerCase();return items.filter(d=>{const types=knowledgeTypes(d),t=types.join(' ').toLocaleLowerCase(),classified=!category||category==='subjects'&&(d.tags||[]).some(t=>t.startsWith('subject/'))||category==='people'&&(types.some(t=>t.toLocaleLowerCase()==='person')||propertyValues(d,'people').length)||category==='reading'&&(/book|reading|literature|reference/.test(t)||(d.tags||[]).some(t=>/reading|literature/.test(t)))||category==='concepts'&&/concept|idea|argument|theory/.test(t)||category==='history'&&isHistory(d.path)||category==='bases'&&/\.base$/i.test(d.path);return classified&&(history||!isHistory(d.path))&&(!q||[d.path,...d.aliases||[],JSON.stringify(d.values)].join(' ').toLocaleLowerCase().includes(q))&&(!tag||(d.tags||[]).some(t=>t===tag||t.startsWith(tag+'/')))&&(!type||types.includes(type))&&(!people||propertyValues(d,'people').includes(people))&&(!property||(propertyMissing?!propertyValues(d,property).length:propertyValues(d,property).includes(propertyValue)));});
}
export const KNOWLEDGE_CLASSES=Object.freeze({
 person:{label:'People',color:'#ad5984'},reading:{label:'Reading',color:'#bd862d'},concept:{label:'Concepts',color:'#368976'},project:{label:'Projects',color:'#4b87ba'},other:{label:'Other declared types',color:'#8870a8'},unclassified:{label:'Unclassified / unknown',color:'#96918c'},history:{label:'History',color:'#777776'},folder:{label:'Folders',color:'#528e97'}
});
export function knowledgeClass(item){
 if(item.group)return'folder';if(isHistory(item.path))return'history';const types=knowledgeTypes(item),tokens=new Set(types.join(' ').toLocaleLowerCase().split(/[\s/_-]+/));
 if(['person','people'].some(t=>tokens.has(t)))return'person';if(['book','reading','literature','reference'].some(t=>tokens.has(t)))return'reading';if(['concept','idea','argument','theory'].some(t=>tokens.has(t)))return'concept';if(tokens.has('project'))return'project';return types.length?'other':'unclassified';
}
export function knowledgeOpenOptions(event){const e=event?.original||event||{},modifier=Boolean(e.metaKey||e.ctrlKey),middle=e.button===1;return{newTab:middle||modifier,background:middle||modifier&&Boolean(e.shiftKey)};}
export function graphCamera(state){return state&&['x','y','angle','ratio'].every(k=>Number.isFinite(state[k]))&&state.ratio>0?{x:state.x,y:state.y,angle:state.angle,ratio:state.ratio}:null;}
// Scope and filter intent identify the same graph across metadata/theme paints.
export function graphViewKey(scope,mode,center,filters={}){return JSON.stringify([scope.root,scope.path||'',mode,['local','two'].includes(mode)&&center?[center.root,center.path]:null,...['query','tag','type','people','category'].map(k=>filters[k]||''),filters.history!==false,filters.property||'',filters.property?filters.propertyValue??null:null,Boolean(filters.property&&filters.propertyMissing)]);}
export function knowledgeGraph(items,{center,hops=1,limit=400,grouped=false}={}){
 const byPath=new Map(items.map(d=>[key(d),d])),names=new Map(),edges=[],ambiguous=[],unresolved=[];
 for(const d of items)for(const name of new Set([stem(d.path),stem(d.path).split('/').pop(),...d.aliases||[]])){const k=JSON.stringify([d.root,name.toLocaleLowerCase()]);if(!names.has(k))names.set(k,[]);names.get(k).push(key(d));}
 for(const d of items)for(const ref of d.forward||[]){let part;try{part=referenceParts(ref.target);}catch{continue;}if(part.external)continue;const target=part.target;if(!target)continue;
   const parent=d.path.split('/').slice(0,-1),normalize=p=>{const out=[];for(const x of p.split('/'))if(x==='..')out.pop();else if(x&&x!=='.')out.push(x);return out.join('/');};
   let matches=[];for(const p of [normalize([...parent,target].join('/')),normalize(target)]){const found=[p,p+'.md',p+'.markdown'].find(path=>byPath.has(JSON.stringify([d.root,path])));if(found){matches=[JSON.stringify([d.root,found])];break;}}
   if(!matches.length)matches=names.get(JSON.stringify([d.root,stem(target).toLocaleLowerCase()]))||[];
   if(matches.length===1){if(matches[0]!==key(d))edges.push({from:key(d),to:matches[0],kind:ref.kind||'link'});}else(matches.length?ambiguous:unresolved).push({root:d.root,path:d.path,target});
 }
 let selected=new Set(items.map(key));if(center){selected=new Set([JSON.stringify([center.root,center.path])]);for(let i=0;i<Math.min(2,hops);i++){const next=new Set(selected);for(const e of edges)if(selected.has(e.from)||selected.has(e.to)){next.add(e.from);next.add(e.to);}selected=next;}}
 let nodes=items.filter(d=>selected.has(key(d))).map(d=>({id:key(d),root:d.root,path:d.path,label:stem(d.path).split('/').pop(),history:isHistory(d.path),type:knowledgeTypes(d).join(', '),classification:knowledgeClass(d),pending:d.pending}));
 let links=edges.filter(e=>selected.has(e.from)&&selected.has(e.to));
 if(grouped&&!center){const groups=new Map(),groupKey=n=>JSON.stringify([n.root,n.path.split('/').slice(0,Math.min(3,n.path.split('/').length-1)).join('/')]);for(const n of nodes){const id=groupKey(n);const group=groups.get(id)||{id,root:n.root,path:JSON.parse(id)[1],label:JSON.parse(id)[1]||n.root,count:0,group:true,classification:'folder'};group.count++;groups.set(id,group);}const mapping=new Map(nodes.map(n=>[n.id,groupKey(n)]));links=links.map(e=>({...e,from:mapping.get(e.from),to:mapping.get(e.to)})).filter(e=>e.from!==e.to);nodes=[...groups.values()];}
 const truncated=nodes.length>limit;nodes=nodes.slice(0,limit);const ids=new Set(nodes.map(n=>n.id)),unique=new Map();for(const e of links)if(ids.has(e.from)&&ids.has(e.to))unique.set(JSON.stringify([e.from,e.to]),e);
 return{nodes,edges:[...unique.values()].slice(0,2000),unresolved,ambiguous,truncated};
}
