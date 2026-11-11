import { app, BrowserWindow, ipcMain, protocol, dialog, Menu, shell, clipboard,nativeImage,autoUpdater,powerMonitor } from 'electron';
import { Dashboard, chronological } from './dashboard.mjs';
import { readFile, mkdir, writeFile, rename, mkdtemp, rm, stat } from 'node:fs/promises';
import { createReadStream, watch } from 'node:fs';
import { Readable } from 'node:stream';
import path from 'node:path';
import os from 'node:os';
import { randomUUID } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { performance } from 'node:perf_hooks';
import { MainFiles } from './transport.mjs';
import { LocalFiles, normalize, TEXT_LIMIT } from './local-files.mjs';
import { FileCache } from './cache.mjs';
import { NotesSync } from './notes-sync.mjs';
import { Operations } from './operations.mjs';
import { resolveReference } from './note-navigation.mjs';
import { queryBase,editBaseDefinition,baseCatalogScope } from './base-model.mjs';
import { OfflineFolders } from './offline-folders.mjs';
import { SelectedFolders } from './selected-folders.mjs';
import { BinaryActions } from './binary-actions.mjs';
import { acquireNativeOwner, startNativeRecovery, stageNativeRecovery } from './native-recovery.mjs';
import { retargetDrafts, draftFor, storeDraft, clearSavedDraft } from './path-drafts.mjs';
import { isEditableText } from './file-formats.mjs';
import { nativeDirectoryMover } from './tree-moves.mjs';
import {NoteGraph} from './note-graph.mjs';
import {WorkspaceHistory} from './history.mjs';
import {WindowLifecycle} from './window-lifecycle.mjs';
import {UploadQueue,interruptedAttachment} from './uploads.mjs';
import {AppUpdates,privateReleaseAccess} from './app-updates.mjs';
import {exportProjectSnapshot} from './project-export.mjs';
import {makeDirectoryCopy,importWorkingCopy} from './directory-copy.mjs';
import {WorkspacePaths} from './workspace-paths.mjs';
import {applicationRoot,applicationGroup,applicationRoots,archivePage,cachedArchiveMember,archiveCacheKey} from './application-data.mjs';
import {downloadFileCopy,cachedFileStream} from './file-download.mjs';
import {MacBox,localFileAction} from './mac-box.mjs';
import {Attachments} from './attachments.mjs';
import {attachmentReference,attachmentLink} from './attachment-reference.mjs';
import {directoryMutation,ensureAttachmentDirectory} from './upload-directories.mjs';
import {clipboardImage} from './attachment-clipboard.mjs';
import {relocateWorkingRoot} from './working-root.mjs';
import {CopyReviews,copySnapshot,guardCopyDestination,checkCopyDrafts} from './copy-review.mjs';
import {InterfaceZoom,zoomShortcut} from './interface-zoom.mjs';
import {InteractiveSearch} from './interactive-search.mjs';
import {BoxRoots,isBoxRef,isWritableBox} from './box-roots.mjs';
import {DirectoryWatch} from './directory-watch.mjs';

const directory = path.dirname(fileURLToPath(import.meta.url));
const rendererFile = path.join(directory, '../dist/renderer/index.html');
const rendererURL = pathToFileURL(rendererFile).href;
// Display identity changes must not move existing drafts, replicas or outbox.
app.setName('Loom');
app.setPath('userData',path.join(app.getPath('appData'),'LOOM Files'));
protocol.registerSchemesAsPrivileged([{ scheme: 'loom-file', privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: true, stream: true } }]);

let window, remote, cache, socketDirectory, local, notes, operations, offlineFolders, selections, noteGraph,dashboard,windowLifecycle,starting,uploads,appUpdates;
let nativeOwner,recoveryServer,recoveryActive=false,macBox,macGraph;
let filenameController;
let interfaceZoom,interactiveSearch,explorerWatch,attachments;
const activeHandlers=new Set();
const activeHandlerNames=new Map();
let closingRuntime=false;
const annotationCache = new Map();
const enrichmentPreviews = new Map();
const previews = new Map();
const timings = [];
const directoryCache = new Map();
const staleCopies = new Set();
const drafts = new Map();
const copyReviews=new CopyReviews();
const assertNoCopyDraft=(root,relative)=>checkCopyDrafts(drafts,root,relative,p=>macBox.read(p));
let draftWrites = Promise.resolve();
let noteEvents = Promise.resolve();
let localWatcher, watchTimer;
let roots = [{ ref: 'local', label: 'Local recovery', node: 'mac', source_path: '', available: true, read_only: true, hidden: true }];
let connectionState = 'disconnected';
const boxes=new BoxRoots();

function record(operation, origin, ms, detail = '') {
  timings.unshift({ operation, origin, ms: Math.round(ms * 10) / 10, detail, at: new Date().toISOString() });
  timings.splice(50);
}
function emit(type, data) { if(type==='note-sync')noteGraph?.invalidate(data.root);if (window && !window.isDestroyed()) window.webContents.send('workspace:event', { type, ...data }); }
function resource(input) {
  if (!input || !roots.some(root => root.ref === input.root)) throw new Error('Unknown file root.');
  return { root: input.root, relative: normalize(input.path, { allowHidden: !['local','mac'].includes(input.root) }) };
}
function draftKey(root, relative) { return JSON.stringify([root === 'local' ? local.root : root, relative]); }
function persistDrafts() {
  const snapshot = JSON.stringify([...drafts]);
  draftWrites = draftWrites.catch(() => {}).then(async () => {
    const destination = path.join(app.getPath('userData'), 'drafts.json');
    await writeFile(`${destination}.tmp`, snapshot, { mode: 0o600 });
    await rename(`${destination}.tmp`, destination);
  });
  return draftWrites;
}

async function rememberApplicationRoots(values) {
  for(const value of values){const prior=roots.find(root=>root.ref===value.ref);if(prior)Object.assign(prior,value);else roots.push(value);}
  remote.setApplicationViews(roots);
  await writeFile(path.join(app.getPath('userData'),'roots.json'),JSON.stringify(roots.slice(1)),{mode:0o600});
  emit('roots-changed',{});
}

async function connect() {
  try {
    const catalog=(await remote.api('/v1/boxes')).data;
    boxes.replace(catalog);
    const result = await remote.json('roots');
    roots = [roots[0], ...roots.filter(r=>r.ref==='mac'), ...result.data.map(root => ({ ...root, hidden: root.ref === 'archive' }))];
    roots.push({...applicationGroup});
    try{const page=await applicationRoots(remote,{selection:{all_boxes:true}});roots.push(...page.roots);}catch(error){roots.at(-1).reason=error.message;}
    remote.setApplicationViews(roots);
    await writeFile(path.join(app.getPath('userData'), 'roots.json'), JSON.stringify(roots.slice(1)), { mode: 0o600 });
    await writeFile(path.join(app.getPath('userData'),'boxes.json'),JSON.stringify(catalog),{mode:0o600});
    connectionState = 'connected';
    explorerWatch?.reconnect();
    notes?.pump();for(const box of boxes.boxes.values())if(box.available!==false)selections?.sourceChanged([],true,box.ref);notes?.startWatch();
    for(const v of attachments?.rows()||[])if(v.uploadID&&v.state==='pending'){const upload=uploads.get(v.uploadID);if(interruptedAttachment(upload))void uploads.control({id:v.uploadID,action:'resume'});}
    record('connect', 'main', result.ms);
    emit('connection', { state: connectionState });
    remote.lane('content').catch(() => {});
    return roots;
  } catch (error) {
    connectionState = 'offline';
    emit('connection', { state: connectionState, error: error.message });
    throw error;
  }
}

