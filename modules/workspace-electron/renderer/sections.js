import { form, confirmAction } from './dialogs.js';
import {DashboardView,projectItem,jobItem,scheduleItem,nativeItem} from './dashboard-view.js';
import {scheduleTiming} from '../src/schedule-timing.mjs';
import { navigationIntent } from './navigation-intent.js';
import { projectProse, resolveProjectLink, projectExternalURL, alternateProjectNavigation } from './project-links.js';
import {rememberProject} from './project-recents.js';

const el = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text !== undefined) n.textContent = text; return n; };
const human = value => String(value ?? 'Unknown').replaceAll('_', ' ');
const when = value => value ? new Date(value).toLocaleString() : '—';
export const relativeTime = (value, now = Date.now()) => {
  if(!value||!Number.isFinite(Date.parse(value)))return '—';
  const seconds=(Date.parse(value)-now)/1000,absolute=Math.abs(seconds),unit=absolute<60?'second':absolute<3600?'minute':absolute<86400?'hour':'day';
  return new Intl.RelativeTimeFormat(undefined,{numeric:'auto'}).format(Math.round(seconds/({second:1,minute:60,hour:3600,day:86400}[unit])),unit);
};
const statusClass = value => /failed|critical|error|blocked|conflict/.test(value)?'danger':/manual|paused|warning/.test(value)?'attention':/^(ok|healthy|accepted|completed|succeeded)$/.test(value)?'good':'';

export class Sections {
  constructor({ api, box=()=>'box', boxLabel=box, container, navigation, icons, icon, notice, openFolder, focusFolder, timeline, openFile, lifecycle, exportSnapshot,workingCopy,openData }) {
    this.box=box;this.boxLabel=boxLabel;
    Object.assign(this, { api, container, navigation, icons, icon, notice, openFolder, focusFolder,timeline, openFile, lifecycle, exportSnapshot,workingCopy,openData });
    this.section = 'files'; this.filter = 'all'; this.query = ''; this.generation = 0; this.commands = [];
    this.dashboardMode='overview';this.dashboardBlocks={};this.viewMemory=new Map();
    this.dashboardView=new DashboardView({container:document.createElement('div'),box,select:item=>this.inspect({...item,scope:this.currentScope()}).catch(e=>this.notice(e.message)),inventory:(filter,extra)=>this.openInventory(filter,extra),icons:this.icons,button:this.button.bind(this)});
    this.api.onEvent?.(event=>{
      if(event.kind==='dashboard'&&event.token===this.dashboardToken&&this.dashboardActive){if(event.name==='projects'&&this.dashboardQuery!==this.query)return;this.dashboardBlocks[event.name]=event.value;this.paintDashboard();}
      if(event.kind==='dashboard-visibility'){this.windowHidden=!event.visible;if(this.windowHidden)this.stopDashboard();else if(this.section!=='files'&&this.section!=='project')this.showDashboard(this.section,{resume:true});}
    });
    document.addEventListener('visibilitychange',()=>{if(document.hidden)this.stopDashboard();else if(!this.windowHidden&&this.dashboardActive)this.refreshDashboard();});
  }

  currentScope(){return this.mainWide?{all_boxes:true}:{box:this.box()};}

  button(label, symbol, run, primary = false) {
    const b = el('button', primary ? 'action-button primary' : 'icon');
    b.type='button';
    b.dataset.tooltip = label; b.setAttribute('aria-label', label); b.append(this.icon(symbol));
    if (primary) b.append(el('span', '', label));
    b.onclick = event => Promise.resolve().then(() => run(event)).catch(error => this.notice(error.message)); return b;
  }

  async show(section, { refresh = false, ref } = {}) {
    if(this.api.dashboard&&section!=='project')return this.showDashboard(section,{refresh});
    this.stopDashboard();this.dashboardActive=false;
    if (section !== this.section) { this.filter = 'all'; this.query = ''; }
    this.section = section; this.ref = ref;
    const generation = ++this.generation;
    this.commands = [];
    this.container.replaceChildren(el('p', 'empty', 'Loading'));
    this.renderNavigation();
    try {
      const data = await this.api.overview({ section, ref, refresh });
      if (generation !== this.generation) return;
      this.data = data; this.render();
    } catch (error) {
      if (generation !== this.generation) return;
      this.container.replaceChildren(el('h1', 'section-title', section === 'project' ? 'Project' : human(section)), el('p', 'empty', error.message), this.button('Retry', 'refresh-cw', () => this.show(section, { ref, refresh: true })));
      this.icons();
    }
  }

  hide() { if(this.dashboardActive)this.viewMemory.set(this.section,{mode:this.dashboardMode,filter:this.filter,query:this.query,cursor:this.cursor,scroll:this.container.scrollTop});this.stopDashboard();this.dashboardActive=false;this.section='files';this.generation++; this.api.cancelOverview().catch(() => {}); }

