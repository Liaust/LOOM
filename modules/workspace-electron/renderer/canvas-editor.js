import interact from 'interactjs';
import {Marked} from 'marked';
import DOMPurify from 'dompurify';
import {EditorState} from '@codemirror/state';
import {EditorView, Decoration, ViewPlugin, keymap} from '@codemirror/view';
import {defaultKeymap, history, historyKeymap} from '@codemirror/commands';
import {markdown} from '@codemirror/lang-markdown';
import {syntaxTree} from '@codemirror/language';
import {autocompletion} from '@codemirror/autocomplete';
import {CanvasModel,canvasPlacement,canvasBounds,canvasColor,canvasEdgeGeometry,canvasMoveIds} from '../src/canvas-model.mjs';
import {canvasPoint,canvasZoomAt,canvasFit,canvasWheelZoom} from '../src/canvas-controls.mjs';
import {markdownPresentation} from './markdown-presentation.js';
import {noteCompletionSource} from './note-completions.js';
import {navigationIntent,bindNavigation} from './navigation-intent.js';

const svgNS = 'http://www.w3.org/2000/svg';
const el = (tag,className,text) => {
  const n = document.createElement(tag); n.className = className || '';
  if (text !== undefined) n.textContent = text;
  return n;
};
const svg = (tag,attrs={}) => {
  const n = document.createElementNS(svgNS,tag);
  for (const [key,value] of Object.entries(attrs)) n.setAttribute(key,String(value));
  return n;
};
const isInput = target => Boolean(target.closest?.('input,textarea,select,[contenteditable="true"],.cm-editor'));
const escapeHTML = value => String(value).replace(/[&<>"']/g,c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const cardMarkdown = new Marked({extensions:[{
  name:'canvasWiki',level:'inline',start:src => src.indexOf('[['),
  tokenizer(src) {
    const match = /^!?\[\[([^\]\n]+)\]\]/.exec(src);
    if (match) return {type:'canvasWiki',raw:match[0],reference:match[1].split('|')[0],
      label:match[1].includes('|') ? match[1].slice(match[1].indexOf('|')+1) : match[1]};
  },
  renderer:token => `<a href="#" data-canvas-reference="${escapeHTML(token.reference)}">${escapeHTML(token.label)}</a>`,
}]});

