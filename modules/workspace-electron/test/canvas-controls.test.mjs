import test from 'node:test';
import assert from 'node:assert/strict';
import {CanvasModel,canvasData,canvasMoveIds,canvasBounds,canvasEdgeGeometry,canvasColor} from '../src/canvas-model.mjs';
import {canvasPoint,canvasZoomAt,canvasFit,canvasWheelZoom} from '../src/canvas-controls.mjs';
import {CanvasEditor} from '../renderer/canvas-editor.js';

const textNode = (id,x,y,width=100,height=80) => ({id,type:'text',x,y,width,height,text:'[[Note]] #physics'});
const fixture = () => ({custom:{keep:['unrecognised']},nodes:[
  {id:'group',type:'group',x:-300,y:-200,width:600,height:500,label:'Group',custom:{keep:true}},
  textNode('inside',-250,-150),
  {id:'nested',type:'group',x:0,y:0,width:200,height:200,label:'Nested'},
  textNode('nested-child',20,20), textNode('partial',280,280), textNode('outside',900,100),
],edges:[{id:'edge',fromNode:'inside',toNode:'outside',fromSide:'top',toSide:'bottom',fromEnd:'arrow',toEnd:'none',label:'Keep',color:'5',custom:{untouched:42}}]});
const close = (actual,expected) => assert.ok(Math.abs(actual-expected)<.000001,`${actual} != ${expected}`);

test('opening optional empty JSON Canvas arrays and no-op operations preserves exact source bytes',() => {
  for (const source of ['{}','{ "edges": [], "custom": 1 }\r\n','{ "nodes": [] }\n']) {
    const model = new CanvasModel(source);
    assert.deepEqual(model.nodes,[]); assert.equal(model.text(),source);
    assert.equal(model.remove('missing'),false); assert.equal(model.removeEdge('missing'),false);
    assert.equal(model.text(),source); assert.equal(model.undoStack.length,0);
    model.add('text',0,0,'New'); assert.equal(model.nodes.length,1);
    model.undo(); assert.equal(model.text(),source);
  }
});

test('group movement moves each fully contained card once, in one reversible history step',() => {
  const source = JSON.stringify(fixture()), model = new CanvasModel(source);
  assert.deepEqual([...canvasMoveIds(model.nodes,'group')],['group','inside','nested','nested-child']);
  assert.equal(model.move('group',-600,-500),true); assert.equal(model.undoStack.length,1);
  assert.equal(model.nodes.find(n => n.id==='inside').x,-550);
  assert.equal(model.nodes.find(n => n.id==='nested-child').y,-280);
  assert.equal(model.nodes.find(n => n.id==='partial').x,280);
  assert.deepEqual(model.data.edges,fixture().edges); assert.deepEqual(model.data.custom,fixture().custom);
  assert.equal(model.undo(),true); assert.equal(model.text(),source);
  assert.equal(model.redo(),true); assert.equal(model.nodes.find(n => n.id==='nested-child').y,-280);
  const moved = model.text(); assert.equal(model.move('group',-600,-500),false); assert.equal(model.text(),moved);
  assert.throws(() => model.move('group',1000001,0),/Invalid/); assert.equal(model.text(),moved);
});

test('connections retain side and endpoint semantics, allow different side pairs, and preserve unknown JSON',() => {
  const model = new CanvasModel(JSON.stringify(fixture()));
  assert.equal(model.connect('inside','outside','left','top'),true);
  assert.equal(model.connect('inside','outside','left','top'),false);
  assert.equal(model.connect('inside','outside','bottom','right'),true);
  assert.equal(model.connect('inside','inside','top','bottom'),false);
  assert.equal(model.data.edges.length,3); assert.equal(model.data.edges[1].fromSide,'left');
  assert.throws(() => model.connect('inside','outside','wrong','top'),/side/);
  const before = model.text(); assert.throws(() => model.connect('missing','outside','top','left'),/Invalid/);
  assert.equal(model.text(),before);
  model.updateEdge('edge',{label:'Revised'}); assert.deepEqual(model.data.edges[0].custom,{untouched:42});
  assert.equal(model.data.edges[0].fromEnd,'arrow'); assert.equal(model.data.edges[0].toEnd,'none');
  model.removeEdge('edge'); assert.equal(model.nodes.length,fixture().nodes.length);
  model.undo(); assert.equal(model.data.edges[0].label,'Revised');
  for (const bad of [{fromSide:'diagonal'},{toEnd:'both'},{id:''},{label:4}]) {
    const data = fixture(); Object.assign(data.edges[0],bad); assert.throws(() => canvasData(JSON.stringify(data)),/Invalid/);
  }
});

