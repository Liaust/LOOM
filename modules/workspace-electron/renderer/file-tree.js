import { DirectoryTree } from './tree-model.js';
import { TreeSelection, treeEntryKey } from './tree-selection.js';
import {isBoxRef,isWritableBox} from '../src/box-roots.mjs';
import {visibleExplorerEntries} from './explorer-visibility.js';
const internal='application/x-loom-entry';
export const fileDropEffect=(source,destination,roots=[])=>{
  const writable=ref=>isWritableBox(roots.find(root=>root.ref===ref)||ref==='box'&&{ref:'box'});
  if(source==='mac')return destination==='mac'?'move':writable(destination)?'copy':null;
  if(isBoxRef(source)&&isBoxRef(destination))return source===destination&&writable(source)?'move':null;
  return ['box','storage'].includes(source)&&['box','storage'].includes(destination)?'move':null;
};

export class FileTree {
  constructor(container, { loader, roots, selected, openFile, selectFolder, contextMenu, command, icon, icons, decorate,dropFiles,dropEntry,hideReadmeAgents=false,onScopes }) {
    this.container = container;
    this.model = new DirectoryTree(loader);
    this.selection = new TreeSelection();
    this.rowEntries = new WeakMap();
    this.hideReadmeAgents = hideReadmeAgents === true;
    container.setAttribute('aria-multiselectable', 'true');
    Object.assign(this, { roots, selected, openFile, selectFolder, contextMenu, command, icon, icons, decorate, dropEntry,onScopes });
    if(dropFiles||dropEntry){
      const target=event=>event.target.closest('[data-directory="true"]');
      container.addEventListener('dragover',event=>{const row=target(event),types=Array.from(event.dataTransfer?.types||[]),own=types.includes(internal),effect=row&&(own?fileDropEffect(this.dragSource?.root,row.dataset.root,this.roots()):isWritableBox(this.roots().find(r=>r.ref===row.dataset.root))&&types.includes('Files')?'copy':null);if(effect){event.preventDefault();event.dataTransfer.dropEffect=effect;container.querySelector('.drop-target')?.classList.remove('drop-target');row.classList.add('drop-target');}});
      container.addEventListener('dragleave',event=>{if(!container.contains(event.relatedTarget))container.querySelector('.drop-target')?.classList.remove('drop-target');});
      container.addEventListener('drop',event=>{const row=target(event);container.querySelector('.drop-target')?.classList.remove('drop-target');if(!row)return;const own=event.dataTransfer?.getData(internal);if(own&&dropEntry){event.preventDefault();try{const entry=JSON.parse(own);if(fileDropEffect(entry.root,row.dataset.root,this.roots())&&typeof entry.path==='string')dropEntry(entry,{root:row.dataset.root,path:row.dataset.path,box_id:this.roots().find(r=>r.ref===row.dataset.root)?.box_id});}catch{}return;}const root=this.roots().find(r=>r.ref===row.dataset.root);if(!isWritableBox(root)||!event.dataTransfer?.files.length)return;event.preventDefault();dropFiles?.(Array.from(event.dataTransfer.files),{root:row.dataset.root,box_id:root.box_id,path:row.dataset.path});});
    }
    this.container.addEventListener('keydown', event => this.keydown(event));
    const document=container.ownerDocument;
    if(document){
      document.addEventListener('pointerdown',()=>{if(this.creation&&!this.creation.name?.trim()&&!this.creation.busy)this.namingPointer=true;},true);
      const release=()=>{if(!this.namingPointer)return;this.namingPointer=false;const value=this.creation;setTimeout(()=>{if(this.creation===value&&document.activeElement!==this.container.querySelector('.tree-create-input'))this.cancelEmptyCreate();},0);};
      document.addEventListener('pointerup',release,true);document.addEventListener('pointercancel',release,true);
    }
  }

  setHideReadmeAgents(hidden) {
    const value = hidden === true;
    if (this.hideReadmeAgents === value) return;
    this.hideReadmeAgents = value;
    this.render();
  }

