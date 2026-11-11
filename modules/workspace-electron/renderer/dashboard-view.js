import {Chart,LineController,LineElement,PointElement,LinearScale,CategoryScale,BarController,BarElement,Tooltip} from 'chart.js';
import {recentProjects} from './project-recents.js';
Chart.register(LineController,LineElement,PointElement,LinearScale,CategoryScale,BarController,BarElement,Tooltip);
const node=(tag,cls,text)=>{const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const date=v=>v?new Date(v).toLocaleString():'Unknown';
const clock=v=>new Date(v).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
const number=v=>Number.isFinite(v)?v.toLocaleString():'—';
const bytes=v=>Number.isFinite(v)?`${(v/1024**3).toFixed(1)} GB`:'Unknown';
const human=v=>String(v||'unknown').replaceAll('_',' ');
export const groupEdits=items=>{const groups=new Map();for(const item of items){const key=JSON.stringify([item.root,item.path||item.name]);const previous=groups.get(key);if(previous)previous.edits++;else groups.set(key,{...item,edits:1});}return [...groups.values()];};
export const projectItem=p=>({id:p.project_id,name:p.name,description:p.description,status:p.status,posture:p.work_state||'unclassified',owner:'loom',at:p.updated_at});
export const jobItem=j=>({id:j.job_id||j.id,name:j.display_name||human(j.job_type),description:j.failure_code==='codex_needs_input'?'Coding agent is waiting for clarification':j.failure_code?human(j.failure_code):'',owner:'jobs',status:j.manual_action_required?'requires_manual_action':j.status,at:j.updated_at});
export const scheduleItem=s=>({id:s.schedule_id,name:s.display_name||s.schedule_key,owner:'loom',status:s.status,enabled:s.status==='active',expression:s.schedule_expr,timezone:s.timezone,next:s.next_fire_at,last:s.last_fire_at,result:typeof s.last_result==='object'?s.last_result?.status||'unknown':s.last_result||'unknown'});
export const nativeItem=(s,source={},management)=>{const profile=s.profile||source.profile,m=management?.items?.find(j=>j.id===s.id&&(j.profile||management.profile||'mina')===profile);return{id:s.id,name:m?.name||s.display_name||`${profile||'Hermes'} · ${s.id}`,owner:'hermes',host:s.host||source.host,profile,managed:Boolean(s.project)||Boolean(m?.managed),controllable:Boolean(m&&!m.managed&&!m.terminal),status:s.native_state,enabled:s.enabled,next:s.next_run_at,last:s.last_run_at,result:s.last_result,expression:s.schedule?.expression,timezone:s.schedule?.timezone||m?.timezone||management?.timezone};};

export class DashboardView{
 constructor({container,box=()=>'box',select,inventory,icons,button}){Object.assign(this,{container,box,select,inventory,icons,button});this.charts=[];this.samples=[];window.addEventListener('workspace-theme-changed',()=>this.retheme());}
 retheme(){for(const chart of this.charts){this.chartColors(chart);chart.update('none');}}
 chartColors(chart){
  const css=getComputedStyle(document.documentElement),color=role=>css.getPropertyValue(`--${role}`).trim();
  for(const scale of Object.values(chart.options.scales)){scale.ticks.color=color('muted');if(scale.grid.display!==false)scale.grid.color=color('chart-grid');}
  Object.assign(chart.options.plugins.tooltip,{backgroundColor:color('raised'),titleColor:color('heading'),bodyColor:color('text'),borderColor:color('border'),borderWidth:1,cornerRadius:8});
  for(const dataset of chart.data.datasets){dataset.borderColor=color(dataset.tone||'info');if(chart.config.type==='bar')dataset.backgroundColor=dataset.borderColor;}
 }
 destroy(){for(const c of this.charts)c.destroy();this.charts=[];}
 band(title,subtitle){const n=node('section','dashboard-band');const h=node('div','dashboard-band-heading');h.append(node('h2','',title));if(subtitle)h.append(node('small','',subtitle));n.append(h);this.container.append(n);return n;}
 action(label,run,cls='dashboard-link'){const b=node('button',cls,label);b.onclick=run;return b;}
 stats(parent,counts,entries,owner){const strip=node('div','dashboard-stats');for(const [key,label,color]of entries){const b=this.action('',()=>owner==='workers'?this.inventory('workers',{workerMode:key}):this.inventory(key,{owner}),'dashboard-stat');b.dataset.tone=color||key;b.append(node('strong','',number(counts?.[key])),node('span','',label));strip.append(b);}parent.append(strip);}
 quality(block){if(!block?.available)return 'Unavailable';return `${block.stale?'Stale · ':block.cached?'Cached · ':''}${date(block.observedAt)}`;}
 block(name,block){const n=this.band(name,this.quality(block));if(!block?.available)n.append(node('p','dashboard-empty',block?.error||'Loading owner evidence…'));else if(block.error)n.append(node('p','section-qualification warning',block.error));return n;}
 render(section,blocks,local){this.destroy();const scroll=this.container.scrollTop;this.container.replaceChildren();this[section](blocks,local);this.container.scrollTop=scroll;this.icons(this.container);}
 chart(parent,{type='line',labels,series,label,onClick,ymax}){
  const frame=node('div','dashboard-chart'),canvas=node('canvas');canvas.setAttribute('role','img');canvas.setAttribute('aria-label',label);canvas.textContent=label;frame.append(canvas);parent.append(frame);
  const c=new Chart(canvas,{type,data:{labels,datasets:series.map(s=>({...s,borderWidth:2,pointRadius:0,tension:.18,spanGaps:false}))},options:{responsive:true,maintainAspectRatio:false,animation:false,normalized:true,plugins:{legend:{display:false},tooltip:{enabled:true}},scales:{x:{grid:{display:false},ticks:{maxTicksLimit:6,maxRotation:0},border:{display:false}},y:{beginAtZero:true,...ymax?{max:ymax}:{},grid:{},ticks:{maxTicksLimit:4,precision:0},border:{display:false}}},onClick:(_e,items)=>{if(onClick&&items.length)onClick(items[0].index);}}});this.chartColors(c);c.update('none');this.charts.push(c);
 }
 projects(b){
  const block=b.projects,d=block?.data,n=this.block('Work states',block);if(!d)return;
  this.stats(n,d.counts,[['working','Working','sage'],['passive','Passive','blue'],['archived','Archived','neutral'],['unclassified','Unclassified','amber']]);
  n.append(this.action(`All ${number(d.total)} projects`,()=>this.inventory('all')));
  const recent=recentProjects(localStorage,this.box());
  if(recent.length){const r=this.band('Recently opened','On this Mac');const list=node('div','dashboard-recents');for(const p of recent.slice(0,6))list.append(this.action(p.name,()=>this.select({...p,owner:'loom'})));r.append(list);}
  const current=(d.items||[]).filter(p=>p.status!=='archived'),gallery=this.band('Current projects','Recently updated in the loaded registry page · archives remain in All projects'),grid=node('div','project-gallery');
  for(const p of current.slice(0,8)){const item=projectItem(p),card=this.action('',()=>this.select(item),'project-tile');card.dataset.tone=item.posture;const top=node('div','project-tile-heading');top.append(node('i',''));top.append(node('strong','',p.name));card.append(top,node('p','',p.description||p.slug),node('small','',`${item.posture==='unclassified'?'Work state not set':human(item.posture)} · ${date(p.updated_at)}`));grid.append(card);}gallery.append(grid);if(!current.length)gallery.append(node('p','dashboard-empty','No current projects in this registry page'));
  const runtime=b.runtime?.data,services=runtime?.services,passive=new Set((d.items||[]).filter(p=>p.work_state==='passive').map(p=>p.project_id));
  const band=this.band('Passive infrastructure','Stored service observations; no probes are started');
  const rows=(services?.items||[]).filter(s=>passive.has(s.project_id));
  for(const s of rows){const p=(d.items||[]).find(p=>p.project_id===s.project_id);band.append(this.resource(`${p.name} · ${s.name}`,s.health==='unknown'?'not observed':s.health,s.observed_at,()=>this.select(projectItem(p))));}
  if(!rows.length)band.append(node('p','dashboard-empty',services?'No service observations for passive projects in this page':'Service evidence unavailable'));
  if(services?.complete===false)band.append(node('p','section-qualification','Service inventory is bounded; missing observations are not proof of absence.'));
 }
 resource(name,status,at,run){const r=this.action('',run,'dashboard-resource');const mark=node('span','dashboard-mark');mark.dataset.state=status;r.append(mark,node('strong','',name),node('span','',human(status)),node('small','',at?date(at):'No dated evidence'));return r;}
 activity(b,local){
  const block=b.jobs,d=block?.data,n=this.block('Main jobs',block);
  if(d){this.stats(n,d.counts,[['running','Running','blue'],['queued','Queued','amber'],['attention','Needs attention','red']],'jobs');
   const buckets=d.buckets||[],graph=this.band('Job outcomes','Latest retained terminal result per job · not historical queue depth');
   if(buckets.length)this.chart(graph,{type:'bar',labels:buckets.map(v=>clock(v.at)),label:`Job outcomes, ${date(d.window?.start)} to ${date(d.window?.end)}`,series:[{label:'Succeeded',data:buckets.map(v=>v.succeeded||0),tone:'success'},{label:'Failed',data:buckets.map(v=>(v.failed||0)+(v.timed_out||0)),tone:'danger'},{label:'Cancelled',data:buckets.map(v=>v.cancelled||0),tone:'chart-neutral'}],onClick:i=>this.inventory('all',{bucket:buckets[i].at})});
   else graph.append(node('p','dashboard-empty','No retained outcome samples in this window'));
   const attention=(d.items||[]).filter(j=>j.manual_action_required||['failed','timed_out'].includes(j.status)&&j.failure_attention_status==='active');
   const a=this.band('Needs attention','Unresolved Main job attention');for(const j of attention.slice(0,6)){const i=jobItem(j);a.append(this.resource(i.name,i.status,i.at,()=>this.select(i)));}if(!attention.length)a.append(node('p','dashboard-empty',d.counts?.attention?'Open all attention records to inspect older failures':'No unresolved job attention reported'));a.append(this.action('View job records',()=>this.inventory('all')));
  }
  const sync=this.band('This Mac','Local save and Main acceptance are separate');
  if(local?.summary){const c=local.summary.counts;this.stats(sync,{pending:Object.entries(c).filter(([k])=>!['accepted','held','conflict','resolved'].includes(k)).reduce((s,[,v])=>s+v,0),held:(c.held||0)+(c.conflict||0),accepted:c.accepted||0},[['pending','Awaiting Main','amber'],['held','Held for review','red'],['accepted','Accepted by Main','sage']],'local');sync.append(node('small','section-qualification',`${number(local.summary.placement_pending)} local placements pending · ${number(local.summary.local_recovery)} local recovery holds`));}
  else sync.append(node('p','dashboard-empty','Local summary loading'));
  const recent=this.band('Recent edits','This Mac');for(const i of groupEdits(local?.items||[]).slice(0,5))recent.append(this.resource(`${i.name}${i.edits>1?' · '+i.edits+' edits':''}`,i.status,i.at,()=>this.select(i)));recent.append(this.action('All recent activity',()=>this.inventory('all')));
  n.after(sync,recent);const graph=[...this.container.children].find(b=>b.querySelector('h2')?.textContent==='Job outcomes');if(graph){this.container.append(graph);if(!(d?.buckets||[]).some(b=>b.succeeded||b.failed||b.timed_out||b.cancelled))graph.querySelector('.dashboard-chart')?.remove();}
 }
 schedules(b){
  const block=b.schedules,d=block?.data,n=this.block('LOOM schedules',block),h=b.hermes?.data;
  if(d)this.stats(n,d.counts,[['active','Active','sage'],['paused','Paused','amber'],['disabled','Disabled','neutral']]);
  const items=(d?.items||[]).map(scheduleItem),native=(h?.availability==='available'?h.jobs||[]:[]).map(s=>nativeItem(s,h.source,b.hermesManagement?.data));
  const occurrences=(d?.occurrences||[]).map(o=>({at:o.at,item:items.find(s=>s.id===o.schedule_id)})).filter(o=>o.item);
  for(const item of native)if(item.enabled&&item.next)occurrences.push({at:item.next,item});
  occurrences.sort((a,b)=>a.at.localeCompare(b.at));const groups=new Map();for(const o of occurrences){const day=new Date(o.at).toLocaleDateString([],{weekday:'short',month:'short',day:'numeric'});if(!groups.has(day))groups.set(day,[]);groups.get(day).push(o);}
  const timeline=this.band('Upcoming occurrences',`Display timezone: ${Intl.DateTimeFormat().resolvedOptions().timeZone}`),grid=node('div','schedule-timeline');
  for(const [day,os]of groups){const column=node('section','schedule-day');column.append(node('h3','',day));for(const {at,item}of os.slice(0,30)){const btn=this.action('',()=>this.select(item),'schedule-occurrence');btn.dataset.owner=item.owner;btn.append(node('time','',clock(at)),node('strong','',item.name),node('small','',`${item.owner==='hermes'?'Hermes · '+item.profile:'LOOM'} · ${item.timezone||'Timezone not supplied'}`));column.append(btn);}if(os.length>30)column.append(node('small','',`${os.length-30} more occurrences in this day`));grid.append(column);}timeline.append(grid);
  if(!occurrences.length)timeline.append(node('p','dashboard-empty','No supplied upcoming occurrences'));
  timeline.append(node('p','section-qualification','Hermes shows its supplied next run only; LOOM shows bounded recurrence previews. These are plans, not execution guarantees.'));
  if(d?.preview_limited||d?.complete===false)timeline.append(node('p','section-qualification warning','Preview or schedule page is bounded. Open the complete inventory for more routines.'));
  if(h?.availability!=='available')timeline.append(node('p','section-qualification warning','Hermes inventory unavailable; this is not an empty schedule list.'));
  const results=this.band('Last reported results','Dispatch is not task completion');for(const i of [...items,...native].filter(i=>i.last).sort((a,b)=>b.last.localeCompare(a.last)).slice(0,6))results.append(this.resource(i.name,i.result,i.last,()=>this.select(i)));results.append(this.action('All schedules',()=>this.inventory('all')));
 }
 system(b){
  const block=b.host,d=block?.data,host=d?.host,n=this.block('Main resources',block);
  if(host){if(!block.stale&&(!this.samples.length||this.samples.at(-1).at!==host.generated_at)){this.samples.push({at:host.generated_at,cpu:host.cpu?.available?host.cpu.value:null,memory:host.memory?.available?host.memory.value:null,temperature:host.temperature?.available?host.temperature.value:null});this.samples=this.samples.slice(-720);}
   const plots=node('div','host-plots');n.append(plots);
   for(const [key,label,tone,unit,max]of [['cpu','CPU','info','%',100],['memory','Memory','success','%',100],['temperature','Temperature','warning','°C',undefined]]){const plot=node('section','host-plot'),v=host[key];plot.append(node('h3','',label),node('strong','host-value',v?.available?`${v.value.toFixed(1)}${unit}`:key==='cpu'?'Warming up':'Unavailable'));plots.append(plot);this.chart(plot,{labels:this.samples.map(s=>clock(s.at)),series:[{label,data:this.samples.map((s,i)=>i&&Date.parse(s.at)-Date.parse(this.samples[i-1].at)>20_000?null:s[key]),tone}],label:`${label}, observed in this System session`,ymax:max});}
   n.append(node('p','section-qualification',`Live session trace · ${this.samples.length} samples · no collection while this page is hidden`));
   const disks=this.band('Disk capacity','Filesystem counters; no folder scans'),seen=new Set();for(const [name,m]of [['Storage',host.storage],['System',d.system_disk]]){const c=m?.capacity;if(!c){disks.append(node('p','dashboard-empty',`${name} capacity unavailable`));continue;}if(seen.has(c.filesystem))continue;seen.add(c.filesystem);const line=node('div','capacity-row');line.append(node('strong','',name),node('span','',`${bytes(c.used_bytes)} used · ${bytes(c.available_bytes)} available / ${bytes(c.total_bytes)}`));const meter=node('meter','');meter.min=0;meter.max=c.total_bytes;meter.value=c.used_bytes;meter.setAttribute('aria-label',`${name} used bytes`);line.append(meter,node('small','',c.path));disks.append(line);}
  }
  const runtime=b.runtime?.data,r=this.block('Services and workers',b.runtime);
  if(runtime){const workers=runtime.workers;this.stats(r,workers?.counts,[['running','Running workers','blue'],['manual','Manual','neutral'],['scheduled','Scheduled','sage'],['paused','Paused','amber']],'workers');
   const serviceRows=runtime.services?.items||[];for(const s of serviceRows.slice(0,6))r.append(this.resource(s.name,s.health,s.observed_at,()=>this.inventory('services')));
   r.append(this.action(`${number(runtime.services?.total)} services · ${number(workers?.total)} workers`,()=>this.inventory('workers')));
   for(const [owner,value]of [['Services',runtime.services],['Workers',workers]])if(!value)r.append(node('p','section-qualification warning',`${owner} unavailable`));
  }
  const p=this.block('Protection',b.protection),protection=b.protection?.data;
  if(protection){for(const [label,v]of [['Local backup',protection.local],['Cloud snapshot',protection.cloud]]){const status=!v?.available?'unavailable':Date.now()-Date.parse(v.source_updated_at)>v.stale_after_seconds*1000?'stale':v.state;p.append(this.resource(label,status,v?.last_success_at,()=>this.inventory('protection')));}if(protection.findings?.open)p.append(node('p','section-qualification warning',`${protection.findings.open} open protection findings · ${protection.findings.critical} critical`));p.append(node('p','section-qualification','Notes cohort recovery and this client’s unsent edits require separate evidence; a recent backup time alone does not prove their protection.'));}
 }
}
