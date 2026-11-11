import { Compartment, EditorState, EditorSelection, Transaction, Prec } from '@codemirror/state';
import { EditorView, Decoration, ViewPlugin, WidgetType, keymap, lineNumbers, drawSelection, highlightSpecialChars } from '@codemirror/view';
import { history, historyKeymap, defaultKeymap, indentWithTab } from '@codemirror/commands';
import { markdown, markdownKeymap } from '@codemirror/lang-markdown';
import { syntaxTree } from '@codemirror/language';
import { GFM } from '@lezer/markdown';
import { isMarkdown, editorLanguage } from '../src/file-formats.mjs';
import { markdownPresentation } from './markdown-presentation.js';
import { propertiesExtension } from './properties-view.js';
import { frontmatter } from './frontmatter.js';
import {titleExtension} from './document-title.js';
import {syntaxTheme,languageSupport} from './syntax-themes.js';
import {autocompletion} from '@codemirror/autocomplete';
import {noteCompletionSource} from './note-completions.js';
import {search,searchKeymap,openSearchPanel} from '@codemirror/search';
import {languages} from '@codemirror/language-data';
import {richMarkdown} from './rich-markdown.js';
import {editorSearchPanel} from './editor-search.js';
import {navigationIntent} from './navigation-intent.js';
import {attachmentEmbeds,attachmentSpans} from './attachment-embeds.js';

class Bullet extends WidgetType {
  eq() { return true; }
  toDOM() { const mark = document.createElement('span'); mark.className = 'lp-bullet'; mark.textContent = '•'; return mark; }
}
class Task extends WidgetType {
  constructor(from, checked) { super(); this.from = from; this.checked = checked; }
  eq(other) { return other.from === this.from && other.checked === this.checked; }
  toDOM(view) {
    const input = document.createElement('input'); input.type = 'checkbox'; input.className = 'lp-task'; input.checked = this.checked;
    input.disabled = view.state.readOnly; input.setAttribute('aria-label', this.checked ? 'Completed task' : 'Incomplete task');
    input.onmousedown = event => event.preventDefault();
    input.onclick = () => {
      if (view.state.readOnly) return;
      const current = view.state.doc.sliceString(this.from, this.from + 3);
      if (/^\[[ xX]\]$/.test(current)) view.dispatch({ changes: { from: this.from + 1, to: this.from + 2, insert: this.checked ? ' ' : 'x' }, userEvent: 'input' });
    };
    return input;
  }
  ignoreEvent() { return true; }
}
class ImageReference extends WidgetType {
  constructor(reference, label, owner) { super(); this.reference = reference; this.label = label; this.owner = owner; }
  eq(other) { return other.reference === this.reference && other.label === this.label && other.owner === this.owner; }
  toDOM() {
    const dom=document.createElement('span'),link=document.createElement('button');dom.className='lp-attachment';link.className='note-attachment';link.textContent=this.label||this.reference;link.onclick=event=>this.owner.follow(this.reference,navigationIntent(event));dom.append(link);return dom;
  }
  destroy(dom){dom._attachment?.destroy();}
  ignoreEvent(event) { return !['dragover','drop'].includes(event.type); }
}

function liveMarkdown(owner) {
  return ViewPlugin.fromClass(class {
    constructor(view) { this.decorations = this.build(view); }
    update(update) {
      if (update.docChanged || update.selectionSet || update.viewportChanged || update.focusChanged || syntaxTree(update.startState) !== syntaxTree(update.state)) this.decorations = this.build(update.view);
    }
    build(view) {
      const ranges = [], seen = new Set();
      const blocks=attachmentSpans(view.state);
      for (const visible of view.visibleRanges) for (const span of markdownPresentation(view.state, syntaxTree(view.state), visible.from, visible.to, view.hasFocus)) {
        const key = JSON.stringify(span); if (seen.has(key)) continue; seen.add(key);
        if(span.kind==='image'&&blocks.some(block=>span.from>=block.from&&span.to<=block.to))continue;
        const decoration = span.kind === 'line' ? Decoration.line({ class: span.className })
          : span.kind === 'mark' ? Decoration.mark({ class: span.className, ...(span.reference ? { attributes: { 'data-note-reference': span.reference } } : span.tag?{attributes:{'data-note-tag':span.tag}}:span.className==='lp-strong'?{attributes:{role:'strong'}}:span.className==='lp-emphasis'?{attributes:{role:'emphasis'}}:{}) })
          : Decoration.replace({ ...(span.kind === 'bullet' ? { widget: new Bullet() } : span.kind === 'task' ? { widget: new Task(span.from, span.checked) } : span.kind === 'image' ? { widget: new ImageReference(span.reference, span.label, owner) } : {}) });
        ranges.push(decoration.range(span.from, span.kind === 'line' ? span.from : span.to));
      }
      return Decoration.set(ranges, true);
    }
  }, { decorations: value => value.decorations });
}