test('edge geometry attaches to actual saved sides and colors distinguish JSON Canvas presets',() => {
  const a = textNode('a',0,0,200,100), b = textNode('b',400,300,100,60);
  const expected = {top:{x:100,y:0},right:{x:200,y:50},bottom:{x:100,y:100},left:{x:0,y:50}};
  for (const side of Object.keys(expected)) assert.deepEqual(canvasEdgeGeometry({fromSide:side,toSide:'left'},a,b).from,expected[side]);
  const edge = canvasEdgeGeometry({fromSide:'bottom',toSide:'top'},a,b,{x:-10,y:-20});
  assert.deepEqual(edge.from,{x:110,y:120}); assert.deepEqual(edge.to,{x:460,y:320}); assert.deepEqual(edge.label,{x:285,y:220});
  assert.deepEqual(canvasEdgeGeometry({},a,b).from,{x:200,y:50});
  assert.equal(new Set(['1','2','3','4','5','6'].map(value => canvasColor(value))).size,6);
  assert.equal(canvasColor('#AAbB09'),'#AAbB09'); assert.equal(canvasColor('url(unsafe)'),'var(--subtle)');
  assert.equal(canvasColor('constructor'),'var(--subtle)');
});

test('text editing coalesces one editing session and complete undo restores original formatting',() => {
  const source = ' { "nodes": [ '+JSON.stringify(textNode('a',0,0))+' ], "unknown": 9 }\r\n';
  const model = new CanvasModel(source), key = Symbol();
  for (const text of ['a','ab','abc [[Target]] #tag']) model.update('a',{text},{mergeKey:key});
  assert.equal(model.undoStack.length,1); model.undo(); assert.equal(model.text(),source);
  model.redo(); assert.equal(model.nodes[0].text,'abc [[Target]] #tag');
  model.update('a',{text:'Next session'},{mergeKey:Symbol()}); assert.equal(model.undoStack.length,2);
  model.undo(); assert.equal(model.nodes[0].text,'abc [[Target]] #tag');
  assert.equal(model.data.unknown,9);
});

test('pointer-anchored zoom keeps the exact world point fixed across zoom and wheel modes',() => {
  const camera = {x:80,y:-20,zoom:.5}, anchor = {x:412,y:225}, before = canvasPoint(camera,anchor.x,anchor.y);
  for (const next of [.001,.2,1,3,99]) {
    const zoomed = canvasZoomAt(camera,next,anchor.x,anchor.y), after = canvasPoint(zoomed,anchor.x,anchor.y);
    close(after.x,before.x); close(after.y,before.y); assert.ok(zoomed.zoom<=4);
  }
  const wheel = canvasWheelZoom(camera,{...anchor,deltaY:-40,deltaMode:0},600);
  assert.ok(wheel.zoom>camera.zoom); close(canvasPoint(wheel,anchor.x,anchor.y).x,before.x);
  assert.deepEqual(canvasWheelZoom(camera,{...anchor,deltaY:1,deltaMode:1},600),canvasWheelZoom(camera,{...anchor,deltaY:16,deltaMode:0},600));
  assert.deepEqual(canvasZoomAt(camera,NaN,0,0),camera);
});

test('fit frames negative and very large coordinates without changing any model data',() => {
  const model = new CanvasModel(JSON.stringify(fixture())), source = model.text();
  for (const nodes of [model.nodes,[textNode('a',-900000,-700000),textNode('b',900000,700000)]]) {
    const bounds = canvasBounds(nodes), camera = canvasFit(bounds,800,500);
    const min = {x:bounds.x*camera.zoom+camera.x,y:bounds.y*camera.zoom+camera.y};
    const max = {x:(bounds.x+bounds.width)*camera.zoom+camera.x,y:(bounds.y+bounds.height)*camera.zoom+camera.y};
    assert.ok(min.x>=47.999 && min.y>=47.999); assert.ok(max.x<=752.001 && max.y<=452.001);
  }
  assert.equal(model.text(),source); assert.deepEqual(canvasFit(null,800,500),{x:400,y:250,zoom:1});
});

function gestureEditor() {
  const editor = Object.create(CanvasEditor.prototype);
  Object.assign(editor,{model:new CanvasModel(JSON.stringify(fixture())),camera:{x:100,y:50,zoom:.5},editable:true,
    cards:new Map(fixture().nodes.map(n => [n.id,{style:{}}])),edgeRecords:[],viewport:{focus(){}},
    board:{style:{}},tools:{querySelector(){return null;}},
    saves:0,renders:0,changed(){this.saves++;},render(){this.renders++;},selection(){},notice(message){throw Error(message);}});
  return editor;
}

test('drag and resize previews do not mutate source, then flush commits only one gesture',() => {
  const editor = gestureEditor(), before = editor.model.text();
  editor.startGesture(editor.model.nodes[0],'move');
  editor.previewGesture({dx:-20,dy:10}); editor.previewGesture({dx:-30,dy:15});
  assert.equal(editor.model.text(),before); assert.equal(editor.saves,0);
  assert.equal(editor.cards.get('inside').style.left,'-350px');
  editor.flush(); assert.equal(editor.saves,1); assert.equal(editor.model.undoStack.length,1);
  assert.equal(editor.model.nodes[0].x,-400); assert.equal(editor.model.nodes[1].y,-100);
  editor.flush(); assert.equal(editor.saves,1);
  editor.startGesture(editor.model.nodes[1],'resize'); editor.previewGesture({deltaRect:{width:20,height:10}});
  assert.equal(editor.model.nodes[1].width,100); editor.flush();
  assert.equal(editor.model.nodes[1].width,140); assert.equal(editor.model.nodes[1].height,100);
  assert.equal(editor.model.undoStack.length,2); editor.model.undo(); assert.equal(editor.model.nodes[1].width,100);
  editor.startGesture(editor.model.nodes[1],'move'); editor.previewGesture({dx:99,dy:33}); editor.cancelGesture();
  assert.equal(editor.saves,2); assert.equal(editor.model.nodes[1].x,-350);
});

