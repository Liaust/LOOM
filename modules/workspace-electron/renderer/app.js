import { FileTree } from './file-tree.js';
import { DocumentTabs } from './tabs.js';
import { Sections } from './sections.js';
import { form, confirmAction } from './dialogs.js';
import {reviewProjectLifecycle} from './project-lifecycle.js';
import { canvasView, baseView, scrollHeading } from './note-views.js';
import { isMarkdown, editorLanguage,isEditableText } from '../src/file-formats.mjs';
import { LiveEditor } from './live-editor.js';
import { DocumentSaver } from './document-saver.js';
import { documentTemplate,documentName,quickOpenPath } from './file-commands.js';
import { documentMetrics } from './document-metrics.js';
import { sidebarWidth,sidebarBounds } from './sidebar-sizing.js';
import { ContextMenu } from './context-menu.js';
import {groupFileMenu} from './file-menu-model.js';
import {syntaxThemes} from './syntax-themes.js';
import {installTooltips} from './tooltips.js';
import {Settings} from './settings.js';
import {themeOptions} from './themes.js';
import {ExplorerFocus} from './explorer-focus.js';
import {CsvEditor} from './csv-editor.js';
import {CanvasEditor} from './canvas-editor.js';
import {PdfViewer} from './pdf-viewer.js';
import {interactiveBase} from './base-view.js';
import {KnowledgeView} from './knowledge-view.js';
import {TimelineView} from './timeline-view.js';
import {loadWorkspaceSession,saveWorkspaceSession} from '../src/session-state.mjs';
import {navigationIntent,bindNavigation} from './navigation-intent.js';
import {loadEditorPreferences,applyEditorPreferences} from './editor-preferences.js';
import {clipboardEntries,pasteTarget} from './file-clipboard.js';
import {NoteContext} from './note-context.js';
import {LinkPreview} from './link-preview.js';
import {mergeSearchHits} from '../src/search-results.mjs';
import {isBoxRef} from '../src/box-roots.mjs';
import {visibleExplorerEntries,loadExplorerVisibility,saveExplorerVisibility} from './explorer-visibility.js';
import {attachmentLink} from '../src/attachment-reference.mjs';
import {TabStrip} from './tab-strip.js';

const $ = selector => document.querySelector(selector);
const api = new Proxy({}, {get:(_,name)=>{
  const value=window.workspace[name];
  if(!['overview','inspect','action','dashboard','applicationData'].includes(name)||typeof value!=='function')return value;
  return (input={})=>{
    const explicit=input.root||input.input?.root||input.from_root;
    const box=input.box|| (isBoxRef(explicit)?explicit:state.activeBox);
    return value({...input,...input.all_boxes?{}:{box}});
  };
}});
installTooltips();
let statisticsTimer,paletteSearchTimer,paletteSearchSequence=0,contextSequence=0;
const state = { roots: [], root: 'box', path: '', section: 'files', filesView:'empty', entries: [], page: null, selected: -1, document: null, recents: [], editing: false, dirty: false, sequence: 0, paletteIndex: 0, paletteItems: [], paletteMode:'commands', cache: null };
state.activeBox=localStorage.getItem('active-box')||'box';
state.hideReadmeAgents=loadExplorerVisibility(localStorage);
const writableRoot=root=>root==='mac'||isBoxRef(root)&&state.roots.some(value=>value.ref===root&&value.available!==false&&value.read_only!==true);
const mainContentRoot=root=>isBoxRef(root)||root==='storage';
const tabs = new DocumentTabs();
const tabStrip=new TabStrip($('#document-tabs'),$('#tabs-previous'),$('#tabs-next'));
const retainedSession=loadWorkspaceSession(localStorage);let restoringSession=true;
const editors = new WeakMap();
const visualEditors=new WeakMap();let activeVisual=null;
let activeKnowledge=null;
applyEditorPreferences(loadEditorPreferences(localStorage));
const visualFile = value => /\.(canvas|base|csv|tsv)$/i.test(value.path);
const saver = new DocumentSaver({
  persist: persistDraft,
  write: (value, text, base) => api.save({ root: value.root, path: value.path, text, base, binding_id: value.binding_id }),
  saved: (value, result) => {
    if (state.document === value) { state.dirty = value.dirty; $('#document-dirty').hidden = !value.dirty; renderAvailability(value); latency(value.root==='mac'?'Saved on this Mac':result.pending ? 'Saved locally · awaiting Main' : 'Saved locally · Main accepted'); }
    renderTabs();
  },
  failed: (value, error) => { if (state.document === value) { state.dirty = true; $('#document-dirty').hidden = false; notice(`Draft preserved: ${error.message}`); } },
});
const annotationRequests = new Map();
try { state.recents = JSON.parse(localStorage.getItem('recent-files') || '[]').slice(0, 30); } catch { /* A damaged recent list must not prevent opening files. */ }
let noticeTimer, draftTimer;
const icons = (root = document) => lucide.createIcons({ root, attrs: { 'aria-hidden': 'true' } });
const node = (tag, className, text) => { const element = document.createElement(tag); if (className) element.className = className; if (text !== undefined) element.textContent = text; return element; };
const icon = name => { const element = node('i'); element.dataset.lucide = name; return element; };
const renderedIcon=name=>{const holder=node('span');holder.append(icon(name));icons(holder);return holder.firstChild;};
const explorerFocus=new ExplorerFocus(localStorage);
const contextMenu=new ContextMenu($('#file-context-menu'),{icon:renderedIcon,failed:notice});
const formatBytes = size => !Number.isFinite(size) ? '' : size < 1024 ? `${size} B` : size < 1024 ** 2 ? `${(size / 1024).toFixed(1)} KB` : size < 1024 ** 3 ? `${(size / 1024 ** 2).toFixed(1)} MB` : `${(size / 1024 ** 3).toFixed(1)} GB`;
const fileIcon = entry => entry.kind === 'directory' ? 'folder' : /\.pdf$/i.test(entry.name) ? 'file-type' : /\.(png|jpg|jpeg|gif|webp)$/i.test(entry.name) ? 'image' : 'file-text';
const rootLabel = root => state.roots.find(value => value.ref === root)?.label || root;
const rawApplicationRoots = new Set();
let fileClipboard=null,clipboardBusy=false;
const tree = new FileTree($('#file-tree'), {
  onScopes:scopes=>{state.explorerScopes=scopes;watchTree();},
  hideReadmeAgents:state.hideReadmeAgents,
  loader: async input => { const page = await api.list({ ...input, showRaw:rawApplicationRoots.has(input.root), watch: false }); annotate(input.root, page.entries); return page; }, roots: () => explorerFocus.roots(state.roots.filter(root => !root.hidden)),
  selected: () => state.document?{root:state.document.root,path:state.document.path,member:state.document.member}:state.creationTarget||{root:state.root,path:state.path},
  selectFolder:(root,path)=>{state.creationTarget={root,path};},
  contextMenu:(root,entry,event)=>showFileMenu(root,entry,event).catch(error=>notice(error.message)),
  command:(action,entries,event)=>treeCommand(action,entries,event).catch(error=>notice(error.message)),
  openFile, icon, icons, decorate: entry => indicators(entry.annotations),
  dropFiles:async(files,target)=>{try{await api.uploadDrop(files,target);await uploadProgress();}catch(error){notice(error.message);}},
  dropEntry:async(source,destination)=>{try{await moveEntry(source.path,source.root,{name:source.path.split('/').pop(),kind:source.directory?'directory':'file'},destination);}catch(error){notice(error.message);}},
});
let explorerWatchSignature='';
function watchTree(){
  const scopes=state.section==='files'?[...(!state.sidebarKnowledge?state.explorerScopes||[]:[]),...(state.filesView==='directory'?[{root:state.root,path:state.path}]:[])]:[];
  const signature=JSON.stringify(scopes);if(signature===explorerWatchSignature)return;
  explorerWatchSignature=signature;
  api.watchDirectories(scopes).catch(error=>{if(explorerWatchSignature===signature)explorerWatchSignature='';latency(`Live folders: ${error.message}`);});
}
const sections = new Sections({ api, box:()=>state.activeBox, boxLabel:()=>rootLabel(state.activeBox), container: $('#section-pane'), navigation: $('#section-navigation'), icons, icon, notice, openFolder: navigate, focusFolder:(root,path)=>focusFolder(root,path), timeline:(root,path)=>openTimeline({root,path}), openFile, lifecycle:(project,operation)=>projectLifecycle(project,operation), exportSnapshot,workingCopy:makeWorkingCopy,openData:openProjectData });
let activeSyntaxTheme='one-dark';try{activeSyntaxTheme=localStorage.getItem('syntax-theme')||activeSyntaxTheme;}catch{}
function setSyntaxTheme(id){
  if(!syntaxThemes.some(theme=>theme.id===id))return;
  activeSyntaxTheme=id;
  try{localStorage.setItem('syntax-theme',id);}catch{notice('Code highlighting changed for this session. The preference could not be saved.');}
  for(const value of tabs.items.values())editors.get(value)?.setTheme(id);
}
const settings=new Settings({dialog:$('#settings-dialog'),trigger:$('#settings-button'),icon,icons,notice,syntaxChanged:setSyntaxTheme,preferencesChanged:prefs=>{applyEditorPreferences(prefs);for(const value of tabs.items.values())editors.get(value)?.setPreferences(prefs);},api,uploads:uploadProgress,offline:offlineProgress,metadata:()=>metadataRefresh()});
let detailsNode;
const noteContext=new NoteContext($('#details-body'),{icon:renderedIcon,icons,navigate:(target,intent)=>target.reference?followLink({root:target.root,path:target.from},target.reference,intent):openFile(target.path,target.root,intent),heading:item=>{const editor=editors.get(state.document);if(editor){editor.view.dispatch({selection:{anchor:item.from},scrollIntoView:true});editor.focus();}},preview:previewLink});
try{noteContext.select(localStorage.getItem('note-context-tab')||'details');}catch{}
$('#details-body').addEventListener('click',()=>localStorage.setItem('note-context-tab',noteContext.selectedTab));
const editorPreviews=new LinkPreview($('#document-content'),{target:anchor=>state.document&&{root:state.document.root,from:state.document.path,reference:anchor.dataset.noteReference},preview:previewLink});
$('#document-content').addEventListener('pointerover',event=>{const link=event.target.closest?.('[data-note-reference]');if(link)link.dataset.noteLink='';},true);
async function previewLink(target,{signal,maxChars=1200}={}){
  if(isBoxRef(target.root)&&isBoxRef(state.document?.root)&&target.root!==state.document.root)return null;
  let resolved=target;if(target.reference){resolved=await api.resolveReference({root:target.root,from:target.from,reference:target.reference});if(signal?.aborted||!resolved||resolved.external||resolved.choices||resolved.requires_navigation||resolved.kind==='directory')return null;}
  if(isBoxRef(resolved.root)&&isBoxRef(target.root)&&resolved.root!==target.root)return null;
  if(signal?.aborted)return null;const current=tabs.get(resolved.root,resolved.path);if(current?.kind==='text')return {text:current.text.slice(0,maxChars),title:current.entry.name,qualification:current.dirty?'Local draft · not yet accepted':current.root==='mac'?'On this Mac':'Open working copy'};
  const result=await api.previewText({root:resolved.root,path:resolved.path});if(signal?.aborted)return null;return {text:result.text.slice(0,maxChars),title:resolved.path.split('/').pop(),qualification:result.origin+(result.truncated?' · excerpt':'')};
}
function refreshNoteContext(){noteContext.update({document:state.document,graph:state.document?.graph,detailsNode});}

function showSection(section) {
  if(section!=='files')tree.cancelEmptyCreate();
  if (section !== 'files') { stashDocument(); flushDraft().catch(error => notice(error.message)); }
  state.section = section;
  renderBoxScope();
  setDetailsOpen(section==='files'&&localStorage.getItem('note-context-open')==='true');
  document.body.classList.toggle('section-open', section !== 'files');
  $('#file-tree').hidden = section !== 'files'||Boolean(state.sidebarKnowledge); $('#knowledge-navigation').hidden=section!=='files'||!state.sidebarKnowledge;$('#tree-tools').hidden = section !== 'files'; $('#section-navigation').hidden = section === 'files'; $('#section-pane').hidden = section === 'files';
  $('#explorer-modes').hidden=section!=='files';
  $('#navigation-title').textContent = section==='files'?'':section[0].toUpperCase() + section.slice(1);
  $('#navigation-title').hidden=section==='files';$('#details-toggle').disabled=section!=='files';
  document.querySelectorAll('.rail>button').forEach(b => b.classList.toggle('selected', (b.dataset.section || b.id) === section));
  if (section === 'files') { sections.hide(); breadcrumbs(); }
  else { $('#breadcrumbs').textContent = section[0].toUpperCase() + section.slice(1); sections.show(section); }
  resizeSidebar();renderFilesView();renderTabs();
}

function selectBox(root){
  if(!isBoxRef(root)||!state.roots.some(value=>value.ref===root&&value.available!==false))throw Error('Choose an available Box.');
  state.activeBox=root;localStorage.setItem('active-box',root);renderBoxScope();
}
function renderBoxScope(){
  let button=$('#box-scope');
  if(!button){button=document.createElement('button');button.id='box-scope';button.className='explorer-scope';$('#navigation').querySelector('header').after(button);button.onclick=()=>{const available=state.roots.filter(value=>isBoxRef(value.ref)&&value.available!==false);contextMenu.show(available.map(value=>({id:value.ref,label:value.label,icon:'folder',run:()=>{const section=state.section;sections.hide();selectBox(value.ref);showSection(section);}})),{x:button.getBoundingClientRect().left,y:button.getBoundingClientRect().bottom});};}
  button.hidden=state.section==='files'||state.section==='system';
  button.textContent=state.roots.find(value=>value.ref===state.activeBox)?.label||'Personal';
  button.setAttribute('aria-label','Select workspace Box');
}

function renderFilesView(){
  watchTree();
  editorPreviews.cancel();
  if(state.filesView!=='document')noteContext.update({document:null});
  const visible=state.section==='files';
  if(!visible||state.filesView!=='document'){activeVisual?.destroy?.();activeVisual=null;}
  if(!visible||state.filesView!=='knowledge'){activeKnowledge?.destroy();activeKnowledge=null;}
  $('#knowledge-pane').hidden=!visible||state.filesView!=='knowledge';
  if(visible&&state.filesView==='knowledge'&&!activeKnowledge)renderKnowledge();
  $('#empty-pane').hidden=!visible||state.filesView!=='empty';
  $('#unavailable-pane').hidden=!visible||state.filesView!=='unavailable';
  $('#directory-pane').hidden=!visible||state.filesView!=='directory';
  $('#document-pane').hidden=!visible||state.filesView!=='document';
  $('#document-actions').hidden=!visible||state.filesView!=='document';
  $('.toolbar').hidden=visible&&state.filesView==='empty'&&!tabs.target(-1)&&!tabs.target(1);
  document.body.classList.toggle('document-open',visible&&state.filesView==='document');
  renderDocumentStats();
}

async function newTab(){
  tree.cancelEmptyCreate();
  stashDocument();await flushDraft();const tab=tabs.newTab();displayEmpty({tab,record:false});
}
function displayEmpty(options){
  tabs.visit({kind:'empty'},options);++state.sequence;
  state.document=null;state.dirty=false;state.editing=false;state.filesView='empty';
  showSection('files');renderTabs();tree.render();
}
async function showPage(page,options){
  if(page.kind==='timeline')return openTimeline({root:page.root,path:page.path},options,page.saved);
  if(page.kind==='knowledge')return openKnowledge(page.view,{root:page.root,path:page.path},page.center,options,page.saved);
  if(page.kind==='file')return openFile(page.path,page.root,{...options,sessionPage:page,member:page.member});
  if(page.kind==='directory')return navigate(page.root,page.path,options);
  stashDocument();await flushDraft();displayEmpty(options);
}
async function selectTab(tab){const page=tabs.page(tab);if(page)return showPage(page,{tab,record:false});}
async function goHistory(step){const page=tabs.target(step);if(page)return showPage(page,{tab:tabs.activeTab,step});}
async function reopenTab(){stashDocument();await flushDraft();const previous=tabs.activeTab,id=tabs.reopenClosed();if(!id)return notice('No recently closed tabs.');tabs.activeTab=previous;return selectTab(id);}
async function cycleTabs(step){stashDocument();await flushDraft();const previous=tabs.activeTab,id=tabs.cycle(step);tabs.activeTab=previous;if(id)return selectTab(id);}
function pruneEditors(){for(const value of tabs.prune(value=>saver.running.has(value))){editors.get(value)?.destroy();editors.delete(value);}}

function toggleNavigation(force){
  const collapsed=typeof force==='boolean'?force:!document.body.classList.contains('navigation-collapsed');
  document.body.classList.toggle('navigation-collapsed',collapsed);$('#navigation').inert=collapsed;
  $('#navigation-toggle').setAttribute('aria-expanded',String(!collapsed));
  localStorage.setItem('navigation-collapsed',String(collapsed));
  resizeSidebar();
}

let preferredSidebar=Number(localStorage.getItem('navigation-width'))||246;
let preferredSectionSidebar=Number(localStorage.getItem('section-navigation-width'))||184;
function resizeSidebar(value=state.section==='files'?preferredSidebar:preferredSectionSidebar,persist=false){
  if(persist){if(state.section==='files'){preferredSidebar=value;localStorage.setItem('navigation-width',String(value));}else{preferredSectionSidebar=value;localStorage.setItem('section-navigation-width',String(value));}}
  const details=document.body.classList.contains('details-open')?parseFloat(getComputedStyle(document.body).getPropertyValue('--details-width'))||280:0;
  const width=sidebarWidth(value,innerWidth,details),bounds=sidebarBounds(innerWidth,details),handle=$('#navigation-resize');
  document.body.style.setProperty('--navigation-width',document.body.classList.contains('navigation-collapsed')?'0px':`${width}px`);
  handle.setAttribute('aria-valuemin',String(bounds.min));handle.setAttribute('aria-valuemax',String(bounds.max));handle.setAttribute('aria-valuenow',String(Math.round(width)));
}