  render() {
    const focused = this.container.contains(document.activeElement) ? document.activeElement.dataset.treeKey : null;
    const scroll = this.container.scrollTop;
    const renderNode = (root, entry, depth, isRoot = false) => {
      if(entry.root_ref){root=entry.source||this.roots().find(r=>r.ref===entry.root_ref)||root;entry={name:entry.name,relative_path:'',kind:'directory'};isRoot=true;}
      const relative = entry.relative_path;
      const archive=!entry.member&&root.format==='zotero-webdav'&&entry.kind==='file'&&/\.zip$/i.test(entry.name);
      const directory = entry.kind === 'directory'||archive;
      const expanded = directory && this.model.isExpanded(root.ref, relative);
      const branch = directory ? this.model.branch(root.ref, relative) : null;
      const group = document.createElement('div');
      const row = document.createElement('button');
      const item = { root: root.ref, path: relative, directory, isRoot, ...(entry.member ? { member: entry.member } : {}) };
      this.rowEntries.set(row, item);
      row.className = `tree-row${isRoot ? ' tree-root' : ''}`;
      row.style.paddingLeft=`${5+Math.min(depth-1,6)*7}px`;
      row.setAttribute('role', 'treeitem');
      row.setAttribute('aria-level', String(depth));
      row.setAttribute('aria-label', isRoot ? `${root.label} ${root.node === 'mac' ? 'Mac' : 'Main'}` : entry.name);
      if (directory) row.setAttribute('aria-expanded', String(expanded));
      row.dataset.treeKey = treeEntryKey(item);
      row.dataset.root = root.ref;
      row.dataset.path = relative;
      row.dataset.directory = String(directory);
      if(!isRoot&&this.dropEntry&&(isWritableBox(root)||['storage','mac'].includes(root.ref))){row.draggable=true;row.ondragstart=event=>{this.dragSource={root:root.ref,box_id:root.box_id,path:relative,directory};event.dataTransfer.effectAllowed=root.ref==='mac'?'copyMove':'move';event.dataTransfer.setData(internal,JSON.stringify(this.dragSource));};row.ondragend=()=>{this.dragSource=null;};}
      row.tabIndex = -1;
      row.dataset.tooltip = `${root.rootLabel||root.label}/${relative}`;
      const chevron = directory ? this.icon(expanded ? 'chevron-down' : 'chevron-right') : document.createElement('span');
      chevron.classList.add('tree-chevron');
      const symbol = isRoot ? root.source_kind?.startsWith('application_data')?'database':root.node === 'mac' ? 'hard-drive' : root.ref === 'archive' ? 'archive' : 'server' : archive?'file-archive':directory ? expanded ? 'folder-open' : 'folder' : /\.pdf$/i.test(entry.name) ? 'file-type' : 'file-text';
      const label = document.createElement('span'); label.className = 'tree-label'; label.textContent = entry.name;
      row.append(chevron, this.icon(symbol), label);
      if (!directory && this.decorate) row.append(this.decorate(entry));
      if (isRoot) { const location = document.createElement('small'); location.textContent = root.node === 'mac' ? 'Mac' : 'Main'; row.append(location); }
      row.onclick = event => this.activate(row, event);
      row.onmousedown = event => { if (event.button === 1 && !directory) event.preventDefault(); };
      row.onauxclick = event => {
        if (event.button !== 1 || directory) return;
        event.preventDefault();
        this.openFile(relative, root.ref, { member: entry.member, newTab: true, background: true });
      };
      row.oncontextmenu=event=>{event.preventDefault();this.selectRow(row,{preserve:true});this.focus(row);this.contextMenu?.(root.ref,entry,event);};
      row.addEventListener('keydown',event=>{if(event.key==='ContextMenu'||event.shiftKey&&event.key==='F10'){event.preventDefault();event.stopPropagation();this.selectRow(row,{preserve:true});const rect=row.getBoundingClientRect();this.contextMenu?.(root.ref,entry,{clientX:rect.left+30,clientY:rect.bottom});}});
      group.append(row);
      if (expanded) {
        const entries = branch.page?.entries || [];
        const visibleEntries = visibleExplorerEntries(entries, this.hideReadmeAgents);
        const children = document.createElement('div'); children.className = 'tree-children'; children.setAttribute('role', 'group');
        if(this.creation?.root===root.ref&&this.creation.path===relative)children.append(this.creationRow());
        for (const child of visibleEntries) children.append(renderNode(root, child, depth + 1));
        if (branch.error) {
          const retry = document.createElement('button'); retry.className = 'tree-message tree-retry'; retry.textContent = 'Could not load · retry'; retry.dataset.tooltip = branch.error;
          retry.onclick = () => this.load(root.ref, relative, { refresh: true }); children.append(retry);
        } else if (branch.pending && !branch.page) {
          const loading = document.createElement('div'); loading.className = 'tree-message'; loading.textContent = 'Loading'; children.append(loading);
        } else if (branch.page && !visibleEntries.length && !branch.page.has_more) {
          const empty = document.createElement('div'); empty.className = 'tree-message'; empty.textContent = entries.length ? 'No visible files' : 'Empty folder'; children.append(empty);
        }
        if (branch.page?.has_more) {
          const more = document.createElement('button'); more.className = 'tree-message tree-more'; more.textContent = branch.pending ? 'Loading' : 'Load more'; more.disabled = Boolean(branch.pending);
          more.onclick = () => this.load(root.ref, relative, { more: true }); children.append(more);
        }
        group.append(children);
      }
      return group;
    };
    const activeInput=this.container.querySelector('.tree-create-input'),editing=activeInput===document.activeElement,creationSelection=editing?[activeInput.selectionStart,activeInput.selectionEnd]:null;
    const roots=this.roots();
    this.container.replaceChildren(...roots.filter(root=>!root.parent_ref||!roots.some(r=>r.ref===root.parent_ref)).map(root => renderNode(root, { name: root.label, relative_path: root.path||'', kind: 'directory' }, 1, true)));
    this.icons(this.container);
    const rows = [...this.container.querySelectorAll('[role=treeitem]')];
    this.onScopes?.(rows.filter(row=>row.getAttribute('aria-expanded')==='true').map(row=>{const item=this.rowEntries.get(row);return {root:item.root,path:item.path};}));
    this.selection.sync(rows.map(row => this.rowEntries.get(row)), this.selected?.());
    this.updateSelection();
    const focus = rows.find(row => row.dataset.treeKey === focused);
    const selected = rows.find(row => row.getAttribute('aria-selected') === 'true');
    (focus || selected || rows[0])?.setAttribute('tabindex', '0');
    if (focused) (focus || selected || rows[0])?.focus({ preventScroll: true });
    this.container.scrollTop = scroll;
    if(this.creation&&(this.creation.focus||editing)){const input=this.container.querySelector('.tree-create-input');input?.focus({preventScroll:true});if(creationSelection)input?.setSelectionRange(...creationSelection);else input?.scrollIntoView({block:'nearest'});this.creation.focus=false;}
  }