async function list(input) {
  const { root, relative } = resource(input);
  if(root===applicationGroup.ref){const page=await applicationRoots(remote,{cursor:Number(input.cursor)||0,selection:{all_boxes:true}});await rememberApplicationRoots(page.roots);return {root,path:'',entries:page.roots.map(r=>({name:r.label,relative_path:'',kind:'directory',root_ref:r.ref,source:r})),has_more:Boolean(page.next_cursor),next_cursor:page.next_cursor?String(page.next_cursor):'',ms:0};}
  if(applicationRoot(root)&&/\.zip$/i.test(relative)&&roots.find(r=>r.ref===root)?.format==='zotero-webdav')return archivePage(remote,root,relative);
  const started = performance.now();
  const key = JSON.stringify([root, relative, input.cursor || '', Boolean(input.showHidden),Boolean(input.showRaw)]);
  if (input.cached && directoryCache.has(key)) return { ...directoryCache.get(key), cached: true, ms: 0 };
  if (input.cacheOnly) return offlineFolders.page(root,relative,input.cursor||'');
  let page, ms;
  if (root === 'local'||root==='mac') {
    const owner=root==='mac'?macBox:local;
    page = await owner.list(relative);
    ms = performance.now() - started;
    if (input.watch !== false) {
      localWatcher?.close();
      try {
        localWatcher = watch(await owner.resolve(relative), { persistent: false }, () => {
          directoryCache.clear();
          clearTimeout(watchTimer);
          watchTimer = setTimeout(() => emit('directory-changed', { root, path: relative }), 200);
        });
        localWatcher.on('error', () => localWatcher?.close());
      } catch { /* Explicit refresh still works when native watching is unavailable. */ }
    }
  } else {
    try {
      const result = await remote.json('entries', { root, path: relative, limit: 100, ...(input.cursor ? { cursor: input.cursor } : {}) });
      page = result.data;offlineFolders.remember(root,relative,input.cursor||'',page);
    }catch(error){
      if(Number.isInteger(error.status))throw error;
      let retained=offlineFolders.page(root,relative,input.cursor||'');
      if(!retained&&isBoxRef(root)&&selections.covers(relative,root)&&!input.cursor){
        const inventory=await notes.local.list(notes.localPath(root,relative));retained={...inventory,entries:inventory.entries.map(e=>({...e,relative_path:notes.relativePath(root,e.relative_path)})),cached:true,offline:true,ms:0};
      }
      if(!retained)throw error;
      page=retained;connectionState='offline';
    }
    ms = performance.now() - started;
    if(!page.offline)connectionState = 'connected';
  }
  const value = { ...page, entries: page.entries.filter(entry => (input.showHidden || !entry.name.startsWith('.'))&&(input.showRaw||roots.find(r=>r.ref===root)?.format!=='zotero-webdav'||!entry.name.endsWith('.prop'))), cached: false, ms: Math.round(ms * 10) / 10, observedAt: Date.now() };
  if (isBoxRef(root)) {
    for (const replica of notes.records.values()) {
      const target = replica.path;
      if (replica.root !== root) continue;
      if (replica.deleted || replica.pending && replica.source.path !== target) value.entries = value.entries.filter(entry => entry.relative_path !== replica.source.path);
      if (replica.deleted || !replica.pending) continue;
      if (path.posix.dirname(target) !== (relative || '.') || value.entries.some(entry => entry.relative_path === target)) continue;
      const entry = await notes.local.stat(replica.localPath).catch(() => null);
      if (entry) value.entries.push({ ...entry, relative_path: target, pending: true, annotations: { availability: 'local', pending: true } });
    }
  }
  directoryCache.delete(key);
  directoryCache.set(key, value);
  while (directoryCache.size > 128) directoryCache.delete(directoryCache.keys().next().value);
  record('list', ['local','mac'].includes(root) ? 'local' : 'main', ms, relative);
  return value;
}

const imageExtensions = /\.(png|jpe?g|gif|webp|avif|bmp)$/i;

async function openFile(input) {
  const { root, relative } = resource(input);
  const started = performance.now();
  if(input.member){
    if(!applicationRoot(root))throw Error('Archive attachments require an approved application data source.');
    const result=await cachedArchiveMember(remote,cache,root,relative,input.member,{offline:connectionState!=='connected'});
    const token=randomUUID();previews.set(token,{root,relative,metadata:result.metadata,cacheRelative:result.cacheKey,member:true});while(previews.size>64)previews.delete(previews.keys().next().value);
    const kind=/\.pdf$/i.test(result.metadata.name)?'pdf':imageExtensions.test(result.metadata.name)?'image':'binary';
    return {root,path:relative,member:result.member,entry:result.metadata,kind,mode:'cached',origin:'cached',offline:result.offline,editable:false,pinned:false,url:`loom-file://preview/${token}/${encodeURIComponent(result.metadata.name)}`,ms:Math.round(performance.now()-started),writeQualification:'Read-only archive attachment - source ZIP unchanged'};
  }
  let metadata, cached, mode, writeQualification, offline = false;
  let replica = isBoxRef(root) ? notes.get(root, relative) : null;
  if(replica && (input.refresh||replica.restoreReady) && !replica.pending)await notes.refresh(relative,root);
  if (isBoxRef(root) && !replica && isEditableText(relative) && connectionState === 'connected') {
    replica = await notes.adopt(root, relative).catch(error => {writeQualification=error.message;return null;});
  }
  const localOwner = root === 'mac'?macBox:root === 'local' ? local : notes.local;
  let localPath = ['local','mac'].includes(root) ? relative : replica?.localPath;
  if (localPath !== undefined) {
    try { metadata = await localOwner.stat(localPath); } catch (error) { if (['local','mac'].includes(root)) throw error; localPath = undefined; replica = null; }
  }
  if (localPath !== undefined) {
    mode = 'local';
    offline = !['local','mac'].includes(root) && connectionState !== 'connected';
  } else {
    const retained = cache.get(root, relative);
    if (retained && !input.refresh && !staleCopies.has(draftKey(root, relative))) {
      cached = retained;
      metadata = { name: path.posix.basename(relative), relative_path: relative, size: cached.size, revision: cached.revision, modified_at: cached.modifiedAt };
      mode = 'cached';
      offline = connectionState !== 'connected';
      if (!offline) remote.json('stat', { root, path: relative }).then(({ data }) => {
        const current = data.revision === retained.revision;
        if (!current) staleCopies.add(draftKey(root, relative));
        emit('source-current', { root, path: relative, current });
      }).catch(error => emit('source-current', { root, path: relative, current: false, error: error.message }));
    } else {
      try {
        const result = await remote.json('stat', { root, path: relative });
        metadata = result.data;
        cached = cache.get(root, relative, metadata.revision);
        staleCopies.delete(draftKey(root, relative));
        mode = cached ? 'cached' : 'remote';
      } catch (error) {
        // Explicit offline copies are never passed off as current Main originals.
        if (!/connect|SSH|timed out|did not respond|ECONN|socket|unavailable/i.test(error.message)) throw error;
        cached = cache.get(root, relative);
        if (!cached) throw error;
        metadata = { name: path.posix.basename(relative), relative_path: relative, size: cached.size, revision: cached.revision };
        mode = 'cached';
        offline = true;
      }
    }
  }
  if (metadata.kind === 'directory') throw new Error('Open the folder instead.');
  let kind = isEditableText(relative) ? 'text' : /\.pdf$/i.test(relative) ? 'pdf' : imageExtensions.test(relative) ? 'image' : 'binary';
  if (kind === 'text' && metadata.size > TEXT_LIMIT) kind = 'binary';
  const lifecycleBlocked=isBoxRef(root)&&notes.lifecycle?.blocked(relative,root);
  const result = { root, path: relative, entry: metadata, kind, mode, offline, pinned: Boolean(cached?.pinned), editable: Boolean(replica) && notes.available(root) && !replica.retainedProject && !lifecycleBlocked && replica.pathOperation?.kind !== 'delete' && isEditableText(relative), local_path: localPath, binding_id: replica?.id, pending: replica?.pending, held: replica?.held, pendingTarget:replica?.pathOperation?.target, localRecoveryNeeded:replica?.localRecoveryNeeded, writeQualification:lifecycleBlocked?'Project operation awaiting review · local copy preserved':replica?.retainedProject?(replica.retainedProjectKind==='retain_child'?'Folder retained in Storage · local copy preserved':'Archived project · retained local copy'):writeQualification, annotations: annotationCache.get(draftKey(root, relative)) };
  if (kind === 'text') {
    if (localPath !== undefined) Object.assign(result, await localOwner.read(localPath));
    else {
      cached ||= await cache.fetch(root, relative, metadata, () => remote.content(root, relative));
      const bytes = await readFile(cached.diskPath);
      if (bytes.includes(0)) throw new Error('Binary data cannot be opened as a note.');
      result.text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
      result.mode = 'cached';
    }
    if(root==='mac'){result.editable=isEditableText(relative)&&!macBox.blocked(relative);result.writeQualification=macBox.blocked(relative)?'Local operation needs review · files and drafts preserved':'Mac-owned file · Workspace does not sync this root';}
    result.draft = draftFor(drafts,root,relative,result.binding_id);
  } else {
    // Small assets share one complete download; larger ones remain ranged streams
    // until the user explicitly asks to keep a complete offline copy.
    if (localPath === undefined && !cached && metadata.size <= 8 * 1024 ** 2) cached = await cache.fetch(root, relative, metadata, () => remote.content(root, relative));
    if (cached) result.mode = 'cached';
    const token = randomUUID();
    previews.set(token, { root, relative, metadata, offline, localPath, localOwner,leased:input.attachmentLease===true });
    trimPreviews();
    if(input.attachmentLease)result.lease=token;
    result.url = `loom-file://preview/${token}/${encodeURIComponent(metadata.name)}`;
  }
  result.ms = Math.round((performance.now() - started) * 10) / 10;
  result.origin = mode;
  record('open', mode, result.ms, relative);
  return result;
}

function trimPreviews(){const unleased=[...previews].filter(([,v])=>!v.leased);for(const [id]of unleased.slice(0,-64))previews.delete(id);}
function attachmentContext(input){
  const {root,relative}=resource(input);
  const record=input.binding?[...(root==='mac'?macBox.records:notes.records).values()].find(v=>v.id===input.binding&&v.root===root):root==='mac'?macBox.records.get(relative):notes.get(root,relative);
  if(!record||record.deleted)throw Error('The attachment note is no longer available. Staged files are preserved.');
  const editable=root==='mac'?!macBox.blocked(record.path):notes.available(root)&&!record.retainedProject&&!record.pathOperation&&!record.held&&!notes.lifecycle.blocked(record.path,root);
  return {root,path:record.path,binding:record.id,editable,...root==='mac'?{}:boxes.identity(root)};
}