test('middle-button pan works over a card and changes only the camera, including after capture release',() => {
  const editor = gestureEditor(), captured = new Set(); let prevented = 0;
  editor.viewport = {focus(){},setPointerCapture:id => captured.add(id),hasPointerCapture:id => captured.has(id),releasePointerCapture:id => captured.delete(id)};
  editor.dom = {classList:{remove(){}}};
  const before = editor.model.text(), event = {button:1,pointerId:7,clientX:40,clientY:50,target:{},preventDefault(){prevented++;},stopPropagation(){}};
  editor.pointerDown(event); assert.equal(captured.has(7),true); assert.equal(prevented,1);
  editor.pointerMove({pointerId:7,clientX:90,clientY:10}); assert.deepEqual(editor.camera,{x:150,y:10,zoom:.5});
  editor.pointerUp({pointerId:7}); assert.equal(captured.size,0); assert.equal(editor.pan,null);
  editor.pointerMove({pointerId:7,clientX:900,clientY:900}); assert.equal(editor.camera.x,150);
  assert.equal(editor.model.text(),before); assert.equal(editor.saves,0);
});

test('dragging linked card content suppresses only its own trailing click',() => {
  const editor=gestureEditor();let prevented=0,stopped=0;
  const click={preventDefault(){prevented++;},stopPropagation(){stopped++;}};
  editor.cardClick(click);assert.equal(prevented,0);
  editor.startGesture(editor.model.nodes[1],'move');editor.previewGesture({dx:30,dy:20});editor.flush();
  editor.cardClick(click);assert.equal(prevented,1);assert.equal(stopped,1);
  editor.cardClick(click);assert.equal(prevented,1);
  editor.suppressCardClick=true;
  editor.pointerDown({button:0,target:{}});
  editor.cardClick(click);assert.equal(prevented,1);
  assert.equal(editor.model.undoStack.length,1);
});

test('read-only Canvas rejects mutations at public action boundaries',async() => {
  const editor = gestureEditor(), before = editor.model.text(); editor.editable = false;
  editor.addAt('text',1,2,true); await editor.add('text'); editor.selected = 'inside'; editor.removeSelected();
  editor.connect('inside','outside','top','bottom'); await editor.editEdge('edge');
  assert.equal(editor.model.text(),before); assert.equal(editor.saves,0);
});

test('double-click only creates a text card on empty space at the pointer world coordinate',() => {
  const editor = gestureEditor(); let prevented = 0;
  editor.viewport.getBoundingClientRect = () => ({left:20,top:30});
  const event = {button:0,clientX:320,clientY:280,target:editor.viewport,preventDefault(){prevented++;}};
  editor.createAtPointer(event);
  const node = editor.model.nodes.at(-1);
  assert.equal(node.type,'text'); assert.equal(node.text,''); assert.equal(node.x,400); assert.equal(node.y,400);
  assert.equal(editor.editing,node.id); assert.equal(editor.saves,1); assert.equal(prevented,1);
  for (const patch of [{target:{}},{button:1}]) editor.createAtPointer({...event,...patch});
  assert.equal(editor.saves,1);
  editor.editable = false; editor.createAtPointer(event); assert.equal(editor.saves,1);
});

test('connection drag release uses actual target side and is a single undoable mutation',() => {
  const editor = gestureEditor(), oldDocument = globalThis.document; let removed = 0;
  const port = {dataset:{node:'outside',side:'bottom'}};
  editor.viewport.hasPointerCapture = () => false;
  editor.board.contains = target => target === port; editor.dom = {classList:{remove(){}}};
  editor.connection = {pointer:8,id:'inside',side:'right',line:{remove(){removed++;}}};
  globalThis.document = {elementFromPoint:() => ({closest:selector => selector === '.canvas-port' ? port : null})};
  try {
    editor.pointerUp({pointerId:8,clientX:400,clientY:200});
    const edge = editor.model.data.edges.at(-1);
    assert.equal(edge.fromNode,'inside'); assert.equal(edge.toNode,'outside');
    assert.equal(edge.fromSide,'right'); assert.equal(edge.toSide,'bottom');
    assert.equal(editor.model.undoStack.length,1); assert.equal(editor.saves,1); assert.equal(removed,1);
    editor.model.undo(); assert.equal(editor.model.data.edges.length,1);
    editor.connection = {pointer:9,id:'inside',side:'left',line:{remove(){}}};
    globalThis.document.elementFromPoint = () => null;
    editor.pointerUp({pointerId:9,clientX:0,clientY:0});
    assert.equal(editor.saves,1); assert.equal(editor.model.data.edges.length,1);
  } finally {if (oldDocument === undefined) delete globalThis.document; else globalThis.document = oldDocument;}
});
