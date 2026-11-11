import {selectControl} from './choice-controls.js';
import {form} from './dialogs.js';
const el=(tag,className,text)=>{const n=document.createElement(tag);n.className=className||'';if(text!==undefined)n.textContent=text;return n;};
const display=v=>Array.isArray(v)?v.join(', '):v==null?'':typeof v==='object'?JSON.stringify(v):String(v);
export function interactiveBase({query,open,active,configure}){
 const dom=el('div','base-view');let index=0,offset=0,search='',sort,sequence=0,timer;
 const button=(name,symbol,run,disabled=false)=>{const b=el('button','icon');b.innerHTML=`<i data-lucide="${symbol}"></i>`;b.setAttribute('aria-label',name);b.dataset.tooltip=name;b.disabled=disabled;b.onclick=()=>Promise.resolve(run()).catch(error);return b;};
 const error=e=>{if(active())dom.replaceChildren(el('p','section-qualification warning',e.message));};
 const load=async({more=false,refresh=false}={})=>{
  const token=++sequence,result=await query({viewIndex:index,offset,query:search,sort,more,refresh});if(!active()||token!==sequence)return;
  const focused=dom.querySelector('input')===document.activeElement,selection=dom.querySelector('input')?.selectionStart;
  const tools=el('div','view-controls'),select=selectControl({label:'Base view',name:'view',value:index,items:result.views.map((v,i)=>({value:i,label:v.name}))});select.addEventListener('change',()=>{index=Number(select.querySelector('input').value);offset=0;sort=undefined;load().catch(error);});tools.append(select);
  const filter=el('input','table-filter');filter.placeholder='Filter notes';filter.setAttribute('aria-label','Filter Base notes');filter.value=search;filter.oninput=()=>{search=filter.value;offset=0;clearTimeout(timer);timer=setTimeout(()=>load().catch(error),180);};tools.append(filter);
  if(configure)tools.append(button('Configure Base view','sliders-horizontal',async()=>{
   const v=result.definition.views[index],fields=await form('Configure Base view',[{name:'type',label:'Layout',value:v.type,options:[{value:'table',label:'Table'},{value:'cards',label:'Cards'}]},{name:'columns',label:'Columns',value:(v.order||['file.name','file.folder']).join(', ')},{name:'group',label:'Group by property',value:typeof v.groupBy==='string'?v.groupBy:v.groupBy?.property||'',required:false},{name:'filter',label:'Filter expression',value:typeof v.filters==='string'?v.filters:'',required:false}],{submit:'Save view'});if(!fields)return;
   const changes={type:fields.type,order:fields.columns.split(',').map(v=>v.trim()).filter(Boolean),groupBy:fields.group?{property:fields.group,direction:'ASC'}:null};if(typeof v.filters==='string'||!v.filters)changes.filters=fields.filter||null;await configure(index,changes);offset=0;await load();
  }));
  tools.append(button('Refresh known catalog','refresh-cw',()=>load({refresh:true})),button('Previous rows','chevron-left',()=>{offset=Math.max(0,offset-100);return load();},!offset),button('Next rows','chevron-right',()=>{offset+=100;return load();},!result.hasMore),el('span','section-qualification',`${result.total.toLocaleString()} files`));
  if(result.canLoadMoreMetadata){const more=el('button','base-link','Load more known files');more.onclick=()=>{more.disabled=true;load({more:true}).catch(error);};tools.append(more);}
  const output=el('div',result.type==='cards'?'base-cards':'base-table-container');let previousGroup;
  const table=el('table','base-table'),head=el('tr'),body=el('tbody');for(const column of result.columns){const th=el('th'),b=el('button','base-link',column.name);b.onclick=()=>{sort=[{property:column.key,direction:sort?.[0]?.property===column.key&&sort[0].direction==='ASC'?'DESC':'ASC'}];offset=0;load().catch(error);};th.append(b);head.append(th);}table.append(head,body);
  for(const row of result.rows){
   if(result.groupProperty&&previousGroup!==row.group){previousGroup=row.group;if(result.type==='cards')output.append(el('h3','base-group',row.group));else{const tr=el('tr','base-group'),td=el('td','',row.group);td.colSpan=result.columns.length;tr.append(td);body.append(tr);}}
   const item=el(result.type==='cards'?'article':'tr',result.type==='cards'?'base-card':'');for(const [i,c]of result.columns.entries()){const cell=el(result.type==='cards'?'div':'td');if(!i){const link=el('button','base-link',display(row.values[c.key])||row.path);link.dataset.tooltip=`${row.root}/${row.path}`;bindNavigation(link,intent=>open(row.path,row.root,intent));cell.append(link);}else cell.textContent=(result.type==='cards'?c.name+': ':'')+display(row.values[c.key]);item.append(cell);}if(result.type==='cards')output.append(item);else body.append(item);
  }
  if(result.type!=='cards')output.append(table);if(!result.rows.length)output.append(el('p','empty','No notes in the current metadata coverage.'));
  dom.replaceChildren(tools,el('p','section-qualification',result.qualification),output);lucide.createIcons({root:tools});if(focused){filter.focus();filter.setSelectionRange(selection,selection);}
 };
 load().catch(error);return dom;
}
import {bindNavigation} from './navigation-intent.js';