async function attachmentPreview(input){
  const {root,relative}=resource(input),parsed=attachmentReference(input.reference);
  if(parsed.external||parsed.kind==='file')throw Error('Open this attachment explicitly; no external content is fetched automatically.');
  if([...previews.values()].filter(v=>v.leased).length>=64)throw Error('Too many active attachment previews. Close another view and retry.');
  const pending=await attachments.pending(root,parsed.target,relative,input.binding);
  if(pending){const owner=new LocalFiles(attachments.directory),localPath=path.basename(pending.stagePath),metadata=await owner.stat(localPath),token=randomUUID();previews.set(token,{root,relative:pending.destination,metadata,localPath,localOwner:owner,leased:true});return {kind:parsed.kind,entry:metadata,lease:token,url:`loom-file://preview/${token}/${encodeURIComponent(metadata.name)}`,pending:true};}
  let target;
  try{target=await resolveReference({root,from:relative,reference:parsed.target},{roots,records:root==='mac'?macBox.records.values():notes.records.values(),stat:async(owner,p)=>owner==='mac'?macBox.stat(p):owner==='local'?local.stat(p):(await remote.json('stat',{root:owner,path:p})).data});}
  catch(error){if(!/File not found in the linked folders/.test(error.message))throw error;target=await(root==='mac'?macGraph:noteGraph).resolve({root,from:relative,reference:parsed.target,allowCrossBox:false});if(!target)throw error;}
  if(target.root!==root||target.requires_navigation||target.choices)throw Error('Open the attachment to choose its location; automatic previews stay in this Box.');
  return openFile({root:target.root,path:target.path,attachmentLease:true});
}

async function annotations(input) {
  const { root } = resource({ root: input.root, path: '' });
  if (!Array.isArray(input.paths) || input.paths.length > 100) throw new Error('Use at most 100 visible files.');
  if(root==='mac')return {entries:input.paths.map(p=>({path:normalize(p),owner:'mac',availability:'local',index:'unknown',embeddings:'unknown',ocr:'unknown',vision:'unknown'}))};
  const paths = input.paths.map(value => normalize(value, { allowHidden: root !== 'local' }));
  const result = await remote.json('annotations', { root, paths: JSON.stringify(paths) });
  const entries = result.data.entries || [];
  for (const entry of entries) {
    const replica = notes.get(root, entry.path), cached = cache.get(root, entry.path);
    entry.availability = replica ? 'local' : cached?.pinned ? 'offline' : cached ? 'cached' : 'remote';
    entry.pending = replica?.pending; entry.held = replica?.held;
    annotationCache.set(draftKey(root, entry.path), entry);
  }
  while (annotationCache.size > 1000) annotationCache.delete(annotationCache.keys().next().value);
  return { ...result.data, entries };
}

async function mutate(input) {
  const { root, relative } = resource(input);
  if(root==='mac'){
    const target=input.operation==='rename'?normalize(input.target||path.posix.join(path.posix.dirname(relative),input.name||'')):null;
    if(target)for(const key of drafts.keys()){const [r,p]=JSON.parse(key);if(r==='mac'&&(p===target||p.startsWith(target+'/')))throw Error('The destination has a recovery draft. No move was made.');}
    const result=await macBox.mutate({...input,path:relative});directoryCache.clear();return result;
  }
  if (!isWritableBox(roots.find(r=>r.ref===root))) throw new Error('File changes require an available writable Box. Storage remains read-only.');
  if (!['create_note', 'create_folder', 'rename', 'trash', 'confirm_folder_move'].includes(input.operation)) throw new Error('Unknown file action.');
  if (input.operation === 'confirm_folder_move') return notes.trees.confirm(input.operation_id);
  const destination = input.operation === 'rename' ? input.target || path.posix.join(path.posix.dirname(relative), input.name || '') : null;
  if (destination && drafts.has(draftKey(root, destination))) throw new Error('The destination has a recovery draft. No move was made.');
  let directory=false;
  if(!input.operation.startsWith('create_')) {
    const retained=notes.get(root,relative);
    directory=retained?Boolean(retained.directory):(await remote.json('stat',{root,path:relative})).data.kind==='directory';
  }
  const result = !directory&&!input.operation.startsWith('create_')&&!isEditableText(relative)
    ? await notes.binary.mutate({...input,root,path:relative})
    : await notes.mutate({ ...input, root, path:relative, directory });
  directoryCache.clear();
  return { ...result, from:relative };
}

async function physicalPaths(input){
  const {root,relative}=resource(input),descriptor=roots.find(value=>value.ref===root);
  const result={remote:!['local','mac'].includes(root)&&descriptor?.source_path?path.posix.join(descriptor.source_path,relative):null,local:null};
  if(input.member){const cached=cache.get(root,archiveCacheKey(relative,input.member));return {...result,member:input.member.name,local:cached?.diskPath||null,cached:true};}
  const owner=root==='mac'?macBox:root==='local'?local:notes.local,localPath=['local','mac'].includes(root)?relative:isBoxRef(root)?notes.get(root,relative)?.localPath||notes.localPath(root,relative):path.posix.join(root,relative);
  try{await owner.stat(localPath);result.local=await owner.resolve(localPath);}catch(error){if(error.code!=='ENOENT'&&error.code!=='ENOTDIR')throw error;}
  if(!result.local&&!['local','mac'].includes(root)){const cached=cache.get(root,relative);if(cached&&(await stat(cached.diskPath).catch(()=>null))?.isFile())result.local=cached.diskPath;}
  return result;
}

async function search(input) {
  if(input?.cancel){interactiveSearch.cancel();return true;}
  const { root, relative } = resource(input);
  if (typeof input.query !== 'string' || !input.query.trim() || input.query.length > 500) throw new Error('Enter a search query of at most 500 characters.');
  const request={root,path:relative,query:input.query};
  return input.phase==='local'?interactiveSearch.local(request):interactiveSearch.indexed(request);
}

async function findFiles(input={}){
  filenameController?.abort();filenameController=null;if(input.cancel)return true;
  const {root,relative}=resource(input);if(root==='local')throw Error('Filename discovery uses Box or Storage on Main.');
  if(input.phase==='known'){
    if(typeof input.query!=='string'||input.query.trim().length<2||input.query.length>160)throw Error('Enter 2–160 filename characters.');
    const entries=new Map(),query=input.query.trim().toLocaleLowerCase();
    const remember=e=>{const p=e.relative_path||e.path;if(e.deleted||!p||relative&&p!==relative&&!p.startsWith(relative+'/'))return;if(p.toLocaleLowerCase().includes(query))entries.set(p,{...e,relative_path:p,name:e.name||path.posix.basename(p),kind:'file'});};
    for(const [key,page]of directoryCache)if(JSON.parse(key)[0]===root)for(const entry of page.entries||[])if(entry.kind==='file')remember(entry);
    const graph=root==='mac'?macGraph:noteGraph;
    for(const entry of (await graph.catalog({root,path:'',compact:true,cacheOnly:true})).items)remember(entry);
    return {entries:[...entries.values()].slice(0,60),notice:'Cached filenames and working copies'};
  }
  if(root==='mac'){
    if(typeof input.query!=='string'||input.query.trim().length<2)throw Error('Enter at least two filename characters.');
    const query=input.query.toLocaleLowerCase(),entries=new Map();
    for(const [key,page]of directoryCache)if(JSON.parse(key)[0]===root)for(const entry of page.entries||[])if((!relative||entry.relative_path.startsWith(relative+'/'))&&entry.name.toLocaleLowerCase().includes(query))entries.set(entry.relative_path,entry);
    for(const entry of (await macBox.list(relative)).entries)if(entry.name.toLocaleLowerCase().includes(query))entries.set(entry.relative_path,entry);
    return {entries:[...entries.values()].slice(0,60),has_more:false,notice:'Mac filenames in browsed folders only. No recursive indexing or Main query.'};
  }
  if(applicationRoot(root)||root==='application-data'){
    if(typeof input.query!=='string'||input.query.trim().length<2||input.cursor)throw Error('Filter the currently loaded application folder; deeper discovery is not automatic.');
    const page=await list({root,path:relative,cached:true});return {...page,entries:page.entries.filter(v=>v.name.toLowerCase().includes(input.query.trim().toLowerCase())),examined:page.entries.length,coverage:'loaded_folder',has_more:false,next_cursor:'',notice:'Matches in the loaded folder only.'};
  }
  if(typeof input.query!=='string'||input.query.trim().length<2||input.query.length>160)throw Error('Enter 2–160 filename characters.');
  const controller=new AbortController();filenameController=controller;
  try{return (await remote.json('search',{root,path:relative,query:input.query,limit:60,...input.cursor?{cursor:input.cursor}:{}},{signal:controller.signal})).data;}
  finally{if(filenameController===controller)filenameController=null;}
}

async function enrich(input) {
  if (input.token) {
    const preview = enrichmentPreviews.get(input.token);
    if (!preview || Date.now() - preview.at > 300000) throw new Error('Preview expired. Preview the current files again.');
    const result = await remote.api('/v1/knowledge/notes/enrich', preview.selection, { method: 'POST', headers: { 'X-Loom-Idempotency-Key': input.token }, body: { bindings: preview.bindings, stages: preview.stages, confirm: true } });
    enrichmentPreviews.delete(input.token); return result.data;
  }
  const { root, relative } = resource(input);
  const selection=boxes.identity(root);
  const stages = { ocr: input.ocr === true, vision: input.vision === true, embeddings: input.embeddings === true };
  if (!Object.values(stages).some(Boolean)) throw new Error('Select at least one enrichment stage.');
  const ref = path.posix.join(roots.find(r => r.ref === root).source_path, relative);
  const { data } = await remote.api('/v1/knowledge/notes/enrich', { ...selection,ref, kind: input.directory ? 'folder' : 'file', node: 'main', limit: 100, recursive: input.recursive === true, ...stages });
  const token = randomUUID();
  const bindings = (data.entries || []).filter(entry => !entry.blocked_reason).map(entry => entry.binding);
  enrichmentPreviews.set(token, { bindings, stages, selection, at: Date.now() });
  while (enrichmentPreviews.size > 8) enrichmentPreviews.delete(enrichmentPreviews.keys().next().value);
  return { token, count: bindings.length, truncated: data.truncated, entries: (data.entries || []).map(entry => ({ path: entry.source_path, blocked: entry.blocked_reason, wait: entry.wait_reason })), inventory: data.inventory };
}