function stashDocument() {
  if(state.filesView==='knowledge'&&activeKnowledge?.snapshot){const page=tabs.page();if(page?.kind==='knowledge'||page?.kind==='timeline'){page.saved={...activeKnowledge.snapshot(),scroll:$('#knowledge-pane').scrollTop};if(page.kind==='knowledge')page.view=page.saved.mode;}}
  activeVisual?.flush();
  commitPropertyInput();
  if (!state.document) return;
  const editor = editors.get(state.document);
  Object.assign(state.document, { dirty: state.dirty, editing: state.editing, selection: editor ? editor.selection() : state.document.selection });
}

function renderTabs() {
  const container = $('#document-tabs');
  const scrollLeft=container.scrollLeft;
  container.replaceChildren(...tabs.visible().map(({id,page,value,pinned}) => {
    const name=page.kind==='empty'?'New tab':page.kind==='timeline'?'Timeline':page.kind==='knowledge'?page.view==='graph'?'Connections':'Knowledge':page.kind==='directory'?page.path.split('/').pop()||rootLabel(page.root):value?.entry.name||page.member?.name||page.path.split('/').pop();
    const tab = node('div', 'document-tab'); tab.setAttribute('role', 'tab'); tab.setAttribute('aria-selected', String(state.section==='files'&&tabs.activeTab===id)); tab.dataset.tooltip = page.kind==='empty'?name:`${rootLabel(page.root)}/${page.path}`;
    const label = node('button', 'tab-label');if(page.kind!=='empty')label.append(icon(page.kind==='timeline'?'history':page.kind==='knowledge'?page.view==='graph'?'network':'library':page.kind==='directory'?'folder':fileIcon(value?.entry||{name})));label.append(node('span','',`${value?.dirty ? '• ' : ''}${name}`)); label.onclick = () => selectTab(id).catch(error=>notice(error.message));
    const close = node('button', 'icon'); close.dataset.tooltip = 'Close tab'; close.setAttribute('aria-label', `Close ${name}`); close.append(icon('x')); close.onclick = () => closeTab(id).catch(error => notice(error.message));
    if(pinned){tab.classList.add('pinned');const mark=node('span','tab-pin');mark.append(icon('pin'));tab.append(mark);}
    tab.draggable=true;tab.ondragstart=event=>{event.dataTransfer.setData('application/x-loom-tab',id);event.dataTransfer.effectAllowed='move';};tab.ondragover=event=>{if([...event.dataTransfer.types].includes('application/x-loom-tab')){event.preventDefault();event.dataTransfer.dropEffect='move';}};tab.ondrop=event=>{event.preventDefault();const source=event.dataTransfer.getData('application/x-loom-tab'),rect=tab.getBoundingClientRect(),order=[...tabs.slots.keys()],before=event.clientX>rect.left+rect.width/2?order[order.indexOf(id)+1]||null:id;if(source&&tabs.reorder(source,before))renderTabs();};
    tab.onauxclick=event=>{if(event.button===1){event.preventDefault();closeTab(id).catch(e=>notice(e.message));}};
    tab.oncontextmenu=event=>{event.preventDefault();contextMenu.show([{label:pinned?'Unpin tab':'Pin tab',icon:'pin',run:()=>{tabs.pin(id,!pinned);renderTabs();}},{label:'Close tab',icon:'x',run:()=>closeTab(id)},{label:'Close other unpinned tabs',icon:'copy-x',run:async()=>{for(const slot of tabs.visible())if(slot.id!==id&&!slot.pinned)await closeTab(slot.id);}},{label:'Close unpinned tabs',icon:'list-x',run:async()=>{for(const slot of tabs.visible())if(!slot.pinned)await closeTab(slot.id);}},{separator:true},{label:'Reopen closed tab',icon:'rotate-ccw',run:reopenTab}],{x:event.clientX,y:event.clientY});};
    tab.append(label, close); return tab;
  }));
  icons(container);
  container.scrollLeft=scrollLeft;tabStrip.render(tabs.activeTab);
  rememberSession();
}
function rememberSession(){
 if(restoringSession)return;
 const visible=tabs.page();if(visible?.kind==='file'&&state.document){visible.selection=editors.get(state.document)?.selection()||state.document.selection;visible.sourceMode=state.document.sourceMode===true;}
 saveWorkspaceSession(localStorage,{version:1,tabs:[...tabs.slots].map(([id,slot])=>({id,...slot})),active:tabs.activeTab,section:state.section,sidebarKnowledge:state.sidebarKnowledge});
}

function indicators(annotation) {
  const group = node('span', 'file-indicators'); if (!annotation) return group;
  const availability = { local: ['hard-drive', annotation.owner==='mac'?'On this Mac':'Local replica'], offline: ['pin', 'Kept offline'], cached: ['download', 'Cached'], remote: ['cloud', 'On demand'] }[annotation.availability];
  if (availability) { const mark = node('span'); mark.dataset.tooltip = availability[1]; mark.setAttribute('role','img');mark.setAttribute('aria-label',availability[1]);mark.append(icon(availability[0])); group.append(mark); }
  const stages = [['index', 'scan-text', 'Search index'], ['embeddings', 'network', 'Embeddings'], ['ocr', 'text-search', 'OCR'], ['vision', 'scan-eye', 'Image description']];
  for (const [field, symbol, label] of stages) {
    const status = annotation[field]; if (!status || status === 'not_requested' || status === 'unknown') continue;
    const mark = node('span', /failed|blocked|stale/.test(status) ? 'warning' : ''); mark.dataset.tooltip = `${label}: ${status.replaceAll('_', ' ')}`;mark.setAttribute('role','img');mark.setAttribute('aria-label',mark.dataset.tooltip);mark.append(icon(symbol)); group.append(mark);
  }
  if (annotation.pending || annotation.held) { const mark = node('span', annotation.held ? 'warning' : ''); mark.dataset.tooltip = annotation.held ? 'Local sync requires attention' : 'Local changes pending source acceptance';mark.setAttribute('role','img');mark.setAttribute('aria-label',mark.dataset.tooltip);mark.append(icon(annotation.held ? 'circle-alert' : 'refresh-cw')); group.append(mark); }
  return group;
}

function annotate(root, entries, refresh = false) {
  if (root === 'local' || root==='application-data'||root.startsWith('appdata_')||!entries.length) return;
  const paths = entries.filter(entry => !entry.pending).slice(0, 100).map(entry => entry.relative_path), key = JSON.stringify([root, paths]);
  if (!paths.length) return;
  let request = annotationRequests.get(key);
  if (!request || refresh || Date.now() - request.at > 30000) { request = { promise: api.annotations({ root, paths }), at: Date.now() }; annotationRequests.set(key, request); while (annotationRequests.size > 128) annotationRequests.delete(annotationRequests.keys().next().value); }
  request.promise.then(result => {
    for (const entry of entries) entry.annotations = result.entries.find(value => value.path === entry.relative_path);
    if (state.root === root && state.entries.some(entry => paths.includes(entry.relative_path))) {
      for (const entry of state.entries) entry.annotations = result.entries.find(value => value.path === entry.relative_path) || entry.annotations;
      renderEntries(false);
    } else tree.render();
    if (state.document?.root === root) {
      const annotation = result.entries.find(value => value.path === state.document.path);
      if (annotation) { state.document.annotations = annotation; editControl(state.document); }
    }
  }).catch(() => { annotationRequests.delete(key); });
}

function notice(message) { $('#notice').textContent = message; $('#notice').hidden = false; clearTimeout(noticeTimer); noticeTimer = setTimeout(() => { $('#notice').hidden = true; }, 10000); }
function connection(connected, text) { $('#connection-dot').classList.toggle('offline', !connected);const label=text||(connected?'Main connected':'Main offline');$('#connection-button').dataset.tooltip=`${label} · click to reconnect`;$('#connection-button').setAttribute('aria-label',label); }
function latency(text) { state.lastActivity=text;$('#connection-button').dataset.tooltip=`${$('#connection-button').getAttribute('aria-label')} · ${text} · click to reconnect`; }
function renderDocumentStats(){
  const value=state.document,visible=state.section==='files'&&state.filesView==='document'&&Boolean(value);$('#document-status').hidden=!visible;if(!visible)return;
  const text=value.kind==='text'&&typeof value.text==='string',markdown=text&&isMarkdown(value.path),metrics=text?documentMetrics(value.text,{links:false}):null;
  $('#status-links').hidden=!markdown;$('#status-properties').hidden=!markdown;
  $('#status-backlinks').hidden=!markdown;$('#status-backlinks').textContent=value.graph?`${value.graph.backlinks.length}${value.graph.truncated?'+':''} ${value.graph.origin==='main'?'known':'cached/local'} backlinks`:'Backlinks';$('#status-backlinks').dataset.tooltip=value.graph?.qualification||'Load known backlinks';
  $('#status-properties').textContent=`${metrics?.properties||0} properties`;$('#status-properties').dataset.tooltip=metrics?.propertyError||'Edit YAML properties';
  $('#status-words').hidden=!text;$('#status-characters').hidden=!text;
  $('#status-words').textContent=text?`${metrics.words.toLocaleString()} words`:'';$('#status-characters').textContent=text?`${metrics.characters.toLocaleString()} characters`:'';
  $('#latency').textContent=value.openTiming||'';$('#latency').dataset.tooltip='File retrieval and first editor frame; not indexing or source-acceptance latency';
}
function renderRoots() {
  tree.render();
  $('#explorer-global').setAttribute('aria-selected',String(!state.sidebarKnowledge&&explorerFocus.mode==='explorer'));
  $('#explorer-focus').setAttribute('aria-selected',String(!state.sidebarKnowledge&&explorerFocus.mode==='focus'));
  $('#explorer-knowledge').setAttribute('aria-selected',String(Boolean(state.sidebarKnowledge)));
  const scope=$('#explorer-scope');scope.hidden=explorerFocus.mode!=='focus';scope.textContent=explorerFocus.scope?.path.split('/').pop()||rootLabel(explorerFocus.scope?.root);scope.dataset.tooltip=explorerFocus.scope?`${rootLabel(explorerFocus.scope.root)}/${explorerFocus.scope.path} · choose focus`:'';
}
async function focusFolder(root,path){
  state.sidebarKnowledge=false;tree.cancelEmptyCreate();showSection('files');
  const scroll=explorerFocus.switch('focus',tree.model,$('#file-tree'),{root,path});
  state.creationTarget={root,path};
  await tree.load(root,path);renderRoots();$('#file-tree').scrollTop=scroll;toggleNavigation(false);
}
async function changeExplorer(mode){
  state.sidebarKnowledge=false;showSection('files');
  if(mode==='focus'&&!explorerFocus.scope)return focusFolder(state.creationTarget?.root||state.root,state.creationTarget?.path??state.path);
  tree.cancelEmptyCreate();const scroll=explorerFocus.switch(mode,tree.model,$('#file-tree'));renderRoots();
  if(mode==='focus'){state.creationTarget=explorerFocus.scope;await tree.load(explorerFocus.scope.root,explorerFocus.scope.path);}
  $('#file-tree').scrollTop=scroll;
}
async function revealInTree(root,relative){
  state.sidebarKnowledge=false;showSection('files');
  const scope=explorerFocus.mode==='focus'&&explorerFocus.scope;
  if(scope&&(scope.root!==root||relative!==scope.path&&!relative.startsWith(scope.path+'/')))await changeExplorer('explorer');
  renderRoots();toggleNavigation(false);const parent=relative.split('/').slice(0,-1).join('/');await tree.reveal(root,parent);tree.render();
  const row=[...$('#file-tree').querySelectorAll('[role=treeitem]')].find(row=>row.dataset.root===root&&row.dataset.path===relative);row?.scrollIntoView({block:'nearest'});if(row)tree.focus(row);
}
async function chooseFocus(){
  const choices=explorerFocus.recent.map(s=>({value:JSON.stringify(s),label:`${rootLabel(s.root)}/${s.path}`}));
  if(!choices.length)return focusFolder(state.root,state.path);
  const choice=await form('Focused folder',[{name:'scope',label:'Folder',options:choices}],{submit:'Focus'});if(choice){const scope=JSON.parse(choice.scope);await focusFolder(scope.root,scope.path);}
}
function breadcrumbs() {
  const parts = state.path.split('/').filter(Boolean);
  const root = node('button', '', rootLabel(state.root)); root.onclick = () => navigate(state.root, '');
  const children = [root];
  parts.forEach((part, index) => { const button = node('button', '', part); button.dataset.tooltip = part; button.onclick = () => navigate(state.root, parts.slice(0, index + 1).join('/')); children.push(icon('chevron-right'), button); });
  if(state.document){children.push(icon('chevron-right'),node('span','breadcrumb-current',state.document.entry.name));children.at(-1).dataset.tooltip=state.document.path;const scope=explorerFocus.mode==='focus'&&explorerFocus.scope;if(scope&&(scope.root!==state.document.root||state.document.path!==scope.path&&!state.document.path.startsWith(scope.path+'/'))){const reveal=node('button','outside-focus');reveal.setAttribute('aria-label','Outside focused folder · reveal in tree');reveal.dataset.tooltip='Outside focused folder · reveal in tree';reveal.append(icon('locate-fixed'));reveal.onclick=()=>revealInTree(state.document.root,state.document.path).catch(e=>notice(e.message));children.push(reveal);}}
  $('#breadcrumbs').replaceChildren(...(state.filesView==='empty'?[]:children));
  $('#up').disabled = !state.path;
  $('#back').disabled = !tabs.target(-1);$('#forward').disabled=!tabs.target(1);
  icons();
}

function renderKnowledge(){
 const page=state.knowledgePage;if(!page)return;
 const Owner=page.kind==='timeline'?TimelineView:KnowledgeView;
 const choices=new Map(),add=(root,path)=>{const value=JSON.stringify({root,path});choices.set(value,{value,label:(root==='box'?'Main Box':root==='mac'?'Mac Box':rootLabel(root))+(path?' / '+path:'')});};
 for(const root of state.roots.filter(r=>!r.hidden&&r.available&&r.ref!=='local')){add(root.ref,'');for(const entry of tree.model.branches.get(tree.model.key(root.ref,''))?.page?.entries||[])if(entry.kind==='directory')add(root.ref,entry.relative_path);}
 if(explorerFocus.scope)add(explorerFocus.scope.root,explorerFocus.scope.path);add(page.root,page.path);
 activeKnowledge=new Owner({api,scope:{root:page.root,path:page.path},scopeChoices:[...choices.values()],changeScope:scope=>openKnowledge(activeKnowledge.mode,scope,null,undefined,activeKnowledge.snapshot()).catch(e=>notice(e.message)),changed:markKnowledgeNavigation,mode:page.view,restore:page.saved,center:page.center,open:openFile,folder:navigate,icon:renderedIcon,notice});$('#knowledge-pane').replaceChildren(activeKnowledge.dom);const current=activeKnowledge;current.ready?.then(()=>{if(activeKnowledge===current)$('#knowledge-pane').scrollTop=page.saved?.scroll||0;});
}
async function openKnowledge(view='categories',scope=explorerFocus.mode==='focus'?explorerFocus.scope:{root:state.root==='local'?'box':state.root,path:''},center,options,saved){
 stashDocument();await flushDraft();activeKnowledge?.destroy();activeKnowledge=null;
 const page={kind:'knowledge',root:scope.root,path:scope.path||'',view,center,saved};tabs.visit(page,options);++state.sequence;state.knowledgePage=page;state.root=page.root;state.path=page.path;state.document=null;state.dirty=false;state.filesView='knowledge';showSection('files');renderTabs();renderRoots();
}
async function openTimeline(scope=explorerFocus.mode==='focus'?explorerFocus.scope:{root:state.root==='local'?'box':state.root,path:state.document?.path||state.path},options,saved){
 stashDocument();await flushDraft();activeKnowledge?.destroy();activeKnowledge=null;
 const page={kind:'timeline',root:scope.root,path:scope.path||'',saved};tabs.visit(page,options);++state.sequence;state.knowledgePage=page;state.root=page.root;state.path=page.path;state.document=null;state.dirty=false;state.filesView='knowledge';showSection('files');renderTabs();
}
function knowledgeNavigation(){
 const nav=$('#knowledge-navigation');nav.replaceChildren();
 for(const [label,symbol,category]of [['Overview','library',''],['All known files','files','all'],['Subjects','tags','subjects'],['People','users','people'],['Reading','book-open','reading'],['Concepts','lightbulb','concepts'],['History','history','history'],['Saved Bases','table-properties','bases']]){const b=node('button','knowledge-nav-item');b.dataset.category=category;b.append(renderedIcon(symbol),node('span','',label));b.onclick=()=>Promise.resolve().then(async()=>{if(state.knowledgePage?.kind!=='knowledge'||!activeKnowledge)await openKnowledge();const view=activeKnowledge;await view.ready;if(activeKnowledge!==view||!view.result)return;view.filters={history:true,category:category==='all'?'':category};view.mode=category?'table':'categories';view.page=0;view.render();}).catch(e=>notice(e.message));nav.append(b);}if(activeKnowledge?.snapshot&&state.knowledgePage?.kind==='knowledge')markKnowledgeNavigation(activeKnowledge.snapshot());
}
function markKnowledgeNavigation(view){const page=tabs.page();if(page?.kind==='knowledge'){page.view=view.mode;page.saved={...view,scroll:$('#knowledge-pane').scrollTop};state.knowledgePage=page;rememberSession();}const category=view.filters.category||(view.mode==='categories'?'':view.mode==='table'?'all':null);for(const b of $('#knowledge-navigation').children)if(b.dataset.category===category)b.setAttribute('aria-current','page');else b.removeAttribute('aria-current');}