  stopDashboard(){clearTimeout(this.dashboardTimer);clearTimeout(this.filterTimer);this.dashboardQueued=false;this.dashboardView.destroy();this.api.cancelOverview().catch(()=>{});this.dashboardToken=null;this.dashboardBusy=false;}
  async showDashboard(section,{refresh=false,resume=false}={}){
    this.stopDashboard();this.generation++;
    const boxChanged=this.boundBox!==this.box();
    if(section!==this.section||!['activity','schedules'].includes(section))this.mainWide=false;
    if(boxChanged){this.mainWide=false;this.boundBox=this.box();this.cursor=null;this.dashboardBlocks={};this.dashboardLocal=null;}
    if(section!==this.section&&!resume){this.viewMemory.set(this.section,{mode:this.dashboardMode,filter:this.filter,query:this.query,cursor:this.cursor,scroll:this.container.scrollTop});const saved=this.viewMemory.get(section)||{};this.dashboardMode=saved.mode||'overview';this.filter=saved.filter||'all';this.query=saved.query||'';this.cursor=saved.cursor;this.restoreScroll=saved.scroll||0;this.dashboardBlocks={};}
    if(boxChanged){this.cursor=null;this.filter='all';this.query='';this.bucket=null;this.activityOwner=null;}
    this.section=section;this.ref=undefined;this.dashboardActive=true;
    this.commands=[{label:'Refresh current section',icon:'refresh-cw',run:()=>this.showDashboard(section,{refresh:true})},...(section==='projects'?[{label:'Create project',icon:'plus',run:()=>this.newProject()}]:[]),...(section==='schedules'?[{label:'Create schedule',icon:'calendar-plus',run:()=>this.newSchedule()}]:[])];
    this.container.replaceChildren();const header=el('div','section-heading');header.append(el('h1','',section[0].toUpperCase()+section.slice(1)),el('small','section-qualification',this.mainWide?'Main-wide':section==='system'?'Main-wide':this.boxLabel()));const controls=el('div','section-controls');
    if(section==='projects')controls.append(this.button('New project','plus',()=>this.newProject(),true));
    if(section==='schedules'&&!this.mainWide)controls.append(this.button('New schedule','plus',()=>this.newSchedule(),true));
    header.append(controls);this.container.append(header);
    this.dashboardContent=el('div','dashboard-content');this.container.append(this.dashboardContent);this.dashboardView.container=this.dashboardContent;
    this.renderDashboardNavigation();this.paintDashboard();this.icons();await this.refreshDashboard(refresh);
    if(this.restoreScroll){this.container.scrollTop=this.restoreScroll;this.restoreScroll=0;}
  }
  renderDashboardNavigation(){
    const labels=this.section==='projects'?[['all','All projects'],['working','Working'],['passive','Passive'],['archived','Archived'],['unclassified','Unclassified']]:this.section==='system'?[['services','Services'],['workers','Workers'],['protection','Protection']]:[['all',`All ${this.section}`],...(this.section==='schedules'?[['loom','LOOM'],['hermes','Hermes']]:[['attention','Needs attention'],['running','In progress'],['completed','Completed']])];
    this.navigation.replaceChildren(...[['overview','Overview'],...labels,...['activity','schedules'].includes(this.section)?[['main-wide','Main-wide']]:[]].map(([key,label])=>{const selected=key==='main-wide'?this.mainWide:this.mainWide?false:this.dashboardMode==='overview'?key==='overview':key===this.filter;const b=el('button',`section-nav-item${selected?' selected':''}`,label);b.onclick=()=>{if(!['overview','main-wide'].includes(key))return this.openInventory(key);this.mainWide=key==='main-wide';this.dashboardBlocks={};this.dashboardLocal=null;this.dashboardMode='overview';this.filter='all';this.query='';this.cursor=null;this.bucket=null;this.activityOwner=null;this.workerMode=null;return this.showDashboard(this.section);};return b;}));
  }
  async refreshDashboard(refresh=false){
    if(!this.dashboardActive||this.section==='files'||this.section==='project'||this.windowHidden||document.hidden)return;
    if(this.dashboardBusy){this.dashboardQueued=true;return;}
    const token=crypto.randomUUID();this.dashboardToken=token;this.dashboardBusy=token;
    const query=this.query;this.dashboardQuery=query;
    try{
      const result=await this.api.dashboard({...this.currentScope(),section:this.section,token,refresh,state:this.section==='activity'&&this.bucket?'completed':this.dashboardMode==='inventory'&&['projects','activity'].includes(this.section)?this.filter:'all',status:this.section==='schedules'&&['active','paused','disabled'].includes(this.filter)?this.filter:undefined,query:this.query||undefined,cursor:this.cursor||undefined,hours:this.hours||24,days:7,...this.bucket?{since:this.bucket,until:new Date(Date.parse(this.bucket)+(this.hours===1?60:900)*1000).toISOString()}:{} });
      if(this.dashboardToken!==token)return;if(result.hidden)return;
      for(const [name,value]of Object.entries(result.blocks))if(name!=='projects'||query===this.query)this.dashboardBlocks[name]=value;if(result.local)this.dashboardLocal=result.local;this.paintDashboard();
      this.dashboardFailures=Object.values(result.blocks).every(b=>!b.available||b.stale)?(this.dashboardFailures||0)+1:0;
    }catch(e){if(this.dashboardToken===token)this.notice(e.message);}
    finally{if(this.dashboardBusy===token)this.dashboardBusy=false;if(this.dashboardToken===token&&this.dashboardQueued){this.dashboardQueued=false;queueMicrotask(()=>this.refreshDashboard());}else if(this.dashboardToken===token&&this.dashboardMode==='overview'&&this.section!=='projects'&&!this.windowHidden&&!document.hidden){const delay=(this.section==='system'?5000:this.section==='activity'?15000:60000)*Math.min(8,2**(this.dashboardFailures||0));this.dashboardTimer=setTimeout(()=>this.refreshDashboard(),Math.min(delay,120000));}}
  }
  openInventory(filter='all',extra={}){this.dashboardMode='inventory';this.filter=({pending:'running',held:'attention',accepted:'completed'})[filter]||filter;this.cursor=null;this.bucket=extra?.bucket;this.activityOwner=extra?.owner;this.workerMode=extra?.workerMode;return this.showDashboard(this.section);}
  paintDashboard(){
    if(!this.dashboardContent||!this.dashboardActive)return;
    if(this.dashboardMode==='overview'){this.dashboardView.render(this.section,this.dashboardBlocks,this.dashboardLocal);return;}
    this.dashboardView.destroy();const outer=this.dashboardContent;
    if(this.section==='projects'){
      if(!outer.querySelector('.section-filter')){const input=el('input','section-filter');input.type='search';input.placeholder='Filter projects';input.setAttribute('aria-label','Filter projects');input.value=this.query;
        const search=()=>{clearTimeout(this.filterTimer);this.refreshDashboard();};
        input.oninput=()=>{this.query=input.value;this.cursor=null;this.renderRows();clearTimeout(this.filterTimer);this.filterTimer=setTimeout(search,200);};
        input.onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();search();}};outer.append(input,el('div','inventory-results'));
      }
    }else outer.replaceChildren();
    const pane=this.section==='projects'?outer.querySelector('.inventory-results'):outer;pane.replaceChildren();let data,items=[],next;
    if(this.section==='projects'){data=this.dashboardBlocks.projects?.data;items=(data?.items||[]).map(projectItem);next=data?.next_cursor;}
    if(this.section==='activity'){data=this.dashboardBlocks.jobs?.data;items=[...(!this.bucket&&this.activityOwner!=='jobs'?this.dashboardLocal?.items||[]:[]),...(this.activityOwner!=='local'?data?.items||[]:[]).map(jobItem)].sort((a,b)=>(b.at||'').localeCompare(a.at||''));}
    if(this.section==='schedules'){data=this.dashboardBlocks.schedules?.data;const h=this.dashboardBlocks.hermes?.data;items=[...(data?.items||[]).map(scheduleItem),...(h?.availability==='available'?h.jobs||[]:[]).map(s=>nativeItem(s,h.source,this.dashboardBlocks.hermesManagement?.data))];next=data?.next_cursor;}
    if(this.section==='system'){
      const runtime=this.dashboardBlocks.runtime?.data,p=this.dashboardBlocks.protection?.data;
      if(this.filter==='protection'){for(const [name,v]of [['Local backup',p?.local],['Cloud snapshot',p?.cloud]]){const band=el('section','detail-band');band.append(el('h2','',name),el('p',`status-label ${statusClass(v?.state)}`,v?`${human(v.state)} · last success ${relativeTime(v.last_success_at)} · ${v.source_state}`:'Evidence unavailable'));pane.append(band);}pane.append(this.button('Inspect protection findings','shield-alert',async()=>this.inspectDetail('Protection findings',await this.api.inspect({kind:'protection'})),true),el('p','section-qualification',p?.notes_recovery?.reason||'Notes cohort and unsent client edits require separate protection evidence.'));return;}
      const owner=runtime?.[this.filter];pane.append(el('p','section-qualification',`${owner?.total??'Unknown'} ${this.filter} · ${owner?.complete?'complete current inventory':'bounded or unavailable inventory'}`));
      if(this.workerMode&&this.filter==='workers')pane.append(el('p','section-qualification',`${human(this.workerMode)} workers`));
      for(const v of owner?.items||[]){const posture=!v.enabled?'disabled':v.paused?'paused':v.current_run?'running':v.mode==='manual'?'manual':['interval','daily_local'].includes(v.mode)?'scheduled':'unknown';if(this.filter==='workers'&&this.workerMode&&posture!==this.workerMode)continue;const band=el('button','dashboard-resource runtime-record');band.onclick=async()=>{try{const detail=await this.api.inspect({kind:this.filter==='workers'?'worker':'service',ref:v.id});await form(v.name,[],{submit:'Close',description:detail.summary});}catch(e){this.notice(e.message);}};band.append(el('strong','',v.name),el('span','',this.filter==='workers'?`${human(posture)} · ${human(v.mode)} · last health ${human(v.health)}`:human(v.health)),el('small','',this.filter==='workers'?`Last success ${when(v.last_success)} · last failure ${when(v.last_failure)}`:`Observed ${when(v.observed_at)} · process ${v.process}`));pane.append(band);}return;
    }
    for(const block of Object.values(this.dashboardBlocks))if(block.error)pane.append(el('p','section-qualification warning',block.error));
    this.data={items,limited:data?.complete===false||this.section==='activity',observedAt:data?.observed_at};
    const target=el('div',`section-table resources-${this.section}`);target.id='section-rows';pane.append(target);this.renderRows();
    if(this.cursor){const first=el('button','action-button','First page');first.onclick=()=>{this.cursor=null;this.refreshDashboard();};pane.append(first);}
    if(next){const b=el('button','action-button','Next page');b.onclick=()=>{this.cursor=next;this.refreshDashboard();};pane.append(b);}
  }

  renderNavigation() {
    const items = this.section === 'projects' ? [['all', 'All projects'], ['working', 'Working'], ['passive', 'Passive'], ['archived', 'Archived']]
      : this.section === 'activity' ? [['all', 'All activity'], ['attention', 'Needs attention'], ['running', 'In progress'], ['completed', 'Completed']]
        : this.section === 'schedules' ? [['all', 'All schedules'], ['enabled', 'Enabled'], ['loom', 'LOOM'], ['hermes', 'Hermes']]
          : this.section === 'system' ? [['all', 'Overview'], ['services', 'Services'], ['workers', 'Workers'], ['protection', 'Protection']]
            : [['all', 'Project overview']];
    this.navigation.replaceChildren(...items.map(([key, label]) => {
      const b = el('button', `section-nav-item${this.filter === key ? ' selected' : ''}`, label);
      if(this.filter===key)b.setAttribute('aria-current','page');
      b.onclick = () => {
        this.filter = key;
        if (this.section === 'system' && (key === 'protection' || this.ref === 'protection')) this.show('system', { ref: key === 'protection' ? 'protection' : undefined });
        else { this.renderNavigation(); if (this.data) this.render(); }
      }; return b;
    }));
  }

  render() {
    const pane = this.container; pane.replaceChildren();
    const title = this.section === 'project' ? this.data.name || 'Project' : human(this.section).replace(/^./, c => c.toUpperCase());
    const header = el('div', 'section-heading'); header.append(el('h1', '', title));
    const controls = el('div', 'section-controls');
    if (this.section === 'projects') controls.append(this.button('New project', 'plus', () => this.newProject(), true));
    if (this.section === 'schedules') controls.append(this.button('New schedule', 'plus', () => this.newSchedule(), true));
    header.append(controls); pane.append(header);
    if (this.data.cached || this.data.offline) pane.append(el('p', 'section-qualification', `${this.data.offline ? 'Offline snapshot' : 'Snapshot'} · ${when(this.data.observedAt)}`));
    for (const error of this.data.errors || []) pane.append(el('p', 'section-qualification warning', error));
    if (['projects', 'activity', 'schedules'].includes(this.section)) {
      const search = el('input', 'section-filter'); search.type = 'search'; search.setAttribute('aria-label', `Filter ${this.section}`); search.placeholder = 'Filter'; search.value = this.query;
      search.oninput = () => { this.query = search.value; this.renderRows(); }; pane.append(search);
      const target = el('div', `section-table resources-${this.section}`); target.id = 'section-rows'; pane.append(target); this.renderRows();
    } else if (this.section === 'project') this.renderProject();
    else this.renderSystem();
    this.commands = [
      ...(this.section === 'projects' ? [{ label: 'Create project', icon: 'plus', run: () => this.newProject() }] : []),
      ...(this.section === 'schedules' ? [{ label: 'Create schedule', icon: 'calendar-plus', run: () => this.newSchedule() }] : []),
      ...(this.section === 'project'&&this.data.path&&this.exportSnapshot?[{label:'Export project snapshot',icon:'package-open',run:()=>this.exportSnapshot(this.ref)}]:[]),
      { label: 'Refresh current section', icon: 'refresh-cw', run: () => this.show(this.section, { ref: this.ref, refresh: true }) },
    ];
    this.icons();
  }

  renderRows() {
    const rows = this.container.querySelector('#section-rows');
    if (!rows) return;
    const query = this.query.toLowerCase();
    const items = (this.data.items || []).filter(item => {
      if (query && !`${item.name} ${item.owner} ${item.status} ${item.description || ''}`.toLowerCase().includes(query)) return false;
      if (this.filter === 'all') return true;
      if (this.section === 'projects') return item.posture === this.filter || this.filter === 'working' && item.legacyActive === true;
      if (this.section === 'schedules') return ['active','paused','disabled'].includes(this.filter)?item.owner==='loom'&&item.status===this.filter:this.filter === 'enabled' ? item.enabled : item.owner === this.filter;
      if (this.filter === 'attention') return /failed|error|blocked|conflict|manual|unavailable/.test(item.status);
      if (this.filter === 'running') return /running|pending|queued|processing|waiting/.test(item.status);
      if (this.filter === 'completed') return /completed|succeeded|accepted/.test(item.status);
      return true;
    });
    const header=el('div','resource-heading');
    for(const label of ['Name',this.section==='projects'?'Work state':'Owner',this.section==='projects'?'Lifecycle':'Status',this.section==='schedules'?'Next run':'Updated',...(this.section==='projects'?[]:['Actions'])])header.append(el('span','',label));
    rows.replaceChildren(header);
    for (const item of items) {
      item.scope||=this.currentScope();
      const row = el('div', 'resource-row');
      const name = el('button', 'resource-name'); name.append(el('strong', '', item.name));
      if(item.description||item.path)name.append(el('small','',item.description||item.path));name.dataset.tooltip=[item.name,item.description||item.path].filter(Boolean).join('\n');
      name.onclick = () => this.inspect(item).catch(error => this.notice(error.message));
      const status = el('span', `resource-status status-label ${statusClass(item.status)}`, human(item.status));
      row.append(name, el('span', 'resource-owner', this.section==='projects'?(['unspecified','archived'].includes(item.posture)?'—':human(item.posture)):item.owner || '—'), status);
      const actions=el('div','resource-actions');
      if (this.section === 'schedules') {
        const time = el('span', 'resource-time', item.enabled ? relativeTime(item.next) : '—'); time.dataset.tooltip = `Next: ${when(item.next)}\n${item.expression || ''} · ${item.timezone || 'Native timezone'}\nLast: ${when(item.last)} · ${item.result || 'Unknown'}`; row.append(time);
        if (item.owner === 'loom'||item.owner==='hermes'&&item.controllable&&!item.managed) {
          if (['active', 'scheduled', 'paused', 'disabled'].includes(item.status)) actions.append(this.button(item.enabled ? 'Pause schedule' : 'Resume schedule', item.enabled ? 'pause' : 'play', () => this.scheduleStatus(item)));
          if (item.status === 'paused') actions.append(this.button('Edit timing', 'calendar-cog', () => this.editTiming(item)));
        }
      } else {const time=el('span','resource-time',relativeTime(item.at));time.dataset.tooltip=when(item.at);row.append(time);}
      if (this.section === 'activity' && item.owner === 'jobs' && ['failed', 'timed_out', 'queued'].includes(item.status)) {
        actions.append(this.button(item.status === 'queued' ? 'Cancel queued job' : 'Retry job', item.status === 'queued' ? 'square' : 'rotate-cw', () => this.jobAction(item)));
      }
      if(this.section!=='projects')row.append(actions);rows.append(row);
    }
    if (!items.length) rows.append(el('p', 'empty', 'No matching entries'));
    const foot = el('p', 'section-qualification', `${items.length} shown${this.data.limited ? ' · bounded recent window' : ''} · Loaded ${relativeTime(this.data.observedAt)}`);foot.dataset.tooltip=when(this.data.observedAt); rows.append(foot); this.icons(rows);
  }

  async inspect(item) {
    if (this.section === 'projects') {try{rememberProject(localStorage,this.box(),item);}catch{}this.viewMemory.set('projects',{mode:this.dashboardMode,filter:this.filter,query:this.query,cursor:this.cursor,scroll:this.container.scrollTop});return this.show('project', { ref: item.id });}
    if(item.owner==='local'&&['conflict','held'].includes(item.status)){
      const review=await this.api.reviewNote({root:item.root,path:item.path});
      const result=await form('Review note conflict',[
        {name:'main',label:review.sourceExists?'Current Main version':'Main file is absent',type:'textarea',value:review.source,readOnly:true},
        ...(review.external!==undefined?[{name:'external',label:'External local version',type:'textarea',value:review.external,readOnly:true}]:[]),
        {name:'text',label:'Version to save',type:'textarea',value:review.local,required:false},
      ],{submit:review.sourceExists?'Save reviewed version':'Recreate reviewed note',description:review.path});
      if(result){await this.api.resolveNote({token:review.token,text:result.text});this.notice('Reviewed version saved locally · awaiting Main');await this.show('activity',{refresh:true});}
      return;
    }
    if (item.path && item.root) return this.openFile(item.path, item.root);
    const scope=item.scope||this.currentScope(),detail = await this.api.inspect({ ...scope,kind: this.section, ref: item.id, owner: item.owner, profile:item.profile,host:item.host,evidence:true });
    if(detail.job){const j=detail.job;detail.summary=`Owner: Hermes\nProfile: ${j.profile||item.profile}\nStatus: ${j.paused?'Paused':j.terminal?'Completed':'Scheduled'}\nTimezone: ${detail.timezone}\nName: ${j.name}\nSchedule: ${j.schedule?.expr||j.schedule?.run_at||j.schedule?.display||'Unknown'}\nManagement: ${j.managed?'Project declaration; edit the project source':'Native agent routine'}`;detail.sections=[{title:'Task',text:j.prompt}];detail.qualification='Read from the exact registered profile on Main. Viewing does not run this task.';}
    await this.inspectDetail(item.name,detail,scope);
  }

  async inspectDetail(title,detail,scope=this.currentScope()){
    const content=el('div','owner-evidence');
    for(const link of detail.links||[])content.append(this.button(link.name,'external-link',()=>this.api.external(link.url),true));
    for(const section of detail.sections||[]){
      const band=el('section','evidence-section');band.append(el('h3','',section.title),el('pre','evidence-text',section.text||'No retained value'));
      if(section.inspect)band.append(this.button(section.label||'Inspect artifact','file-search',async()=>{
        try{
          const next=await this.api.inspect({...section.inspect,...scope});if(!content.isConnected)return;
          const dialog=document.querySelector('#action-dialog');
          await new Promise(resolve=>{dialog.addEventListener('close',resolve,{once:true});dialog.close();});
          await this.inspectDetail(section.title,next,scope);
        }catch(error){let failure=band.querySelector('.field-error');if(!failure){failure=el('p','field-error');failure.setAttribute('role','status');band.append(failure);}failure.textContent=error.message;}
      },true));content.append(band);
    }
    if(detail.qualification)content.append(el('p','section-qualification',detail.qualification));
    if(detail.sections&&!detail.sections.length)content.append(el('p','empty','No matching retained findings'));
    return form(title,[],{submit:'Close',description:detail.summary,content});
  }

  renderProject() {
    const d = this.data, p = this.container;
    const actions = el('div', 'section-controls');
    if(this.openData)actions.append(this.button('Application data','database',()=>this.openData(this.ref),true));
    if(this.lifecycle){const archived=d.facts?.Status==='archived';actions.append(this.button(archived?'Restore project…':'Archive project…',archived?'archive-restore':'archive',()=>this.lifecycle(this.ref,archived?'restore':'archive'),true));}
    if (d.path) actions.append(this.button('Open folder', 'folder-open', () => this.openFolder(d.root || 'box', d.path), true));
    if (d.path&&this.focusFolder) actions.append(this.button('Focus folder', 'focus', () => this.focusFolder(d.root||'box',d.path), true));
    if(d.path&&this.timeline)actions.append(this.button('Timeline','history',()=>this.timeline(d.root||'box',d.path),true));
    if(d.path&&this.exportSnapshot)actions.append(this.button('Export snapshot','package-open',()=>this.exportSnapshot(this.ref),true));
    if(d.path&&this.workingCopy)actions.append(this.button('Working copy on Mac','copy',()=>this.workingCopy(d.root||'box',d.path),true));
    const basecamp = projectExternalURL(d.basecamp);
    if (basecamp) actions.append(this.button('Open Basecamp', 'external-link', () => this.api.external(basecamp), true));
    actions.append(this.button('Project context', 'notebook-text', async()=>{
      const context=await this.api.inspect({kind:'project-context',ref:this.ref});
      if(context.sections?.length)context.summary=context.summary.split('\n').filter(line=>!['purpose:','current_focus:','progress:','blockers:','next_action:'].some(key=>line.startsWith(key))).join('\n');
      await this.inspectDetail('Project context',context);
    },true));
    actions.append(this.button('Back to projects', 'arrow-left', () => this.show('projects'))); p.append(actions);
    const status=el('div','project-statusline');for(const key of ['Status','Work state'])if(d.facts?.[key])status.append(el('span',`status-label ${statusClass(d.facts[key])}`,human(d.facts[key]==='unspecified'?'unclassified':d.facts[key])));p.append(status);
    const overview=el('div','project-overview'),current=el('div','project-current'),resources=el('div','project-resources');overview.append(current,resources);p.append(overview);
    current.append(el('p','section-qualification','Project-file guidance · service observations are reported separately.'));
    const proseKeys=['Focus','Next action','Blockers','Progress','Purpose','Description'];
    const prose = value => projectProse(value, d, { navigate: (target, intent) => this.openProjectLink(target, intent), failed: error => this.notice(error.message) });
    for(const key of proseKeys)if(d.facts?.[key]){const band=el('section','project-context-band');band.append(el('h2','',key==='Focus'?'Current focus':key),prose(d.facts[key]));(['Purpose','Description'].includes(key)?p:current).append(band);}
    const evidence=el('details','project-evidence');evidence.append(el('summary','','Project evidence'));
    const facts = el('dl', 'facts');
    for (const [label, value] of Object.entries(d.facts || {})) { if(proseKeys.includes(label)||value===null||value===undefined||value==='')continue;facts.append(el('dt', '', label), el('dd', '', typeof value === 'string' ? value : JSON.stringify(value))); }evidence.append(facts);p.append(evidence);
    for (const group of d.groups || []) {
      const band = el('section', 'detail-band'); band.append(el('h2', '', group.title));
      for (const item of group.items || []) {
        const row = el('div', 'detail-line project-resource'); row.append(el('strong', '', item.name), prose(item.description || item.status || ''));
        const target = resolveProjectLink(item.path, d, { rooted: true, root: item.root || d.root || 'box' });
        if (target) {
          if (!target.external && item.kind === 'directory') target.directory = true;
          row.append(alternateProjectNavigation(this.button('Open resource', 'arrow-up-right', event => {
            event.preventDefault();return this.openProjectLink(target, navigationIntent(event));
          })));
        }
        band.append(row);
      }
      if (!group.items?.length) band.append(el('p', 'section-qualification', group.empty || 'No entries')); resources.append(band);
    }
  }

  openProjectLink(target, intent) {
    if (target.external) return this.api.external(target.external);
    if (target.directory) return this.openFolder(target.root, target.path, intent);
    return this.openFile(target.path, target.root, { ...intent, ...(target.heading ? { heading: target.heading } : {}) });
  }

  renderSystem() {
    for (const group of this.data.groups || []) {
      if (this.filter !== 'all' && this.filter !== group.key) continue;
      const band = el('section', 'detail-band'); band.append(el('h2', '', group.title));
      for (const item of group.items || []) {
        const row = el('div', 'detail-line'),evidence=el('div','evidence-summary');
        row.append(el('strong', '', item.name), el('span', /^-?\d/.test(item.status)?'metric-value':`status-label ${statusClass(item.status)}`, human(item.status)));
        if(item.evidenceAt){const age=el('small','',`${item.evidenceLabel||'Observed'} ${relativeTime(item.evidenceAt)}`);age.dataset.tooltip=when(item.evidenceAt);evidence.append(age);}
        if(item.detail){const detail=el('details',''),summary=el('summary','','Evidence');detail.append(summary,el('p','',item.detail));evidence.append(detail);}
        row.append(evidence);band.append(row);
      }
      if (!group.items?.length) band.append(el('p', 'section-qualification', 'No current data')); this.container.append(band);
    }
  }

  async newProject() {
    const box=this.box(),label=this.boxLabel();
    const value = await form('New project', [
      { name: 'name', label: 'Name' }, { name: 'slug', label: 'Folder name', placeholder: 'my-project' },
    ], { submit: 'Create project',description:label });
    if (!value) return;
    const result = await this.api.action({ kind: 'project-create', ...value,box });
    this.notice(result.summary || 'Project created'); await this.show('projects', { refresh: true });
  }

  async newSchedule() {
    if(this.mainWide)throw Error('Select a Box before creating a schedule.');
    const scope={box:this.box()},label=this.boxLabel();
    const choice=await form('New schedule',[{name:'owner',label:'Run with',options:[{value:'hermes',label:'Box agent · Hermes on Main'},{value:'loom',label:'LOOM capability on Main'}]},{name:'cadence',label:'Timing',options:[{value:'daily',label:'Every day'},{value:'weekly',label:'Every week'},{value:'once',label:'Once'},{value:'advanced',label:'Advanced cron'}]}],{submit:'Continue',description:label});if(!choice)return;
    const hermes=choice.owner==='hermes',options=await this.api.inspect({...scope,kind:hermes?'hermes-options':'schedule-options'}),zone=hermes?options.timezone:Intl.DateTimeFormat().resolvedOptions().timeZone;
    const profile=hermes?(options.profile||(options.profiles?.length===1?options.profiles[0].id:null)):null,agent=hermes?(options.profiles?.find(p=>p.id===profile)?.name||profile):null;
    if(hermes&&!profile)throw Error('A registered native agent is not available for this Box.');
    const value = await form(hermes?`Schedule a ${agent} task`:'Schedule a LOOM capability', [
      { name: 'name', label: 'Name' },
      ...hermes?[{name:'prompt',label:`Task for ${agent}`,type:'textarea'}]:[{name:'capability',label:'Capability',options:options.capabilities},{name:'project',label:'Project',required:false,options:[{value:'',label:'No project'},...options.projects]}],
      ...this.timingFields(choice.cadence,zone,hermes),
      ...hermes?[]:[{ name: 'input', label: 'Input JSON (advanced)', type: 'textarea', value: '{}' }],
    ], { submit: 'Create paused',description:hermes?`${label} · ${agent} on Main · ${zone}. Created paused; delivery remains in Hermes.`:`${label} · Main. Creation does not execute the capability; resume is separate.` });
    if (!value) return;
    const timing=scheduleTiming({...value,cadence:choice.cadence});
    await this.api.action({ ...scope,kind: 'schedule-create', ...value,...timing,schedule_kind:timing.kind,owner:choice.owner,...hermes?{host:'main',profile}:{} }); await this.show('schedules', { refresh: true }); this.notice('Schedule created paused');
  }

  timingFields(cadence,zone,native=false){return cadence==='once'?[{name:'at',label:`Run at (${Intl.DateTimeFormat().resolvedOptions().timeZone})`,type:'datetime-local'}]:[
    ...(cadence==='advanced'?[{name:'expression',label:'Cron expression',placeholder:'0 9 * * 1'}]:[{name:'time',label:'Time',type:'time',value:'09:00'},...(cadence==='weekly'?[{name:'weekday',label:'Day',options:['Sunday','Monday','Tuesday','Wednesday','Thursday','Friday','Saturday'].map((label,value)=>({label,value})),value:'1'}]:[])]),
    {name:'timezone',label:native?'Hermes profile timezone':'Timezone',value:zone,readOnly:native}];}

  async scheduleStatus(item) {
    const scope=item.scope||this.currentScope();let native;
    if(item.owner==='hermes'){const d=await this.api.inspect({...scope,kind:'schedule',ref:item.id,owner:'hermes',profile:item.profile,host:item.host});native=d.job;if(!native||native.managed||native.terminal)throw Error('This routine stays managed by its project declaration or terminal history.');}
    if (!await confirmAction(item.enabled ? 'Pause schedule?' : 'Resume schedule?', `${item.name}\n${item.expression || ''} · ${item.timezone || 'UTC'}`, item.enabled ? 'Pause' : 'Resume')) return;
    await this.api.action({ ...scope,kind: item.enabled ? 'schedule-pause' : 'schedule-resume', ref: item.id,...native?{owner:'hermes',host:item.host,profile:item.profile,revision:native.revision}:{} });
    await this.show('schedules', { refresh: true });
  }

  async editTiming(item) {
    const scope=item.scope||this.currentScope();
    if(item.owner==='hermes'){
      const d=await this.api.inspect({...scope,kind:'schedule',ref:item.id,owner:'hermes',profile:item.profile,host:item.host}),j=d.job;
      if(!j?.editable||!j.paused)throw Error('Only paused native prompt routines can be edited here. Declared/script routines use their original owner.');
      const value=await form(`Edit paused ${j.profile||item.profile} routine`,[{name:'name',label:'Name',value:j.name},{name:'prompt',label:'Task',type:'textarea',value:j.prompt},{name:'expression',label:'Schedule',value:j.schedule?.expr||j.schedule?.run_at||j.schedule?.display}],{submit:'Save paused',description:`Hermes profile timezone: ${d.timezone}. This does not resume or run the task.`});
      if(value)await this.api.action({...scope,kind:'schedule-edit',owner:'hermes',host:item.host,profile:item.profile,ref:item.id,revision:j.revision,...value});
      if(value)await this.show('schedules',{refresh:true});return;
    }
    const value = await form('Edit paused schedule', [{ name: 'expression', label: 'Cron expression', value: item.expression }, { name: 'timezone', label: 'Timezone', value: item.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone }], { submit: 'Save timing' });
    if (!value) return;
    await this.api.action({ ...scope,kind: 'schedule-timing', ref: item.id, ...value }); await this.show('schedules', { refresh: true });
  }

  async jobAction(item) {
    const scope=item.scope||this.currentScope();
    const cancel = item.status === 'queued';
    if (!await confirmAction(cancel ? 'Cancel this queued job?' : 'Retry this job?', item.name, cancel ? 'Cancel job' : 'Retry')) return;
    await this.api.action({ ...scope,kind: cancel ? 'job-cancel' : 'job-retry', ref: item.id });
    await this.show('activity', { refresh: true });
  }
}