function textPreview(text) {
  const body = el('div','canvas-card-text canvas-card-preview');
  body.innerHTML = DOMPurify.sanitize(cardMarkdown.parse(text),{FORBID_TAGS:['img','iframe','video','audio','form','input','style']});
  const walk = document.createTreeWalker(body,NodeFilter.SHOW_TEXT), texts = [];
  while (walk.nextNode()) if (!walk.currentNode.parentElement.closest('a,code,pre')) texts.push(walk.currentNode);
  for (const node of texts) {
    const matches = [...node.textContent.matchAll(/(^|[\s[(])#([\p{L}\p{N}_][\p{L}\p{N}_/-]*)/gu)];
    if (!matches.length) continue;
    const fragment = document.createDocumentFragment(); let from = 0;
    for (const match of matches) {
      const at = match.index+match[1].length;
      fragment.append(document.createTextNode(node.textContent.slice(from,at)));
      const tag = el('a','lp-tag',`#${match[2]}`); tag.href = '#'; tag.dataset.canvasTag = match[2]; fragment.append(tag);
      from = at+match[2].length+1;
    }
    fragment.append(document.createTextNode(node.textContent.slice(from))); node.replaceWith(fragment);
  }
  return body;
}

function textLinks() {
  return ViewPlugin.fromClass(class {
    constructor(view) { this.decorations = this.build(view); }
    update(update) {
      if (update.docChanged || update.selectionSet || update.viewportChanged || update.focusChanged)
        this.decorations = this.build(update.view);
    }
    build(view) {
      const marks = [];
      for (const range of view.visibleRanges) for (const span of markdownPresentation(view.state,syntaxTree(view.state),range.from,range.to,view.hasFocus)) {
        if (span.kind !== 'mark' || !span.reference && !span.tag) continue;
        marks.push(Decoration.mark({class:span.className,attributes:span.reference ?
          {'data-canvas-reference':span.reference} : {'data-canvas-tag':span.tag}}).range(span.from,span.to));
      }
      return Decoration.set(marks,true);
    }
  },{decorations:value => value.decorations});
}

export class CanvasEditor {
  constructor({text,editable,onChange,icon,follow,form,notice=()=>{},suggest=async()=>({items:[]}),openTag=()=>{}}) {
    Object.assign(this,{editable,onChange,icon,follow,form,notice,suggest,openTag});
    this.model = new CanvasModel(text); this.text = text; this.camera = {x:400,y:300,zoom:1};
    this.selected = null; this.selectedEdge = null; this.interactions = []; this.cardEditors = [];
    this.markerID = `canvas-${globalThis.crypto.randomUUID()}`;
    this.dom = el('div','canvas-editor'); this.tools = el('div','view-controls');
    this.viewport = el('div','canvas-edit-viewport'); this.viewport.tabIndex = 0;
    this.viewport.setAttribute('aria-label','Canvas');
    Object.assign(this.viewport.style,{position:'relative',overflow:'hidden',touchAction:'none'});
    this.board = el('div','canvas-edit-board');
    Object.assign(this.board.style,{position:'absolute',left:'0',top:'0',width:'1px',height:'1px',transformOrigin:'0 0'});
    this.viewport.append(this.board); this.dom.append(this.tools,this.viewport);
    this.dom.onkeydown = event => this.keydown(event);
    this.dom.onkeyup = event => {if (event.code === 'Space') this.spaceDown = false;};
    this.dom.onfocusout = event => {if (!this.dom.contains(event.relatedTarget)) this.spaceDown = false;};
    this.viewport.addEventListener('pointerdown',event => this.pointerDown(event),true);
    this.viewport.addEventListener('click',event => this.cardClick(event),true);
    this.viewport.onpointermove = event => this.pointerMove(event);
    this.viewport.onpointerup = event => this.pointerUp(event);
    this.viewport.onpointercancel = () => this.cancelPointer();
    this.viewport.onlostpointercapture = () => this.cancelPointer();
    this.viewport.ondblclick = event => this.createAtPointer(event);
    this.viewport.onauxclick = event => {if (event.button === 1) event.preventDefault();};
    this.viewport.addEventListener('wheel',event => {
      event.preventDefault();
      if (this.gesture || this.connection) return;
      const p = this.localPoint(event);
      this.camera = canvasWheelZoom(this.camera,{x:p.x,y:p.y,deltaY:event.deltaY,deltaMode:event.deltaMode},this.viewport.clientHeight);
      this.applyCamera();
    },{passive:false});
    this.render();
  }
  get zoom() { return this.camera.zoom; }
  localPoint(event) {const rect = this.viewport.getBoundingClientRect(); return {x:event.clientX-rect.left,y:event.clientY-rect.top};}
  point(event) {const p = this.localPoint(event); return canvasPoint(this.camera,p.x,p.y);}
  background(target) {return target === this.viewport || target === this.board || target === this.edges;}
  createAtPointer(event) {
    if (event.button !== 0 || !this.editable || !this.background(event.target)) return;
    event.preventDefault(); const point = this.point(event);
    this.addAt('text',point.x,point.y,true);
  }
  button(label,symbol,run,disabled=false) {
    const b = el('button','icon'); b.type = 'button'; b.append(this.icon(symbol));
    b.setAttribute('aria-label',label); b.dataset.tooltip = label; b.disabled = disabled;
    b.onclick = () => Promise.resolve().then(run).catch(error => this.notice(error.message)); return b;
  }
  changed() {this.text = this.model.text(); this.onChange(this.text); this.controls();}
  controls() {
    for (const [name,disabled] of [['Undo',!this.model.undoStack.length],['Redo',!this.model.redoStack.length],
      ['Delete selected card or connection',!this.selected && !this.selectedEdge],['Connect selected card',!this.selected],
      ['Edit connection label',!this.selectedEdge]]) {
      const button = this.tools.querySelector(`[aria-label="${name}"]`);
      if (button) button.disabled = !this.editable || disabled;
    }
    const connect = this.tools.querySelector('[aria-label="Connect selected card"]');
    if (connect) {connect.setAttribute('aria-pressed',String(Boolean(this.from))); connect.dataset.tooltip = this.from ? 'Choose another side · Escape to cancel' : 'Connect selected card';}
  }
  keydown(event) {
    if (event.key === 'Escape') {
      if (this.connection || this.pan || this.gesture) {event.preventDefault(); this.cancelPointer(); this.cancelGesture(); return;}
      if (this.from || this.editing) {event.preventDefault(); this.from = null; this.editing = null; this.render(); this.viewport.focus(); return;}
      this.selected = null; this.selectedEdge = null; this.selection(); return;
    }
    if (isInput(event.target)) return;
    if (event.code === 'Space' && !event.target.closest('button')) {event.preventDefault(); this.spaceDown = true; return;}
    if (event.shiftKey && event.key === '1') {event.preventDefault(); this.fit(); return;}
    if (!this.editable) return;
    if ((event.metaKey || event.ctrlKey) && ['z','y'].includes(event.key.toLowerCase())) {
      event.preventDefault(); this.flush();
      if (event.shiftKey || event.key.toLowerCase() === 'y' ? this.model.redo() : this.model.undo()) {
        this.changed(); this.render(); this.viewport.focus();
      }
    } else if ((event.key === 'Delete' || event.key === 'Backspace') && (this.selected || this.selectedEdge)) {
      event.preventDefault(); this.removeSelected();
    }
  }
  selection() {
    for (const card of this.board.querySelectorAll('.canvas-edit-card')) {
      card.classList.toggle('selected',card.dataset.node === this.selected);
      card.style.borderColor = card.dataset.node === this.selected ? 'var(--accent)' : card.style.getPropertyValue('--canvas-card-color');
      for (const port of card.querySelectorAll('.canvas-port')) port.tabIndex = card.dataset.node === this.selected ? 0 : -1;
    }
    for (const record of this.edgeRecords || []) {
      record.line.classList.toggle('selected',record.edge.id === this.selectedEdge);
      record.line.style.strokeWidth = record.edge.id === this.selectedEdge ? '3px' : '2px';
    }
    this.controls();
  }
  applyCamera() {
    this.board.style.transform = `translate(${this.camera.x}px,${this.camera.y}px) scale(${this.zoom})`;
    const output = this.tools.querySelector('.canvas-zoom');
    if (output) output.textContent = `${Number((this.zoom*100).toFixed(this.zoom < .1 ? 2 : 0))}%`;
  }
  setZoom(zoom) {
    if (this.gesture || this.connection) return;
    this.camera = canvasZoomAt(this.camera,zoom,this.viewport.clientWidth/2,this.viewport.clientHeight/2); this.applyCamera();
  }
  fit() {
    if (!this.viewport.clientWidth || !this.viewport.clientHeight) return;
    this.flush(); this.camera = canvasFit(canvasBounds(this.model.nodes),this.viewport.clientWidth,this.viewport.clientHeight); this.applyCamera();
  }
  mount() {
    if (this.suspended) {this.suspended = false; this.render();}
    if (!this.positioned) {this.positioned = true; requestAnimationFrame(() => {if (this.dom.isConnected) this.fit();});}
  }
  destroy() {
    this.flush(); this.cancelPointer(); this.spaceDown = false;
    for (const instance of this.interactions) instance.unset(); this.interactions = [];
    for (const editor of this.cardEditors) editor.destroy(); this.cardEditors = [];
    this.suspended = true;
  }
  replaceText(text) {
    if (text === this.text) return;
    const model = new CanvasModel(text);
    this.cancelPointer(); this.gesture = null; this.editing = null;
    this.model = model; this.text = text; this.render();
  }
  removeSelected() {
    if (!this.editable) return;
    this.flush();
    const changed = this.selected ? this.model.remove(this.selected) : this.selectedEdge ? this.model.removeEdge(this.selectedEdge) : false;
    this.selected = null; this.selectedEdge = null; this.from = null; this.editing = null;
    if (changed) this.changed(); this.render(); this.viewport.focus();
  }
  endTextEditing() {
    const id = this.editing; if (!id) return;
    this.editing = null;
    const editor = this.cardEditors.find(view => view.canvasNodeID === id);
    if (editor) {editor.destroy(); this.cardEditors = this.cardEditors.filter(view => view !== editor);}
    const card = this.cards.get(id), node = this.model.nodes.find(n => n.id === id);
    card?.querySelector('.canvas-card-editing')?.remove();
    if (card && node) this.textCard(node,card);
  }
  async add(type) {
    if (!this.editable) return;
    let value = '';
    if (type !== 'text') {
      const field = type === 'file' ? 'File path' : type === 'link' ? 'URL' : 'Group name';
      const result = await this.form(`New ${type} card`,[{name:'value',label:field}],{submit:'Add'});
      if (!result) return; value = result.value;
      if (type === 'link' && !/^https?:\/\//i.test(value)) throw Error('Use an HTTP or HTTPS link.');
    }
    const point = canvasPoint(this.camera,this.viewport.clientWidth/2,this.viewport.clientHeight/2);
    const placed = canvasPlacement(this.model.nodes,point.x-150,point.y-90,type==='group'?500:300,type==='group'?350:180);
    this.addAt(type,placed.x,placed.y,type==='text',value);
  }
  addAt(type,x,y,edit=false,value='') {
    if (!this.editable) return;
    this.flush(); this.selected = this.model.add(type,x,y,value); this.selectedEdge = null;
    this.editing = edit ? this.selected : null; this.changed(); this.render();
  }
  pointerDown(event) {
    this.suppressCardClick = false;
    if (event.button === 1 || event.button === 0 && (this.spaceDown && !isInput(event.target) || this.background(event.target))) {
      event.preventDefault(); event.stopPropagation(); this.flush();
      this.endTextEditing();
      if (event.button === 0 && !this.spaceDown) {this.selected = null; this.selectedEdge = null; this.selection();}
      this.pan = {pointer:event.pointerId,x:event.clientX,y:event.clientY,camera:{...this.camera}};
      this.viewport.setPointerCapture(event.pointerId); this.viewport.focus({preventScroll:true});
    }
  }
  cardClick(event) {
    if (!this.suppressCardClick) return;
    this.suppressCardClick = false;
    event.preventDefault(); event.stopPropagation();
  }
  pointerMove(event) {
    if (this.pan?.pointer === event.pointerId) {
      this.camera = {...this.pan.camera,x:this.pan.camera.x+event.clientX-this.pan.x,y:this.pan.camera.y+event.clientY-this.pan.y}; this.applyCamera();
    } else if (this.connection?.pointer === event.pointerId) {
      const point = this.point(event), from = this.connection.point;
      for (const [key,value] of Object.entries({x1:from.x,y1:from.y,x2:point.x,y2:point.y})) this.connection.line.setAttribute(key,value);
    }
  }
  pointerUp(event) {
    if (this.connection?.pointer === event.pointerId) {
      const connection = this.connection;
      const port = document.elementFromPoint(event.clientX,event.clientY)?.closest('.canvas-port');
      this.cancelPointer();
      if (port && this.board.contains(port)) this.connect(connection.id,port.dataset.node,connection.side,port.dataset.side);
    } else if (this.pan?.pointer === event.pointerId) this.cancelPointer();
  }
  cancelPointer() {
    const pointer = this.pan?.pointer ?? this.connection?.pointer;
    this.pan = null; this.connection?.line.remove(); this.connection = null;
    this.dom.classList.remove('canvas-connecting');
    if (pointer !== undefined && this.viewport.hasPointerCapture(pointer)) this.viewport.releasePointerCapture(pointer);
  }
  connect(from,to,fromSide,toSide) {
    if (!this.editable) return;
    if (this.model.connect(from,to,fromSide,toSide)) this.changed();
    this.from = null; this.render();
  }
  port(node,side) {
    const button = el('button',`canvas-port canvas-port-${side}`); button.type = 'button';
    button.dataset.node = node.id; button.dataset.side = side;
    button.setAttribute('aria-label',`Connect ${node.type === 'group' ? node.label || 'group' : 'card'} ${side} side`);
    button.dataset.tooltip = `Connect ${side} side`; button.tabIndex = this.selected === node.id ? 0 : -1;
    button.onpointerdown = event => {
      if (event.button !== 0 || !this.editable || this.pan) return;
      event.preventDefault(); event.stopPropagation(); this.flush(); this.selected = node.id; this.selectedEdge = null; this.selection();
      const point = canvasEdgeGeometry({fromSide:side,toSide:side},node,node).from;
      const line = svg('line',{x1:point.x,y1:point.y,x2:point.x,y2:point.y});
      line.classList.add('canvas-edge-preview'); line.style.pointerEvents = 'none'; line.style.stroke = 'var(--accent)';
      line.setAttribute('vector-effect','non-scaling-stroke'); this.edges.append(line);
      this.connection = {pointer:event.pointerId,id:node.id,side,point,line}; this.dom.classList.add('canvas-connecting');
      this.viewport.setPointerCapture(event.pointerId); this.viewport.focus({preventScroll:true});
    };
    button.onclick = event => {
      event.stopPropagation(); if (event.detail !== 0 || !this.editable) return;
      if (this.from && this.from.id !== node.id) this.connect(this.from.id,node.id,this.from.side,side);
      else {this.from = {id:node.id,side}; this.controls();}
    };
    return button;
  }
  async editEdge(id) {
    if (!this.editable) return;
    const edge = this.model.data.edges?.find(e => e.id === id); if (!edge) return;
    const model = this.model, oldLabel = edge.label;
    const result = await this.form('Connection label',[{name:'label',label:'Label',value:oldLabel || '',required:false}],{submit:'Save'});
    if (!result) return;
    const current = this.model.data.edges?.find(e => e.id === id);
    if (this.model !== model || !current || current.label !== oldLabel) {
      this.notice('The connection changed while its label was open. Please reopen it.'); return;
    }
    if (this.model.updateEdge(id,{label:result.label})) {this.changed(); this.render();}
  }
  edgePosition(record,index) {
    const geometry = canvasEdgeGeometry(record.edge,index.get(record.edge.fromNode),index.get(record.edge.toNode));
    for (const [key,value] of Object.entries({x1:geometry.from.x,y1:geometry.from.y,x2:geometry.to.x,y2:geometry.to.y})) record.line.setAttribute(key,value);
    if (record.label) {record.label.setAttribute('x',geometry.label.x); record.label.setAttribute('y',geometry.label.y-8);}
  }
  renderEdges() {
    this.edges = svg('svg',{width:1,height:1}); this.edges.classList.add('canvas-edit-edges');
    this.edges.style.overflow = 'visible'; this.board.append(this.edges); this.edgeRecords = [];
    const defs = svg('defs'); this.edges.append(defs);
    const index = new Map(this.model.nodes.map(n => [n.id,n])), markers = new Map();
    for (const edge of this.model.data.edges || []) {
      const color = canvasColor(edge.color), line = svg('line',{'vector-effect':'non-scaling-stroke'});
      line.style.stroke = color; line.style.pointerEvents = 'stroke';
      let marker = markers.get(color);
      if (!marker) {
        marker = `${this.markerID}-${markers.size}`; markers.set(color,marker);
        const arrow = svg('marker',{id:marker,viewBox:'0 0 8 8',refX:8,refY:4,markerWidth:8,markerHeight:8,orient:'auto-start-reverse'});
        const path = svg('path',{d:'M 0 0 L 8 4 L 0 8 z'}); path.style.fill = color; arrow.append(path); defs.append(arrow);
      }
      if (edge.fromEnd === 'arrow') line.setAttribute('marker-start',`url(#${marker})`);
      if ((edge.toEnd || 'arrow') === 'arrow') line.setAttribute('marker-end',`url(#${marker})`);
      const select = event => {event.stopPropagation(); this.endTextEditing(); this.selected = null; this.selectedEdge = edge.id; this.selection(); this.viewport.focus({preventScroll:true});};
      line.onclick = select;
      line.ondblclick = event => {event.stopPropagation(); this.editEdge(edge.id).catch(error => this.notice(error.message));};
      const record = {edge,line}; this.edges.append(line);
      if (edge.label) {
        const label = svg('text',{'text-anchor':'middle'}); label.textContent = edge.label;
        label.style.pointerEvents = 'all'; label.onclick = select; label.ondblclick = line.ondblclick;
        record.label = label; this.edges.append(label);
      }
      this.edgeRecords.push(record); this.edgePosition(record,index);
    }
  }
  startGesture(node,kind) {
    this.flush(); this.endTextEditing(); this.selected = node.id; this.selectedEdge = null; this.selection();
    this.suppressCardClick = true;
    const current = this.model.nodes.find(n => n.id === node.id);
    const moved = kind === 'move' ? canvasMoveIds(this.model.nodes,node.id) : new Set([node.id]);
    this.gesture = {id:node.id,kind,x:current.x,y:current.y,width:current.width,height:current.height,
      before:new Map(this.model.nodes.filter(n => moved.has(n.id)).map(n => [n.id,{...n}]))};
    this.viewport.focus({preventScroll:true});
  }
  previewGesture(event) {
    const g = this.gesture; if (!g) return;
    const initial = g.before.get(g.id);
    if (g.kind === 'move') {g.x += event.dx/this.zoom; g.y += event.dy/this.zoom;}
    else {
      g.width = Math.max(Math.min(120,initial.width),g.width+event.deltaRect.width/this.zoom);
      g.height = Math.max(Math.min(80,initial.height),g.height+event.deltaRect.height/this.zoom);
    }
    const index = new Map(this.model.nodes.map(n => [n.id,n]));
    for (const [id,n] of g.before) {
      const next = g.kind === 'move' ? {...n,x:n.x+g.x-initial.x,y:n.y+g.y-initial.y} : {...n,width:g.width,height:g.height};
      index.set(id,next); const card = this.cards.get(id);
      Object.assign(card.style,{left:`${next.x}px`,top:`${next.y}px`,width:`${next.width}px`,height:`${next.height}px`});
    }
    for (const record of this.edgeRecords) if (g.before.has(record.edge.fromNode) || g.before.has(record.edge.toNode)) this.edgePosition(record,index);
  }
  flush() {
    const g = this.gesture; this.gesture = null; if (!g || !this.editable) return;
    try {
      const changed = g.kind === 'move' ? this.model.move(g.id,g.x,g.y) : this.model.update(g.id,{width:Math.round(g.width),height:Math.round(g.height)});
      if (changed) this.changed(); this.render();
    } catch (error) {this.render(); this.notice(error.message);}
  }
  cancelGesture() {if (this.gesture) {this.gesture = null; this.render();}}
  textCard(node,card) {
    if (this.editing === node.id && this.editable) {
      const mergeKey = Symbol('Canvas text edit'), owner = this;
      const extensions = [EditorState.lineSeparator.of(node.text.includes('\r\n')?'\r\n':'\n'),markdown(),EditorView.lineWrapping,history(),keymap.of([...historyKeymap,...defaultKeymap]),
        autocompletion({override:[noteCompletionSource(this)],icons:false,activateOnTypingDelay:150}),textLinks(),
        EditorView.contentAttributes.of({'aria-label':'Canvas card text'}),
        EditorView.editorAttributes.of({class:'canvas-card-editor'}),
        EditorView.theme({'&':{height:'100%',backgroundColor:'transparent'},'.cm-scroller':{overflow:'auto'},'.cm-content':{padding:'8px 14px'},'&.cm-focused':{outline:'none'}}),
        EditorView.updateListener.of(update => {
          if (update.docChanged && this.model.update(node.id,{text:update.state.sliceDoc()},{mergeKey})) this.changed();
        }),
        EditorView.domEventHandlers({click(event,view) {
          if (!view.state.selection.main.empty&&!event.metaKey&&!event.ctrlKey) return false;
          const tag = event.target.closest('[data-canvas-tag]'), link = event.target.closest('[data-canvas-reference]');
          if (tag || link) {event.preventDefault(); owner.activate(tag ? () => owner.openTag(tag.dataset.canvasTag) : () => owner.follow(link.dataset.canvasReference,navigationIntent(event))); return true;}
          return false;
        }}),
      ];
      if (typeof __LOOM_EDITOR_NONCE__ !== 'undefined') extensions.push(EditorView.cspNonce.of(__LOOM_EDITOR_NONCE__));
      const view = new EditorView({state:EditorState.create({doc:node.text,extensions})});
      view.canvasNodeID = node.id;
      this.cardEditors.push(view); const body = el('div','canvas-card-text canvas-card-editing'); body.append(view.dom); card.append(body);
      requestAnimationFrame(() => {if (view.dom.isConnected && this.editing === node.id) view.focus();});
    } else {
      const body = textPreview(node.text); body.setAttribute('aria-label','Canvas card text');
      body.ondblclick = event => {
        if (!this.editable || event.target.closest('a')) return;
        event.stopPropagation(); this.editing = node.id; this.selected = node.id; this.render();
      };
      body.onclick = event => {
        const tag = event.target.closest('[data-canvas-tag]'), link = event.target.closest('a');
        if (tag || link) {
          event.preventDefault(); event.stopPropagation();
          this.activate(tag ? () => this.openTag(tag.dataset.canvasTag) : () => this.follow(link.dataset.canvasReference || link.getAttribute('href'),navigationIntent(event)));
        }
      };
      body.onauxclick=event=>{if(event.button===1)body.onclick(event);};
      card.append(body);
    }
  }
  activate(run) {Promise.resolve().then(run).catch(error => this.notice(error.message));}
  render() {
    for (const instance of this.interactions) instance.unset(); this.interactions = [];
    for (const editor of this.cardEditors) editor.destroy(); this.cardEditors = [];
    if (!this.model.nodes.some(n => n.id === this.selected)) this.selected = null;
    if (!this.model.nodes.some(n => n.id === this.from?.id)) this.from = null;
    if (!this.model.data.edges?.some(e => e.id === this.selectedEdge)) this.selectedEdge = null;
    this.cards = new Map(); this.board.replaceChildren();
    this.tools.replaceChildren(...[['text','square-pen'],['file','file-plus'],['link','link'],['group','group']].map(([type,symbol]) =>
      this.button(`Add ${type} card`,symbol,() => this.add(type),!this.editable)),
      this.button('Connect selected card','waypoints',() => {this.flush(); this.from = this.from ? null : {id:this.selected,side:'right'}; this.controls();},!this.editable),
      this.button('Edit connection label','pencil',() => this.editEdge(this.selectedEdge),!this.editable),
      this.button('Delete selected card or connection','trash-2',() => this.removeSelected(),!this.editable),
      this.button('Undo','undo-2',() => {this.flush(); if (this.model.undo()) {this.editing = null; this.changed(); this.render();}},!this.editable),
      this.button('Redo','redo-2',() => {this.flush(); if (this.model.redo()) {this.editing = null; this.changed(); this.render();}},!this.editable),
      this.button('Zoom out','minus',() => this.setZoom(this.zoom/1.25)),el('output','canvas-zoom'),
      this.button('Zoom in','plus',() => this.setZoom(this.zoom*1.25)),this.button('Fit Canvas','scan',() => this.fit()));
    this.renderEdges();
    for (const n of this.model.nodes) {
      const card = el('div',`canvas-edit-card canvas-edit-${n.type}`); card.dataset.node = n.id;
      card.ondragstart = event => event.preventDefault();
      Object.assign(card.style,{left:`${n.x}px`,top:`${n.y}px`,width:`${n.width}px`,height:`${n.height}px`});
      if (n.color) {const color = canvasColor(n.color,''); if (color) card.style.setProperty('--canvas-card-color',color);}
      const handle = el('div','canvas-card-handle');
      handle.append(this.icon(n.type==='text'?'grip-horizontal':n.type==='file'?'file-text':n.type==='group'?'group':'link'));
      handle.append(el('span','',n.type==='group'?n.label || 'Group':n.type==='file'?(n.file || 'File').split('/').pop():n.type==='link'?'Link':'Note'));
      card.append(handle);
      card.onpointerdown = event => {
        if (event.button !== 0 || event.target.closest('.canvas-port')) return;
        if (this.from && this.from.id !== n.id && this.editable) {
          event.preventDefault(); this.connect(this.from.id,n.id,this.from.side,'left'); return;
        }
        if (this.editing && this.editing !== n.id) this.endTextEditing();
        this.selected = n.id; this.selectedEdge = null; this.selection();
        if (!isInput(event.target) && !event.target.closest('button,a')) this.viewport.focus({preventScroll:true});
      };
      if (n.type === 'text') this.textCard(n,card);
      else if (n.type !== 'group') {
        const link = el('button','canvas-card-file',n.type==='file'?n.file:n.url); link.type = 'button';
        bindNavigation(link,intent=>this.activate(()=>this.follow(n.type==='file'?n.file+(n.subpath || ''):n.url,intent)));card.append(link);
      }
      if (this.editable) for (const side of ['top','right','bottom','left']) card.append(this.port(n,side));
      this.board.append(card); this.cards.set(n.id,card);
      if (!this.editable) continue;
      const instance = interact(card).draggable({ignoreFrom:'.canvas-port,.canvas-card-editor,input,textarea,select,[contenteditable="true"]',listeners:{
        start:() => this.startGesture(n,'move'), move:event => this.previewGesture(event), end:() => this.flush(),
      }}).resizable({edges:{right:true,bottom:true},ignoreFrom:'.canvas-port',listeners:{
        start:() => this.startGesture(n,'resize'), move:event => this.previewGesture(event), end:() => this.flush(),
      }});
      this.interactions.push(instance);
    }
    this.selection(); this.applyCamera();
    // Creation already attached the new editor. Focus before the next input
    // event, rather than losing a fast first keystroke to the viewport.
    const editing=this.cardEditors.find(view=>view.canvasNodeID===this.editing);
    if(editing?.dom.isConnected)editing.focus();
  }
}