function renderEntries(remember = true) {
  const sorted = [...state.entries].sort((a, b) => (b.kind === 'directory') - (a.kind === 'directory') || a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' }));
  state.entries = sorted;
  const visible=visibleExplorerEntries(sorted,state.hideReadmeAgents);
  $('#entries').replaceChildren(...visible.map((entry, index) => {
    const row = node('button', 'file-row'); row.setAttribute('role', 'option'); row.setAttribute('aria-selected', String(index === state.selected)); row.dataset.path = entry.relative_path;
    const name = node('span', 'file-name'); name.append(icon(fileIcon(entry)), node('span', '', entry.name), indicators(entry.annotations));
    row.append(name, node('span', 'file-date', entry.modified_at ? new Date(entry.modified_at).toLocaleDateString(undefined, { day: '2-digit', month: 'short', year: 'numeric' }) : ''), node('span', 'file-size', entry.kind === 'directory' ? '' : formatBytes(entry.size)));
    row.dataset.tooltip = entry.relative_path;
    row.oncontextmenu = event => { event.preventDefault();showFileMenu(state.root,entry,event).catch(error=>notice(error.message)); };
    bindNavigation(row,intent => { state.selected = index; $('#entries').querySelectorAll('[role=option]').forEach((value, i) => value.setAttribute('aria-selected', String(i === index))); if (entry.kind !== 'directory') openFile(entry.relative_path,state.root,{member:entry.member,...intent}); });
    row.ondblclick = () => { if (entry.kind === 'directory') navigate(entry.root_ref||state.root, entry.relative_path);else if(!entry.member&&state.roots.find(r=>r.ref===state.root)?.format==='zotero-webdav'&&/\.zip$/i.test(entry.name))navigate(state.root,entry.relative_path); };
    row.onkeydown = event => { if (event.key === 'Enter') { event.preventDefault(); entry.kind === 'directory' ? navigate(entry.root_ref||state.root, entry.relative_path) : openFile(entry.relative_path,state.root,{member:entry.member}); } };
    return row;
  }));
  if (!visible.length) $('#entries').append(node('p', 'empty', state.loading ? 'Loading' : state.page?.has_more ? 'More entries available' : sorted.length ? 'No visible files' : 'Empty folder'));
  if (remember && state.page && !state.searchingResults) tree.model.remember(state.root, state.path, { ...state.page, entries: sorted });
  tree.render();
  $('#load-more').hidden = !state.page?.has_more;
  icons();
}

function renderExplorerVisibility(){
  const button=$('#hide-readme-agents');
  button.setAttribute('aria-pressed',String(state.hideReadmeAgents));
  button.dataset.tooltip=state.hideReadmeAgents?'Show README.md and AGENTS.md':'Hide README.md and AGENTS.md';
  button.setAttribute('aria-label',button.dataset.tooltip);
}
function toggleExplorerVisibility(){
  state.hideReadmeAgents=!state.hideReadmeAgents;
  try{saveExplorerVisibility(localStorage,state.hideReadmeAgents);}catch{notice('Visibility changed for this session. The preference could not be saved.');}
  state.selected=-1;
  tree.setHideReadmeAgents(state.hideReadmeAgents);
  renderExplorerVisibility();
  renderEntries(false);
}
renderExplorerVisibility();

function acceptedRemoval(root,relative){
  const inside=value=>value===relative||value.startsWith(`${relative}/`);
  tree.model.remove(root,relative);
  for(const value of tabs.items.values())if(value.root===root&&inside(value.path)){
    value.deleted=true;value.editable=false;editors.get(value)?.configure(value.path,false,Boolean(value.sourceMode));
    if(value===state.document){$('#edit').disabled=true;$('#save').disabled=true;$('#availability').textContent='Removed on Main · local recovery retained';}
  }
  state.recents=state.recents.filter(entry=>entry.root!==root||!inside(entry.path));
  localStorage.setItem('recent-files',JSON.stringify(state.recents));
  if(state.root===root){
    state.entries=state.entries.filter(entry=>!inside(entry.relative_path));
    if(state.page)state.page={...state.page,entries:state.entries};
    renderEntries(false);
    if(state.filesView==='directory'){
      const parent=relative.split('/').slice(0,-1).join('/');
      if(inside(state.path))navigate(root,parent,{refresh:true}).catch(error=>notice(error.message));
      else if(state.path===parent)navigate(root,state.path,{refresh:true}).catch(error=>notice(error.message));
    }
  }else tree.render();
}

async function flushDraft() {
  commitPropertyInput();
  clearTimeout(draftTimer);
  if (!state.document || !state.dirty) return;
  const document = state.document;
  document.dirty = state.dirty; document.editing = state.editing;
  await persistDraft(document);
}
async function persistDraft(value) { await api.draft({ root: value.root, path: value.path, text: value.text, base: value.base, binding_id: value.binding_id }); }
window.flushDraft = flushDraft;
window.flushWorkspace = async () => {
  stashDocument(); const documents = [...tabs.items.values()];
  await Promise.all(documents.filter(value => value.dirty).map(persistDraft));
  await saver.flush(documents);
  rememberSession();
};
window.beginRecovery=async()=>{document.body.inert=true;try{await window.flushWorkspace();}catch(error){document.body.inert=false;throw error;}};
window.endRecovery=()=>{document.body.inert=false;};

async function navigate(root, relative, { tab=tabs.activeTab,step=0,record=true,refresh = false,newTab=false,background=false } = {}) {
  let opening=false;
  try {
    if(newTab||background){stashDocument();await flushDraft();tab=tabs.newTab({activate:!background,reuseEmpty:false});if(background){tabs.visit({kind:'directory',root,path:relative},{tab,activate:false});renderTabs();return;}}
    if (state.section !== 'files') showSection('files');
    stashDocument();
    await flushDraft();
    opening=true;
    const sequence = ++state.sequence;
    const changed = state.root !== root || state.path !== relative;
    tabs.visit({kind:'directory',root,path:relative},{tab,step,record:record&&!refresh});
    state.root = root; state.path = relative; state.creationTarget={root,path:relative};state.document = null; state.filesView='directory';state.dirty = false; state.editing = false; state.selected = -1;
    if(isBoxRef(root))selectBox(root);
    state.searchingResults = false;
    renderTabs();
    state.loading = true;
    if (changed) { state.entries = []; state.page = null; renderEntries(); }
    renderFilesView();
    renderRoots(); breadcrumbs();
    const started = performance.now();
    if (!refresh) {
      const cached = await api.list({ root, path: relative, showRaw:rawApplicationRoots.has(root), cached: true, cacheOnly: true }).catch(() => null);
      if (sequence !== state.sequence) return;
      if (cached) { state.entries = cached.entries; state.page = cached; renderEntries(); latency(`${root === 'local' ? 'Local' : cached.cached ? 'Directory cache' : 'Main'} · ${Math.round(performance.now() - started)} ms`); }
    }
    const page = await api.list({ root, path: relative, showRaw:rawApplicationRoots.has(root) });
    if (sequence !== state.sequence) return;
    state.entries = page.entries; state.page = page; state.loading = false; renderEntries();
    annotate(root, state.entries, refresh);
    latency(`${page.offline?'Offline inventory':root === 'local' ? 'Local' : 'Main'} · ${page.ms} ms request · ${state.entries.length}${page.has_more ? '+' : ''} entries`);
    requestAnimationFrame(() => tree.reveal(root, relative));
    await renderDetails();
  } catch (error) { state.loading = false;if(opening&&!state.page&&!state.entries.length)showUnavailable({kind:'directory',root,path:relative},error,()=>navigate(root,relative,{record:false,refresh:true}));else{notice(error.message);latency('Folder refresh failed');} }
}

async function openFile(relative, root = state.root, { tab=tabs.activeTab,step=0,record=true,refresh = false, edit = false,sessionPage,member,newTab=false,background=false,heading } = {}) {
  let opening=false;
  try {
    if(!newTab&&!background&&!step&&record&&!refresh&&tabs.slots.get(tab)?.pinned&&tabs.page(tab)?.kind==='file'&&(tabs.page(tab).root!==root||tabs.page(tab).path!==relative))newTab=true;
    if((newTab||background)&&tabs.slots.size>=tabs.limit)throw Error('Close a tab before opening another.');
    if (state.section !== 'files'&&!background) showSection('files');
    stashDocument();
    await flushDraft();
    opening=true;
    const sequence = background?state.sequence:++state.sequence;
    const started = performance.now();
    const prior = tabs.get(root, relative,member), retained = refresh ? null : prior;
    let result = retained || await api.open({ root, path: relative, refresh,member });
    if (!background&&sequence !== state.sequence) return;
    if(!background&&isBoxRef(root))selectBox(root);
    if (refresh && prior) {
      if (prior.dirty) { result = prior; notice('Your current draft is preserved. Save or resolve it before replacing its content.'); }
      else { const sourceMode = prior.sourceMode; Object.assign(prior, result, { sourceMode }); result = prior; }
    }
    if(background){
      if(result.draft&&result.draft.text!==result.text){result.text=result.draft.text;result.base=result.draft.base;result.dirty=true;}
      tabs.open(result,{newTab:true,background:true});if(heading)tabs.page([...tabs.slots.keys()].at(-1)).heading=heading;renderTabs();return result;
    }
    if(newTab)tab=tabs.newTab({reuseEmpty:false});
    const parent = relative.split('/').slice(0, -1).join('/');
    if (state.root !== root || state.path !== parent) {
      state.root = root; state.path = parent; renderRoots(); breadcrumbs();
      tree.model.load(root, parent).then(page => {
        if (sequence === state.sequence) { state.entries = page.entries; state.page = page; renderEntries(); }
      }).catch(() => {});
    }
    if(!retained&&sessionPage){result.selection=sessionPage.selection;result.sourceMode=sessionPage.sourceMode===true;}
    tabs.open(result,{tab,step,record:record&&!refresh}); state.document = result;state.filesView='document';state.creationTarget={root,path:parent}; state.dirty = Boolean(result.dirty); state.editing = Boolean(result.editable && (!visualFile(result) || result.editing || result.sourceMode||edit));
    if (!retained && result.draft && result.draft.text !== result.text) {
      result.text = result.draft.text; result.base = result.draft.base; state.dirty = true;
      result.dirty = true;
      notice('Recovered your local draft. Saving checks the current file on disk.');
    }
    state.recents = [{ root, path: relative,...member?{member}:{} }, ...state.recents.filter(value => value.root !== root || value.path !== relative||value.member?.id!==member?.id)].slice(0, 30);
    localStorage.setItem('recent-files', JSON.stringify(state.recents));
    renderFilesView();
    renderDocument();
    const destination=heading||sessionPage?.heading;if(destination){applyDestination(result,destination);delete tabs.page().heading;}
    renderTabs();
    pruneEditors();
    if(isMarkdown(result.path))refreshGraph(result);
    requestAnimationFrame(() => {
      if(state.document!==result)return;
      result.openTiming=`${retained&&!refresh?0:result.ms} ms open · ${Math.round(performance.now()-started)} ms view`;renderDocumentStats();
      latency(`${result.origin === 'local' ? 'Local' : result.origin === 'cached' ? 'Cache' : 'Main'} · ${result.ms} ms open · ${Math.round(performance.now() - started)} ms to view${result.offline ? ' · offline copy' : ''}`);
      tree.reveal(root, parent).then(()=>{if(state.document===result)$('#file-tree [aria-selected=true]')?.scrollIntoView({block:'nearest'});});
    });
    await renderDetails();return result;
  } catch (error) {
    if(background){notice(error.message);return;}
    if(!opening){notice(error.message);return;}
    const page={kind:'file',root,path:relative,...member?{member}:{},...sessionPage?{selection:sessionPage.selection,sourceMode:sessionPage.sourceMode}:{}};tabs.visit(page,{tab,step,record});showUnavailable(page,error,()=>openFile(relative,root,{record:false,sessionPage:page,member}));
  }
}
function showUnavailable(page,error,retry){state.root=page.root;state.path=page.path.split('/').slice(0,-1).join('/');state.document=null;state.dirty=false;state.filesView='unavailable';$('#unavailable-title').textContent=page.path.split('/').pop()||rootLabel(page.root);$('#unavailable-reason').textContent=error.message;$('#unavailable-retry').onclick=retry;$('#unavailable-folder').onclick=()=>navigate(page.root,state.path);showSection('files');renderTabs();}

function renderAvailability(value) {
  $('#availability').textContent = value.root==='mac'?(value.deleted?'Removed on Mac · recovery retained':'On this Mac'):value.mainMovedTo ? 'Main moved · preview preserved' : value.deleted ? 'Removed on Main · recovery retained' : value.binding_id ? value.localRecoveryNeeded ? 'Main accepted · local recovery needed' : value.held ? 'Local · needs attention' : value.pendingTarget ? `Move pending · ${value.pendingTarget}` : value.pending ? 'Local · awaiting Main' : 'Local · Main accepted'
    : value.mode === 'local' ? 'Local recovery' : value.offline ? 'Offline copy' : value.mode === 'cached' ? 'Cached' : 'On demand';
  $('#availability').dataset.tooltip=value.writeQualification||'';
}

async function linkedTarget(value, reference) {
  let target=await api.resolveReference({root:value.root,from:value.path,reference,navigate:true});
  if(target.choices){
    const choice=await form('Choose linked file',[{name:'path',label:'File',options:target.choices.map(c=>({value:JSON.stringify([c.root||value.root,c.path]),label:`${rootLabel(c.root||value.root)} / ${c.path}`}))}],{submit:'Open'});
    if(!choice)return null;const [root,path]=JSON.parse(choice.path);target={root,path,heading:target.heading};
  }
  return target;
}
async function followLink(value, reference, intent={}) {
  try {
    const target=await linkedTarget(value,reference);if(!target)return;
    if(target.external)return api.external(target.external);
    if(target.kind==='directory')return navigate(target.root,target.path,intent);
    if(intent.newTab||target.path!==state.document?.path||target.root!==state.document?.root)await openFile(target.path,target.root,{...intent,heading:target.heading});
    else if(target.heading)applyDestination(state.document,target.heading);
  }catch(error){notice(error.message);}
}
function applyDestination(value,heading){const page=value.kind==='pdf'&&/^page=(\d+)/i.exec(heading);if(page)activeVisual?.go?.(Number(page[1]));else if(!editors.get(value)?.heading(heading))scrollHeading($('#document-content'),heading);}

async function refreshGraph(value){
  try{const graph=await api.noteGraph({root:value.root,path:value.path});value.graph=graph;if(state.document===value){renderDocumentStats();refreshNoteContext();}return graph;}
  catch(error){value.graphError=error.message;if(state.document===value)$('#status-backlinks').dataset.tooltip=`Backlinks unavailable: ${error.message}`;return null;}
}
async function openTag(value,tag){
  state.tagResults=[];state.tagMessage=`#${tag}`;palette('tag');const sequence=paletteSearchSequence;
  try{const result=await api.noteGraph({root:value.root,path:value.path,action:'tag',tag});if(sequence!==paletteSearchSequence||!$('#palette').open)return;
    state.tagResults=result.items.map(item=>command(item.name,'hash',intent=>openFile(item.path,item.root,intent),item.path));state.tagMessage=`#${tag} · ${result.origin} · ${result.coverage}`;renderPalette();
  }catch(error){if(sequence===paletteSearchSequence){state.tagMessage=error.message;renderPalette();}}
}
async function showRelations(backlinks=false){
  const value=state.document;if(!value)return;const graph=await refreshGraph(value);if(state.document!==value)return;
  if(!graph)return notice(value.graphError||'Link metadata unavailable.');
  state.linkResults=backlinks?graph.backlinks.map(item=>command(item.name||item.path.split('/').pop(),'corner-up-left',intent=>openFile(item.path,item.root,intent),`${item.kind==='local_pending'?'Local pending · ':''}${item.path}`))
    :graph.forward.map(item=>command(item.target,'link',intent=>followLink(value,item.target,intent)));
  state.linkMessage=`${graph.origin} · ${graph.qualification}${graph.truncated?' · bounded result, more links may exist':''}${graph.warnings?.length?' · '+graph.warnings.join('; '):''}`;palette('links');
}

function editControl(value) {
  const button = $('#edit'); button.hidden = !isMarkdown(value.path)&&!visualFile(value);button.disabled=Boolean(value.deleted);
  button.dataset.tooltip = isMarkdown(value.path) ? value.sourceMode ? 'Live preview' : 'Markdown source' : state.editing ? 'View file' : value.editable?'Edit source':'View source';
  button.setAttribute('aria-label', button.dataset.tooltip); button.classList.toggle('active', isMarkdown(value.path) ? Boolean(value.sourceMode) : state.editing);
  button.replaceChildren(icon(isMarkdown(value.path) ? value.sourceMode ? 'text' : 'code-2' : 'pencil'));
  icons(button);
}

function renderDocument() {
  editorPreviews.cancel();
  const value = state.document;
  if (!value) return;
  breadcrumbs();
  $('#document-name').textContent = value.entry.name;
  $('#document-dirty').hidden = !state.dirty;
  renderAvailability(value);
  editControl(value); $('#save').hidden = !value.editable;
  $('#pin').hidden = value.local_path !== undefined || value.root === 'local'||value.root.startsWith('appdata_'); $('#pin').classList.toggle('active', value.pinned);
  $('#pin').disabled = value.entry.size > 256 * 1024 ** 2;
  const content = $('#document-content');
  activeVisual?.destroy?.();activeVisual=null;
  const changed=text=>{value.text=text;value.dirty=true;if(state.document===value){state.dirty=true;$('#document-dirty').hidden=false;renderDocumentStats();}saver.schedule(value);persistDraft(value).catch(e=>notice(e.message));};
  const showEditor = () => {
    let editor = editors.get(value);
    if (!editor) {
      editor = new LiveEditor({ text: value.text, path: value.path, editable: value.editable, sourceMode: Boolean(value.sourceMode), selection: value.selection,
        onChange: text => {
          value.text = text; value.dirty = true;
          if (state.document === value) { state.dirty = true; $('#document-dirty').hidden = false; }
          clearTimeout(draftTimer); draftTimer = setTimeout(() => persistDraft(value).catch(error => notice(`Draft could not be preserved: ${error.message}`)), 350);
          saver.schedule(value);
          clearTimeout(statisticsTimer);statisticsTimer=setTimeout(()=>{renderDocumentStats();if(document.body.classList.contains('details-open'))refreshNoteContext();},250);
        }, onSave: () => saver.save(value), follow: (reference,intent) => followLink(value, reference,intent),
        asset:reference=>api.attachmentPreview({root:value.root,path:value.path,binding:value.binding_id,reference}),releaseAsset:lease=>api.releaseAttachment(lease).catch(()=>{}),
        attachmentLinked:async item=>{await persistDraft(value);return api.attachments({root:value.root,path:value.path,binding:value.binding_id,action:'linked',id:item.id,reference:item.reference});},
        attach:(action,payload)=>action==='drop'?api.attachDrop(payload,{root:value.root,path:value.path,binding:value.binding_id}):api.attachments({root:value.root,path:value.path,binding:value.binding_id,action}),
        existingAttachment:async entry=>{if(entry.root!==value.root||entry.directory||entry.kind==='directory'||!entry.path)throw Error('Choose a file from this Box. Cross-Box copies require an explicit copy action.');return `![[${attachmentLink(value.path,entry.path)}]]`;},
        external:url=>api.external(url),form,icon:renderedIcon,notice,rename:name=>renameTitle(value,name),theme:activeSyntaxTheme,suggest:query=>query.attachment?api.attachments({root:value.root,path:value.path,binding:value.binding_id,action:'complete',query:query.query}):api.noteGraph({root:value.root,path:value.path,action:'complete',...query}),openTag:tag=>openTag(value,tag),
      }); editors.set(value, editor);
    } else { editor.replaceText(value.text); editor.configure(value.path, value.editable, Boolean(value.sourceMode)); }
    if(isMarkdown(value.path)&&value.editable)api.attachments({root:value.root,path:value.path,binding:value.binding_id,action:'list'}).then(items=>editor.reconcileAttachments(items)).catch(()=>{});
    if (content.firstChild !== editor.dom) content.replaceChildren(editor.dom);
    editor.focus();
  };
  if (value.kind === 'text') {
    if (!visualFile(value) || state.editing) showEditor();
    else if (/\.(canvas|csv|tsv)$/i.test(value.path)) {
      try {let view=visualEditors.get(value);if(!view||view.path!==value.path||view.editable!==value.editable){const Owner=/\.canvas$/i.test(value.path)?CanvasEditor:CsvEditor;view=new Owner({text:value.text,path:value.path,editable:value.editable,onChange:changed,icon:renderedIcon,notice,follow:(reference,intent)=>followLink(value,reference,intent),form,suggest:query=>api.noteGraph({root:value.root,path:value.path,action:'complete',...query}),openTag:tag=>openTag(value,tag)});view.path=value.path;visualEditors.set(value,view);}else view.replaceText(value.text);activeVisual=view;content.replaceChildren(view.dom);view.mount?.();}
      catch(error){notice(error.message);state.editing=true;showEditor();editControl(value);}
    } else content.replaceChildren(interactiveBase({query:options=>api.baseQuery({root:value.root,path:value.path,text:value.text,...options}),open:openFile,active:()=>state.document===value&&!state.editing,configure:value.editable?async(viewIndex,changes)=>{const text=await api.baseDefinition({root:value.root,path:value.path,text:value.text,viewIndex,changes});changed(text);}:null}));
  } else if (value.kind === 'pdf') {
    const view=new PdfViewer({url:value.url,size:value.entry.size,name:value.entry.name,icon:renderedIcon,notice,form,
      external:url=>api.external(url),page:value.pdfPage||1,onPage:page=>{value.pdfPage=page;},
      onReady:ms=>{if(state.document===value&&activeVisual===view){value.openTiming=`${value.ms} ms open · ${Math.round(ms)} ms PDF`;renderDocumentStats();}},
      download:()=>api.downloadFile({root:value.root,path:value.path,member:value.member}).then(result=>{if(!result.cancelled)notice('PDF copy saved. Original unchanged.');}),
      copyPage:value.member?null:page=>api.copyPath({root:value.root,path:value.path,page}).then(()=>notice('PDF page link copied.'))});
    activeVisual=view;content.replaceChildren(view.dom);view.mount();
  } else if (value.kind === 'image') {
    const image = node('img', 'image-preview'); image.alt = value.entry.name; image.src = value.url; image.onerror = () => notice('Image preview failed. Refresh and reopen the file.'); content.replaceChildren(image);
  } else {
    const view = node('div', 'binary'); view.append(icon('file'), node('strong', '', value.entry.name), node('span', '', `${formatBytes(value.entry.size)} · ${value.mode === 'local' ? 'Local file' : value.mode === 'cached' ? 'Cached on this Mac' : 'Available from Main'}`)); content.replaceChildren(view);
  }
  icons();
  renderDocumentStats();
  refreshNoteContext();
}

async function save() {
  activeVisual?.flush();
  commitPropertyInput();
  const value = state.document;
  if (!value?.editable) return;
  await saver.save(value);
}

function commitPropertyInput(){
  const input=document.activeElement;
  if(input?.matches('input,textarea')&&input.closest('.note-properties')){
    if(!input.validity.valid)throw Error('Complete the property value before leaving this note.');
    input.blur();
  }
}

async function renderDetails() {
  const snapshot = await api.state(); state.cache = snapshot.cache;
  const body = node('div','note-context-details');detailsNode=body;
  const value = state.document;
  const root = state.roots.find(root => root.ref === (value?.root || state.root));
  body.append(node('h2', '', 'Original path'), node('div', 'detail-path', `${root?.source_path || ''}/${value?.path || state.path}`));
  if(value?.member)body.append(node('h2','','Archive member'),node('div','detail-path',value.member.name));
  const detail = (label, content) => { const row = node('div', 'detail-row'); row.append(node('span', '', label), node('span', '', content)); body.append(row); };
  detail('Location', (value?.root||state.root)==='mac'?'Mac-owned file':value?.mode || (state.root === 'local' ? 'Local' : 'Main'));
  if (value) { detail('Size', formatBytes(value.entry.size)); detail('Access', value.editable ? 'Local note editing' : 'Read-only'); detail('Offline', value.mode === 'local' ? 'Available locally' : value.pinned ? 'Pinned' : value.mode === 'cached' ? 'Evictable cache' : 'Not downloaded'); }
  if (value?.local_path !== undefined) detail(value.root==='mac'?'Local file':'Local replica', value.local_path);
  const annotation = value?.annotations || state.entries.find(e => e.relative_path === value?.path)?.annotations;
  if (annotation) for (const [field, label] of [['index', 'Text index'], ['embeddings', 'Embeddings'], ['ocr', 'OCR'], ['vision', 'Image description'], ['freshness', 'Source observation']]) detail(label, (annotation[field] || 'unknown').replaceAll('_', ' '));
  if (isBoxRef(value?.root) && !annotation) detail('Enrichment', 'Not observed');
  body.append(node('h2', '', 'Recent requests'));
  snapshot.timings.slice(0, 12).forEach(timing => { const row = node('div', 'timing'); row.dataset.tooltip = timing.detail; row.append(node('span', '', `${timing.operation} / ${timing.origin}`), node('strong', '', `${timing.ms} ms`)); body.append(row); });
  refreshNoteContext();
}

function setDetailsOpen(open){document.body.classList.toggle('details-open',open);$('#details').hidden=!open;$('#details-toggle').setAttribute('aria-expanded',String(open));}
function details() { const open = !document.body.classList.contains('details-open');setDetailsOpen(open);try{localStorage.setItem('note-context-open',String(open));}catch{}resizeSidebar(); renderDetails().catch(error => notice(error.message)); }

async function createEntry(directory,kind='note',destination) {
  try {
    const chosen=destination||(explorerFocus.mode==='focus'?explorerFocus.scope:state.creationTarget)||{root:state.root,path:state.path};
    const root=writableRoot(chosen.root)?chosen.root:!destination&&state.filesView==='empty'?state.activeBox:chosen.root,parent=writableRoot(chosen.root)?chosen.path:'';
    if (!writableRoot(root)) return notice('Create files in an available Box on Main or Mac. Storage is read-only.');
    const template=documentTemplate(kind);state.sidebarKnowledge=false;showSection('files');renderRoots();toggleNavigation(false);
    const result=await tree.beginCreate(root,parent,{directory,placeholder:directory?'New folder':template.name,onCommit:name=>api.mutate({root,path:parent,operation:directory?'create_folder':'create_note',name:directory?name:documentName(name,kind),...(!directory?{text:template.text}:{})})});
    if(!result)return;
    await tree.model.load(root,parent,{refresh:true});tree.render();
    if(!directory)await openFile(result.path,root);
    else if(state.filesView==='directory'&&state.root===root&&state.path===parent)await navigate(root,parent,{refresh:true});
    notice(root==='mac'?'Created on this Mac':'Created locally · source acceptance pending');
  } catch (error) { notice(error.message); }
}

async function renameTitle(value,name){
  if(value.titleRenaming)throw Error('This rename is still waiting for Main.');
  const path=value.path;await flushDraft();if(!await saver.save(value))throw Error('The note draft is preserved; resolve its save before renaming.');
  if(value.path!==path||value.deleted)throw Error('This file changed path. Review its current title before renaming.');
  value.titleRenaming=true;
  try{const result=await api.mutate({root:value.root,path,operation:'rename',name,binding_id:value.binding_id,base:value.base});notice(result.status==='pending_main'?'Rename saved · awaiting Main':'Rename requested');return result;}
  finally{value.titleRenaming=false;}
}

function fileActions(relative,root,entry){const rect=$('#file-actions').getBoundingClientRect(),member=state.document?.root===root&&state.document.path===relative?state.document.member:entry.member;return showFileMenu(root,{...entry,relative_path:relative,member},{clientX:rect.left,clientY:rect.bottom});}
async function showFileMenu(root,entry,event){
  const sequence=++contextSequence,relative=entry.relative_path,directory=entry.kind==='directory',target={root,path:directory?relative:relative.split('/').slice(0,-1).join('/')};
  const actions=[],action=(id,label,symbol,run,danger=false)=>actions.push({id,label,icon:symbol,run,danger}),run=key=>performFileAction(relative,root,entry,key);
  if(!directory){action('open-tab','Open in new tab','plus',()=>openFile(relative,root,{member:entry.member,newTab:true}));action('open-background','Open in background tab','copy-plus',()=>openFile(relative,root,{member:entry.member,newTab:true,background:true}));}
  action('reveal-tree','Reveal in file tree','locate-fixed',()=>revealInTree(root,relative));
  const lifecycle=directory&&relative&&mainContentRoot(root)?await api.action({kind:'file-review',input:{action:'inspect',root,path:relative}}).catch(()=>null):null;
  if(sequence!==contextSequence)return;
  if(lifecycle?.project)action('project-lifecycle',lifecycle.project.status==='archived'?'Restore project…':'Archive project…',lifecycle.project.status==='archived'?'archive-restore':'archive',()=>projectLifecycle(lifecycle.project.id,lifecycle.project.status==='archived'?'restore':'archive'));
  if(lifecycle?.project)action('export-project','Export project snapshot…','package-open',()=>exportSnapshot(lifecycle.project.id));
  if(directory&&relative&&mainContentRoot(root))action('working-copy','Make working copy on Mac…','copy',()=>makeWorkingCopy(root,relative));
  if(root==='storage'&&directory&&relative&&lifecycle?.decision==='retained_content')action('move-retained','Move retained collection…','folder-input',()=>moveEntry(relative,root,entry));
  if(directory)action('focus','Focus folder','focus',()=>focusFolder(root,relative));
  if(isBoxRef(root))action('timeline','Timeline','history',()=>openTimeline({root,path:relative}));
  if(!directory&&(mainContentRoot(root)||root==='archive'))action('save-copy','Save copy to Mac…','download',()=>api.downloadFile({root,path:relative}).then(result=>{if(!result.cancelled)notice('Copy saved on Mac. Main original unchanged.');}));
  if(writableRoot(root)){
    const selected=tree.selectedEntries(),entries=selected.some(e=>e.root===root&&e.path===relative)?selected:[{root,path:relative,directory,member:entry.member}];
    if(relative){action('copy','Copy','copy',()=>treeCommand('copy',entries));action('cut','Cut','scissors',()=>treeCommand('cut',entries));}
    if(directory&&fileClipboard)action('paste','Paste','clipboard-paste',()=>treeCommand('paste',[{root,path:relative,directory:true}]));
    if(directory&&isBoxRef(root))action('upload','Upload from Mac…','upload',()=>uploadFiles(target));
    if(root==='mac'&&relative)action('copy-main','Copy to Main…','upload',()=>sendMac(relative));
    action('new-note','New note','square-pen',()=>createEntry(false,'note',target));
    action('new-canvas','New canvas','layout-dashboard',()=>createEntry(false,'canvas',target));action('new-base','New base','list-tree',()=>createEntry(false,'base',target));
    action('new-folder','New folder','folder-plus',()=>createEntry(true,'note',target));
    if(relative&&!directory&&isEditableText(relative)&&entry.size<=512*1024)action('duplicate','Duplicate','copy',()=>duplicateFile(root,relative,entry));
    if(relative){action('rename','Rename','pencil',()=>run('rename'));action('move',directory?'Move folder to…':'Move file to…','folder-input',()=>run('move'));}
  }
  if(directory){action('find','Find file in folder','search',()=>openSearch('files',{root,path:relative}));if(mainContentRoot(root))action('offline','Download for offline use','download',()=>downloadFolder(root,relative));}
  if(directory&&state.roots.find(r=>r.ref===root)?.format==='zotero-webdav')action('webdav-files',rawApplicationRoots.has(root)?'Hide WebDAV companion files':'Show WebDAV companion files','files',async()=>{if(rawApplicationRoots.has(root))rawApplicationRoots.delete(root);else rawApplicationRoots.add(root);await tree.model.load(root,relative,{refresh:true});tree.render();if(state.filesView==='directory'&&state.root===root&&state.path===relative)await navigate(root,relative,{refresh:true});});
  if(root.startsWith('appdata_')&&!directory){action('open',entry.member?'Open attachment':'Open file','file',()=>openFile(relative,root,{member:entry.member}));if(!entry.member&&/\.zip$/i.test(entry.name))action('browse-attachments','Browse attachments','folder-open',()=>navigate(root,relative));action('save-copy',entry.member?'Save attachment copy…':'Download original…','download',()=>api.downloadFile({root,path:relative,member:entry.member}).then(result=>{if(!result.cancelled)notice('Copy saved. Main source unchanged.');}));}
  if(!root.startsWith('appdata_')&&root!=='application-data')action('properties',root==='mac'?'Properties':'Properties and folder rules','sliders-horizontal',()=>run('details'));
  if(isBoxRef(root)&&relative)action('enrich','Selective enrichment','sparkles',()=>run('enrich'));
  const paths=await api.paths({root,path:relative,member:entry.member}).catch(()=>({remote:state.roots.find(r=>r.ref===root)?.source_path?`${state.roots.find(r=>r.ref===root).source_path}/${relative}`:null}));
  if(sequence!==contextSequence)return;
  if(paths.remote)action('copy-path',entry.member?'Copy archive path':'Copy path','copy',()=>api.copyPath({root,path:relative,member:entry.member,location:'remote'}));
  if(entry.member)action('copy-member','Copy member name','copy',()=>api.copyPath({root,path:relative,member:entry.member,location:'member'}));
  if(paths.local){action('copy-local-path',paths.cached?'Copy cached-copy path':'Copy local path','hard-drive',()=>api.copyPath({root,path:relative,member:entry.member,location:'local'}));action('reveal-finder','Reveal in Finder','folder-open',()=>api.revealLocal({root,path:relative,member:entry.member}));}
  if(writableRoot(root)&&relative)action('trash','Remove with recovery','trash-2',()=>run('trash'),true);
  contextMenu.show(groupFileMenu(actions),{x:event.clientX,y:event.clientY});
}
async function duplicateFile(root,relative,entry){
  await flushDraft();const current=state.document?.root===root&&state.document.path===relative?state.document:await api.open({root,path:relative});
  if(current.kind!=='text')throw Error('Only editable text can be duplicated here.');
  const dot=entry.name.lastIndexOf('.'),suggestion=dot>0?`${entry.name.slice(0,dot)} copy${entry.name.slice(dot)}`:`${entry.name} copy.md`;
  const choice=await form('Duplicate file',[{name:'name',label:'Name',value:suggestion}],{submit:'Duplicate'});if(!choice)return;
  const result=await api.mutate({root,path:relative.split('/').slice(0,-1).join('/'),operation:'create_note',name:documentName(choice.name),text:current.text});
  await tree.refresh();await openFile(result.path,root);
}
async function treeCommand(action,entries){
  if(action==='rename'){const selected=clipboardEntries(entries);if(selected.length!==1)throw Error('Rename one entry at a time.');const e=selected[0];return performFileAction(e.path,e.root,{name:e.path.split('/').pop(),kind:e.directory?'directory':'file'},'rename');}
  if(action==='copy'||action==='cut'){fileClipboard={cut:action==='cut',entries:clipboardEntries(entries)};notice(`${fileClipboard.entries.length} ${action==='cut'?'ready to move':'copied'} · choose a destination folder and paste`);return;}
  if(action!=='paste')return;if(!fileClipboard)throw Error('Copy or cut files in the file tree first.');if(clipboardBusy)throw Error('A paste is already in progress.');
  const selected=entries.length===1?entries[0]:null,target=selected?{root:selected.root,path:selected.directory?selected.path:selected.path.split('/').slice(0,-1).join('/')} :state.creationTarget||{root:state.root,path:state.path};
  const snapshot=fileClipboard,plans=snapshot.entries.map(entry=>({entry,...pasteTarget(entry,target,snapshot.cut)}));
  // Validate the complete selection before the first owner is called.
  for(const {entry}of plans)if(!snapshot.cut&&entry.directory&&!(entry.root==='mac'&&isBoxRef(target.root)||mainContentRoot(entry.root)&&target.root==='mac'))throw Error('Folder copies use the reviewed Main ↔ Mac working-copy actions. Same-machine folder duplication is not available; nothing was changed.');
  clipboardBusy=true;let completed=0;
  try{stashDocument();await flushDraft();for(const {entry,path,name}of plans){
    const opened=tabs.get(entry.root,entry.path);if(opened?.dirty&&!await saver.save(opened))throw Error('Save the source draft before pasting.');
    if(snapshot.cut){const result=await moveEntry(entry.path,entry.root,{name:entry.path.split('/').pop(),kind:entry.directory?'directory':'file'},target);if(!result)throw Error('Move cancelled; remaining entries stay on the clipboard.');}
    else if(entry.root==='mac'&&isBoxRef(target.root)){if(!await sendMac(entry.path,target.path,target.root))throw Error('Copy cancelled; originals preserved.');}
    else if(entry.directory){if(!await makeWorkingCopy(entry.root,entry.path,path))throw Error('Copy cancelled; originals preserved.');}
    else{const value=opened||await api.open({root:entry.root,path:entry.path});if(value.kind!=='text'||value.entry.size>4*1024*1024)throw Error('This file needs Download original; clipboard paste supports text and reviewed working-copy folders.');await api.mutate({root:target.root,path:target.path,operation:'create_note',name,text:value.text});}
    completed++;if(snapshot.cut)snapshot.entries=snapshot.entries.filter(e=>e!==entry);
  }await tree.model.load(target.root,target.path,{refresh:true});tree.render();notice(`${completed} ${snapshot.cut?'move requests submitted':'copy requests submitted'} · source acceptance is shown separately`);
  }catch(error){notice(`${completed} of ${plans.length} submitted. ${error.message}`);}finally{clipboardBusy=false;if(fileClipboard===snapshot&&snapshot.cut&&!snapshot.entries.length)fileClipboard=null;}
}

async function exportSnapshot(project){
  const choice=await form('Export project snapshot',[{name:'mode',label:'Format',value:'portable',options:[{value:'portable',label:'Portable project'},{value:'human',label:'Human-readable documents'},{value:'archival',label:'Project with registration references'}]}],{submit:'Choose destination',description:'Policy-filtered tar snapshot. Source lifecycle is unchanged. Ownership and timestamps are normalized; retained recovery history is not included.'});
  if(!choice)return;
  const result=await api.action({kind:'project-export',project,mode:choice.mode});
  if(!result.cancelled)notice(`Snapshot saved · ${result.summary.included.count} entries · ${formatBytes(result.summary.archive_bytes)} · ${result.summary.ignored.count} excluded by policy`);
}
async function makeWorkingCopy(root,relative,target){
  await flushDraft();
  const choice=await form('Make working copy on Mac',[{name:'target',label:'New folder path in Mac Box',value:target||`${relative.split('/').at(-1)} copy`}],{submit:'Review copy',description:`${rootLabel(root)}/${relative}\nThe Main original stays unchanged. This is an independent copy, not sync or project registration.`});
  if(!choice)return;
  const review=await api.directoryCopy({action:'review',root,path:relative,target:choice.target}),summary=review.summary;
  if(!await confirmAction('Create this working copy?',`${rootLabel(root)}/${relative}\n→ Mac Box/${choice.target}\n\n${summary.included.count} entries · ${formatBytes(summary.included.bytes)} · ${summary.ignored.count} excluded\n${review.qualification}`,'Create copy'))return;
  const result=await api.directoryCopy({action:'apply',review:review.review,target:choice.target});
  await tree.refresh();notice(`Working copy created on Mac · ${result.summary.included.count} entries · Main original preserved`);return result;
}
async function performFileAction(relative,root,entry,action){
  await flushDraft();const choice={action};
  if(choice.action==='offline')return downloadFolder(root,relative);
  if (choice.action === 'details') {
    if (['local','mac'].includes(root)) { if (!document.body.classList.contains('details-open')) details(); return; }
    const explanation = await api.explain({ root, path: relative });
    const intent = explanation.intent || {};
    await form(entry.name, [], { submit: 'Close', description: [explanation.source_path, `Access: ${explanation.read_only ? 'Read-only' : 'Source permissions'}`, `Search policy: ${intent.search || 'Unknown'}`, `Enrichment policy: ${Array.isArray(intent.enrichment) ? intent.enrichment.join(', ') || 'None' : 'Inherited / unknown'}`, `Protection policy: ${intent.protect === null ? 'Inherited / unknown' : intent.protect}`, `Descriptor: ${intent.descriptor || 'Not observed'}`, explanation.qualification].join('\n\n') });
    return;
  }
  if (choice.action === 'enrich') {
    const selection = await form('Selective enrichment', [{ name: 'ocr', label: 'OCR', type: 'checkbox' }, { name: 'vision', label: 'Image descriptions', type: 'checkbox' }, { name: 'embeddings', label: 'Embeddings', type: 'checkbox' }, ...entry.kind === 'directory' ? [{ name: 'recursive', label: 'Include subfolders', type: 'checkbox' }] : []], { submit: 'Preview' });
    if (!selection) return;
    const preview = await api.enrich({ root, path: relative, directory: entry.kind === 'directory', ocr: Boolean(selection.ocr), vision: Boolean(selection.vision), embeddings: Boolean(selection.embeddings), recursive: Boolean(selection.recursive) });
    const description = `${preview.count} eligible files${preview.truncated ? ' · bounded selection; more files exist' : ''}\n${preview.inventory}\n\n${preview.entries.map(value => `${value.path}${value.blocked ? ` · blocked: ${value.blocked}` : value.wait ? ` · ${value.wait}` : ''}`).join('\n')}`;
    if (!preview.count) { await form('Enrichment preview', [], { submit: 'Close', description }); return; }
    if (!await confirmAction('Queue enrichment?', description, 'Queue selected stages')) return;
    const receipt = await api.enrich({ token: preview.token }); notice(`Enrichment requested · ${receipt.failed || 0} failed requests`); return;
  }
  const isMove = choice.action === 'move';
  if(isMove)return moveEntry(relative,root,entry);
  let name, target;
  if (choice.action === 'rename' || isMove) {
    const value = await form(isMove ? 'Move within Box' : 'Rename', [{ name: 'value', label: isMove ? 'Destination path relative to Box' : 'New name', value: isMove ? relative : entry.name }], { submit: isMove ? 'Move' : 'Rename' });
    if (!value) return;
    if (isMove) target = value.value; else name = value.value;
  } else {
    if(isBoxRef(root)&&entry.kind==='directory'){
      const review=await api.action({kind:'file-review',input:{action:'delete',root,path:relative}});
      if(review.decision==='archive_project')return projectLifecycle(review.project.id,'archive');
      if(!review.ordinary)throw Error(review.message);
    }
    if(!await confirmAction('Remove file or folder?', `${relative}\n\n${root==='mac'?'Mac retains the original in a hidden source-side recovery journal. Nothing is sent to Main.':'Main retains the existing source recovery journal.'} This does not erase recovery history.`, 'Remove with recovery'))return;
  }
  if(choice.action==='rename'&&isBoxRef(root)&&entry.kind==='directory')return moveEntry(relative,root,entry,{root,path:[relative.split('/').slice(0,-1).join('/'),name].filter(Boolean).join('/'),exact:true});
  const active=state.document?.root===root&&state.document?.path===relative;
  let result = await api.mutate({ root, path: relative, operation: choice.action === 'trash' ? 'trash' : 'rename', name, target, binding_id: active ? state.document.binding_id : undefined, base: active ? state.document.base : undefined });
  if (result.status === 'preview') {
    if (!await confirmAction('Move folder on Main?', `${result.from}\n→ ${result.to}\n\n${result.entries} entries · ${formatBytes(result.bytes)}\n${result.local_files} existing local records will follow; remote contents will not be downloaded.\n\n${result.qualification}`, 'Move folder')) return;
    result = await api.mutate({ root, path: relative, operation: 'confirm_folder_move', operation_id: result.operation_id });
  }
  await tree.model.load(root, relative.split('/').slice(0, -1).join('/'), { refresh: true }).catch(() => {}); tree.render();
  if (!state.document) await navigate(root, state.path, { refresh: true });
  notice(root==='mac'?'Saved on this Mac':result.status==='pending_main'?'Request saved · awaiting Main':'Request saved · check Activity for acceptance');
}

async function flushProject(relative,root=state.activeBox){
  stashDocument();await flushDraft();
  for(const value of tabs.items.values())if(value.root===root&&(value.path===relative||value.path.startsWith(relative+'/'))){
    if(value.dirty){await persistDraft(value);if(!await saver.save(value))throw Error('A project draft needs saving. All local text is preserved.');}
  }
}
async function projectLifecycle(project,operation,saved){
  const root=saved?.root||state.activeBox;
  const scopedAPI={action:input=>api.action({...input,box:root})};
  return reviewProjectLifecycle({api:scopedAPI,project,operation,saved,notice,flush:()=>saved?flushProject(saved.boxPath,root):window.flushWorkspace(),changed:async result=>{
    for(const value of tabs.items.values())if(result.kind==='archive'&&value.root===root&&(value.path===result.boxPath||value.path.startsWith(result.boxPath+'/'))){value.editable=false;editors.get(value)?.configure(value.path,false,Boolean(value.sourceMode));}
    await tree.refresh();if(sections.section==='project')await sections.show('project',{ref:project||result.project,refresh:true});
  }});
}
async function savedProjectOperations(){
  const saved=[...await api.action({kind:'project-lifecycle-list'}),...(await api.action({kind:'retained-list'})).map(op=>({...op,retained:true})),...(await api.action({kind:'workspace-path-list'})).map(op=>({...op,workspacePath:true})),...(await api.directoryCopy({action:'saved'})).map(op=>({...op,localCopy:true}))];if(!saved.length)return notice('No saved filesystem operations.');
  const choice=await form('Saved filesystem operations',[{name:'id',label:'Operation',options:saved.map(op=>({value:op.id,label:`${op.localCopy?op.path:op.boxPath||op.source} · ${op.kind||'relocation'} · ${op.state}`}))}],{submit:'Inspect'});
  if(choice){const op=saved.find(op=>op.id===choice.id);if(op.localCopy){
    if(op.state==='complete')return form('Working copy complete',[],{submit:'Close',description:`Mac Box/${op.path}\nMain source and project lifecycle are unchanged.`});
    if(!op.request)return form('Working copy not published',[],{submit:'Close',description:op.error||'The source was not changed. Create a fresh review to try again.'});
    if(await confirmAction('Recover this exact working copy?',`Mac Box/${op.path}\n\n${op.error||''}\nUse the saved verified snapshot. Existing files are never overwritten; an already published copy is recognised only by its exact native receipt.`,'Recover')){await api.directoryCopy({action:'recover',id:op.id});await tree.refresh();notice('Working copy verified · source unchanged');}
  }else if(op.workspacePath)await workspacePathReview(op);else if(op.retained)await retainedMove(op);else await projectLifecycle(op.project,op.kind,op);}
}
async function workspacePathReview(op){
  const kind=op.kind==='retain_child'?'Retain folder':'Move project';
  if(!['planned','preview','complete','cancelled'].includes(op.state))op=await api.action({kind:'workspace-path-inspect',id:op.id});
  const description=`${rootLabel(op.from_root||'box')}/${op.source}\n→ ${rootLabel(op.to_root)}/${op.destination}\n\n${op.entries} entries · ${formatBytes(op.bytes)}\n${op.qualification}\n${op.error||''}`;
  if(['complete','cancelled'].includes(op.state))return form(`${kind} · ${op.state}`,[],{submit:'Close',description});
  await flushProject(op.source);
  const choice=await form(kind,[{name:'action',label:'Action',value:'apply',options:[{value:'apply',label:['planned','preview'].includes(op.state)?'Apply reviewed move':'Recover this exact operation'},{value:'cancel',label:'Cancel only if nothing moved'}]}],{submit:'Continue',description});
  if(!choice)return;
  if(!await confirmAction(choice.action==='cancel'?'Cancel this operation?':kind,description,choice.action==='cancel'?'Verify and cancel':'Confirm'))return;
  const action=choice.action==='cancel'?'cancel':['planned','preview'].includes(op.state)?'apply':'recover';
  const result=await api.action({kind:`workspace-path-${action}`,id:op.id});
  await tree.refresh();
  if(['complete','cancelled'].includes(result.state))notice(result.state==='cancelled'?'Cancelled · no files moved':result.kind==='retain_child'?'Folder retained · parent project remains active':'Project moved · identity and history preserved');
  else await form('Filesystem operation needs review',[],{submit:'Close',description:`${result.error||result.qualification||'Main acceptance or local placement is not yet confirmed.'}\n\nOperation: ${op.id}\nUse Saved filesystem operations to inspect this same request. No automatic retry was started.`});
  return result;
}
async function retainedMove(op){
  if(!['planned','complete','cancelled'].includes(op.state))op=await api.action({kind:'retained-inspect',id:op.id});
  if(op.state==='complete'){await form('Retained move complete',[],{submit:'Close',description:`Storage/${op.source}\n→ Storage/${op.destination}\n\n${op.qualification}`});return op;}
  if(op.state==='cancelled'){await form('Retained move cancelled',[],{submit:'Close',description:`Storage/${op.source}\n\nMain verified the original source root and absent destination. Nothing was moved or deleted. The saved review and history remain.`});return op;}
  const recovery=op.state!=='planned';
  const decision=await form(recovery?'Saved retained operation':'Retained move review',[{name:'action',label:'Action',value:'apply',options:[{value:'apply',label:recovery?'Recover this exact move':'Move collection'},{value:'cancel',label:'Cancel if nothing moved'}]}],{submit:'Review',description:`Storage/${op.source}\n→ Storage/${op.destination}\n\n${op.entries} entries · ${formatBytes(op.bytes)}\n${op.qualification}\n${op.error||''}`});
  if(!decision)return;
  if(decision.action==='cancel'){
    if(!await confirmAction('Cancel this exact move?','Main will cancel only if the original source root still exists and the destination is absent. No files, newer edits or history will be removed.','Verify and cancel'))return;
    const result=await api.action({kind:'retained-cancel',id:op.id});
    if(result.state==='cancelled')notice('Move cancelled · files and history preserved');
    else await form('Cancellation needs review',[],{submit:'Close',description:`${result.error||'Cancellation not confirmed.'}\n\nOperation: ${op.id}\nRecover the same saved request; no automatic retry was started.`});
    return result;
  }
  if(!await confirmAction(recovery?'Review saved retained move':'Move retained collection?',`Storage/${op.source}\n→ Storage/${op.destination}\n\n${op.entries} entries · ${formatBytes(op.bytes)}\n${op.qualification}\n${op.error||''}`,recovery?'Recover this exact move':'Move collection'))return;
  const result=await api.action({kind:'retained-apply',id:op.id});
  if(result.state==='complete'){await tree.refresh();notice('Retained collection moved · original history preserved');}
  else await form('Retained move needs review',[],{submit:'Close',description:`${result.error||'Move not confirmed.'}\n\nOperation: ${op.id}\nNo automatic retry was started. Use Saved filesystem operations to inspect and recover this exact request.`});
  return result;
}
async function moveEntry(relative,root,entry,destination){
  await flushDraft();
  if(root==='mac'&&isBoxRef(destination?.root))return sendMac(relative,destination.path,destination.root);
  if(root==='mac'){
    if(destination&&destination.root!=='mac')throw Error('Copy to Main explicitly; local files are never silently archived or removed.');
    const choice=destination?{path:[destination.path,entry.name].filter(Boolean).join('/')} :await form('Move on Mac',[{name:'path',label:'Destination path in Mac Box',value:relative}],{submit:'Move'});
    if(!choice)return;const result=await api.mutate({root,path:relative,operation:'rename',target:choice.path});await tree.refresh();notice('Moved on this Mac');return result;
  }
  if(destination?.root==='mac'){if(entry.kind==='directory')return makeWorkingCopy(root,relative,[destination.path,entry.name].filter(Boolean).join('/'));throw Error('Use Save copy to Mac to keep a separate local copy. Main files are not moved to another machine.');}
  const choice=destination?{root:destination.root,path:destination.exact?destination.path:[destination.path,entry.name].filter(Boolean).join('/')}:
    await form('Move to…',[{name:'root',label:'Destination',value:root,options:[...(isBoxRef(root)?[{value:root,label:rootLabel(root)}]:[]),{value:'storage',label:'Storage'}]},{name:'path',label:'Path including final name',value:relative}],{submit:'Review'});
  if(!choice)return;
  const input={action:'move',root,path:relative,to_root:choice.root,to_path:choice.path};
  const review=await api.action({kind:'file-review',input});
  if(review.decision==='archive_project'||review.decision==='restore_project')return projectLifecycle(review.project.id,review.decision==='archive_project'?'archive':'restore');
  if(review.decision==='retained_content'&&root==='storage'&&choice.root==='storage'&&entry.kind==='directory')return retainedMove(await api.action({kind:'retained-preview',from:relative,to:choice.path}));
  if(['relocate_project','archive_collection'].includes(review.decision)&&review.project&&entry.kind==='directory')return workspacePathReview(await api.action({kind:'workspace-path-preview',operation:review.decision==='relocate_project'?'relocate_project':'retain_child',project:review.project.id,from:relative,to:choice.path}));
  if(!review.ordinary)throw Error(review.message);
  if(!isBoxRef(root)||choice.root!==root)throw Error('Cross-Box moves require an explicit reassociation review. No files were moved.');
  let result=await api.mutate({root,path:relative,operation:'rename',target:choice.path});
  if(result.status==='preview'){
    if(!await confirmAction('Move folder on Main?',`${result.from}\n→ ${result.to}\n\n${result.entries} entries · ${formatBytes(result.bytes)}\n${result.local_files} local records follow without downloading remote descendants.\n\n${result.qualification}`,'Move folder'))return;
    result=await api.mutate({root,path:relative,operation:'confirm_folder_move',operation_id:result.operation_id});
  }
  await tree.refresh();notice(result.status==='pending_main'?'Move saved · awaiting Main':'Move requested');return result;
}

async function sendMac(relative,destination,targetRoot=state.activeBox){
  if(destination===undefined){const choice=await form('Copy to Main',[{name:'path',label:`Destination folder in ${rootLabel(targetRoot)}`,value:''}],{submit:'Review copy',description:'The Mac original stays on this Mac. This does not enroll or index it.'});if(!choice)return;destination=choice.path;}
  await flushDraft();const review=await api.sendMac({action:'review',path:relative,destination,targetRoot});
  if(!await confirmAction('Copy to Main?',`${relative}\n→ ${rootLabel(targetRoot)} / ${destination||''}\n\n${review.entries.toLocaleString()} entries · ${(review.bytes/1024).toFixed(1)} KiB\nKeep the Mac original. Existing destination names are kept separately. This is a copy, not a move or sync enrollment.`,'Copy'))return;
  const result=await api.sendMac({action:'apply',review:review.review});notice('Copy queued to Main · Mac original preserved');return result;
}

async function downloadFolder(root=state.root,relative=state.path){
  try{
    const fields=[{name:'assets',label:'Also download attachments and other files',type:'checkbox'}];
    if(isBoxRef(root))fields.push({name:'continuous',label:'Keep this folder in sync',type:'checkbox'});
    const choice=await form('Offline folder',fields,{submit:'Preview',description:`${rootLabel(root)}/${relative}`});if(!choice)return;
    const preview=await api.offline({operation:'preview',root,path:relative,assets:Boolean(choice.assets)});
    if(!await confirmAction('Download folder?',`${preview.files}${preview.limited?'+':''} files · ${formatBytes(preview.bytes)}${preview.limited?' or more':''}\n${preview.qualification}\n\nNo files are moved. Indexing and enrollment settings do not change.`,'Download'))return;
    await api.offline({operation:choice.continuous?'select':'start',root,path:relative,assets:Boolean(choice.assets)});notice(choice.continuous?'Continuous folder sync enabled.':'Folder download started.');
  }catch(error){notice(error.message);}
}
async function offlineProgress(){
  try{
    const selected=await api.offline({operation:'selections'});
    const jobs=[...selected.map(s=>({...s,selection:true,files:0})),...await api.offline({operation:'jobs'})];if(!jobs.length)return notice('No folder downloads or sync selections.');
    const choice=await form('Folder downloads',[{name:'id',label:'Operation',options:jobs.map(j=>({value:j.id,label:`${j.path||j.root} · ${j.state} · ${j.files} files · ${formatBytes(j.bytes)}${j.reason?' · '+j.reason:''}`}))},{name:'operation',label:'Action',options:[{value:'close',label:'Close'},{value:'pause',label:'Pause'},{value:'resume',label:'Resume'}]}],{submit:'Continue'});
    if(choice&&choice.operation!=='close'){const job=jobs.find(j=>j.id===choice.id);await api.offline({...choice,operation:job?.selection?choice.operation==='pause'?'pause_selection':'resume_selection':choice.operation});notice(choice.operation==='pause'?'Paused. Local files are preserved.':'Resumed.');}
  }catch(error){notice(error.message);}
}

function openSearch(mode='files',scope={root:state.activeBox,path:''}){if(mode==='search'&&scope.root==='workspace')scope={root:state.activeBox,path:''};state.fileScope={...scope};state.searchScope={...scope};palette(mode);}
const command = (label, iconName, run, hint = '') => ({ label, icon: iconName, run, hint });
const modeCommand=(label,symbol,run,hint)=>({...command(label,symbol,run,hint),keepOpen:true});
async function openProjectData(project){
  const page=await api.applicationData({project});state.roots=(await api.state()).roots;renderRoots();
  if(!page.roots.length)return notice('This project has no approved application content view.');
  if(page.roots.length===1)return navigate(page.roots[0].ref,'');
  state.applicationChoices=page.roots.map(root=>command(root.label,'database',()=>navigate(root.ref,''),root.available?'Read-only - on demand':root.reason||'Unavailable'));
  palette('application-data');$('#palette-input').placeholder='Application data';$('#palette-input').setAttribute('aria-label','Application data');
}
function fileCommands(){
  const known=new Map(),remember=(root,relative,name,symbol='file-text',member)=>{
    const scope=state.paletteMode==='files'&&state.fileScope;if(scope&&(scope.root==='workspace'?!['box','mac'].includes(root):scope.root!==root||scope.path&& !relative.startsWith(scope.path+'/')))return;
    const k=JSON.stringify([root,relative,member]);if(!known.has(k))known.set(k,command(name||member?.name||relative.split('/').pop(),symbol,intent=>openFile(relative,root,{member,...intent}),`${root==='box'?'Main':root==='mac'?'Mac':rootLabel(root)} · ${relative.split('/').slice(0,-1).join('/')}`));
  };
  for(const value of state.recents){
    const parent=value.path.split('/').slice(0,-1).join('/'),page=tree.model.branches.get(tree.model.key(value.root,parent))?.page;
    if(tabs.get(value.root,value.path)?.deleted||page&&!page.has_more&&!page.entries.some(entry=>entry.relative_path===value.path))continue;
    remember(value.root,value.path,null,'history',value.member);
  }
  for(const value of tabs.items.values())if(!value.deleted)remember(value.root,value.path,value.entry.name,fileIcon(value.entry),value.member);
  for(const branch of tree.model.branches.values())for(const entry of branch.page?.entries||[])if(entry.kind==='file')remember(branch.root,entry.relative_path,entry.name,fileIcon(entry),entry.member);
  for(const entry of state.filenameResults||[])remember(entry.root,entry.relative_path,entry.name,fileIcon(entry));
  return [...known.values()];
}
async function uploadFiles(target=state.creationTarget||{root:state.root,path:state.path}) {
  if(!isBoxRef(target.root))throw Error('Select a Box folder for the upload.');
  await api.uploadPick(target);return uploadProgress();
}
async function metadataRefresh(target={root:state.root==='local'?'box':state.root,path:state.path}){
  if(!isBoxRef(target.root))throw Error('Markdown metadata refresh is available for Box.');
  let snapshot=await api.metadataRefresh({action:'start',path:target.path||''}),running=true,closed=false,pending,lastError='';
  const finished=()=>snapshot.state.startsWith('complete')||snapshot.state==='cancelled';
  const panel=node('div','metadata-progress'),status=node('p'),counts=node('dl','inspector-facts'),problems=node('p','muted'),button=node('button','quiet-action','Pause');button.type='button';panel.append(status,counts,problems,button);
  const render=()=>{status.textContent=`${snapshot.state} · Box/${snapshot.path}`;counts.replaceChildren();for(const [key,value]of Object.entries(snapshot.counts||{}))counts.append(node('dt','',key),node('dd','',String(value)));problems.textContent=[lastError,...snapshot.diagnostics||[]].filter(Boolean).join('\n');button.textContent=snapshot.state==='complete_with_errors'?'Retry failed files':running?'Pause':'Resume';button.disabled=snapshot.state==='complete'||snapshot.state==='cancelled';};
  const run=async()=>{while(running&&!closed&&!finished()){snapshot=await api.metadataRefresh({action:'step',id:snapshot.id});render();}if(finished()){running=false;render();}};
  const start=()=>{pending=run().catch(error=>{running=false;lastError=error.message;render();notice(error.message);});};
  button.onclick=async()=>{try{if(running){running=false;await pending;snapshot=await api.metadataRefresh({action:'pause',id:snapshot.id});render();}else{if(snapshot.state==='complete_with_errors')snapshot=await api.metadataRefresh({action:'retry',id:snapshot.id});lastError='';running=true;render();start();}}catch(error){lastError=error.message;render();}};
  render();start();try{await form('Markdown metadata',[],{submit:'Close',content:panel});}finally{closed=true;running=false;await pending;if(!finished())await api.metadataRefresh({action:'pause',id:snapshot.id}).catch(()=>{});}
}
let uploadPanel;
async function uploadProgress(){
  if($('#action-dialog').open)return;
  uploadPanel=node('div','transfer-list');
  const panel=uploadPanel;
  const render=async()=>{
    const entries=await api.uploads();if(uploadPanel!==panel)return;
    uploadPanel.replaceChildren();
    const controls=node('div','settings-actions');for(const [action,title]of [['pause_all','Pause all'],['resume_all','Resume paused']]){const b=node('button','quiet-action',title);b.type='button';b.onclick=async()=>{try{await api.uploads({action:'control',control:action});await render();}catch(error){notice(error.message);}};controls.append(b);}uploadPanel.append(controls);
    if(!entries.length)uploadPanel.append(node('p','muted','No uploads.'));
    for(const item of entries){
      const row=node('div','transfer-row'),label=node('div','transfer-description');
      label.append(node('strong','',item.path||item.name),node('small','muted',`${item.state}${item.kind==='file'?` · ${formatBytes(item.offset)} / ${formatBytes(item.size)}`:''}${item.error?` · ${item.error}`:''}`));
      if(item.exclusionCount)label.append(node('small','muted',`${item.exclusionCount} exclusions\n${item.excluded.join('\n')}`));
      row.append(label);
      if(item.kind==='file'&&item.size){const p=document.createElement('progress');p.max=item.size;p.value=item.offset;label.append(p);}
      if(['queued','uploading','paused','held'].includes(item.state))for(const [action,symbol] of [[['paused','held'].includes(item.state)?'resume':'pause',['paused','held'].includes(item.state)?'play':'pause'],['cancel','x']]){
        const b=node('button','icon');b.type='button';b.setAttribute('aria-label',`${action} ${item.name}`);b.dataset.tooltip=action;b.append(icon(symbol));b.onclick=async()=>{b.disabled=true;try{await api.uploads({action:'control',id:item.id,control:action});}catch(error){notice(error.message);}finally{b.disabled=false;await render();}};row.append(b);
      }
      uploadPanel.append(row);
    }
    icons(uploadPanel);
  };
  await render();const stop=api.onEvent(event=>{if(event.type==='uploads-changed')render().catch(error=>notice(error.message));});
  try{await form('Uploads',[],{submit:'Close',content:uploadPanel});}finally{stop();uploadPanel=null;}
}
function openCurrentGraph(){
  let scope=explorerFocus.mode==='focus'?explorerFocus.scope:{root:state.root==='local'?'box':state.root,path:''};const value=state.document;
  if(value&&(scope.root!==value.root||scope.path&&value.path!==scope.path&&!value.path.startsWith(scope.path+'/')))scope={root:value.root,path:value.path.split('/').slice(0,-1).join('/')};
  return openKnowledge('graph',scope,value?{root:value.root,path:value.path}:null);
}
function commands() {
  const actions = [
    command('Focus current folder','focus',()=>focusFolder(state.root,state.path)),command('Show all files','folders',()=>changeExplorer('explorer')),command('Choose focused folder','folder-search',chooseFocus),
    ...['files', 'projects', 'activity', 'schedules', 'system'].map(section => command(`Open ${section}`, section === 'files' ? 'folder' : section === 'projects' ? 'blocks' : section === 'activity' ? 'activity' : section === 'schedules' ? 'calendar-clock' : 'server-cog', () => showSection(section))),
    ...sections.commands,
    ...state.roots.filter(root => !root.hidden).map(root => command(`Open ${root.ref==='mac'?'Mac Box':root.ref==='box'?'Main Box':root.label}`, root.ref === 'storage' ? 'archive' : 'folder', () => navigate(root.ref, ''))),
    command('Open local recovery files', 'hard-drive', () => navigate('local', '')),
    command('New tab','plus',newTab),
    modeCommand('Search files','file-search',()=>openSearch('files'),'⌘O'),
    modeCommand('Search contents','search',()=>openSearch('search')),
    command('Reopen closed tab','rotate-ccw',reopenTab,'⌘⇧T'),command('Keyboard shortcuts','keyboard',()=>settings.open('shortcuts')),
    command('New note', 'square-pen', () => createEntry(false),'⌘N'), command('New folder', 'folder-plus', () => createEntry(true)),
    command('Upload files or folders to Main…','upload',()=>uploadFiles()),command('Upload progress','list-checks',uploadProgress),
    command('Check for Loom updates','download',async()=>{await api.updates({action:'check'});settings.open('updates');}),
    command('Refresh Markdown navigation metadata','tags',()=>metadataRefresh()),command('Files and sync settings','folder-sync',()=>settings.open('files')),
    command('New base','list-tree',()=>createEntry(false,'base')),command('New canvas','layout-dashboard',()=>createEntry(false,'canvas')),
    command('Browse knowledge','library',()=>openKnowledge()),command('Open graph','network',openCurrentGraph),
    command('Open timeline','history',()=>openTimeline()),
    command('Zoom in','zoom-in',()=>api.zoom({action:'in'}),'⌘+'),command('Zoom out','zoom-out',()=>api.zoom({action:'out'}),'⌘−'),command('Reset interface zoom','scan',()=>api.zoom({action:'reset'}),'⌘0'),
    command('Download current folder for offline use','download',()=>downloadFolder()),
    ...(mainContentRoot(state.root)&&state.path?[command('Make working copy of current folder on Mac…','copy',()=>makeWorkingCopy(state.root,state.path))]:[]),
    ...state.roots.filter(value=>isBoxRef(value.ref)&&value.available!==false).map(value=>command(`Use ${value.label} workspace`,'folder',()=>{const section=state.section;sections.hide();selectBox(value.ref);showSection(section);})),
    command('Folder download progress and pause/resume','list-checks',offlineProgress),
    command('Saved filesystem operations','archive',savedProjectOperations),
    command('Refresh view', 'refresh-cw', refreshView),
    command('Open settings','settings',()=>settings.open(),'⌘,'),
    ...themeOptions.map(theme=>command(`Theme: ${theme.name}`,'palette',()=>settings.selectTheme(theme.id))),
    command('Parent folder', 'arrow-up', () => navigate(state.root, state.path.split('/').slice(0, -1).join('/'))),
    command('Show file details', 'panel-right', details), command('Reconnect to Main', 'server', reconnect),
    command('Toggle left sidebar','panel-left',()=>toggleNavigation(),'⌘\\'),
    ...(state.document&&isMarkdown(state.document.path)?[command('Show backlinks','corner-up-left',()=>showRelations(true)),command('Show forward links','link',()=>showRelations(false))]:[]),
    ...syntaxThemes.map(theme=>command(`Syntax theme: ${theme.name}`,'palette',()=>setSyntaxTheme(theme.id))),
    command('Open local recovery folder', 'folder-input', chooseLocal),
    command('Collapse all folders', 'fold-vertical', () => { tree.cancelEmptyCreate();tree.model.collapseAll(); tree.render(); }),
    command(state.hideReadmeAgents?'Show README.md and AGENTS.md':'Hide README.md and AGENTS.md', 'eye-off', toggleExplorerVisibility),
    command('Refresh expanded folders', 'refresh-cw', () => tree.refresh()),
  ];
  if (state.document) {actions.push(command('Reveal current file in tree','locate-fixed',()=>revealInTree(state.document.root,state.document.path)));actions.push(command('Close file', 'x', closeDocument)); actions.push(command('Refresh current file', 'refresh-cw', () => openFile(state.document.path, state.document.root, { refresh: true })));if(editors.has(state.document))actions.push(command('Find and replace in file','search',()=>editors.get(state.document).find(),'⌘F')); }
  if(state.document&&(mainContentRoot(state.document.root)||state.document.root==='archive'))actions.push(command('Save copy to Mac…','download',()=>api.downloadFile({root:state.document.root,path:state.document.path})));
  if (state.document?.editable) {
    if(state.document.root==='mac'&&isMarkdown(state.document.path))actions.push(command('Retry local attachment imports','rotate-ccw',async()=>{const value=state.document;const items=await api.attachments({root:value.root,path:value.path,binding:value.binding_id,action:'retry-local'});editors.get(value)?.reconcileAttachments(items);notice('Local attachment imports checked. Existing files preserved.');}));
    if (isMarkdown(state.document.path)) {actions.push(command(state.document.sourceMode ? 'Live Markdown preview' : 'Markdown source', 'code-2', toggleEdit));actions.push(command('Add property','list-plus',()=>{if(state.document.sourceMode)toggleEdit();editors.get(state.document)?.addProperty();}));actions.push(command('Insert attachment…','paperclip',()=>editors.get(state.document)?.importAttachment()));actions.push(command('Insert section divider','minus',()=>editors.get(state.document)?.insertDivider()));}
    else if (visualFile(state.document)) actions.push(command(state.editing ? 'View file' : 'Edit source', 'pencil', toggleEdit));
    actions.push(command('Save local file', 'save', save, '⌘S'));
  }
  if (state.document && state.document.local_path === undefined && !state.document.root.startsWith('appdata_')&&state.document.root !== 'local' && state.document.entry.size <= 256 * 1024 ** 2) actions.push(command(state.document.pinned ? 'Allow cache eviction' : 'Keep offline', 'pin', pin));
  if (state.document) actions.push(command('File actions', 'ellipsis', () => fileActions(state.document.path, state.document.root, state.document.entry)));
  if (state.page?.has_more) actions.push(command('Load more files', 'chevrons-down', loadMore));
  if(tabs.target(-1))actions.push(command('Back','arrow-left',()=>goHistory(-1),'⌘['));
  if(tabs.target(1))actions.push(command('Forward','arrow-right',()=>goHistory(1),'⌘]'));
  return [...actions.map(value=>({...value,group:'Commands'})), ...state.entries.filter(entry=>entry.kind==='directory').map(entry => ({...command(entry.name, fileIcon(entry), () => navigate(state.root, entry.relative_path), 'folder'),group:'Folders'})), ...fileCommands().map(value=>({...value,group:'Recent and loaded files'}))];
}
function renderPalette() {
  const query = $('#palette-input').value.toLowerCase();
  const source=state.paletteMode==='application-data'?state.applicationChoices||[]:state.paletteMode==='search'?state.searchResults||[]:state.paletteMode==='links'?state.linkResults||[]:state.paletteMode==='tag'?state.tagResults||[]:state.paletteMode==='files'?fileCommands():commands();
  state.paletteItems = source.filter(value => state.paletteMode==='search'||`${value.label} ${value.hint}`.toLowerCase().includes(query)).slice(0, 60);
  const exact=['files','commands'].includes(state.paletteMode)?quickOpenPath($('#palette-input').value,state.roots,state.root):null;
  if(exact)state.paletteItems.unshift(command(exact.path.split('/').pop(),'file-input',intent=>openFile(exact.path,exact.root,intent),`${rootLabel(exact.root)}/${exact.path}`));
  state.paletteIndex = Math.max(0, Math.min(state.paletteIndex, state.paletteItems.length - 1));
  if(state.paletteItems[state.paletteIndex]?.disabled){const next=state.paletteItems.findIndex((value,index)=>index>=state.paletteIndex&&!value.disabled);state.paletteIndex=next<0?state.paletteItems.findIndex(value=>!value.disabled):next;}
  const results=[];let group;
  for(const [index,value] of state.paletteItems.entries()){
    if(value.group&&value.group!==group){const heading=node('div','palette-group',value.group);heading.setAttribute('role','presentation');results.push(heading);group=value.group;}
    const button = node('button', 'palette-result'); button.id=`palette-option-${index}`;button.tabIndex=-1;button.setAttribute('role', 'option'); button.setAttribute('aria-selected', String(index === state.paletteIndex)); button.append(icon(value.icon), node('span', '', value.label), node('small', '', value.hint));
    button.disabled=Boolean(value.disabled);if(value.disabled)button.setAttribute('aria-disabled','true');
    button.onpointermove=()=>{if(button.disabled||state.paletteIndex===index)return;state.paletteIndex=index;$('#palette-results [aria-selected=true]')?.setAttribute('aria-selected','false');button.setAttribute('aria-selected','true');$('#palette-input').setAttribute('aria-activedescendant',button.id);};
    bindNavigation(button,(intent,event)=>runPaletteItem(value,event));results.push(button);
  }
  $('#palette-results').replaceChildren(...results);
  $('#palette-input').setAttribute('role','combobox');$('#palette-input').setAttribute('aria-autocomplete','list');$('#palette-input').setAttribute('aria-controls','palette-results');$('#palette-input').setAttribute('aria-expanded','true');
  if(state.paletteItems[state.paletteIndex])$('#palette-input').setAttribute('aria-activedescendant',`palette-option-${state.paletteIndex}`);else $('#palette-input').removeAttribute('aria-activedescendant');
  $('#palette-hint').textContent=state.paletteMode==='search'?state.searchMessage||'Current working copies + published Main index':state.paletteMode==='files'?state.filenameMessage||'Recent and cached files':state.paletteMode==='tag'?state.tagMessage:state.paletteMode==='links'?state.linkMessage:'';
  $('#palette-hint').hidden=!$('#palette-hint').textContent;
  if (!state.paletteItems.length) $('#palette-results').append(node('p', 'empty', state.paletteMode==='search'?state.searchMessage||'Enter a query':state.paletteMode==='links'||state.paletteMode==='tag'?'No known matches in this coverage':state.paletteMode==='files'?state.filenameMessage||'No matching recent files':'No matching files or commands'));
  if(state.paletteMode==='files'&&state.filenameCursor){const more=node('button','palette-more',state.filenameLoading?'Searching…':'Continue filename search');more.disabled=Boolean(state.filenameLoading);more.onclick=()=>loadFilenamePage(true);$('#palette-results').append(more);}
  icons();
}
function runPaletteItem(value,event){if(value.disabled)return;if(!value.keepOpen)$('#palette').close();Promise.resolve().then(()=>value.run(navigationIntent(event))).catch(error=>notice(error.message));}
function palette(mode='commands') {
  if(document.querySelector('dialog[open]:not(#palette)'))return;
  clearTimeout(paletteSearchTimer);++paletteSearchSequence;api.search({cancel:true}).catch(()=>{});api.find({cancel:true}).catch(()=>{});state.filenameLoading=null;state.filenameResults=[];state.filenameCursor=null;state.filenameMessage='';state.paletteMode=mode;state.searchResults=[];state.searchMessage='';$('#palette-back').hidden=mode==='commands';$('#palette-input').placeholder=mode==='search'?'Search contents':mode==='links'?'Linked notes':mode==='tag'?'Tagged notes':mode==='files'?'Search files':'Open file or run command';$('#palette-input').setAttribute('aria-label',$('#palette-input').placeholder);$('#palette-input').value = ''; state.paletteIndex = 0;renderPaletteScopes(); renderPalette(); if(!$('#palette').open)$('#palette').showModal(); $('#palette-input').focus();
}
function renderPaletteScopes(){
  const kinds=$('#palette-kind');kinds.replaceChildren();kinds.hidden=!['files','search'].includes(state.paletteMode);
  if(!kinds.hidden)for(const [mode,label]of [['files','Files'],['search','Contents']]){const b=node('button',`palette-scope${state.paletteMode===mode?' selected':''}`,label);b.setAttribute('aria-pressed',String(state.paletteMode===mode));b.onclick=()=>{const query=$('#palette-input').value,scope=state[state.paletteMode==='files'?'fileScope':'searchScope'];openSearch(mode,scope);$('#palette-input').value=query;updatePalette();};kinds.append(b);}
  const bar=$('#palette-scope');bar.replaceChildren();bar.hidden=!['files','search'].includes(state.paletteMode);if(bar.hidden)return;
  const mode=state.paletteMode,key=mode==='files'?'fileScope':'searchScope',current=state[key]||{root:'box',path:''};state[key]=current;
  const scopes=[...(mode==='files'?[{root:'workspace',path:'',label:'Personal + Mac'}]:[]),...state.roots.filter(value=>isBoxRef(value.ref)&&value.available!==false).map(value=>({root:value.ref,path:'',label:`${value.label} · Main`})),{root:'storage',path:'',label:'Storage'},{root:'mac',path:'',label:'Box · Mac'}];
  if(isBoxRef(state.root)&&state.path){const project=state.path.startsWith('Projects/')?state.path.split('/').slice(0,2).join('/'):'';if(mode==='files'||project)scopes.push({root:state.root,path:mode==='files'?state.path:project,label:mode==='files'?'This folder':'This project'});}
  if(!scopes.some(s=>s.root===current.root&&s.path===current.path))scopes.push({...current,label:current.path.split('/').pop()||rootLabel(current.root)});
  for(const scope of scopes){const b=node('button',`palette-scope${current.root===scope.root&&current.path===scope.path?' selected':''}`,scope.label);b.setAttribute('aria-pressed',String(current.root===scope.root&&current.path===scope.path));b.dataset.tooltip=`${rootLabel(scope.root)}/${scope.path}`;b.onclick=()=>{state[key]={root:scope.root,path:scope.path};renderPaletteScopes();updatePalette();$('#palette-input').focus();};bar.append(b);}
}
async function loadFilenamePage(more=false){
  const sequence=paletteSearchSequence,query=$('#palette-input').value.trim(),scope=state.fileScope||{root:'box',path:''};
  if(query.length<2||state.filenameLoading)return;
  const loading=Symbol();state.filenameLoading=loading;state.filenameMessage='Searching filenames…';renderPalette();
  const current=()=>sequence===paletteSearchSequence&&$('#palette').open;let localMessage='';const started=performance.now();
  const remember=entries=>{const all=new Map(state.filenameResults.map(e=>[JSON.stringify([e.root,e.relative_path]),e]));for(const e of entries)all.set(JSON.stringify([e.root,e.relative_path]),e);state.filenameResults=[...all.values()];};
  try{
    const owner=scope.root==='workspace'?{root:'box',path:''}:scope;
    if(!more)for(const knownScope of scope.root==='workspace'?[{root:'mac',path:''},owner]:[owner]){try{const known=await api.find({...knownScope,query,phase:'known'});if(!current())return;remember(known.entries.map(e=>({...e,root:knownScope.root})));state.filenameMessage=`${state.filenameResults.length} known files · searching filenames…`;renderPalette();}catch(error){localMessage=error.message;}}
    let page,pages=0;do{
      page=await api.find({...owner,query,...(more||pages)&&state.filenameCursor?{cursor:state.filenameCursor}:{}});if(!current())return;
      remember((page.entries||[]).map(e=>({...e,root:owner.root})));state.filenameCursor=page.next_cursor;pages++;
      state.filenameMessage=`${state.filenameResults.length} files · ${rootLabel(owner.root)}/${owner.path} · ${page.has_more?'searching more folders…':page.notice||page.coverage||'partial coverage'}`;renderPalette();
    }while(page.has_more&&page.next_cursor&&pages<12&&performance.now()-started<1800&&state.filenameResults.length<60&&current());
    if(current()){state.filenameMessage=`${state.filenameResults.length} files · ${Math.round(performance.now()-started)} ms · ${page.has_more?'More folders remain':page.notice|| (page.coverage==='complete'?'Filesystem search complete':'Limited coverage')}${localMessage?' · '+localMessage:''}`;renderPalette();}
  }catch(error){if(current()){state.filenameMessage=[error.message,localMessage].filter(Boolean).join(' · ');renderPalette();}}finally{if(state.filenameLoading===loading){state.filenameLoading=null;if(current())renderPalette();}}
}
function updatePalette(){
  state.paletteIndex=0;clearTimeout(paletteSearchTimer);const sequence=++paletteSearchSequence;
  api.search({cancel:true}).catch(()=>{});
  if(state.paletteMode==='files'){
    api.find({cancel:true}).catch(()=>{});state.filenameLoading=null;state.filenameResults=[];state.filenameCursor=null;state.filenameMessage='';renderPalette();
    if($('#palette-input').value.trim().length>=2&&!quickOpenPath($('#palette-input').value,state.roots,state.root))paletteSearchTimer=setTimeout(()=>loadFilenamePage(),200);return;
  }
  if(state.paletteMode!=='search')return renderPalette();
  const query=$('#palette-input').value.trim(),scope=state.searchScope||{root:'box',path:''};state.searchResults=[];state.searchMessage=query.length<2?'Enter at least two characters':'Searching current working copies…';renderPalette();
  if(query.length<2)return;
  paletteSearchTimer=setTimeout(async()=>{
    const current=()=>sequence===paletteSearchSequence&&$('#palette').open;
    const renderHits=hits=>{state.searchResults=hits.map(hit=>({...command(hit.name,'file-search',intent=>openFile(hit.relative_path,hit.root||scope.root,intent),`${hit.relative_path}${hit.excerpt?' · '+hit.excerpt:''}`),group:hit.origin==='working'?'Current working copies':'Published Main index'}));};
    let working=[];
    try{const local=await api.search({...scope,query,phase:'local'});if(!current())return;working=local.results||[];renderHits(working);state.searchMessage=`${working.length} local matches${scope.root==='mac'?' · Known working copies only':' · Searching published Main index…'}`;renderPalette();
      if(scope.root==='mac')return;
      const result=await api.search({...scope,query,phase:'indexed'});if(!current())return;
      renderHits(mergeSearchHits(working,result.results||[]));state.searchMessage=`${working.length} local · ${result.results.length} indexed · ${result.ms} ms · ${result.notice}`;renderPalette();
    }catch(error){if(current()){state.searchMessage=`${working.length} local matches · Main index: ${error.message}`;renderPalette();}}
  },180);
}
async function closeTab(id) {
  stashDocument(); await flushDraft(); const page=tabs.page(id),current=page?.kind==='file'?tabs.get(page.root,page.path,page.member):null,wasActive=id===tabs.activeTab;
  if (current) {
    if (!await saver.save(current)) { notice('The draft remains open. Resolve the save issue before closing it.'); return; }
  }
  const next=tabs.close(id);if(wasActive){if(next)return showPage(next,{tab:tabs.activeTab,record:false});return newTab();}
  renderTabs();pruneEditors();tree.render();renderDetails();
}
async function closeDocument() { return closeTab(tabs.activeTab); }
function toggleEdit() {
  if (!state.document||state.document.deleted) return;
  stashDocument();
  if (isMarkdown(state.document.path)) state.document.sourceMode = !state.document.sourceMode;
  else if (visualFile(state.document)) { state.editing = !state.editing; state.document.editing = state.editing; }
  renderDocument();
}
async function pin() { try { const value = state.document; $('#pin').disabled = true; const result = await api.pin({ root: value.root, path: value.path, value: !value.pinned }); value.pinned = result.pinned; value.mode = 'cached'; renderDocument(); await renderDetails(); } catch (error) { notice(error.message); } finally { if (state.document) $('#pin').disabled = state.document.entry.size > 256 * 1024 ** 2; } }
async function chooseLocal() { try { await closeDocument(); const result = await api.chooseLocal(); if (result) { tree.model.resetRoot('local'); state.roots[0] = result; await navigate('local', ''); } } catch (error) { notice(error.message); } }
async function reconnect() { connection(false, 'Connecting to Main'); try { state.roots = await api.connect(); connection(true); renderRoots(); } catch (error) { connection(false); notice(error.message); } }
async function refreshView(){
  try{
    const current=state.document;await tree.refresh();
    if(state.section!=='files')return sections.show(sections.section,{ref:sections.ref,refresh:true});
    if(current&&state.document===current)return openFile(current.path,current.root,{refresh:true});
    if(state.filesView==='directory')return navigate(state.root,state.path,{refresh:true});
    if((await api.state()).connectionState!=='connected')await reconnect();
  }catch(error){notice(error.message);}
}

$('#palette-button').onclick = ()=>palette(); $('#palette-close').onclick = () => $('#palette').close();$('#palette-back').onclick=()=>palette();
let backdropStart=false;
$('#palette').addEventListener('pointerdown',event=>{const rect=$('#palette').getBoundingClientRect();backdropStart=event.target===$('#palette')&&(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom);});
$('#palette').addEventListener('click',event=>{const rect=$('#palette').getBoundingClientRect();if(backdropStart&&event.target===$('#palette')&&(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom))$('#palette').close();backdropStart=false;});
$('#palette').addEventListener('close',()=>{clearTimeout(paletteSearchTimer);++paletteSearchSequence;api.find({cancel:true}).catch(()=>{});api.search({cancel:true}).catch(()=>{});$('#palette-input').setAttribute('aria-expanded','false');$('#palette-input').removeAttribute('aria-activedescendant');});
document.addEventListener('pointerdown',()=>{++contextSequence;},true);
$('#connection-button').onclick = reconnect; $('#refresh').onclick = refreshView;
$('#explorer-global').onclick=()=>changeExplorer('explorer').catch(e=>notice(e.message));$('#explorer-focus').onclick=()=>changeExplorer('focus').catch(e=>notice(e.message));$('#explorer-scope').onclick=()=>chooseFocus().catch(e=>notice(e.message));
$('#explorer-knowledge').onclick=()=>{state.sidebarKnowledge=true;knowledgeNavigation();openKnowledge().catch(e=>notice(e.message));};
$('#settings-button').onclick=()=>settings.open();
$('#update-button').onclick=()=>settings.open('updates');
function updateIndicator(value){const b=$('#update-button');b.hidden=!value.available||!['available','downloading','ready'].includes(value.state);b.dataset.tooltip=value.available?`Loom ${value.available.version} · ${value.state}`:'Loom update';b.setAttribute('aria-label',b.dataset.tooltip);}
api.updates?.({action:'status'}).then(updateIndicator).catch(()=>{});
$('#status-links').onclick=()=>showRelations();$('#status-backlinks').onclick=()=>showRelations(true);
$('#status-properties').onclick=()=>{if(state.document?.sourceMode)toggleEdit();const editor=editors.get(state.document);if(editor){editor.view.scrollDOM.scrollTop=0;editor.view.dom.querySelector('.property-input')?.focus();}};
const resizeHandle=$('#navigation-resize');let dragPointer=null;
resizeHandle.onpointerdown=event=>{if(event.button!==0)return;event.preventDefault();resizeHandle.focus({preventScroll:true});dragPointer=event.pointerId;resizeHandle.setPointerCapture(event.pointerId);document.body.classList.add('resizing-sidebar');};
resizeHandle.onpointermove=event=>{if(event.pointerId===dragPointer)resizeSidebar(sidebarWidth(event.clientX-44,innerWidth,document.body.classList.contains('details-open')?parseFloat(getComputedStyle(document.body).getPropertyValue('--details-width')):0),true);};
const endResize=()=>{dragPointer=null;document.body.classList.remove('resizing-sidebar');};resizeHandle.onpointerup=endResize;resizeHandle.onpointercancel=endResize;resizeHandle.onlostpointercapture=endResize;
resizeHandle.onkeydown=event=>{const bounds=sidebarBounds(innerWidth,document.body.classList.contains('details-open')?parseFloat(getComputedStyle(document.body).getPropertyValue('--details-width')):0),current=Number(resizeHandle.getAttribute('aria-valuenow'));const value=event.key==='ArrowLeft'?current-16:event.key==='ArrowRight'?current+16:event.key==='Home'?bounds.min:event.key==='End'?bounds.max:null;if(value!==null){event.preventDefault();resizeSidebar(sidebarWidth(value,innerWidth,document.body.classList.contains('details-open')?parseFloat(getComputedStyle(document.body).getPropertyValue('--details-width')):0),true);}};
window.addEventListener('resize',()=>resizeSidebar());
$('#navigation-toggle').onclick=()=>toggleNavigation();$('#new-tab').onclick=()=>newTab().catch(e=>notice(e.message));
$('#details-toggle').onclick = details; $('#details-close').onclick = details;
$('#up').onclick = () => navigate(state.root, state.path.split('/').slice(0, -1).join('/'));
$('#back').onclick = () => goHistory(-1).catch(error=>notice(error.message));$('#forward').onclick=()=>goHistory(1).catch(error=>notice(error.message));
$('#files').onclick = () => showSection('files');
document.querySelectorAll('[data-section]').forEach(button => { button.onclick = () => showSection(button.dataset.section); });
$('#new-note').onclick = () => createEntry(false); $('#new-folder').onclick = () => createEntry(true); $('#search-files').onclick = () => openSearch('search');
$('#empty-new-note').onclick=()=>createEntry(false);$('#empty-new-canvas').onclick=()=>createEntry(false,'canvas');$('#empty-go-file').onclick=()=>openSearch('files');
$('#empty-new-base').onclick=()=>createEntry(false,'base');
$('#file-actions').onclick = () => fileActions(state.document.path, state.document.root, state.document.entry);
$('#collapse-tree').onclick = () => { tree.cancelEmptyCreate();tree.model.collapseAll(); tree.render(); };
$('#hide-readme-agents').onclick = toggleExplorerVisibility;
$('#edit').onclick = toggleEdit; $('#save').onclick = save; $('#pin').onclick = pin;
async function loadMore() { try { const page = await api.list({ root: state.root, path: state.path, cursor: state.page.next_cursor }); state.entries.push(...page.entries); state.page = page; renderEntries(); annotate(state.root, page.entries); } catch (error) { notice(error.message); } }
$('#load-more').onclick = loadMore;
$('#palette-input').oninput = updatePalette;
$('#palette-input').onkeydown = event => {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault();const step=event.key==='ArrowDown'?1:-1;let index=state.paletteIndex+step;while(state.paletteItems[index]?.disabled)index+=step;if(index>=0&&index<state.paletteItems.length)state.paletteIndex=index;renderPalette();$('#palette-results [aria-selected=true]')?.scrollIntoView({block:'nearest'}); }
  if (event.key === 'Enter' && state.paletteItems[state.paletteIndex]) {event.preventDefault();runPaletteItem(state.paletteItems[state.paletteIndex],event);}
};
$('#entries').onkeydown = event => {
  const entries=visibleExplorerEntries(state.entries,state.hideReadmeAgents);
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); state.selected = Math.max(0, Math.min(entries.length - 1, state.selected + (event.key === 'ArrowDown' ? 1 : -1))); renderEntries(); $('#entries [aria-selected=true]')?.scrollIntoView({ block: 'nearest' }); }
  if (event.key === 'Enter' && entries[state.selected]) { const value = entries[state.selected]; value.kind === 'directory' ? navigate(value.root_ref||state.root, value.relative_path) : openFile(value.relative_path,state.root,{member:value.member}); }
};
document.addEventListener('keydown',event=>{
  if(!document.querySelector('dialog[open]')&&state.section==='files'){
    const key=event.key.toLowerCase(),mod=event.metaKey||event.ctrlKey;let run;
    if(mod&&event.shiftKey&&!event.altKey&&key==='t')run=reopenTab;
    else if(event.ctrlKey&&!event.metaKey&&key==='tab')run=()=>cycleTabs(event.shiftKey?-1:1);
    else if(mod&&event.altKey&&(key==='arrowleft'||key==='arrowright'))run=()=>cycleTabs(key==='arrowleft'?-1:1);
    if(run){event.preventDefault();event.stopPropagation();Promise.resolve(run()).catch(e=>notice(e.message));return;}
  }
  if(!(event.metaKey||event.ctrlKey)||event.altKey||event.shiftKey)return;
  const key=event.key.toLowerCase();
  if(key==='f'&&state.section==='files'&&state.document?.kind==='pdf'){event.preventDefault();activeVisual?.toggleFind(true);return;}
  if($('#action-dialog').open)return;
  if(key===','){event.preventDefault();if($('#palette').open)$('#palette').close();settings.open();return;}
  if($('#settings-dialog').open)return;
  if($('#palette').open&&key!=='p')return;
  const run=key==='p'?()=>$('#palette').open?$('#palette').close():palette()
    :key==='o'?()=>openSearch('files'):key==='n'?()=>createEntry(false):key==='t'?newTab:key==='\\'?()=>toggleNavigation()
    :key==='['?()=>goHistory(-1):key===']'?()=>goHistory(1):key==='w'&&state.section==='files'?closeDocument:null;
  if(!run)return;
  event.preventDefault();event.stopPropagation();Promise.resolve(run()).catch(error=>notice(error.message));
},true);
document.addEventListener('keydown', event => {
  if (event.defaultPrevented) return;
  if($('#action-dialog').open||$('#palette').open||$('#settings-dialog').open)return;
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') { event.preventDefault(); save(); }
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'w' && state.section === 'files' && state.document) { event.preventDefault(); closeDocument(); }
  if (event.key === 'Escape' && !$('#palette').open && state.document && !state.editing) closeDocument();
});
api.onEvent(event => {
  if(event.type==='connection'&&event.state==='connected'&&state.document)editors.get(state.document)?.refreshAttachments();
  if(event.type==='attachments-changed')for(const value of tabs.items.values())if(value.root===event.root&&value.binding_id===event.binding_id){const editor=editors.get(value);if(editor)api.attachments({root:value.root,path:value.path,binding:value.binding_id,action:'list'}).then(items=>editor.reconcileAttachments(items)).catch(()=>{});}
  if(['asset-sync','mac-content'].includes(event.type)&&/\.(png|jpe?g|gif|webp|avif|bmp|pdf)$/i.test(event.path||'')&&state.document?.root===event.root)editors.get(state.document)?.refreshAttachments();
  if(event.type==='interface-zoom'){document.documentElement.style.setProperty('--app-zoom',String(event.factor));settings.zoomChanged(event);if(event.error)notice('Zoom changed for this session; the preference could not be saved.');}
  if(event.type==='mac-content'){
    const value=tabs.get('mac',event.path);if(!value)return;
    if(value.dirty){if(value===state.document)$('#availability').textContent='Mac file changed · draft preserved';return;}
    const expected=value.text;api.open({root:'mac',path:event.path}).then(result=>{if(value.dirty||value.text!==expected)return;Object.assign(value,{text:result.text,base:result.base,entry:result.entry});editors.get(value)?.replaceText(value.text);visualEditors.get(value)?.replaceText(value.text);if(value===state.document)renderAvailability(value);}).catch(()=>{});return;
  }
  if(event.type==='mac-path'){
    stashDocument();
    if(event.deleted){
      for(const value of tabs.items.values())if(value.root==='mac'&&(value.path===event.path||value.path.startsWith(event.path+'/'))){value.deleted=true;value.editable=false;editors.get(value)?.configure(value.path,false,Boolean(value.sourceMode));}
      acceptedRemoval('mac',event.path);
    }else{
      explorerFocus.moved('mac',event.previousPath,event.path);
      if(!tabs.move('mac',event.previousPath,event.path,event.path))notice('The Mac move completed. Both open versions are preserved; reopen the current path.');
      if(state.root==='mac'&&(state.path===event.previousPath||state.path.startsWith(event.previousPath+'/')))state.path=event.path+state.path.slice(event.previousPath.length);
      if(state.document?.root==='mac')renderDocument();
    }
    if(event.draftRecoveryNeeded)notice('The Mac move completed. Existing drafts at both paths are preserved for review.');
    renderTabs();if(state.document)renderAvailability(state.document);return;
  }
  if(event.type==='roots-changed'){api.state().then(snapshot=>{state.roots=snapshot.roots;renderRoots();}).catch(error=>notice(error.message));}
  if(event.type==='retained-move'){
    for(const value of tabs.items.values())if(value.root==='storage'&&(value.path===event.from||value.path.startsWith(event.from+'/'))){value.mainMovedTo=event.to+value.path.slice(event.from.length);value.writeQualification=`Main location: Storage/${value.mainMovedTo}. This preview and any offline copy are preserved.`;}
    if(state.document)renderAvailability(state.document);
  }
  if(event.type==='workspace-path'&&event.kind==='retain_child'&&event.state==='complete'){
    for(const value of tabs.items.values())if(value.root===(event.from_root||'box')&&(value.path===event.source||value.path.startsWith(event.source+'/'))){value.editable=false;value.mainMovedTo=event.destination+value.path.slice(event.source.length);value.writeQualification=`Retained in Storage/${value.mainMovedTo}. This local copy and its history are preserved.`;editors.get(value)?.configure(value.path,false,Boolean(value.sourceMode));}
    if(state.document)renderAvailability(state.document);
  }
  if(event.type==='project-lifecycle'){
    for(const value of tabs.items.values())if(value.root===(event.root||'box')&&(value.path===event.boxPath||value.path.startsWith(event.boxPath+'/'))){
      const readOnly=['submitted','needs_review'].includes(event.state)||event.state==='complete'&&event.operation==='archive';
      if(readOnly){value.lifecycleEditable??=value.editable;value.editable=false;editors.get(value)?.configure(value.path,false,Boolean(value.sourceMode));}
      else if(event.state==='rejected'&&value.lifecycleEditable!==undefined){value.editable=value.lifecycleEditable;delete value.lifecycleEditable;editors.get(value)?.configure(value.path,value.editable,Boolean(value.sourceMode));}
    }
    if(state.document)renderAvailability(state.document);
  }
  if(event.type==='app-update'){updateIndicator(event);if($('#settings-dialog').open&&settings.category==='updates')settings.renderCategory();return;}
  if(event.type==='asset-sync'){
    if(event.previousPath&&!event.deleted)explorerFocus.moved(event.root,event.previousPath,event.path);
    const existing=tabs.get(event.root,event.previousPath),active=existing&&existing===state.document;
    if(event.deleted){if(existing){existing.deleted=true;existing.editable=false;}if(active)$('#availability').textContent='Removed on Main · recovery retained';}
    else if(existing){
      const moved=tabs.move(event.root,event.previousPath,event.path,undefined);
      if(moved){renderTabs();if(active)openFile(event.path,event.root,{refresh:true}).catch(e=>notice(e.message));}
      else notice('Main accepted the move. An independent destination tab was preserved.');
    }
    if(event.deleted)acceptedRemoval(event.root,event.path);
    else{
      tree.model.load(event.root,event.previousPath.split('/').slice(0,-1).join('/'),{refresh:true}).then(()=>tree.render()).catch(()=>{});
      if(state.filesView==='directory'&&state.root===event.root)navigate(state.root,state.path,{refresh:true}).catch(()=>{});
    }
    notice(event.deleted?'Removed on Main · recovery retained':'Asset moved on Main');
  }
  if(event.type==='offline-progress'&&event.state==='complete')notice(`Offline download complete · ${event.files} files · ${event.skipped} skipped`);
  if (event.type === 'palette') palette();
  if(event.type==='settings'){if($('#palette').open)$('#palette').close();settings.open();}
  if (event.type === 'connection') connection(event.state === 'connected');
  if (event.type === 'note-sync') {
    if(event.previousPath&&event.previousPath!==event.path){
      explorerFocus.moved(event.root,event.previousPath,event.path);
      if (state.root === event.root && (state.path === event.previousPath || state.path.startsWith(`${event.previousPath}/`))) {
        state.path = event.path + state.path.slice(event.previousPath.length); breadcrumbs();
        if (!state.document) navigate(event.root, state.path, { refresh: true }).catch(error => notice(error.message));
      }
      const previousPath=state.document?.path,previousFormat=previousPath&&editorLanguage(previousPath);
      stashDocument();
      const moved=tabs.move(event.root,event.previousPath,event.path,event.localPath);
      if(!moved)notice('Main accepted the move. Both open drafts are preserved; reopen the moved file to reconcile them.');
      if(moved&&state.document?.root===event.root&&state.document.path!==previousPath){
        renderTabs();$('#document-name').textContent=state.document.entry.name;
        renderDocument();
      }
      tree.model.load(event.root,event.previousPath.split('/').slice(0,-1).join('/'),{refresh:true}).then(()=>tree.render()).catch(()=>{});
    }
    const document=tabs.get(event.root,event.path);
    if(document&&state.document===document&&(event.contentChanged||event.deleted)){
      try{commitPropertyInput();}catch(error){notice(error.message);document.sourceChanged=true;return;}
    }
    if(document){document.pending=event.pending;document.held=event.held;document.pendingTarget=event.pendingTarget;document.localRecoveryNeeded=event.localRecoveryNeeded;}
    if(event.deleted&&document){document.deleted=true;document.editable=false;editors.get(document)?.configure(document.path,false,Boolean(document.sourceMode));if(state.document===document){$('#edit').disabled=true;$('#save').disabled=true;}}
    if(state.document===document)$('#availability').textContent=event.localRecoveryNeeded?'Main accepted · local recovery needed':event.held?'Local · needs attention':event.pendingTarget?`Move pending · ${event.pendingTarget}`:event.pending?'Local · awaiting Main':event.deleted?'Removed on Main · local recovery retained':'Local · Main accepted';
    if(state.document===document&&!event.pending&&!event.held)latency(event.deleted?'Removed on Main · recovery retained':'Main accepted');
    if(event.pathAccepted&&!event.pending&&!event.localRecoveryNeeded)notice(event.deleted?'Removed on Main · recovery retained':'Move accepted on Main');
    if(event.deleted&&(event.pathAccepted||event.sourceAbsent||!event.pending&&!event.held))acceptedRemoval(event.root,event.path);
    if(event.draftRecoveryNeeded)notice('The move was accepted. Existing drafts at both paths were preserved for review.');
    if(event.contentChanged&&document){
      document.sourceChanged=true;
      if(!document.dirty) {
        const expectedText=document.text;
        api.open({root:event.root,path:event.path}).then(result=>{
          if(state.document===document)commitPropertyInput();
          if(document.dirty||document.text!==expectedText)return;
          Object.assign(document,{text:result.text,base:result.base,entry:result.entry,pending:result.pending,held:result.held});
          editors.get(document)?.replaceText(document.text);visualEditors.get(document)?.replaceText(document.text);
          if(state.document===document)renderAvailability(document);
        }).catch(error=>notice(error.message));
      } else if(state.document===document)$('#availability').textContent='Main changed · local draft preserved';
    }
    annotationRequests.clear();
  }
  if(event.type==='sync-error')latency(`Sync: ${event.error}`);
  if(event.type==='source-notification'){
    annotationRequests.clear();
    tree.model.sourceChanged(event.root,event.paths||[]).then(()=>tree.render()).catch(()=>{});
  }
  if(event.type==='explorer-changed'){
    tree.model.sourceChanged(event.root,event.paths||[]).then(()=>tree.render()).catch(()=>{});
    if(state.section==='files'&&state.filesView==='directory'&&state.root===event.root&&(event.paths||[]).includes(state.path))navigate(state.root,state.path,{refresh:true});
  }
  if(event.type==='explorer-watch-status')latency(`Live folders: ${event.error}`);
  if (event.type === 'source-current' && state.document?.root === event.root && state.document?.path === event.path) {
    $('#availability').textContent = event.current ? 'Cached · current' : event.error ? 'Cached · source unverified' : 'Cached · source changed';
  }
  if (event.type === 'directory-changed') {
    tree.model.sourceChanged(event.root,[event.path]).then(()=>tree.render()).catch(()=>{});
    if(state.filesView==='directory' && state.root===event.root && state.path===event.path)navigate(state.root,state.path,{refresh:true});
  }
  if (event.type === 'cached') {
    if (state.document?.root === event.root && state.document?.path === event.path) { state.document.mode = 'cached'; $('#availability').textContent = 'Cached'; $('#pin').disabled = false; }
    renderDetails();
  }
  if (event.type === 'cache-error') notice(`File remains available on demand; cache fill failed: ${event.error}`);
});
(async () => {
  toggleNavigation(localStorage.getItem('navigation-collapsed')==='true');renderFilesView();renderTabs();
  const snapshot = await api.state();document.documentElement.style.setProperty('--app-zoom',String(snapshot.zoomFactor||1)); state.roots = snapshot.roots; renderRoots(); icons();
  if(retainedSession){tabs.restore(retainedSession);state.sidebarKnowledge=retainedSession.sidebarKnowledge;knowledgeNavigation();renderTabs();await showPage(tabs.page(),{tab:tabs.activeTab,record:false});if(retainedSession.section!=='files')showSection(retainedSession.section);}
  restoringSession=false;rememberSession();
  setDetailsOpen(localStorage.getItem('note-context-open')==='true'&&state.section==='files');resizeSidebar();
  await reconnect();
  if(explorerFocus.mode==='focus'&&explorerFocus.scope){if(!state.sidebarKnowledge)await focusFolder(explorerFocus.scope.root,explorerFocus.scope.path);}else if (state.roots.some(root => root.ref === 'box')) {await tree.model.expand('box','');tree.render();}
})().catch(error => notice(error.message));
