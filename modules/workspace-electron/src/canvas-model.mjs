const sides = new Set(['top', 'right', 'bottom', 'left']);
const ends = new Set(['none', 'arrow']);
const presets = {1:'#d87878', 2:'#d99a61', 3:'#c7b65b', 4:'#80b88b', 5:'#71b5cb', 6:'#b194cc'};

export function canvasData(text) {
  const data = JSON.parse(text);
  if (!data || typeof data !== 'object' || Array.isArray(data) ||
      data.nodes !== undefined && !Array.isArray(data.nodes) ||
      data.edges !== undefined && !Array.isArray(data.edges) ||
      (data.nodes?.length || 0) > 2000 || (data.edges?.length || 0) > 4000)
    throw Error('Invalid or oversized Canvas. Source is preserved.');
  const ids = new Set();
  for (const n of data.nodes || []) {
    if (!n || typeof n.id !== 'string' || !n.id || ids.has(n.id) ||
        !['text', 'file', 'link', 'group'].includes(n.type) ||
        ![n.x, n.y, n.width, n.height].every(Number.isFinite) || n.width <= 0 || n.height <= 0 ||
        [n.x, n.y, n.width, n.height].some(v => Math.abs(v) > 1000000) ||
        n.type === 'text' && typeof n.text !== 'string' ||
        n.type === 'file' && typeof n.file !== 'string' ||
        n.type === 'link' && typeof n.url !== 'string' ||
        n.type === 'group' && n.label !== undefined && typeof n.label !== 'string' ||
        n.type === 'file' && n.subpath !== undefined && typeof n.subpath !== 'string')
      throw Error('Invalid Canvas node. Source is preserved.');
    ids.add(n.id);
  }
  const edges = new Set();
  for (const e of data.edges || []) {
    if (!e || typeof e.id !== 'string' || !e.id || edges.has(e.id) ||
        !ids.has(e.fromNode) || !ids.has(e.toNode) ||
        e.fromSide !== undefined && !sides.has(e.fromSide) || e.toSide !== undefined && !sides.has(e.toSide) ||
        e.fromEnd !== undefined && !ends.has(e.fromEnd) || e.toEnd !== undefined && !ends.has(e.toEnd) ||
        e.label !== undefined && typeof e.label !== 'string')
      throw Error('Invalid Canvas connection. Source is preserved.');
    edges.add(e.id);
  }
  return data;
}

export function canvasColor(value, fallback = 'var(--subtle)') {
  return Object.hasOwn(presets,value) ? presets[value] :
    typeof value === 'string' && /^#[0-9a-f]{6}$/i.test(value) ? value : fallback;
}

export function canvasBounds(nodes) {
  if (!nodes.length) return null;
  const x = Math.min(...nodes.map(n => n.x)), y = Math.min(...nodes.map(n => n.y));
  return {x, y, width:Math.max(...nodes.map(n => n.x + n.width)) - x,
    height:Math.max(...nodes.map(n => n.y + n.height)) - y};
}

export function canvasMoveIds(nodes, id) {
  const group = nodes.find(n => n.id === id);
  if (!group) throw Error('Canvas node no longer exists.');
  return new Set(nodes.filter(n => n.id === id || group.type === 'group' &&
    n.x >= group.x && n.y >= group.y && n.x + n.width <= group.x + group.width &&
    n.y + n.height <= group.y + group.height).map(n => n.id));
}