function mime(relative) {
  const extension = path.extname(relative).toLowerCase();
  return ({ '.pdf': 'application/pdf', '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.gif': 'image/gif', '.webp': 'image/webp', '.avif': 'image/avif', '.bmp':'image/bmp' })[extension] || 'application/octet-stream';
}

export function byteRange(header, size) {
  if (!header) return { start: 0, end: Math.max(0, size - 1), partial: false };
  const match = /^bytes=(\d*)-(\d*)$/.exec(header);
  if (!match || (!match[1] && !match[2]) || !size) throw new Error('Invalid byte range.');
  const start = match[1] ? Number(match[1]) : Math.max(0, size - Number(match[2]));
  const end = match[1] && match[2] ? Math.min(Number(match[2]), size - 1) : size - 1;
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start > end || start >= size) throw new Error('Invalid byte range.');
  return { start, end, partial: true };
}

async function preview(request) {
  const url = new URL(request.url);
  const value = previews.get(url.pathname.split('/')[1]);
  if (url.host !== 'preview' || !value || !['GET', 'HEAD'].includes(request.method)) return new Response('Unknown preview.', { status: 404 });
  const { root, relative, metadata } = value;
  try {
    const range = byteRange(request.headers.get('range'), metadata.size);
    const headers = { 'Content-Type': mime(metadata.name), 'Accept-Ranges': 'bytes', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Access-Control-Allow-Origin': 'null', 'Access-Control-Expose-Headers': 'Content-Range, Content-Length', 'Content-Disposition': `inline; filename*=UTF-8''${encodeURIComponent(metadata.name).replaceAll("'", '%27')}` };
    const localPath = value.localPath ?? (root === 'local' ? relative : undefined);
    const cached = localPath !== undefined ? null : cache.get(root, value.cacheRelative||relative, metadata.revision);
    if(value.member&&!cached)return new Response('Attachment cache was evicted. Reopen the attachment.',{status:410});
    if (localPath !== undefined || cached) {
      const owner = value.localOwner || local;
      if (localPath !== undefined && (await owner.stat(localPath)).revision !== metadata.revision) return new Response('File changed. Reopen it.', { status: 409 });
      const file = localPath !== undefined ? await owner.resolve(localPath) : cached.diskPath;
      headers['Content-Length'] = String(metadata.size ? range.end - range.start + 1 : 0);
      if (range.partial) headers['Content-Range'] = `bytes ${range.start}-${range.end}/${metadata.size}`;
      if (request.method === 'HEAD' || !metadata.size) return new Response(null, { headers, status: range.partial ? 206 : 200 });
      const stream = cached ? cache.stream(cached, { start: range.start, end: range.end }) : createReadStream(file, { start: range.start, end: range.end });
      request.signal.addEventListener('abort', () => stream.destroy(), { once: true });
      return new Response(Readable.toWeb(stream), { headers, status: range.partial ? 206 : 200 });
    }
    const response = await remote.content(root, relative, { signal: request.signal, headers: range.partial ? { Range: `bytes=${range.start}-${range.end}` } : {} });
    if (response.headers.etag !== metadata.revision) { response.destroy(); return new Response('Remote file changed. Reopen it.', { status: 409 }); }
    for (const key of ['content-length', 'content-range']) if (response.headers[key]) headers[key] = response.headers[key];
    record('stream headers', 'main', response.loomMs, relative);
    return new Response(Readable.toWeb(response), { headers, status: response.statusCode });
  } catch (error) { return new Response(error.message, { status: /byte range/.test(error.message) ? 416 : 502 }); }
}

function register(name, handler) {
  ipcMain.handle(`workspace:${name}`, async (event, input) => {
    if (event.senderFrame !== window?.webContents.mainFrame || event.senderFrame.url !== rendererURL) throw new Error('Untrusted application frame.');
    try {
      if(recoveryActive&&name!=='state')throw Error('Recovery capture is in progress. Your work is preserved; try again shortly.');
      if(closingRuntime&&name!=='state')throw Error('Workspace is closing; local work has been preserved.');
      const work=Promise.resolve(handler(input));activeHandlers.add(work);activeHandlerNames.set(work,name);
      try{return {ok:true,data:await work};}finally{activeHandlers.delete(work);activeHandlerNames.delete(work);}
    }
    catch (error) { return { ok: false, error: error.message }; }
  });
}

async function start() {
  await mkdir(app.getPath('userData'), { recursive: true, mode: 0o700 });
  interfaceZoom=await new InterfaceZoom(path.join(app.getPath('userData'),'interface.json'),value=>{if(process.platform==='darwin')window?.setWindowButtonPosition({x:12,y:Math.round(Math.max(42,42*value.factor)/2-7)});emit('interface-zoom',value);}).init();
  const moveHelper = app.isPackaged ? path.join(process.resourcesPath, 'loom-workspace-fs') : path.resolve(directory, '../../../.loom-acceptance/workspace-desktop-v1/loom-workspace-fs');
  nativeOwner=await acquireNativeOwner(moveHelper,app.getPath('userData'));
  nativeOwner.child.on('exit',()=>{if(!quitting){notes&&(notes.suspended=true);app.quit();}});
  socketDirectory = await mkdtemp(path.join(os.tmpdir(), 'loom-files-'));
  const retainedBoxes=await readFile(path.join(app.getPath('userData'),'boxes.json'),'utf8').then(JSON.parse).catch(error=>{if(error.code!=='ENOENT')throw error;return undefined;});
  boxes.replace(retainedBoxes);
  remote = new MainFiles(socketDirectory, { host: process.env.LOOM_FILES_SSH_HOST || 'loom-main',boxes });
  local = new LocalFiles(process.env.LOOM_FILES_LOCAL_ROOT || path.join(os.homedir(), 'Obsidian Vaults', 'LOOM NOTES'));
  roots[0].source_path = local.root;
  roots[0].available = await stat(local.root).then(info => info.isDirectory()).catch(() => false);
  const retainedRoots = await readFile(path.join(app.getPath('userData'), 'roots.json'), 'utf8').then(JSON.parse).catch(() => []);
    roots.push(...retainedRoots.filter(root => isBoxRef(root.ref)&&boxes.get(root.ref)||['storage', 'archive','application-data'].includes(root.ref)||applicationRoot(root.ref)));
  roots = roots.map(root => ({ ...root, hidden: root.ref === 'local' || root.ref === 'archive' }));
  remote.setApplicationViews(roots);
  const savedDrafts = await readFile(path.join(app.getPath('userData'), 'drafts.json'), 'utf8').then(JSON.parse).catch(() => []);
  for (const [key, draft] of savedDrafts) if (typeof draft.text === 'string' && Buffer.byteLength(draft.text) <= TEXT_LIMIT) drafts.set(key, draft);
  const workingRoot=await relocateWorkingRoot(app.getPath('userData'),path.join(os.homedir(),'loom-main'),{home:os.homedir(),move:async(root,from,to,identity)=>nativeDirectoryMover(moveHelper)({root},{from,to,identity})});
  notes = new NotesSync(path.join(app.getPath('userData'), 'native-client'), remote, { workingRoot, boxes, moveDirectory: nativeDirectoryMover(moveHelper), emit: (type, data) => {
    directoryCache.clear();
    noteEvents = noteEvents.catch(() => {}).then(async () => {
      if ((type === 'note-sync'||type==='asset-sync') && data.previousPath && data.previousPath !== data.path) {
        if(data.pathAccepted)selections?.followMove(data.previousPath,data.path,data.root);
        const result=retargetDrafts(drafts,data.root,data.previousPath,data.path,data.binding_id);
        if(result.moved||result.collisions)await persistDrafts();
        if(result.collisions)data={...data,draftRecoveryNeeded:true};
      }
      emit(type, data);
    });
    void noteEvents.catch(error=>emit('sync-error',{error:`Draft handover needs attention: ${error.message}`}));
    return noteEvents;
  } }); await notes.init();noteGraph=new NoteGraph(notes,remote,{online:()=>connectionState==='connected'}).init();
  macBox=await new MacBox(path.join(os.homedir(),'loom-box'),moveHelper,{db:notes.db,emit:async(type,data)=>{
    directoryCache.clear();
    if(type==='mac-path'&&!data.deleted){const result=retargetDrafts(drafts,'mac',data.previousPath,data.path);if(result.collisions)data={...data,draftRecoveryNeeded:true};if(result.moved)await persistDrafts();}
    macGraph?.invalidate('mac');emit(type,data);
  }}).init();
  macGraph=new NoteGraph({db:notes.db,records:macBox.records},null,{online:()=>false,localOnly:true}).init();
  explorerWatch=new DirectoryWatch(remote,{localRoot:(root,p)=>(root==='mac'?macBox:local).resolve(p),emit:(type,data)=>{
    if(type==='explorer-changed')for(const key of directoryCache.keys()){const [root,p]=JSON.parse(key);if(root===data.root&&data.paths.includes(p))directoryCache.delete(key);}
    emit(type,data);
  }});
  powerMonitor.on('resume',()=>explorerWatch?.reconnect());
  register('watch-directories',input=>{
    if(!Array.isArray(input)||input.length>4096)throw Error('Invalid explorer subscriptions.');
    const scopes=input.map(value=>{const {root,relative}=resource(value);return {root,path:relative};}).filter(value=>{
      const root=roots.find(r=>r.ref===value.root);
      return root.ref!==applicationGroup.ref&&root.available!==false&&!(root.format==='zotero-webdav'&&value.path.split('/').some(p=>/\.zip$/i.test(p)));
    });
    explorerWatch.setScopes(scopes);return true;
  });
  interactiveSearch=new InteractiveSearch(remote,{roots:()=>roots,records:()=>[...notes.records.values(),...macBox.records.values()].map(value=>{const draft=drafts.get(draftKey(value.root,value.path));return draft?{...value,text:draft.text,pending:true}:value;})});
  roots.push({ref:'mac',label:'Box',node:'mac',source_path:macBox.root,available:true,read_only:false});
  operations = new Operations(remote,{db:notes.db,boxes});
  dashboard=new Dashboard(remote,{boxes});
  register('dashboard',async input=>{
    if(!window?.isVisible()||window.isMinimized())return {section:input.section,token:input.token,blocks:{},hidden:true};
    const result=await dashboard.read(input,event=>window?.webContents.send('workspace:event',event));
    if(input.section==='activity'){const root=boxes.selection(input).root;result.local={summary:notes.activitySummary(root),items:notes.activity(root)};}
    return result;
  });
  cache = new FileCache(path.join(app.getPath('userData'), 'file-cache'));
  await cache.init();
  let build={kind:'local-development'};try{build=JSON.parse(await readFile(path.join(directory,'../build.json'),'utf8'));}catch{}
  const access=privateReleaseAccess({arch:process.arch,appPath:path.dirname(path.dirname(path.dirname(process.execPath)))});
  appUpdates=new AppUpdates({native:autoUpdater,build,version:app.getVersion(),emit,...access,prepareInstall:async()=>{quitting=true;try{await drainRuntime();windowLifecycle.allowClose();}catch(error){quitting=false;closingRuntime=false;await window?.webContents.executeJavaScript('window.endRecovery?.()');throw error;}}});
  register('updates',input=>{switch(input?.action){case 'check':return appUpdates.check();case 'download':return appUpdates.download();case 'install':return appUpdates.install();case 'status':return appUpdates.status();default:throw Error('Unknown update action.');}});
  notes.binary=new BinaryActions(notes,cache);await notes.binary.init();
  offlineFolders=new OfflineFolders(notes,cache,remote,{emit});
  selections=new SelectedFolders(notes,cache,remote,{emit,remember:(...args)=>offlineFolders.remember(...args)});
  uploads=new UploadQueue(notes.db,remote,{emit,boxes,beforeAttachment:(v,binding)=>ensureAttachmentDirectory(remote,v,binding),ensureDirectory:async(destination,collision,id,binding)=>{
    const saved=uploads.get(id);let prepared=saved.directoryPrepared;
    if(!prepared){
      const original=destination;
      for(let attempt=0;attempt<100;attempt++){
        const snapshot=(await remote.json('note',{...binding,path:destination,directory:true})).data;
        if(!snapshot.exists){prepared={path:destination,snapshot};break;}
        if(collision==='skip')return {path:destination,state:'skipped'};
        destination=path.posix.join(path.posix.dirname(original),`${path.posix.basename(original)} (${attempt+2})`);
      }
      if(!prepared)throw Error('Too many folder-name collisions.');
      saved.directoryPrepared=prepared;uploads.save(saved);
    }
    const b=prepared.snapshot;
    const result=(await remote.json('note',{}, {method:'POST',body:directoryMutation(binding,b,prepared.path,id)})).data;
    if(result.operation?.state!=='accepted'&&result.state!=='accepted')throw Error('The imported folder is awaiting source acceptance.');
    return {path:prepared.path,state:'accepted'};
  },accepted:v=>{attachments?.accepted(v);directoryCache.clear();noteGraph.invalidate(v.root);emit('directory-changed',{root:v.root,path:path.posix.dirname(v.destination)==='.'?'':path.posix.dirname(v.destination)});}});
  attachments=await new Attachments(notes.db,path.join(app.getPath('userData'),'native-client','attachments'),{context:attachmentContext,emit,enqueue:v=>uploads.enqueueAttachment(v),importLocal:async v=>{
    const folder=path.posix.dirname(v.destination);
    try{const info=await macBox.stat(folder);if(info.kind!=='directory')throw Error('Attachment folder is occupied by a file.');}catch(error){if(error.code!=='ENOENT')throw error;await macBox.mutate({operation:'create_folder',path:path.posix.dirname(folder),name:path.posix.basename(folder)});}
    await macBox.exclusive(()=>localFileAction(moveHelper,macBox.root,{id:v.id,root_identity:macBox.identity,kind:'import_attachment',path:v.destination,source:v.stagePath,sha256:v.hash,size:v.size}));
    directoryCache.clear();emit('directory-changed',{root:'mac',path:folder});
  }}).init();
  register('attachment-preview',attachmentPreview);
  register('attachment-release',input=>{if(typeof input==='string'&&previews.get(input)?.leased)previews.delete(input);return true;});
  register('attachments',async input=>{
    attachmentContext(input);
    if(input.action==='complete'){
      const known=new Map(),query=String(input.query||'').toLocaleLowerCase();
      for(const [key,page]of directoryCache)if(JSON.parse(key)[0]===input.root)for(const entry of page.entries||[])if(entry.kind==='file')known.set(entry.relative_path,entry.name);
      for(const item of attachments.rows())if(item.root===input.root)known.set(item.destination,path.posix.basename(item.destination));
      return {origin:'known files',items:[...known].filter(([p])=>p.toLocaleLowerCase().includes(query)).slice(0,40).map(([p,name])=>({name,path:attachmentLink(input.path,p)}))};
    }
    if(input.action==='list')return attachments.list(input);
    if(input.action==='linked')return attachments.linked(input);
    if(input.action==='retry-local')return attachments.retryLocal(input);
    if(input.action==='clipboard'){const image=await clipboardImage(clipboard,bytes=>{const value=nativeImage.createFromBuffer(bytes);if(value.isEmpty())throw Error('Clipboard image could not be decoded. Attach the original file instead.');return value.toPNG();});if(!image)return [];return [await attachments.stage(input,image)];}
    let sources=input.sources;
    if(input.action==='pick'){const selection=await dialog.showOpenDialog(window,{title:'Attach files',properties:['openFile','multiSelections']});if(selection.canceled)return [];sources=selection.filePaths;}
    else if(input.action!=='drop')throw Error('Unknown attachment action.');
    await checkUploadSources(sources);
    const result=[];for(const source of sources){try{result.push(await attachments.stage(input,{source}));}catch(error){if(!result.length)throw error;emit('sync-error',{error:`Some attachments were imported. ${error.message}`});break;}}return result;
  });
  register('uploads',input=>input?.action==='control'?uploads.control({id:input.id,action:input.control}):uploads.list());
  register('metadata-refresh',async input=>{
    if(!['start','step','status','latest','pause','cancel','retry'].includes(input?.action))throw Error('Unknown metadata refresh operation.');
    const method=['status','latest'].includes(input.action)?'GET':'POST';
    const payload={action:`refresh_${input.action}`,...boxes.identity(input.root||'box',input.box_id),path:input.path||'',id:input.id||''};
    const result=await remote.json('links',method==='GET'?payload:{},{method,...method==='POST'?{body:payload}:{}});
    if(result.data.state.startsWith('complete'))noteGraph.invalidate(payload.root);
    return result.data;
  });
  register('availability',()=>({copies:[...notes.records.values()].filter(v=>!v.deleted&&!v.directory).length,cache:cache.summary(),selections:selections.list(),downloads:offlineFolders.jobs(),pending:notes.activitySummary(),pins:[...cache.entries.values()].filter(v=>v.pinned&&(isBoxRef(v.root)||v.root==='storage')).slice(0,50).map(v=>({root:v.root,path:v.relative,size:v.size}))}));
  register('upload-pick',async input=>{
    const target=resource(input);boxes.identity(target.root);
    const selection=await dialog.showOpenDialog(window,{title:'Upload to Main',properties:['openFile','openDirectory','multiSelections']});
    if(selection.canceled)return uploads.list();
    await checkUploadSources(selection.filePaths);
    return uploads.enqueue(selection.filePaths,{root:target.root,path:target.relative,collision:input.collision||'keep_both'});
  });
  async function checkUploadSources(sources){
    if(!Array.isArray(sources)||!sources.length||sources.length>100)throw Error('Choose up to 100 original files or folders.');
    for(const source of sources){await guardCopyDestination(source,[app.getPath('userData'),path.join(os.homedir(),'loom-main')],{source:true});if(source===macBox.root)throw Error('Choose a smaller Mac Box folder, not the whole Box.');if(source.startsWith(macBox.root+path.sep))await assertNoCopyDraft('mac',path.relative(macBox.root,source).split(path.sep).join('/'));}
  }
  register('upload-drop',async input=>{const target=resource(input);await checkUploadSources(input.sources);return uploads.enqueue(input.sources,{root:target.root,path:target.relative,collision:'keep_both'});});
  register('send-mac',async input=>{
    if(input.action==='review'){
      const source=normalize(input.path),destination=normalize(input.destination||'');if(!source)throw Error('Choose a file or folder, not all of Mac Box.');
      const targetRoot=input.targetRoot||'box';boxes.identity(targetRoot);
      await assertNoCopyDraft('mac',source);await assertNoCopyDraft(targetRoot,destination);
      const absolute=await macBox.resolve(source),snapshot=await copySnapshot(absolute),target=(await remote.json('stat',{root:targetRoot,path:destination})).data;
      if(target.kind!=='directory')throw Error('Choose an existing Main Box folder.');
      return {review:copyReviews.create({source,destination,targetRoot,absolute,snapshot,targetRevision:target.revision}),entries:snapshot.count,bytes:snapshot.bytes};
    }
    if(input.action!=='apply')throw Error('Review this cross-machine copy before confirming it.');
    const review=copyReviews.take(input.review),targetRoot=review.targetRoot||'box';boxes.identity(targetRoot);await assertNoCopyDraft('mac',review.source);await assertNoCopyDraft(targetRoot,review.destination);
    if(await macBox.resolve(review.source)!==review.absolute)throw Error('Mac source identity changed.');
    const target=(await remote.json('stat',{root:targetRoot,path:review.destination})).data;if(target.kind!=='directory'||target.revision!==review.targetRevision)throw Error('The Main destination changed. Review the copy again.');
    return uploads.enqueue([review.absolute],{root:targetRoot,path:review.destination,collision:'keep_both',expected:review.snapshot.entries});
  });
  register('directory-copy',async input=>{
    const saveOperation=op=>macBox.db.prepare('INSERT INTO mac_operations VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record').run(op.id,JSON.stringify(op));
    if(input.action==='saved')return macBox.db.prepare('SELECT record FROM mac_operations ORDER BY rowid DESC LIMIT 100').all().map(row=>JSON.parse(row.record)).filter(op=>op.kind==='working_copy');
    if(input.action==='recover')return macBox.exclusive(async()=>{
      const row=macBox.db.prepare('SELECT record FROM mac_operations WHERE id=?').get(input.id);if(!row)throw Error('Saved working copy is missing.');const op=JSON.parse(row.record);
      if(op.kind!=='working_copy'||!op.request||op.rootIdentity!==macBox.identity)throw Error('This copy needs review; no import was resubmitted.');
      if(op.state==='complete')return op;
	  await assertNoCopyDraft('mac',op.path);
      await importWorkingCopy(macBox.helper,macBox.root,op.request);op.state='complete';delete op.error;saveOperation(op);directoryCache.clear();emit('directory-changed',{root:'mac',path:path.posix.dirname(op.path)==='.'?'':path.posix.dirname(op.path)});return op;
    });
    const destination=normalize(input.target||'');
    if(!destination)throw Error('Choose a new named folder in Mac Box.');
    let operationID,expectedParent;
    const checkDestination=async()=>{
      await assertNoCopyDraft('mac',destination);
      await macBox.resolve(path.posix.dirname(destination)==='.'?'':path.posix.dirname(destination));
      if(macBox.blocked(destination,operationID))throw Error('The destination has an unfinished Mac operation.');
      const parent=await macBox.stat(path.posix.dirname(destination)==='.'?'':path.posix.dirname(destination));
      if(expectedParent&&parent.revision!==expectedParent)throw Error('The Mac destination parent changed. Review the copy again.');
      const occupied=await macBox.stat(destination).catch(e=>{if(e.code!=='ENOENT')throw e;return null;});
      if(occupied)throw Error('Destination already exists. Choose a new folder name.');return parent.revision;
    };
    if(input.action==='review'){
      const {root,relative}=resource(input);
      if(!(isBoxRef(root)||root==='storage')||!relative)throw Error('Choose one Main content folder.');
      await assertNoCopyDraft(root,relative);const parentRevision=await checkDestination();
      const metadata=(await remote.json('stat',{root,path:relative})).data;
      if(metadata.kind!=='directory')throw Error('Choose a directory.');
      const source={root,path:relative,revision:metadata.revision};
      const review=(await remote.api('/v1/files/snapshot',{}, {method:'POST',body:{...source,action:'plan'}})).data;
      if(!/^sha256:[a-f0-9]{64}$/.test(review.plan_hash)||review.summary?.source_root!==root||review.summary?.source_path!==relative||review.summary?.source_revision!==source.revision)throw Error('Main did not confirm this exact source.');
      return {...review,review:copyReviews.create({kind:'directory-copy',source,target:destination,parentRevision,rootIdentity:macBox.identity,planHash:review.plan_hash})};
    }
    if(input.action!=='apply')throw Error('Review the working copy first.');
    const review=copyReviews.take(input.review);
    if(review.kind!=='directory-copy'||review.target!==destination||review.rootIdentity!==macBox.identity)throw Error('Working-copy review changed.');
    expectedParent=review.parentRevision;
    await assertNoCopyDraft(review.source.root,review.source.path);
    const temporaryRoot=path.join(app.getPath('userData'),'working-copy-downloads');await mkdir(temporaryRoot,{recursive:true,mode:0o700});
    return macBox.exclusive(async()=>{
      await checkDestination();
      operationID=randomUUID();const op={id:operationID,kind:'working_copy',state:'prepared',path:destination,target:destination,rootIdentity:macBox.identity,source:review.source,planHash:review.planHash};saveOperation(op);
      let result;
      try{result=await makeDirectoryCopy({remote,helper:macBox.helper,root:macBox.root,rootIdentity:macBox.identity,target:destination,source:review.source,planHash:review.planHash,temporaryRoot,verifyDestination:checkDestination,id:operationID,prepared:value=>{Object.assign(op,value);saveOperation(op);}});op.state='complete';saveOperation(op);}
      catch(error){op.state=op.request?'held':'cancelled';op.error=error.message;saveOperation(op);throw error;}
      directoryCache.clear();emit('directory-changed',{root:'mac',path:path.posix.dirname(destination)==='.'?'':path.posix.dirname(destination)});
      return result;
    });
  });
  recoveryServer=await startNativeRecovery(app.getPath('userData'),{capture:async options=>{
    if(closingRuntime||quitting)throw Error('Workspace is closing; capture was not started.');
    try{
      await window?.webContents.executeJavaScript('window.beginRecovery?.()');
      recoveryActive=true;notes.suspended=true;selections.suspended=true;offlineFolders.suspended=true;uploads.suspended=true;
      notes.stopWatches();
      await Promise.allSettled([...activeHandlers]);
      await uploads.running;await selections.running;await selections.localRunning;await offlineFolders.running;await notes.running;await notes.serial;await macBox.serial;await noteEvents;await draftWrites;
      return await stageNativeRecovery(app.getPath('userData'),notes.db,options);
    }finally{
      recoveryActive=false;notes.suspended=false;selections.suspended=false;offlineFolders.suspended=false;uploads.suspended=false;
      if(!quitting){notes.watchConnected=false;notes.startWatch();notes.pump();selections.pump();selections.flushLocal();offlineFolders.pump();uploads.pump();}
      await window?.webContents.executeJavaScript('window.endRecovery?.()');
    }
  }});
  protocol.handle('loom-file', preview);
  register('state', () => ({ roots, connectionState, cache: cache.summary(), timings,zoomFactor:interfaceZoom.factor }));
  register('zoom',input=>interfaceZoom.change(input?.action||'status',window?.webContents));
  register('connect', connect);
  register('application-data',async input=>{const selection=boxes.selection(input);const page=await applicationRoots(remote,{project:input?.project||'',cursor:input?.cursor||0,selection});await rememberApplicationRoots(page.roots);return page;});
  register('list', list);
  register('open', openFile);
  register('preview-text',async input=>{
    const {root,relative}=resource(input);if(!isEditableText(relative)||input.member)throw Error('Text previews are available for notes and source files only.');
    const replica=isBoxRef(root)?notes.get(root,relative):null,owner=root==='mac'?macBox:root==='local'?local:notes.local;
    const localPath=['mac','local'].includes(root)?relative:replica?.localPath;
    if(localPath!==undefined)return {...await owner.preview(localPath),root,path:relative,origin:root==='mac'?'Mac':'local working copy'};
    if(connectionState!=='connected')throw Error('No local preview is available while Main is offline.');
    const metadata=(await remote.json('stat',{root,path:relative})).data;if(metadata.kind!=='file')throw Error('Choose a text file.');
    const limit=16384,end=Math.min(metadata.size,limit)-1;if(end<0)return {root,path:relative,text:'',origin:'Main'};
    const response=await remote.content(root,relative,{signal:AbortSignal.timeout(5000),headers:{Range:`bytes=0-${end}`}}),chunks=[];let bytes=0;
    try{for await(const chunk of response){bytes+=chunk.length;if(bytes>limit)throw Error('Main exceeded the bounded preview.');chunks.push(chunk);}}finally{response.destroy();}
    if(response.headers.etag!==metadata.revision||bytes!==end+1)throw Error('The note changed during preview.');
    const payload=Buffer.concat(chunks);if(payload.includes(0))throw Error('Binary content has no text preview.');
    return {root,path:relative,text:new TextDecoder('utf-8',{fatal:true}).decode(payload,{stream:metadata.size>bytes}),truncated:metadata.size>bytes,origin:'Main · on-demand preview'};
  });
  register('download-file',async input=>{
    const {root,relative}=resource(input);
    if(input.member&&!applicationRoot(root))throw Error('Archive attachments require an approved application data source.');
    const member=input.member?await cachedArchiveMember(remote,cache,root,relative,input.member,{offline:connectionState!=='connected'}):null;
    const replica=isBoxRef(root)?notes.get(root,relative):null,owner=root==='mac'?macBox:root==='local'?local:notes.local;
    const localPath=['local','mac'].includes(root)?relative:replica?.localPath;
    const cached=!member&&localPath===undefined?cache.get(root,relative):null;
    const metadata=member?.metadata||(localPath!==undefined?await owner.stat(localPath):cached?{name:path.posix.basename(relative),size:cached.size,revision:cached.revision,kind:'file'}:(await remote.json('stat',{root,path:relative})).data);
    if(!member&&metadata.kind!=='file')throw Error('Choose a single regular file.');
    const destination=await dialog.showSaveDialog(window,{title:member?'Save attachment copy':'Download original',defaultPath:path.join(app.getPath('downloads'),metadata.name),properties:['createDirectory']});
    if(destination.canceled||!destination.filePath)return {cancelled:true};
    const verifyDestination=output=>guardCopyDestination(output,[app.getPath('userData'),path.join(os.homedir(),'loom-main'),notes.local.root]);
    await verifyDestination(destination.filePath);
    return downloadFileCopy({output:destination.filePath,metadata,
      verifyDestination,
      load:async()=>member?cachedFileStream(member.cached.diskPath):localPath!==undefined?cachedFileStream(await owner.resolve(localPath)):cached?cachedFileStream(cached.diskPath):remote.content(root,relative),
      verify:member||cached?undefined:async()=>{const current=localPath!==undefined?await owner.stat(localPath):(await remote.json('stat',{root,path:relative})).data;if(current.revision!==metadata.revision)throw Error('Source changed during download. No copy was published.');}});
  });
  register('paths',physicalPaths);
  register('copy-path',async input=>{const paths=await physicalPaths(input),value=input.location==='member'?paths.member:input.location==='local'?paths.local:paths.remote;if(!value)throw Error('That physical path is not available.');if(input.page!==undefined&&(!Number.isSafeInteger(input.page)||input.page<1||input.page>1000000||!(/\.pdf$/i.test(input.path))))throw Error('Invalid PDF page link.');clipboard.writeText(value+(input.page?`#page=${input.page}`:''));return true;});
  register('reveal-local',async input=>{const paths=await physicalPaths(input);if(!paths.local)throw Error('This file has not been downloaded locally.');shell.showItemInFolder(paths.local);return true;});
  register('note-graph',input=>{const {root,relative}=resource(input),graph=root==='mac'?macGraph:noteGraph;if(input.action==='complete')return graph.complete({root,kind:input.kind,query:input.query});if(input.action==='tag')return graph.tagFiles({root,tag:input.tag});return graph.links({root,path:relative});});
  register('resolve-reference', async input => {
    const { root, relative } = resource({ root: input.root, path: input.from });
    if(!['local','mac'].includes(root)){const target=await noteGraph.resolve({root,from:relative,reference:input.reference,allowCrossBox:input.navigate===true});if(target)return target;if(!noteGraph.lastError||connectionState!=='connected')throw Error('Linked file is not available in the known catalog or downloaded working set.');}
    return resolveReference({ root, from: relative, reference: input.reference }, { roots, records: root==='mac'?macBox.records.values():notes.records.values(), stat: async (owner, target) => {
      if(owner==='mac')return macBox.stat(target);
      if (owner === 'local') return local.stat(target);
      const replica = notes.get(owner, target);
      if (replica) return { ...(await notes.local.stat(replica.localPath)), relative_path: target };
      return (await remote.json('stat', { root: owner, path: target })).data;
    } });
  });
  register('base-query', async input => {
    const { root, relative } = resource(input);
    if (!/\.base$/i.test(relative)) throw Error('Open a Base file first.');
    const file = await openFile({root,path:relative});
    const text=input.text??file.text;
    const catalog=await (root==='mac'?macGraph:noteGraph).catalog({root,path:input.scope||baseCatalogScope(text,input.viewIndex)||'',refresh:Boolean(input.refresh),more:Boolean(input.more)});
    const known=new Map();for(const [key,page]of directoryCache)if(JSON.parse(key)[0]===root)for(const entry of page.entries||[])if(entry.kind==='file')known.set(entry.relative_path,entry);
    const items=catalog.items.map(item=>{const entry=known.get(item.path);return entry?{...item,size:entry.size,modified_at:entry.modified_at}:item;});
    return {...queryBase(text,items,{viewIndex:input.viewIndex,offset:input.offset,basePath:relative,query:input.query||'',sort:input.sort,qualification:catalog.qualification+' Unobserved filesystem dates/sizes remain unknown.'}),canLoadMoreMetadata:catalog.can_load_more};
  });
  register('knowledge-catalog',input=>{const {root,relative}=resource(input);return (root==='mac'?macGraph:noteGraph).catalog({root,path:relative,refresh:Boolean(input.refresh),more:Boolean(input.more),compact:Boolean(input.compact),quick:Boolean(input.quick),cacheOnly:Boolean(input.cacheOnly)});});
  register('history',input=>{const {root,relative}=resource(input);if(root==='mac')throw Error('Mac files retain local recovery journals; Main timeline does not cover this root.');return new WorkspaceHistory(remote,notes,noteGraph).read({...input,root,path:relative});});
  register('base-definition',async input=>{const {relative}=resource(input);if(!/\.base$/i.test(relative))throw Error('Choose a Base file.');return editBaseDefinition(input.text,input.viewIndex,input.changes);});
  register('offline', input => {
    if(input?.operation==='selections')return selections.list();
    if(input?.operation==='pause_selection'||input?.operation==='resume_selection')return selections.state(input.id,input.operation==='pause_selection'?'paused':'active');
    if(input?.operation==='jobs')return offlineFolders.jobs().map(({queue,seen,...job})=>({...job,pendingFolders:queue.length}));
    if(input?.operation==='pause'||input?.operation==='resume')return offlineFolders.setState(input.id,input.operation==='pause'?'paused':'running');
    const {root,relative}=resource(input);
    const selection={root,path:relative,assets:input.assets===true};
    if(input.operation==='select')return selections.start(selection);
    if(input.operation==='preview')return offlineFolders.preview(selection);
    if(input.operation==='start')return offlineFolders.start(selection);
    throw Error('Unknown offline action.');
  });
  register('annotations', annotations);
  register('mutate', mutate); register('search', search);register('find',findFiles); register('enrich', enrich);
  register('note-review', input => { const {root,relative}=resource(input);return notes.review(root,relative); });
  register('note-resolve', input => notes.resolve(input.token,input.text));
  register('explain', input => { const { root, relative } = resource(input); return remote.json('explain', { root, path: relative }).then(result => result.data); });
  register('overview', async input => {
    const result=await operations.overview(input);
    if(input.section==='activity')result.items=chronological([...notes.activity(boxes.selection(input).root),...result.items]);
    return result;
  });
  register('inspect', input => input.owner==='local'?notes.inspect(input.ref):operations.inspect(input));
  const lifecycle=notes.lifecycle;
  Object.assign(lifecycle,{checkDrafts:(relative,binding={root:'box'})=>{
    for(const [key]of drafts){const [root,p]=JSON.parse(key);if(root===binding.root&&(p===relative||p.startsWith(relative+'/')))throw Error('Save all project drafts before changing its lifecycle. Your drafts are preserved.');}
  },checkUploads:(relative,binding={root:'box'})=>{
    for(const row of notes.db.prepare('SELECT record FROM desktop_upload_queue').iterate()){const v=JSON.parse(row.record);if((v.root||'box')===binding.root&&!['accepted','skipped','notice','cancelled'].includes(v.state)&&(v.destination===relative||v.destination.startsWith(relative+'/')))throw Error('Finish or resolve uploads into this project first.');}
    for(const s of selections.list())if(s.root===binding.root&&s.state!=='paused'&&(s.path===relative||s.path.startsWith(relative+'/')||relative.startsWith(s.path+'/')))throw Error('Pause overlapping continuous folder sync before archiving or restoring. Its local files will be preserved.');
  }});
  operations.lifecycle=lifecycle;notes.lifecycle=lifecycle;
  const workspacePaths=new WorkspacePaths(notes,remote,{checkDrafts:lifecycle.checkDrafts,checkUploads:lifecycle.checkUploads});
  lifecycle.paths=workspacePaths;
  register('action', async input => {
    if(input?.kind?.startsWith('workspace-path-')){
      const action=input.kind.slice('workspace-path-'.length);
      if(action==='list')return workspacePaths.list();
      if(action==='preview')return workspacePaths.review(input.operation,input.project,input.from,input.to,boxes.selection(input));
      if(!['apply','recover','cancel','inspect'].includes(action))throw Error('Unsupported saved filesystem action.');
      const result=action==='inspect'?await workspacePaths.inspect(input.id):action==='cancel'?await workspacePaths.cancel(input.id):await workspacePaths.apply(input.id,{recover:action==='recover'});
      if(result.state==='complete'){directoryCache.clear();noteGraph.invalidate(result.from_root||'box');emit('workspace-path',{...result});}
      return result;
    }
    if(input?.kind==='project-export'){
      if(typeof input.project!=='string'||!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(input.project)||input.project.includes('..')||!['human','portable','archival'].includes(input.mode))throw Error('Choose a registered project and snapshot format.');
      const destination=await dialog.showSaveDialog(window,{title:'Export project snapshot',defaultPath:path.join(app.getPath('downloads'),`${input.project}-${input.mode}.tar`),filters:[{name:'Tar archive',extensions:['tar']}],properties:['createDirectory']});
      if(destination.canceled||!destination.filePath)return {cancelled:true};
      return exportProjectSnapshot(remote,{project:input.project,mode:input.mode,output:destination.filePath});
    }
    const result=await operations.action(input);
    if(['retained-apply','retained-inspect'].includes(input?.kind)&&result?.state==='complete'){directoryCache.clear();emit('retained-move',{from:result.source,to:result.destination});}
    if(input?.kind?.startsWith('project-lifecycle-')&&result?.state==='complete'){
      directoryCache.clear();noteGraph.invalidate(result.root||'box');emit('project-lifecycle',{root:result.root||'box',boxPath:result.boxPath,operation:result.kind,project:result.project});
    }
    return result;
  });
  register('cancel-overview', () => { operations.cancel(); dashboard.cancel(); return true; });
  register('save', async input => {
    const { root, relative } = resource(input);
    if(root==='mac'){const result=await macBox.save(relative,input.text,input.base,input.binding_id);clearSavedDraft(drafts,root,input.binding_id,input.text);await persistDrafts();macGraph.invalidate('mac');return result;}
    const replica = isBoxRef(root) ? notes.get(root, relative) : null;
    if (!replica || replica.id !== input.binding_id) throw new Error('Open an application-owned local note before saving. Legacy Obsidian copies are recovery-only; your draft is preserved.');
    const result = await notes.save(root,relative,input.text,input.base);
    clearSavedDraft(drafts,root,replica.id,input.text);
    await persistDrafts();
    return result;
  });
  register('draft', async input => {
    const { root, relative } = resource(input);
    if (!(isBoxRef(root)||['local','mac'].includes(root)) || typeof input.text !== 'string' || Buffer.byteLength(input.text) > TEXT_LIMIT) throw new Error('Invalid local draft.');
    const retained=isBoxRef(root)&&input.binding_id?notes.byID.get(input.binding_id):null;
    if(retained&&retained.root!==root)throw Error('Draft identity belongs to a different Box. The saved draft is unchanged.');
    storeDraft(drafts,root,retained?.path||relative,{ text: input.text, base: input.base, savedAt: Date.now(), binding_id:retained?.id||(root==='mac'?input.binding_id:undefined) });
    await persistDrafts();
    return true;
  });
  register('pin', async input => {
    const { root, relative } = resource(input);
    if (root === 'local'||applicationRoot(root)) throw new Error('Application attachments use an evictable on-demand cache; bulk or pinned copies are not enabled.');
    if (!cache.get(root, relative)) {
      if(input.value===false)return true;
      const { data: metadata } = await remote.json('stat', { root, path: relative });
      await cache.fetch(root, relative, metadata, () => remote.content(root, relative));
    }
    return cache.pin(root, relative, input.value);
  });
  register('choose-local', async () => {
    const selected = await dialog.showOpenDialog(window, { title: 'Open local recovery folder', properties: ['openDirectory'] });
    if (selected.canceled) return null;
    localWatcher?.close();
    local = new LocalFiles(selected.filePaths[0]);
    roots[0] = { ...roots[0], source_path: local.root, available: true, hidden: true };
    directoryCache.clear();
    return roots[0];
  });
  register('external', async input => {
    const url = new URL(input);
    if (['https:', 'http:'].includes(url.protocol)||url.protocol==='codex:'&&url.hostname==='threads'&&/^\/[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}\/?$/i.test(url.pathname)) await shell.openExternal(url.href);
  });

  windowLifecycle=new WindowLifecycle({create:createWindow,flush:w=>w.webContents.executeJavaScript('window.beginRecovery?.() ?? window.flushWorkspace?.() ?? window.flushDraft?.()'),closed:w=>{
    if(window===w)window=null;dashboard.cancel();filenameController?.abort();interactiveSearch?.cancel();explorerWatch.setScopes([]);localWatcher?.close();clearTimeout(watchTimer);
  },failed:(w,error)=>{if(!w.isDestroyed()){void w.webContents.executeJavaScript('window.endRecovery?.()');dialog.showMessageBox(w,{type:'error',message:'Your draft could not be preserved. The window will stay open.',detail:error.message});}}});
  await windowLifecycle.open();
}

function createWindow(){
  const current=window=new BrowserWindow({
    width: 1280, height: 840, minWidth: 800, minHeight: 520, backgroundColor: '#171a19',show:false,
    title: 'Loom', titleBarStyle: 'hiddenInset', trafficLightPosition: { x: 12, y: 14 },
    webPreferences: { preload: path.join(directory, 'preload.cjs'), contextIsolation: true, sandbox: true, nodeIntegration: false, webSecurity: true, plugins: true },
  });
  current.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  current.webContents.on('did-finish-load',()=>{current.webContents.setZoomFactor(interfaceZoom.factor);if(process.platform==='darwin')current.setWindowButtonPosition({x:12,y:Math.round(Math.max(42,42*interfaceZoom.factor)/2-7)});});
  current.on('focus',()=>explorerWatch?.reconnect());
  current.webContents.on('before-input-event',(event,input)=>{const action=zoomShortcut(input);if(action){event.preventDefault();void interfaceZoom.change(action,current.webContents).catch(error=>emit('interface-zoom',{factor:interfaceZoom.factor,error:error.message}));}});
  for(const event of ['minimize','hide'])current.on(event,()=>{dashboard.cancel();current.webContents.send('workspace:event',{kind:'dashboard-visibility',visible:false});});
  for(const event of ['restore','show'])current.on(event,()=>current.webContents.send('workspace:event',{kind:'dashboard-visibility',visible:true}));
  current.webContents.on('will-navigate', event => event.preventDefault());
  current.webContents.on('will-attach-webview', event => event.preventDefault());
  current.webContents.session.setPermissionRequestHandler((_webContents, _permission, callback) => callback(false));
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    { label: 'Loom', submenu: [{ role: 'about' }, {label:'Settings...',accelerator:'CmdOrCtrl+,',click:()=>emit('settings',{})}, { type: 'separator' }, { role: 'hide' }, { role: 'hideOthers' }, { type: 'separator' }, { role: 'quit' }] },
    { label: 'Edit', submenu: [{ role: 'undo' }, { role: 'redo' }, { type: 'separator' }, { role: 'cut' }, { role: 'copy' }, { role: 'paste' }, { role: 'selectAll' }] },
    { label: 'View', submenu: [{ label: 'Command Palette', accelerator: 'CmdOrCtrl+P', click: () => emit('palette', {}) }, {type:'separator'},
      ...[['Zoom In','CmdOrCtrl+Plus','in'],['Zoom Out','CmdOrCtrl+-','out'],['Actual Size','CmdOrCtrl+0','reset']].map(([label,accelerator,action])=>({label,accelerator,click:()=>interfaceZoom.change(action,current.webContents).catch(error=>emit('interface-zoom',{factor:interfaceZoom.factor,error:error.message}))})),
      {type:'separator'},{ role: 'toggleDevTools' }, { role: 'togglefullscreen' }] },
  ]));
  current.once('ready-to-show',()=>current.show());
  current.loadFile(rendererFile).catch(error=>{console.error(error.message);if(!current.isDestroyed())dialog.showMessageBox(current,{type:'error',message:'Loom could not open.',detail:error.message});});
  return current;
}

