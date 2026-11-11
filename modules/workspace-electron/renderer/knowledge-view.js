import Graph from 'graphology';
import Sigma from 'sigma';
import {circular} from 'graphology-layout';
import {categories,filterKnowledge,knowledgeGraph,knowledgeTypes,observedProperties,propertyCategories,KNOWLEDGE_CLASSES,knowledgeOpenOptions,graphCamera,graphViewKey} from '../src/knowledge-model.mjs';
import {selectControl} from './choice-controls.js';
const el=(tag,className,text)=>{const n=document.createElement(tag);n.className=className||'';if(text!==undefined)n.textContent=text;return n;};
export class KnowledgeView{
 constructor({api,scope,scopeChoices=[],changeScope,changed,mode='categories',restore,center,open,folder,icon,notice}){
  Object.assign(this,{api,scope,scopeChoices,changeScope,changed,mode,center,open,folder,icon,notice});this.filters={history:true,...restore?.filters};this.page=Number.isInteger(restore?.page)?Math.max(0,restore.page):0;this.graphMode=restore?.graphMode;this.groupBy=typeof restore?.groupBy==='string'?restore.groupBy:'';
  const camera=graphCamera(restore?.graph?.camera);this.graphState=camera&&typeof restore.graph.key==='string'?{key:restore.graph.key,camera,focus:typeof restore.graph.focus==='string'?restore.graph.focus:null}:null;
  this.alive=true;this.dom=el('section','knowledge-view');this.result={items:[],qualification:'Showing available metadata; not a complete file inventory.'};this.loading=true;this.render();this.onTheme=()=>{if(this.alive&&this.result)this.render();};window.addEventListener('workspace-theme-changed',this.onTheme);this.ready=this.load().catch(e=>this.fail(e));
 }
 snapshot(){if(this.renderer)this.graphState={key:this.activeGraphKey,camera:graphCamera(this.renderer.getCamera().getState()),focus:this.neighborFocus||null};return{mode:this.mode,filters:{...this.filters},page:this.page,graphMode:this.graphMode,groupBy:this.groupBy,graph:this.graphState?{...this.graphState,camera:{...this.graphState.camera}}:null};}
 fail(error){if(this.alive){this.stopGraph();this.dom.replaceChildren(el('p','section-qualification warning',error.message));}}
 stopGraph(){cancelAnimationFrame(this.graphFrame);this.graphFrame=null;this.snapshot();this.renderer?.kill();this.renderer=null;}
 destroy(){this.stopGraph();this.alive=false;clearTimeout(this.timer);window.removeEventListener('workspace-theme-changed',this.onTheme);}
 async load(refresh=false,more=false){
  const sequence=this.sequence=(this.sequence||0)+1,compact=this.mode!=='graph',input={...this.scope,compact,quick:true};this.loading=true;
  if(!refresh&&!more){const cached=await this.api.knowledgeCatalog({...input,cacheOnly:true});if(!this.alive||sequence!==this.sequence)return;this.result=cached;this.render();}
  let result=await this.api.knowledgeCatalog({...input,refresh,more});if(!this.alive||sequence!==this.sequence)return;this.result=result;this.loading=Boolean(compact&&result.can_load_more&&!result.error);this.render();
  // Fill the bounded metadata view progressively, never before first paint or
  // after leaving it. This reads metadata only, not note bodies or embeddings.
  let remainingBatches=10;
  while(compact&&this.alive&&sequence===this.sequence&&!document.hidden&&result.can_load_more&&!result.error&&remainingBatches-->0){
   result=await this.api.knowledgeCatalog({...input,more:true});if(!this.alive||sequence!==this.sequence)return;this.result=result;this.loading=Boolean(result.can_load_more&&!result.error);this.render();
  }
  this.loading=false;if(this.alive&&sequence===this.sequence)this.render();
 }
 button(name,symbol,run){const b=el('button','icon');b.type='button';b.append(this.icon(symbol));b.setAttribute('aria-label',name);b.dataset.tooltip=name;b.onclick=event=>Promise.resolve().then(()=>run(event)).catch(e=>this.fail(e));return b;}
 navigate(run,event){event?.preventDefault?.();this.changed?.(this.snapshot());return Promise.resolve().then(()=>run(knowledgeOpenOptions(event))).catch(error=>this.notice?this.notice(error.message):this.fail(error));}
 bindOpen(button,run){button.onclick=event=>this.navigate(run,event);button.onauxclick=event=>{if(event.button===1)this.navigate(run,event);};}
 render(){
  if(!this.alive)return;const focused=this.dom.contains(document.activeElement)&&document.activeElement.getAttribute('aria-label')==='Filter knowledge'?{start:document.activeElement.selectionStart,end:document.activeElement.selectionEnd}:null;this.stopGraph();this.changed?.(this.snapshot());
  const tools=el('div','view-controls'),heading=el('h2','',this.mode==='graph'?'Connections':'Knowledge');tools.append(heading);
  if(this.changeScope&&this.scopeChoices.length){const scope=selectControl({label:'Knowledge folder',name:'scope',value:JSON.stringify(this.scope),items:this.scopeChoices});scope.addEventListener('change',()=>this.changeScope(JSON.parse(scope.querySelector('input').value)));tools.append(scope);}
  const layout=selectControl({label:'Knowledge layout',name:'layout',value:this.mode,items:[{value:'categories',label:'Categories'},{value:'table',label:'Files'},{value:'graph',label:'Graph'}]});layout.addEventListener('change',()=>{const wasGraph=this.mode==='graph';this.mode=layout.querySelector('input').value;this.page=0;this.render();if(wasGraph!==(this.mode==='graph'))this.load().catch(e=>this.fail(e));});tools.append(layout);
  const search=el('input','table-filter');search.placeholder='Filter knowledge';search.setAttribute('aria-label','Filter knowledge');search.value=this.filters.query||'';search.oninput=()=>{this.filters.query=search.value;clearTimeout(this.timer);this.timer=setTimeout(()=>{const pos=search.selectionStart;this.page=0;this.render();const input=this.dom.querySelector('[aria-label="Filter knowledge"]');input.focus();input.setSelectionRange(pos,pos);},180);};tools.append(search);
  tools.append(this.button('Refresh known catalog','refresh-cw',()=>this.load(true)));
  const history=el('label','table-header-option'),check=el('input');check.type='checkbox';check.checked=this.filters.history;check.onchange=()=>{this.filters.history=check.checked;this.page=0;this.render();};history.append(check,document.createTextNode('History'));tools.append(history);
  const filtered=filterKnowledge(this.result.items,this.filters),summary=el('div','knowledge-summary');summary.append(el('strong','',filtered.length.toLocaleString()),el('span','','loaded files'),el('span','section-qualification',`${this.scope.root==='mac'?'Mac Box':this.scope.root==='box'?'Main Box':this.scope.root}${this.scope.path?' / '+this.scope.path:''}`));if(this.loading)summary.append(el('small','section-qualification','Updating metadata…'));
  const coverage=el('p','section-qualification',this.result.qualification+(this.result.error?' '+this.result.error:''));this.dom.replaceChildren(tools,summary,coverage);
  const source=this.result.items.filter(d=>d.observed_at||d.metadata_origin==='local').length,hints=this.result.items.filter(d=>!d.observed_at&&d.metadata_origin!=='local'&&d.metadata_version>0).length;
  this.dom.append(el('p','section-qualification knowledge-provenance',`${source.toLocaleString()} source observations · ${hints.toLocaleString()} older index hints · ${(this.result.items.length-source-hints).toLocaleString()} names only`));
  if(this.result.can_load_more){const more=el('button','base-link','Load more known files');more.disabled=this.loading;more.onclick=()=>{more.disabled=true;this.load(false,true).catch(e=>this.fail(e));};summary.append(more);}
  if(this.filters.type||this.filters.tag||this.filters.people||this.filters.property){const label=this.filters.property?`${this.filters.property}: ${this.filters.propertyMissing?'Unclassified / unknown':JSON.stringify(this.filters.propertyValue)}`:this.filters.type||this.filters.tag||this.filters.people,clear=this.button(`Clear filter: ${label}`,'x',()=>{for(const key of ['type','tag','people','property','propertyValue','propertyMissing'])delete this.filters[key];this.page=0;this.render();});summary.append(el('span','knowledge-filter-label',label),clear);}
  if(this.mode==='categories')this.categoryView(filtered);else if(this.mode==='graph')this.graphView(filtered);else this.tableView(filtered);
  if(focused){search.focus({preventScroll:true});search.setSelectionRange(focused.start,focused.end);}
 }
 categoryView(items){
  const properties=observedProperties(this.result.items);if(this.groupBy&&!properties.includes(this.groupBy))properties.push(this.groupBy);
  const tools=el('div','view-controls'),group=selectControl({label:'Group categories by',name:'category-property',value:this.groupBy,items:[{value:'',label:'Subjects, types and people'},...properties.map(value=>({value,label:value}))]});tools.append(group);group.addEventListener('change',()=>{this.groupBy=group.querySelector('input').value;this.render();});this.dom.append(tools);
  const groups=categories(items),content=el('div','knowledge-categories');
  const property=this.groupBy?propertyCategories(items,this.groupBy):null;
  if(property)this.dom.append(el('p','section-qualification',`${property.observed} files with source-observed values · ${property.hints} with values without a source observation · ${property.missing} unclassified / unknown for ${this.groupBy}. Counts cover loaded metadata only; missing values do not establish absence in source.`));
  else this.dom.append(el('p','section-qualification',`${items.filter(item=>!knowledgeTypes(item).length).length} files without a usable type / kind in loaded metadata. Unclassified or unobserved metadata is not inferred.`));
  const sections=property?[['property',this.groupBy,property.rows]]:[['subject','Subjects',groups.subject],['type','Types',groups.type],['people','People',groups.people]];
  for(const [kind,title,rows]of sections){const section=el('section','knowledge-category'),limit=this.categoryLimits?.get(title)||12;section.append(el('h3','',`${title} · ${rows.length}`));
   const choose=(value,missing=false)=>{if(kind==='property'){this.filters.property=this.groupBy;this.filters.propertyMissing=missing;if(missing)delete this.filters.propertyValue;else this.filters.propertyValue=value;}else this.filters[kind==='subject'?'tag':kind]=kind==='subject'?'subject/'+value:value;this.mode='table';this.page=0;this.render();};
   for(const row of rows.slice(0,limit)){const b=el('button','category-row'),label=kind==='people'?row.value.replace(/^\[\[|\]\]$/g,'').split('|').pop().split('/').pop():kind==='property'?JSON.stringify(row.value):row.value;b.append(el('span','',label),el('span','category-count',row.count));b.onclick=()=>choose(row.value);section.append(b);}
   if(rows.length>limit){const more=el('button','base-link',`Show more ${title.toLowerCase()} (${Math.min(limit,rows.length)} of ${rows.length})`);more.onclick=()=>{(this.categoryLimits||=new Map()).set(title,limit+48);this.render();};section.append(more);}
   if(property?.missing){const missing=el('button','category-row');missing.append(el('span','','Unclassified / unknown'),el('span','category-count',property.missing));missing.onclick=()=>choose(undefined,true);section.append(missing);}
   if(!rows.length)section.append(el('p','section-qualification','No classified notes in the loaded metadata.'));content.append(section);
  }this.dom.append(content);
 }
 tableView(items){
  if(!this.loading)this.page=Math.min(this.page,Math.max(0,Math.ceil(items.length/100)-1));const list=el('div','knowledge-notes');for(const item of items.slice(this.page*100,(this.page+1)*100)){const b=el('button','knowledge-note');b.append(this.icon('file-text'),el('span','',item.path.split('/').pop().replace(/\.md$/i,'')),el('small','',item.path),el('span','category-count',item.pending?'Local edit':knowledgeTypes(item).join(', ')||'Unclassified / unknown'));this.bindOpen(b,options=>this.open(item.path,item.root,options));list.append(b);}
  if(!items.length)list.append(el('p','empty','No matching known notes.'));this.dom.append(list);
  const paging=el('div','view-controls');if(this.page)paging.append(this.button('Previous notes','chevron-left',()=>{this.page--;this.render();}));if((this.page+1)*100<items.length)paging.append(this.button('Next notes','chevron-right',()=>{this.page++;this.render();}));this.dom.append(paging);
 }
 graphView(items){
  const controls=el('div','view-controls'),mode=selectControl({label:'Graph scope',name:'graphscope',value:this.graphMode|| (this.center?'local':'folders'),items:[...this.center?[{value:'local',label:'Local links'},{value:'two',label:'Two hops'}]:[],{value:'notes',label:'Notes in scope'},{value:'folders',label:'Folder overview'}]});controls.append(mode);mode.addEventListener('change',()=>{this.graphMode=mode.querySelector('input').value;this.render();});this.dom.append(controls);
  const selected=mode.querySelector('input').value,data=knowledgeGraph(items,{center:['local','two'].includes(selected)?this.center:null,hops:selected==='two'?2:1,grouped:selected==='folders'}),caption=el('p','section-qualification',`${data.nodes.length} nodes · ${data.edges.length} links · ${data.unresolved.length} unresolved · ${data.ambiguous.length} ambiguous${data.truncated?' · bounded overview':''}`);this.dom.append(caption);
  const cameraButtons=[this.button('Fit graph','maximize',()=>this.renderer?.getCamera().reset({duration:160})),this.button('Reset graph','rotate-ccw',async()=>{setFocus(null);await this.renderer?.getCamera().reset({duration:160});this.render();}),this.button('Zoom in','zoom-in',()=>this.renderer?.getCamera().zoomIn({duration:160})),this.button('Zoom out','zoom-out',()=>this.renderer?.getCamera().zoomOut({duration:160}))];for(const button of cameraButtons){button.disabled=true;controls.append(button);}
  const stage=el('div','knowledge-graph');this.dom.append(stage);if(!data.nodes.length){stage.append(el('p','empty','No link observations in this scope.'));return;}
  const graph=new Graph({type:'directed',multi:false}),ordered=[...data.nodes].sort((a,b)=>a.id.localeCompare(b.id));for(const n of ordered)graph.addNode(n.id,{...n,label:n.label,size:n.group?Math.min(18,4+Math.sqrt(n.count)):5,color:KNOWLEDGE_CLASSES[n.classification].color,x:0,y:0});for(const e of data.edges)if(!graph.hasEdge(e.from,e.to))graph.addEdge(e.from,e.to,{size:.6,color:'#888582'});circular.assign(graph);
  const key=graphViewKey(this.scope,selected,this.center,this.filters),saved=this.graphState?.key===key?this.graphState:null;this.neighborFocus=saved?.focus&&graph.hasNode(saved.focus)?saved.focus:null;
  let hovered=null,neighbors=null;
  const refreshFocus=()=>{const focus=hovered||this.neighborFocus;neighbors=focus?new Set([focus,...graph.neighbors(focus)]):null;this.renderer?.refresh();};
  const focus=selectControl({label:'Neighbour focus',name:'neighbor-focus',value:this.neighborFocus||'',items:[{value:'',label:'All connections'},...ordered.map(n=>({value:n.id,label:`${n.label} · ${graph.neighbors(n.id).length} neighbours · ${n.path}`}))]});controls.append(focus);
  const setFocus=node=>{this.neighborFocus=node;hovered=null;refreshFocus();this.changed?.(this.snapshot());};
  focus.addEventListener('change',()=>setFocus(focus.querySelector('input').value||null));
  const legend=el('div','view-controls knowledge-legend');legend.setAttribute('aria-label','Graph classifications');for(const [kind,style]of Object.entries(KNOWLEDGE_CLASSES)){const count=data.nodes.filter(n=>n.classification===kind).length;if(!count)continue;const item=el('span','knowledge-legend-item'),swatch=el('span','knowledge-class-swatch');swatch.style.backgroundColor=style.color;swatch.setAttribute('aria-hidden','true');item.append(swatch,el('span','',`${style.label} · ${count}`));legend.append(item);}this.dom.insertBefore(legend,stage);this.dom.insertBefore(el('p','section-qualification','Colours reflect declared type / kind or history paths; folder overviews show folders. Unclassified / unknown does not establish absence in source.'),stage);
  this.graphFrame=requestAnimationFrame(()=>{this.graphFrame=null;if(!this.alive||!stage.isConnected)return;try{
   const labelColor=getComputedStyle(document.documentElement).getPropertyValue('--text').trim();
   this.renderer=new Sigma(graph,stage,{settings:{itemSizesReference:'screen',autoRescaleContent:'nodes',stagePadding:40,labelRenderedSizeThreshold:0},nodeReducer:node=>({labelColor,opacity:neighbors&&!neighbors.has(node)?0.18:1,labelVisibility:neighbors?neighbors.has(node)?'visible':'hidden':data.nodes.length<=30?'visible':'auto'}),edgeReducer:edge=>({opacity:neighbors&&!graph.extremities(edge).includes(hovered||this.neighborFocus)?0.06:1})});
   this.activeGraphKey=key;const camera=this.renderer.getCamera();if(saved)camera.setState(saved.camera);camera.on('updated',()=>this.changed?.(this.snapshot()));for(const button of cameraButtons)button.disabled=false;refreshFocus();this.changed?.(this.snapshot());
   const openNode=(node,event)=>{const n=graph.getNodeAttributes(node);this.navigate(options=>n.group?this.folder(n.root,n.path,options):this.open(n.path,n.root,options),event?.original||event);};
   this.renderer.on('clickNode',({node,event})=>{if(event.original?.button!==1)openNode(node,event);});
   // Sigma 4 emits downNode for middle clicks, but its captor omits mouseup.
   let middleNode=null;stage.addEventListener('pointerdown',()=>{middleNode=null;},{capture:true});this.renderer.on('downNode',({node,event})=>{if(event.original?.button===1){middleNode={node,x:event.original.clientX,y:event.original.clientY};event.original.preventDefault();event.preventSigmaDefault();}});
   stage.onauxclick=event=>{if(event.button!==1)return;const pressed=middleNode;middleNode=null;if(pressed&&Math.hypot(event.clientX-pressed.x,event.clientY-pressed.y)<=5)openNode(pressed.node,event);};
   this.renderer.on('enterNode',({node})=>{hovered=node;refreshFocus();});this.renderer.on('leaveNode',()=>{hovered=null;refreshFocus();});
  }catch(error){this.renderer?.kill();this.renderer=null;for(const button of cameraButtons)button.disabled=true;stage.replaceChildren(el('p','warning',`Graph rendering unavailable: ${error.message}`));}});
 }
}
