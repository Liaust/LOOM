import { selectControl } from './choice-controls.js';
import {bindNavigation} from './navigation-intent.js';
import {mountAttachment} from './attachment-embeds.js';

const element = (tag, className, text) => {
  const value = document.createElement(tag); if (className) value.className = className;
  if (text !== undefined) value.textContent = text; return value;
};
const escape = value => String(value).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const slug = value => value.trim().toLocaleLowerCase().replace(/[^\p{L}\p{N}_ -]/gu, '').replace(/\s+/g, '-');
marked.use({ renderer:{image(token){return `<a data-note-reference="${escape(token.href)}" data-note-embed="true">${escape(token.text||token.href)}</a>`;}}, extensions: [{
  name:'wiki',level:'inline',start:src=>src.search(/!?\[\[/),
  tokenizer(src) {
    const match=/^(!?)\[\[([^\]\n]+)\]\]/.exec(src);
    if(match) return {type:'wiki',raw:match[0],embed:Boolean(match[1]),reference:match[1]?match[2]:match[2].split('|')[0],label:match[2].split('|')[1]||match[2]};
  },
  renderer(token) {return `<a data-note-reference="${escape(token.reference)}"${token.embed?' data-note-embed="true"':''}>${escape(token.label)}</a>`;},
}] });

export function markdownView(text, {follow,asset,releaseAsset,icon,notice=()=>{},external,form,active = () => true}) {
  const article=element('article','markdown');
  article.innerHTML=DOMPurify.sanitize(marked.parse(text),{FORBID_TAGS:['img','iframe','object','embed','style','form','input','svg','math'],FORBID_ATTR:['style','srcset'],ALLOWED_URI_REGEXP:/^(?:https?:|#|[^a-z]|[a-z]+(?:[^a-z:]|$))/i});
  for(const element of article.querySelectorAll('[title]')){element.dataset.tooltip=element.title;element.removeAttribute('title');}
  article.querySelectorAll('h1,h2,h3,h4,h5,h6').forEach(heading=>{heading.id=slug(heading.textContent);});
  const links=[...article.querySelectorAll('a')];
  for(const link of links) {
    const reference=link.dataset.noteReference??link.getAttribute('href');
    link.removeAttribute('target');link.setAttribute('href','#');
    link.dataset.noteReference=reference||'';bindNavigation(link,intent=>{if(reference)follow(reference,intent);});
  }
  // Only locally resolved image URLs reach the DOM. Never let raw Markdown HTML
  // fetch a remote URL, a local file or a tracking pixel directly.
  const embeds=[...article.querySelectorAll('img'),...links.filter(a=>a.dataset.noteEmbed)];
  const resources=[];
  for(const embed of embeds) {
    const reference=embed.dataset.noteReference??embed.getAttribute('src');
    const resource=mountAttachment(reference,{follow,asset:ref=>active()?asset(ref):Promise.reject(Error('Preview is inactive.')),releaseAsset,icon:icon||(()=>element('span')),notice,external,form});resources.push(resource);embed.replaceWith(resource.dom);
  }
  article.dispose=()=>resources.forEach(resource=>resource.destroy());
  return article;
}

export function scrollHeading(container, heading) {
  const target=[...container.querySelectorAll('[id]')].find(h=>h.id===slug(heading));target?.scrollIntoView({block:'start'});
}

export function canvasView(text, {follow}) {
  const data=JSON.parse(text);
  if(!data||!Array.isArray(data.nodes)||data.nodes.length>2000||data.edges&&!Array.isArray(data.edges)||data.edges?.length>4000)throw Error('Invalid or oversized Canvas. Its source is still available for editing.');
  const nodes=data.nodes.map(n=>{
    if(!n||typeof n.id!=='string'||!['text','file','link','group'].includes(n.type)||![n.x,n.y,n.width,n.height].every(Number.isFinite)||n.width<=0||n.height<=0||Math.max(...[n.x,n.y,n.width,n.height].map(Math.abs))>1000000)throw Error('Invalid Canvas node geometry.');
    return n;
  });
  if(new Set(nodes.map(n=>n.id)).size!==nodes.length)throw Error('Canvas node IDs must be unique.');
  const view=element('div','canvas-view'),controls=element('div','view-controls'),viewport=element('div','canvas-viewport'),board=element('div','canvas-board');
  const zoom=document.createElement('input');zoom.type='range';zoom.min='10';zoom.max='150';zoom.value='65';zoom.setAttribute('aria-label','Canvas zoom');
  const label=element('span');controls.append(zoom,label);viewport.append(board);view.append(controls,viewport);
  if(!nodes.length){board.append(element('p','empty','Empty Canvas'));return view;}
  const minX=Math.min(...nodes.map(n=>n.x))-32,minY=Math.min(...nodes.map(n=>n.y))-32;
  const width=Math.max(...nodes.map(n=>n.x+n.width))-minX+32,height=Math.max(...nodes.map(n=>n.y+n.height))-minY+32;
  // A tiny disconnected diagram should not allocate a million-pixel surface.
  if(width>100000||height>100000)throw Error('Canvas extent is too large for this viewer. Source is preserved.');
  board.style.width=`${width}px`;board.style.height=`${height}px`;
  const resize=()=>{const scale=Number(zoom.value)/100;board.style.zoom=String(scale);label.textContent=`${zoom.value}%`;};zoom.oninput=resize;resize();
  const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.classList.add('canvas-edges');svg.setAttribute('width',width);svg.setAttribute('height',height);svg.setAttribute('viewBox',`0 0 ${width} ${height}`);board.append(svg);
  const index=new Map(nodes.map(n=>[n.id,n]));
  for(const edge of data.edges||[]) {
    const a=index.get(edge.fromNode),b=index.get(edge.toNode);if(!a||!b)continue;
    const line=document.createElementNS(svg.namespaceURI,'line');
    for(const [key,value] of Object.entries({x1:a.x+a.width/2-minX,y1:a.y+a.height/2-minY,x2:b.x+b.width/2-minX,y2:b.y+b.height/2-minY}))line.setAttribute(key,String(value));
    svg.append(line);
    if(edge.label){const text=document.createElementNS(svg.namespaceURI,'text');text.setAttribute('x',String((a.x+b.x+a.width/2+b.width/2)/2-minX));text.setAttribute('y',String((a.y+b.y+a.height/2+b.height/2)/2-minY));text.textContent=String(edge.label);svg.append(text);}
  }
  for(const n of [...nodes.filter(n=>n.type==='group'),...nodes.filter(n=>n.type!=='group')]) {
    const card=element('div',`canvas-node canvas-${n.type}`);Object.assign(card.style,{left:`${n.x-minX}px`,top:`${n.y-minY}px`,width:`${n.width}px`,height:`${n.height}px`});
    if(n.type==='text')card.append(markdownView(String(n.text||''),{follow,asset:async()=>({}),active:()=>false}));
    else if(n.type==='group')card.append(element('strong','',n.label||''));
    else {const link=element('button','canvas-link',n.type==='file'?n.file:n.url);link.onclick=()=>follow(n.type==='file'?`${n.file}${n.subpath||''}`:n.url);card.append(link);}
    board.append(card);
  }
  return view;
}

export function baseView({query,open,active}) {
  const view=element('div','base-view');let index=0,offset=0;
  const load=async()=>{
    const result=await query({viewIndex:index,offset});if(!active())return;
    const controls=element('div','view-controls'),select=selectControl({label:'Base view',name:'view',value:index,items:result.views.map((v,i)=>({value:i,label:v.name}))});
    select.addEventListener('change',()=>{index=Number(select.querySelector('input').value);offset=0;load().catch(showError);});controls.append(select,element('span','',`${offset+1 > result.total?0:offset+1}–${offset+result.rows.length} / ${result.total}`));
    const table=element('table','base-table'),head=element('thead'),header=element('tr');result.columns.forEach(c=>header.append(element('th','',c.name)));head.append(header);table.append(head);
    const body=element('tbody');
    for(const row of result.rows){const tr=element('tr');result.columns.forEach((c,i)=>{const cell=element('td'),value=row.values[c.key],text=Array.isArray(value)?value.join(', '):value==null?'':typeof value==='object'?JSON.stringify(value):String(value);if(i===0){const button=element('button','base-link',text||row.path);button.onclick=()=>open(row.path,row.root);cell.append(button);}else cell.textContent=text;tr.append(cell);});body.append(tr);}table.append(body);
    const previous=element('button','icon'),next=element('button','icon');previous.innerHTML='<i data-lucide="chevron-left"></i>';next.innerHTML='<i data-lucide="chevron-right"></i>';previous.dataset.tooltip='Previous rows';next.dataset.tooltip='Next rows';previous.setAttribute('aria-label','Previous rows');next.setAttribute('aria-label','Next rows');previous.disabled=offset===0;next.disabled=!result.hasMore;
    previous.onclick=()=>{offset=Math.max(0,offset-100);load().catch(showError);};next.onclick=()=>{offset+=100;load().catch(showError);};controls.append(previous,next);
    let display=table;
    if(result.type==='cards'){
      display=element('div','base-cards');for(const row of result.rows){const card=element('div','base-card');for(const [i,c] of result.columns.entries()){const value=row.values[c.key],text=Array.isArray(value)?value.join(', '):value==null?'':typeof value==='object'?JSON.stringify(value):String(value);if(!i){const button=element('button','base-link',text||row.path);button.onclick=()=>open(row.path,row.root);card.append(button);}else card.append(element('div','',`${c.name}: ${text}`));}display.append(card);}
    }
    view.replaceChildren(controls,element('p','section-qualification',result.qualification+(result.invalid?` ${result.invalid} invalid metadata records omitted.`:'')),display);lucide.createIcons({root:controls});
  };
  const showError=error=>{if(active())view.replaceChildren(element('p','section-qualification warning',error.message));};load().catch(showError);return view;
}
