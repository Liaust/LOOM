import {StateField} from '@codemirror/state';
import {Decoration,EditorView,WidgetType} from '@codemirror/view';
import {syntaxTree} from '@codemirror/language';
import {attachmentReference} from '../src/attachment-reference.mjs';
import {PdfViewer} from './pdf-viewer.js';
import {navigationIntent} from './navigation-intent.js';
import {attachmentSpans} from './attachment-spans.js';
export {attachmentSpans} from './attachment-spans.js';

// Capacity bounds concurrent visible viewers, never the number of references
// allowed in a note. Off-screen resources give their slots back.
const slots={image:{max:4,active:0,waiting:[]},pdf:{max:2,active:0,waiting:[]}};
function acquire(kind,signal){return new Promise(resolve=>{
  const pool=slots[kind],entry={start:()=>{if(signal.aborted)return resolve(null);pool.active++;let done=false;resolve(()=>{if(done)return;done=true;pool.active--;while(pool.waiting.length&&pool.active<pool.max)pool.waiting.shift().start();});}};
  signal.addEventListener('abort',()=>{const index=pool.waiting.indexOf(entry);if(index>=0){pool.waiting.splice(index,1);resolve(null);}},{once:true});
  if(pool.active<pool.max)entry.start();else pool.waiting.push(entry);
});}

export function mountAttachment(reference,owner,{measure=()=>{},edit}={}){
  const dom=document.createElement('div');dom.className='note-embed';dom.contentEditable='false';
  let parsed;try{parsed=attachmentReference(reference);}catch(error){dom.textContent=error.message;return {dom,destroy(){}};}
  const header=document.createElement('div');header.className='note-embed-header';
  const open=document.createElement('button');open.type='button';open.className='note-attachment';open.textContent=parsed.label;open.dataset.tooltip='Open attachment';
  open.onclick=event=>owner.follow(parsed.target+(parsed.kind==='pdf'?`#page=${parsed.page}`:''),navigationIntent(event));
  open.onauxclick=event=>{if(event.button===1){event.preventDefault();owner.follow(parsed.target,navigationIntent(event));}};
  header.append(open);
  if(edit){const button=document.createElement('button');button.type='button';button.className='icon';button.setAttribute('aria-label','Edit attachment source');button.dataset.tooltip='Edit attachment source';button.append(owner.icon('code'));button.onclick=edit;header.append(button);}
  const body=document.createElement('div');body.className='note-embed-body';body.setAttribute('aria-label',parsed.label);dom.append(header,body);
  if(parsed.width)dom.style.width=`min(100%, ${parsed.width}px)`;
  if(parsed.kind==='file'||parsed.external){body.hidden=true;return{dom,destroy(){}};}
  const status=document.createElement('span');status.className='muted';header.append(status);
  let viewer,controller,value,release,visible=false,disposed=false;
  const reset=()=>{controller?.abort();controller=null;viewer?.destroy();viewer=null;if(value?.lease)owner.releaseAsset?.(value.lease);value=null;release?.();release=null;status.textContent='';body.replaceChildren();body.textContent='Attachment';};
  const load=async()=>{
    if(controller||disposed||!visible)return;
    const attempt=controller=new AbortController();body.textContent='Opening attachment…';dom.classList.remove('embed-error');
    try{
      const slot=await acquire(parsed.kind,attempt.signal);if(!slot)return;if(attempt.signal.aborted){slot();return;}release=slot;
      const asset=await owner.asset(reference);
      if(attempt.signal.aborted||disposed){if(asset?.lease)owner.releaseAsset?.(asset.lease);return;}
      value=asset;if(!value?.url?.startsWith('loom-file://')||value.kind!==parsed.kind)throw Error('Attachment is not available in this view.');status.textContent=value.pending?'Awaiting file acceptance':'';
      body.replaceChildren();
      if(parsed.kind==='image'){
        const image=document.createElement('img');image.className='note-image';image.alt=parsed.label;image.src=value.url;image.loading='lazy';image.decoding='async';
        if(parsed.height)image.style.maxHeight=`${parsed.height}px`;
        image.onload=()=>{if(controller!==attempt)return;measure();release?.();release=null;};image.onerror=()=>{if(controller===attempt)fail(Error('Image could not be displayed.'));};
        image.onclick=open.onclick;body.append(image);
      }else{
        body.style.height=`${Math.max(160,Math.min(1600,parsed.height||420))}px`;
        viewer=new PdfViewer({url:value.url,size:value.entry.size,name:parsed.label,page:parsed.page,icon:owner.icon,notice:owner.notice,
          external:owner.external||(()=>{}),form:owner.form||(async()=>null),compact:true,onReady:measure});
        body.append(viewer.dom);void viewer.mount();
      }
      measure();
    }catch(error){if(!attempt.signal.aborted&&!disposed)fail(error);}
  };
  const fail=error=>{reset();dom.classList.add('embed-error');body.textContent=`${error.message} `;const retry=document.createElement('button');retry.type='button';retry.className='quiet-action';retry.textContent='Retry';retry.onclick=()=>{reset();void load();};body.append(retry);measure();};
  body.textContent='Attachment';
  const observer=new IntersectionObserver(entries=>{visible=entries.some(e=>e.isIntersecting);if(visible)void load();else if(controller)reset();},{rootMargin:'240px'});observer.observe(dom);
  const resize=new ResizeObserver(measure);resize.observe(dom);
  const changed=()=>{if(visible){reset();void load();}};owner.assetEvents?.addEventListener('attachments-refresh',changed);
  return {dom,destroy(){disposed=true;observer.disconnect();resize.disconnect();owner.assetEvents?.removeEventListener('attachments-refresh',changed);reset();}};
}

class AttachmentWidget extends WidgetType{
  constructor(span,owner){super();this.span=span;this.owner=owner;}
  eq(other){return this.span.reference===other.span.reference&&this.owner===other.owner;}
  get estimatedHeight(){try{const p=attachmentReference(this.span.reference);return p.kind==='pdf'?(p.height||420)+36:260;}catch{return 40;}}
  toDOM(view){const mounted=mountAttachment(this.span.reference,this.owner,{measure:()=>view.requestMeasure(),edit:()=>{view.focus();const position=view.posAtDOM(mounted.dom);view.dispatch({selection:{anchor:Math.min(view.state.doc.length,position+1)}});}});mounted.dom._attachment=mounted;return mounted.dom;}
  destroy(dom){dom._attachment?.destroy();}
  ignoreEvent(event){return !['dragover','drop'].includes(event.type);}
}
export function attachmentEmbeds(owner){
  const build=state=>Decoration.set(attachmentSpans(state).filter(span=>!state.selection.ranges.some(range=>range.from<=span.to&&range.to>=span.from)).map(span=>Decoration.replace({block:true,widget:new AttachmentWidget(span,owner)}).range(span.from,span.to)),true);
  return StateField.define({create:build,update:(old,tr)=>tr.docChanged||tr.selection||tr.reconfigured||syntaxTree(tr.startState)!==syntaxTree(tr.state)?build(tr.state):old,provide:field=>EditorView.decorations.from(field)});
}