export function canvasEdgeGeometry(edge, a, b, origin = {x:0, y:0}) {
  const side = (node, target) => Math.abs(target.x + target.width/2 - node.x - node.width/2) >=
    Math.abs(target.y + target.height/2 - node.y - node.height/2)
    ? target.x + target.width/2 >= node.x + node.width/2 ? 'right' : 'left'
    : target.y + target.height/2 >= node.y + node.height/2 ? 'bottom' : 'top';
  const point = (n, s) => ({x:n.x + (s === 'left' ? 0 : s === 'right' ? n.width : n.width/2) - origin.x,
    y:n.y + (s === 'top' ? 0 : s === 'bottom' ? n.height : n.height/2) - origin.y});
  const from = point(a, edge.fromSide || side(a, b)), to = point(b, edge.toSide || side(b, a));
  return {from, to, label:{x:(from.x + to.x)/2, y:(from.y + to.y)/2}};
}
export function canvasPlacement(nodes,x,y,width=300,height=180){
  const cards=nodes.filter(n=>n.type!=='group'),points=[{x,y}];
  for(const n of cards)points.push({x:n.x+n.width+24,y},{x:n.x-width-24,y},{x,y:n.y+n.height+24},{x,y:n.y-height-24});
  points.sort((a,b)=>(a.x-x)**2+(a.y-y)**2-((b.x-x)**2+(b.y-y)**2));
  return points.find(p=>cards.every(n=>p.x+width+16<=n.x||p.x>=n.x+n.width+16||p.y+height+16<=n.y||p.y>=n.y+n.height+16))||{x:Math.max(x,...cards.map(n=>n.x+n.width))+24,y};
}
export class CanvasModel {
  constructor(text) {
    this.data = canvasData(text); this.sourceText = text; this.sourceData = JSON.stringify(this.data);
    this.undoStack = []; this.redoStack = []; this.mergeKey = null;
  }
  get nodes() { return this.data.nodes || []; }
  change(fn, {mergeKey = null} = {}) {
    const before = JSON.stringify(this.data), next = structuredClone(this.data);
    fn(next); canvasData(JSON.stringify(next));
    if (before === JSON.stringify(next)) return false;
    this.data = next;
    if (!mergeKey || mergeKey !== this.mergeKey) this.undoStack.push(before);
    this.mergeKey = mergeKey; this.redoStack = [];
    while (this.undoStack.length > 50 || this.undoStack.reduce((n,s) => n+s.length,0) > 8*1024*1024) this.undoStack.shift();
    return true;
  }
  add(type,x,y,value='') {
    const id = globalThis.crypto.randomUUID();
    this.change(d => {d.nodes ||= []; d.nodes.push({id,type,x:Math.round(x),y:Math.round(y),
      width:type==='group'?500:300,height:type==='group'?350:180,
      ...type==='text'?{text:value}:type==='file'?{file:value}:type==='link'?{url:value}:{label:value}});});
    return id;
  }
  update(id,values,options) {
    return this.change(d => {const n = d.nodes?.find(n => n.id === id);
      if (!n) throw Error('Canvas node no longer exists.'); Object.assign(n,values);}, options);
  }
  move(id,x,y) {
    const node = this.nodes.find(n => n.id === id), ids = canvasMoveIds(this.nodes,id);
    const dx = Math.round(x)-node.x, dy = Math.round(y)-node.y;
    return this.change(d => {for (const n of d.nodes) if (ids.has(n.id)) {n.x += dx; n.y += dy;}});
  }
  remove(id) {
    return this.change(d => {if (!d.nodes?.some(n => n.id === id)) return;
      d.nodes = d.nodes.filter(n => n.id !== id);
      if (d.edges) d.edges = d.edges.filter(e => e.fromNode !== id && e.toNode !== id);});
  }
  updateEdge(id,values) {
    return this.change(d => {const edge = d.edges?.find(e => e.id === id);
      if (!edge) throw Error('Canvas connection no longer exists.'); Object.assign(edge,values);});
  }
  removeEdge(id) { return this.change(d => {if (d.edges) d.edges = d.edges.filter(e => e.id !== id);}); }
  connect(from,to,fromSide='right',toSide='left') {
    if (from === to) return false;
    if (!sides.has(fromSide) || !sides.has(toSide)) throw Error('Invalid Canvas connection side.');
    return this.change(d => {d.edges ||= []; if (!d.edges.some(e => e.fromNode === from && e.toNode === to &&
      e.fromSide === fromSide && e.toSide === toSide))
      d.edges.push({id:globalThis.crypto.randomUUID(),fromNode:from,toNode:to,fromSide,toSide});});
  }
  undo() {
    if (!this.undoStack.length) return false;
    this.redoStack.push(JSON.stringify(this.data)); this.data = JSON.parse(this.undoStack.pop()); this.mergeKey = null; return true;
  }
  redo() {
    if (!this.redoStack.length) return false;
    this.undoStack.push(JSON.stringify(this.data)); this.data = JSON.parse(this.redoStack.pop()); this.mergeKey = null; return true;
  }
  text() { return JSON.stringify(this.data) === this.sourceData ? this.sourceText : JSON.stringify(this.data,null,2)+'\n'; }
}