function wrapSelection(view, marker) {
  const transaction = view.state.changeByRange(range => ({
    changes: [{ from: range.from, insert: marker }, { from: range.to, insert: marker }],
    range: EditorSelection.range(range.from + marker.length, range.to + marker.length),
  }));
  view.dispatch({ ...transaction, userEvent: 'input' }); return true;
}

export class LiveEditor {
  constructor({ text, path, editable, sourceMode = false, selection, onChange, onSave, follow, asset, releaseAsset=()=>{},attachmentLinked=()=>{},attach,existingAttachment,external,form,icon, notice=()=>{},rename=()=>{},theme='one-dark',suggest=async()=>({items:[]}),openTag=()=>{} }) {
    this.format = new Compartment(); this.access = new Compartment();this.language=new Compartment();this.theme=new Compartment();this.themeID=theme; this.assets = new Map();
    this.follow = follow; this.assetLoader = asset;this.icon=icon;this.notice=notice;this.rename=rename;this.suggest=suggest;this.openTag=openTag;this.editable=editable;this.id=Math.random().toString(36).slice(2);
    Object.assign(this,{releaseAsset,attachmentLinked,attach,existingAttachment,external,form});this.assetEvents=new EventTarget();this.imports=new Map();
    this.view = new EditorView({ state: EditorState.create({ doc: text, extensions: [
      EditorView.cspNonce.of(__LOOM_EDITOR_NONCE__),
      EditorState.lineSeparator.of(text.includes('\r\n')?'\r\n':'\n'),
      history(), drawSelection(), highlightSpecialChars(),search({top:true,createPanel:view=>editorSearchPanel(view,icon)}),
      keymap.of([{ key: 'Mod-s', run: () => { onSave(); return true; } }, ...searchKeymap,...historyKeymap, indentWithTab, ...defaultKeymap]),
      this.format.of(this.formatFor(path, sourceMode)), this.access.of(this.accessFor(editable)),
      this.language.of([]),this.theme.of([]),
      EditorView.updateListener.of(update => {if(update.docChanged){for(const anchor of this.imports.values()){anchor.from=update.changes.mapPos(anchor.from,1);anchor.to=update.changes.mapPos(anchor.to,1);}if(!this.externalChange)onChange(update.state.sliceDoc());} }),
      EditorView.domEventHandlers({paste:event=>{
        if(!this.canAttach())return false;
        const files=Array.from(event.clipboardData?.files||[]),image=Array.from(event.clipboardData?.items||[]).some(v=>v.type.startsWith('image/'));
        if(!files.length&&!image)return false;event.preventDefault();event.stopPropagation();void this.importAttachment(image?'clipboard':'drop',image?undefined:files);return true;
      },dragover:event=>{if(this.canAttach()&&([...event.dataTransfer.types].includes('Files')||[...event.dataTransfer.types].includes('application/x-loom-entry'))){event.preventDefault();return true;}return false;},drop:event=>{
        if(!this.canAttach())return false;const internal=event.dataTransfer.getData('application/x-loom-entry'),files=Array.from(event.dataTransfer.files);
        if(!internal&&!files.length)return false;event.preventDefault();event.stopPropagation();
        const position=this.view.posAtCoords({x:event.clientX,y:event.clientY});if(position!==null)this.view.dispatch({selection:{anchor:position}});
        if(internal){try{const entry=JSON.parse(internal);void this.importAttachment('existing',entry);}catch(error){this.notice(error.message);}}else void this.importAttachment('drop',files);return true;
      },click: event => {
        const tag=event.target.closest?.('[data-note-tag]');if(tag&&this.view.state.selection.main.empty){event.preventDefault();this.openTag(tag.dataset.noteTag);return true;}
        const link = event.target.closest?.('[data-note-reference]');
        if (link && (event.metaKey||event.ctrlKey||this.view.state.selection.main.empty)) { event.preventDefault(); this.follow(link.dataset.noteReference,navigationIntent(event)); return true; }
        return false;
      },mousedown:event=>{if((event.metaKey||event.ctrlKey||event.button===1)&&event.target.closest?.('[data-note-reference]')){event.preventDefault();return true;}return false;},auxclick:event=>{const link=event.target.closest?.('[data-note-reference]');if(event.button===1&&link){event.preventDefault();this.follow(link.dataset.noteReference,navigationIntent(event));return true;}return false;} }),
    ] }) });
    this.dom = this.view.dom; this.configure(path, editable, sourceMode);
    if (selection) {
      const end = this.view.state.doc.length,metadata=isMarkdown(path)&&!sourceMode?frontmatter(text):null,start=metadata&&!metadata.error?metadata.end:0,from = Math.max(start,Math.min(end, selection[0])), to = Math.max(start,Math.min(end, selection[1]));
      this.view.dispatch({ selection: EditorSelection.single(selection[3] === 'backward' ? to : from, selection[3] === 'backward' ? from : to) });
      this.view.scrollDOM.scrollTop = selection[2] || 0;
    }
  }
  asset(reference) {
    return this.assetLoader(reference);
  }
  canAttach(){return this.editable&&isMarkdown(this.path)&&!this.sourceMode&&Boolean(this.attach);}
  insertDivider(){if(!this.editable||!isMarkdown(this.path))return;const range=this.view.state.selection.main;this.view.dispatch({changes:{from:range.from,to:range.to,insert:'\n\n---\n\n'},selection:{anchor:range.from+7},userEvent:'input'});this.view.focus();}
  async importAttachment(action='pick',payload){
    if(!this.canAttach())return;
    const range=this.view.state.selection.main,key=Symbol(),anchor={from:range.to,to:range.to};this.imports.set(key,anchor);
    try{
      const items=action==='existing'?[{insert:await this.existingAttachment(payload)}]:await this.attach(action,payload);
      if(this.destroyed){if(items.length)this.notice('Attachment files preserved. Reopen the original note to insert their links.');return;}
      if(!items.length)return;
      const text=items.map(item=>item.insert).join('\n'),before=this.view.state.doc.sliceString(Math.max(0,anchor.from-1),anchor.from),after=this.view.state.doc.sliceString(anchor.to,anchor.to+1);
      const insert=(before&&before!=='\n'?'\n':'')+text+(after&&after!=='\n'?'\n':'');
      this.view.dispatch({changes:{from:anchor.from,to:anchor.to,insert},selection:{anchor:anchor.from+insert.length},userEvent:'input.paste'});
      for(const item of items)if(item.error)this.notice(`Attachment preserved; upload needs attention: ${item.error}`);
      this.refreshAttachments();
    }catch(error){this.notice(error.message);}finally{this.imports.delete(key);}
  }
  refreshAttachments(){this.assetEvents.dispatchEvent(new Event('attachments-refresh'));}
  reconcileAttachments(items){
    if(!this.editable)return;
    const changes=[];for(const item of items){if(!item.original||item.original===item.insert)continue;const text=this.view.state.doc.toString(),start=text.indexOf(item.original);if(start>=0&&text.indexOf(item.original,start+1)<0)changes.push({from:start,to:start+item.original.length,insert:item.insert});}
    if(changes.length)this.view.dispatch({changes:changes.sort((a,b)=>a.from-b.from),userEvent:'input'});
    for(const item of items)if(this.view.state.doc.toString().includes(item.insert))this.attachmentLinked(item).catch(error=>this.notice(error.message));
    this.refreshAttachments();
  }
  accessFor(editable) { return [EditorState.readOnly.of(!editable), EditorView.editable.of(Boolean(editable))]; }
  formatFor(path, sourceMode) {
    const rich = isMarkdown(path) && !sourceMode;
    const attributes = [EditorView.editorAttributes.of({ class: `live-editor ${rich ? 'markdown-editor' : 'technical-editor'}`, 'data-language': editorLanguage(path) }),
      EditorView.contentAttributes.of({ 'aria-label': rich ? 'Markdown editor' : 'Source editor' })];
    const presentation = rich ? [markdown({ extensions: [GFM],codeLanguages:languages }), EditorView.lineWrapping, propertiesExtension(this),richMarkdown(this),attachmentEmbeds(this),liveMarkdown(this),autocompletion({override:[noteCompletionSource(this)],icons:false,activateOnTypingDelay:150}),
      Prec.high(keymap.of([...markdownKeymap, { key: 'Mod-b', run: view => wrapSelection(view, '**') }, { key: 'Mod-i', run: view => wrapSelection(view, '*') }]))] : [lineNumbers(), ...(isMarkdown(path) ? [markdown({ extensions: [GFM] })] : [])];
    return [...attributes,titleExtension(this,path), ...presentation,...(!rich?[autocompletion()]:[])];
  }
  configure(path, editable, sourceMode) {
    if (this.path !== path || this.sourceMode !== sourceMode || this.editable !== editable) {
      this.path = path; this.sourceMode = sourceMode; this.editable = editable; this.assets.clear();
      const metadata=isMarkdown(path)&&!sourceMode?frontmatter(this.view.state.doc.toString()):null;
      this.view.dispatch({ effects: [this.format.reconfigure(this.formatFor(path, sourceMode)), this.access.reconfigure(this.accessFor(editable)),this.theme.reconfigure(syntaxTheme(this.themeID,isMarkdown(path)&&!sourceMode))],...(metadata?.end&&!metadata.error&&this.view.state.selection.main.head<metadata.end?{selection:{anchor:metadata.end}}:{}) });
      const requested=Symbol();this.languageRequest=requested;
      if(!isMarkdown(path))languageSupport(path).then(support=>{if(!this.destroyed&&this.languageRequest===requested)this.view.dispatch({effects:this.language.reconfigure(support)});}).catch(error=>this.notice(`Syntax mode unavailable: ${error.message}`));
      else this.view.dispatch({effects:this.language.reconfigure([])});
    }
  }
  replaceText(text) {
    if (this.view.state.sliceDoc() === text) return;
    this.externalChange = true;
    try { this.view.dispatch({ changes: { from: 0, to: this.view.state.doc.length, insert: text }, annotations: Transaction.addToHistory.of(false) }); }
    finally { this.externalChange = false; }
  }
  selection() {
    const range = this.view.state.selection.main;
    return [range.from, range.to, this.view.scrollDOM.scrollTop, range.anchor > range.head ? 'backward' : 'forward', this.view.scrollDOM.scrollLeft];
  }
  heading(heading) {
    const slug = value => value.trim().toLocaleLowerCase().replace(/[^\p{L}\p{N}_ -]/gu, '').replace(/\s+/g, '-');
    for (let i = 1; i <= this.view.state.doc.lines; i++) {
      const line = this.view.state.doc.line(i), match = /^#{1,6}\s+(.+?)\s*#*$/.exec(line.text);
      if (match && slug(match[1]) === slug(heading)) { this.view.dispatch({ effects: EditorView.scrollIntoView(line.from, { y: 'start' }) }); return true; }
    }
    return false;
  }
  focus() { if (this.editable) this.view.focus(); }
  find(){openSearchPanel(this.view);}
  addProperty(){this.view.scrollDOM.scrollTop=0;const panel=this.view.dom.querySelector('.note-properties');if(panel?.dataset.collapsed==='true'){panel.dataset.collapsed='false';panel.querySelector('.properties-toggle')?.setAttribute('aria-expanded','true');this.view.requestMeasure();}panel?.querySelector('.property-add')?.click();}
  setTheme(id){this.themeID=id;this.view.dispatch({effects:this.theme.reconfigure(syntaxTheme(id,isMarkdown(this.path)&&!this.sourceMode))});}
  setPreferences(prefs){for(const panel of this.dom.querySelectorAll('.note-properties')){panel.dataset.collapsed=String(prefs.propertiesCollapsed);panel.querySelector('.properties-toggle')?.setAttribute('aria-expanded',String(!prefs.propertiesCollapsed));}this.view.requestMeasure();}
  destroy() { this.destroyed=true;this.view.destroy(); }
}