  async beginCreate(root,path,{directory=false,placeholder,onCommit}){
    if(this.creation||this.preparingCreation){this.container.querySelector('.tree-create-input')?.focus();throw Error('Finish naming the current new entry first.');}
    const request={root,path,cancelled:false};this.preparingCreation=request;
    try{
      const scope=this.roots?.().find(r=>r.ref===root);
      await this.model.reveal(root,path,scope?.path||'');await this.model.expand(root,path);
      if(request.cancelled)return null;
      return new Promise(resolve=>{this.creation={root,path,directory,placeholder,onCommit,resolve,name:'',focus:true};this.render();});
    }finally{if(this.preparingCreation===request)this.preparingCreation=false;}
  }
  cancelEmptyCreate(root,path=''){
    const value=this.creation||this.preparingCreation;
    if(!value||value.busy||value.name?.trim()||root&&(value.root!==root||path&&value.path!==path&&!value.path.startsWith(path+'/')))return false;
    if(this.creation===value){this.creation=null;value.resolve(null);}
    if(this.preparingCreation===value){value.cancelled=true;this.preparingCreation=false;}
    // Preserve the pending pointer target: rebuilding here swallows click-away.
    this.container.querySelector('.tree-create-input')?.closest('.tree-create')?.remove();
    return true;
  }
  creationRow(){
    const value=this.creation,row=document.createElement('div');row.className='tree-create';row.append(this.icon(value.directory?'folder':'file-text'));
    const input=document.createElement('input');input.className='tree-create-input';input.value=value.name;input.placeholder=value.placeholder;input.setAttribute('aria-label',value.directory?'New folder name':'New file name');input.disabled=Boolean(value.busy);input.spellcheck=false;input.oninput=()=>{value.name=input.value;};
    input.onblur=()=>queueMicrotask(()=>{if(!this.namingPointer&&this.creation===value&&this.container.querySelector('.tree-create-input')!==document.activeElement)this.cancelEmptyCreate();});
    input.onkeydown=async event=>{
      if(event.key!=='Enter'&&event.key!=='Escape')return;event.preventDefault();event.stopPropagation();if(value.busy)return;
      if(event.key==='Escape'){this.creation=null;value.resolve(null);this.render();return;}
      value.name=input.value;if(!value.name.trim())return this.cancelEmptyCreate();value.busy=true;this.render();
      try{const result=await value.onCommit(value.name.trim());this.creation=null;value.resolve(result);this.render();}
      catch(error){value.busy=false;value.error=error.message;value.focus=true;this.render();}
    };
    row.append(input);if(value.error){const error=document.createElement('span');error.className='tree-create-error';error.textContent=value.error;error.setAttribute('role','alert');row.append(error);}return row;
  }

  focus(row) {
    for (const element of this.container.querySelectorAll('[role=treeitem]')) element.tabIndex = element === row ? 0 : -1;
    row.focus({ preventScroll: true });
  }

  selectedEntries() { return this.selection.entries(); }