let quitting = false;
async function drainRuntime(){
  if(recoveryActive)throw Error('Wait for the active recovery capture before closing.');
  const flushed=window&&!window.isDestroyed()?await window.webContents.executeJavaScript('window.beginRecovery?.() ?? window.flushWorkspace?.() ?? window.flushDraft?.()'):true;
  if(flushed===false)throw Error('Local drafts could not be fully preserved.');
  closingRuntime=true;
  interactiveSearch?.cancel();filenameController?.abort();await interfaceZoom?.pending;
  // The update-install IPC owns this barrier and must not await itself.
  await Promise.allSettled([...activeHandlers].filter(work=>activeHandlerNames.get(work)!=='updates'));
  clearTimeout(watchTimer);localWatcher?.close();
  explorerWatch?.close();
  await recoveryServer?.close();await uploads?.close();await offlineFolders?.close();await selections?.close();
  await macBox?.close();await Promise.all([cache?.persist(),notes?.close()]);await noteEvents;await draftWrites;
  await nativeOwner?.close();await remote?.close();if(socketDirectory)await rm(socketDirectory,{recursive:true,force:true});
}
const activateWindow=()=>{void Promise.resolve(starting).then(()=>{if(!quitting)return windowLifecycle?.open();}).catch(error=>console.error(error.message));};
if (!app.requestSingleInstanceLock()) app.quit();
else {
  app.on('second-instance', activateWindow);
  app.on('activate',activateWindow);
  starting=app.whenReady().then(start);starting.catch(error => { console.error(error); app.quit(); });
  app.on('window-all-closed', () => {if(process.platform!=='darwin')app.quit();});
  process.on('SIGTERM', () => app.quit());
  process.on('SIGINT', () => app.quit());
  app.on('before-quit', event => {
    if (quitting) return;
    event.preventDefault();
    quitting = true;
    drainRuntime().then(()=>{windowLifecycle?.allowClose();app.quit();})
      .catch(error => {
        quitting = false;
        closingRuntime=false;void window?.webContents.executeJavaScript('window.endRecovery?.()');
        if (window && !window.isDestroyed()) dialog.showMessageBox(window, { type: 'error', message: 'The app stayed open to preserve your draft.', detail: error.message });
        else { console.error(error.message); app.quit(); }
      });
  });
}
