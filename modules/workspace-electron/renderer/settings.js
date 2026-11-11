import {themeOptions,themeID,loadTheme,saveTheme,applyTheme} from './themes.js';
import {syntaxThemes} from './syntax-themes.js';
import {selectControl} from './choice-controls.js';
import {loadEditorPreferences,saveEditorPreferences,shortcuts} from './editor-preferences.js';

const node=(tag,cls,text)=>{const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const categories=[{id:'theme',name:'Theme',icon:'palette'},{id:'editor',name:'Editor',icon:'text-cursor-input'},
  {id:'files',name:'Files & sync',icon:'folder-sync'},{id:'updates',name:'Updates',icon:'download'},{id:'shortcuts',name:'Shortcuts',icon:'keyboard'},{id:'advanced',name:'Advanced',icon:'sliders-horizontal',disabled:true}];

export class Settings{
  constructor({dialog,trigger,icon,icons,notice,syntaxChanged,preferencesChanged,api,uploads,offline,metadata}){
    Object.assign(this,{dialog,trigger,icon,icons,notice,syntaxChanged,preferencesChanged,api,uploads,offline,metadata});this.category='theme';
    dialog.setAttribute('aria-labelledby','settings-heading');
    dialog.addEventListener('close',()=>{const target=this.opener?.isConnected&&!this.opener.closest('dialog:not([open])')?this.opener:this.trigger;target.focus({preventScroll:true});});
    let outside=false;
    const isOutside=e=>{const r=dialog.getBoundingClientRect();return e.target===dialog&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom);};
    dialog.addEventListener('pointerdown',e=>outside=isOutside(e));
    dialog.addEventListener('click',e=>{if(outside&&isOutside(e))dialog.close();outside=false;});
  }
  open(category){
    if(this.dialog.open){if(category&&categories.some(c=>c.id===category&&!c.disabled)){this.category=category;this.renderCategory();}this.dialog.querySelector('.settings-content')?.focus();return;}
    if(document.querySelector('dialog[open]'))return;
    this.opener=document.activeElement;
    if(category&&categories.some(c=>c.id===category&&!c.disabled))this.category=category;
    this.dialog.replaceChildren();const shell=node('div','settings-shell'),sidebar=node('aside','settings-sidebar');
    const header=node('h2','settings-title','Settings');header.id='settings-heading';sidebar.append(header);
    const nav=node('nav','settings-categories');nav.setAttribute('aria-label','Settings categories');
    for(const item of categories){const b=node('button','settings-category');b.type='button';b.dataset.category=item.id;b.disabled=!!item.disabled;b.append(this.icon(item.icon),node('span','',item.name));
      if(item.disabled)b.dataset.tooltip='Not available yet';
      b.onclick=()=>{this.category=item.id;this.renderCategory();};nav.append(b);}
    sidebar.append(nav);this.content=node('section','settings-content');this.content.tabIndex=-1;this.content.setAttribute('aria-live','polite');
    const close=node('button','icon settings-close');close.type='button';close.setAttribute('aria-label','Close settings');close.dataset.tooltip='Close settings';close.append(this.icon('x'));close.onclick=()=>this.dialog.close();
    shell.append(sidebar,this.content,close);this.dialog.append(shell);this.renderCategory();this.dialog.showModal();this.icons(this.dialog);
    nav.querySelector(`[data-category="${this.category}"]`).focus();
  }
  selectTheme(id){
    if(themeID(id)!==id)throw Error('Unknown workspace theme');
    applyTheme(document,id);
    if(!saveTheme(id))this.notice('Theme applied for this session. The preference could not be saved.');
    for(const input of this.dialog.querySelectorAll('input[name="workspace-theme"]'))input.checked=input.value===id;
  }
  renderCategory(){
    this.renderSerial=(this.renderSerial||0)+1;
    for(const button of this.dialog.querySelectorAll('.settings-category')){const active=button.dataset.category===this.category;button.classList.toggle('selected',active);if(active)button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');}
    this.content.replaceChildren();this.content.scrollTop=0;
    if(this.category==='theme')this.renderThemes();else if(this.category==='files')this.renderFiles();else if(this.category==='updates')this.renderUpdates();else if(this.category==='shortcuts')this.renderShortcuts();else this.renderEditor();this.icons(this.dialog);
  }
  renderThemes(){
    const h=node('h1','','Theme');h.id='settings-section-heading';this.content.setAttribute('aria-labelledby',h.id);this.content.append(h);
    const group=node('fieldset','theme-options');const legend=node('legend','sr-only','Workspace theme');group.append(legend);
    const current=themeID(document.documentElement.dataset.theme||loadTheme());
    for(const theme of themeOptions){
      const label=node('label','theme-option'),input=node('input');input.type='radio';input.name='workspace-theme';input.value=theme.id;input.checked=theme.id===current;
      const preview=node('span','theme-preview');preview.dataset.previewTheme=theme.id;preview.setAttribute('aria-hidden','true');
      const side=node('span','theme-preview-sidebar');for(let i=0;i<5;i++)side.append(node('span'));
      const page=node('span','theme-preview-page');page.append(node('span','theme-preview-heading'));
      for(let i=0;i<3;i++)page.append(node('span','theme-preview-line'));
      const accent=node('span','theme-preview-colors');for(const tone of ['accent','link','success','warning','danger']){const swatch=node('i');swatch.className=`swatch-${tone}`;accent.append(swatch);}page.append(accent);preview.append(side,page);
      const caption=node('span','theme-caption');caption.append(node('span','',theme.name),node('span','theme-check'));caption.lastChild.append(this.icon('check'));
      label.append(input,preview,caption);input.onchange=()=>{if(input.checked)this.selectTheme(theme.id);};group.append(label);
    }
    this.content.append(group);
    const row=node('div','settings-row'),controls=node('div','settings-actions'),value=node('button','quiet-action','100%');value.dataset.zoomValue='true';value.setAttribute('aria-label','Reset interface zoom');value.dataset.tooltip='Reset interface zoom';
    const change=async action=>{try{this.zoomChanged(await this.api.zoom({action}));}catch(error){this.notice(error.message);}};
    for(const [action,symbol,label]of [['out','minus','Zoom out'],['in','plus','Zoom in']]){const b=node('button','icon');b.type='button';b.setAttribute('aria-label',label);b.dataset.tooltip=label;b.append(this.icon(symbol));b.onclick=()=>change(action);controls.append(b);}
    value.onclick=()=>change('reset');controls.insertBefore(value,controls.lastChild);row.append(node('span','','Interface size'),controls);this.content.append(row);
    this.api.zoom?.({action:'status'}).then(result=>{if(value.isConnected)this.zoomChanged(result);}).catch(()=>{});
  }
  zoomChanged({factor}){const value=this.dialog.querySelector('[data-zoom-value]');if(value)value.textContent=`${Math.round(factor*100)}%`;}
  renderEditor(){
    const h=node('h1','','Editor');h.id='settings-section-heading';this.content.setAttribute('aria-labelledby',h.id);this.content.append(h);
    const row=node('div','settings-row');const title=node('span','','Code highlighting');
    let current='one-dark';try{current=localStorage.getItem('syntax-theme')||current;}catch{}
    const select=selectControl({name:'syntax-theme',label:'Code highlighting',value:syntaxThemes.some(t=>t.id===current)?current:'one-dark',items:syntaxThemes.map(t=>({value:t.id,label:t.name}))});
    select.addEventListener('change',event=>this.syntaxChanged(event.target.value));row.append(title,select);this.content.append(row);
    const prefs=loadEditorPreferences(localStorage);
    const attachment=node('div','settings-row');attachment.append(node('span','','New attachments'),node('span','muted','Attachments/ beside the note'));this.content.append(attachment);
    for(const [key,label,min,max,step]of [['fontSize','Text size',12,26,1],['lineHeight','Line spacing',1.3,2.2,.05],['width','Readable width',540,1400,20]]){
      const line=node('label','settings-row'),input=node('input');input.type='number';input.min=min;input.max=max;input.step=step;input.value=prefs[key];input.setAttribute('aria-label',label);input.onchange=()=>{if(!input.validity.valid)return;const next={...loadEditorPreferences(localStorage),[key]:Number(input.value)};if(!saveEditorPreferences(localStorage,next))this.notice('Preference changed for this session only.');this.preferencesChanged?.(next);};line.append(node('span','',label),input);this.content.append(line);
    }
    const line=node('label','settings-row'),input=node('input');input.type='checkbox';input.checked=prefs.propertiesCollapsed;input.onchange=()=>{const next={...loadEditorPreferences(localStorage),propertiesCollapsed:input.checked};saveEditorPreferences(localStorage,next);this.preferencesChanged?.(next);};line.append(node('span','','Collapse properties by default'),input);this.content.append(line);
  }
  renderShortcuts(){
    this.content.append(node('h1','','Shortcuts'));const input=node('input','table-filter'),list=node('div','shortcut-list');input.placeholder='Find a shortcut';input.setAttribute('aria-label','Find a shortcut');
    const render=()=>{list.replaceChildren();const q=input.value.toLowerCase();for(const [group,label,keys]of shortcuts.filter(row=>row.join(' ').toLowerCase().includes(q))){const row=node('div','settings-row');row.append(node('span','',label),node('kbd','',keys.replaceAll('Mod',navigator.platform.includes('Mac')?'⌘':'Ctrl')));list.append(row);}if(!list.children.length)list.append(node('p','muted','No matching shortcuts.'));};input.oninput=render;this.content.append(input,list);render();
  }
  button(label,run){const b=node('button','quiet-action',label);b.type='button';b.onclick=async()=>{b.disabled=true;try{await run();}catch(error){this.notice(error.message);}finally{b.disabled=false;}};return b;}
  async renderFiles(){
    const content=this.content,serial=this.renderSerial;content.append(node('h1','','Files & sync'));
    const go=run=>()=>{this.dialog.close();return run();};
    const actions=node('div','settings-actions');actions.append(this.button('Uploads',go(this.uploads)),this.button('Offline downloads',go(this.offline)),this.button('Refresh Markdown metadata',go(this.metadata)));content.append(actions);
    try{
      const state=await this.api.availability();if(this.category!=='files'||this.renderSerial!==serial)return;
      const facts=node('dl','inspector-facts');for(const [name,value]of [['Local working copies',state.copies],['Cached files',state.cache.files],['Pinned files',state.cache.pinned],['Cache size',`${(state.cache.bytes/1048576).toFixed(1)} MiB`]])facts.append(node('dt','',name),node('dd','',String(value)));content.append(facts);
      content.append(node('h2','','Folder selections'));
      if(!state.selections.length)content.append(node('p','muted','No continuous folder selections.'));
      for(const selection of state.selections){const row=node('div','settings-row'),label=node('span','',`${selection.path} · ${selection.state}`);row.append(label,this.button(selection.state==='paused'?'Resume':'Pause',async()=>{await this.api.offline({operation:selection.state==='paused'?'resume_selection':'pause_selection',id:selection.id});this.renderCategory();}));content.append(row);}
      if(state.pins.length){content.append(node('h2','','Pinned assets'));for(const file of state.pins){const row=node('div','settings-row');row.append(node('span','',file.path),this.button('Unpin',async()=>{await this.api.pin({root:file.root,path:file.path,value:false});this.renderCategory();}));content.append(row);}}
    }catch(error){if(this.category==='files')content.append(node('p','field-error',error.message));}
  }
  async renderUpdates(){
    const content=this.content,serial=this.renderSerial;content.append(node('h1','','Updates'));
    try{const state=await this.api.updates({action:'status'});if(this.category!=='updates'||this.renderSerial!==serial)return;
      content.append(node('p','',`Installed ${state.version}`),node('p','muted',state.message||state.state));
      if(state.available)content.append(node('h2','',`Version ${state.available.version}`),node('p','',state.available.notes||''));
      const actions=node('div','settings-actions');actions.append(this.button('Check for updates',async()=>{await this.api.updates({action:'check'});this.renderCategory();}));
      if(state.state==='available')actions.append(this.button('Download update',async()=>{await this.api.updates({action:'download'});this.renderCategory();}));
      if(state.state==='ready')actions.append(this.button('Restart and install',()=>this.api.updates({action:'install'})));
      content.append(actions);
    }catch(error){if(this.category==='updates')content.append(node('p','field-error',error.message));}
  }
}