  updateSelection() {
    for (const row of this.container.querySelectorAll('[role=treeitem]')) {
      const selected = this.selection.has(this.rowEntries.get(row));
      row.setAttribute('aria-selected', String(selected));
      row.classList.toggle('is-selected', selected);
    }
  }

  selectRow(row, options) {
    this.selection.select(this.rowEntries.get(row), options);
    this.updateSelection();
  }

  activate(row, event) {
    const entry = this.rowEntries.get(row), modifier = event.metaKey || event.ctrlKey;
    this.selectRow(row, event);
    this.focus(row);
    if (entry.directory) {
      if (!modifier && !event.shiftKey) {
        this.selectFolder?.(entry.root, entry.path);
        this.toggle(entry.root, entry.path);
      }
    } else if (modifier || !event.shiftKey) {
      if (!modifier || !event.shiftKey) this.selection.opening(entry);
      this.openFile(entry.path, entry.root, { member: entry.member, ...(modifier ? { newTab: true, background: Boolean(event.shiftKey) } : {}) });
    }
  }

  async load(root, relative, options) {
    const operation = this.model.load(root, relative, options);
    this.render();
    try { await operation; } catch { /* This branch renders its own retry state. */ }
    this.render();
  }

  async toggle(root, relative) {
    if (this.model.isExpanded(root, relative)) { this.cancelEmptyCreate(root,relative);this.model.collapse(root, relative); this.render(); }
    else { const operation=this.model.expand(root,relative);this.render();try{await operation;}catch{/* Branch renders its retry state. */}this.render(); }
  }

  async reveal(root, relative) {
    const scope=this.roots().find(r=>r.ref===root),base=scope?.path||'';
    if(!scope||base&&relative!==base&&!relative.startsWith(base+'/'))return;
    try {
      if(scope.parent_ref){
        const page=await this.model.expand(scope.parent_ref,'');
        if(!page.entries.some(entry=>entry.root_ref===root))this.model.remember(scope.parent_ref,'',{...page,entries:[...page.entries,{name:scope.label,relative_path:'',kind:'directory',root_ref:root,source:scope}]});
      }
      await this.model.reveal(root, relative,base);
    } catch { /* Keep the existing document and qualify only the failed branch. */ }
    this.render();
  }

  async refresh() {
    const visible = [...this.model.expanded].map(key => JSON.parse(key));
    // Explicit refresh touches only already expanded branches, four at a time.
    for (let index = 0; index < visible.length; index += 4) {
      const requests = visible.slice(index, index + 4).map(([root, relative]) => this.model.load(root, relative, { refresh: true }).catch(() => {}));
      this.render();
      await Promise.all(requests);
    }
    this.render();
  }

  keydown(event) {
    if (event.defaultPrevented || event.isComposing || !this.container.contains(event.target) || event.target.closest('input,textarea,select,[contenteditable="true"]')) return;
    const row = event.target.closest('[role=treeitem]');
    if (!row) return;
    const modifier = event.metaKey || event.ctrlKey;
    const action = !event.altKey && !event.shiftKey && (modifier ? { c: 'copy', x: 'cut', v: 'paste' }[event.key.toLowerCase()] : event.key === 'F2' ? 'rename' : null);
    if (action) {
      event.preventDefault();event.stopPropagation();
      this.command?.(action, this.selectedEntries(), event);
      return;
    }
    const rows = [...this.container.querySelectorAll('[role=treeitem]')];
    const index = rows.indexOf(row);
    const directory = row.dataset.directory === 'true';
    let target;
    if (event.key === 'ArrowDown') target = rows[Math.min(index + 1, rows.length - 1)];
    if (event.key === 'ArrowUp') target = rows[Math.max(index - 1, 0)];
    if (event.key === 'Home') target = rows[0];
    if (event.key === 'End') target = rows.at(-1);
    if (event.key === 'ArrowRight' && directory) {
      if (row.getAttribute('aria-expanded') === 'false' && !modifier && !event.shiftKey) this.toggle(row.dataset.root, row.dataset.path);
      else if (Number(rows[index + 1]?.getAttribute('aria-level')) > Number(row.getAttribute('aria-level'))) target = rows[index + 1];
    }
    if (event.key === 'ArrowLeft') {
      if (directory && row.getAttribute('aria-expanded') === 'true' && !modifier && !event.shiftKey) this.toggle(row.dataset.root, row.dataset.path);
      else target = rows.slice(0, index).reverse().find(parent => Number(parent.getAttribute('aria-level')) < Number(row.getAttribute('aria-level')));
    }
    if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); this.activate(row, event); return; }
    if (target || event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault();
      if (target) { if (!modifier || event.shiftKey) this.selectRow(target, event); this.focus(target); target.scrollIntoView({ block: 'nearest' }); }
    }
  }
}
