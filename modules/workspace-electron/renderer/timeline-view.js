import {Chart,BarController,BarElement,CategoryScale,LinearScale,Tooltip} from 'chart.js';
import {diffLines} from 'diff';
import {selectControl} from './choice-controls.js';
import {historyRange} from '../src/history.mjs';
import {bindNavigation} from './navigation-intent.js';
Chart.register(BarController,BarElement,CategoryScale,LinearScale,Tooltip);
const el=(tag,className,text)=>{const n=document.createElement(tag);n.className=className||'';if(text!==undefined)n.textContent=text;return n;};
const day=at=>new Date(at).toLocaleDateString(),when=at=>new Date(at).toLocaleString();
export class TimelineView{
 constructor({api,scope,open,folder,icon,restore}){Object.assign(this,{api,scope,open,folder,icon});this.alive=true;this.pages=restore?.pages||[];this.kind=restore?.kind||'';this.from=restore?.from;this.to=restore?.to;this.selectedDay=restore?.selectedDay;this.next=restore?.next;this.result=restore?.result;this.error=restore?.error;this.dom=el('section','timeline-view');this.onTheme=()=>{if(this.alive&&this.result)this.render();};window.addEventListener('workspace-theme-changed',this.onTheme);if(this.result){this.render();this.ready=Promise.resolve();}else{this.dom.append(el('p','section-qualification','Reading source receipts…'));this.ready=this.load().catch(e=>this.fail(e));}}
 snapshot(){return{kind:this.kind,from:this.from,to:this.to,selectedDay:this.selectedDay,pages:this.pages,result:this.result,next:this.next,error:this.error};}
 destroy(){this.alive=false;window.removeEventListener('workspace-theme-changed',this.onTheme);this.chart?.destroy();}
 fail(e){if(this.alive){this.error=e.message;this.result||={origin:'unavailable',coverage:'No source receipt result loaded.'};this.render();}}
 button(name,symbol,run,disabled=false){const b=el('button','icon');b.append(this.icon(symbol));b.setAttribute('aria-label',name);b.dataset.tooltip=name;b.disabled=disabled;b.onclick=()=>Promise.resolve().then(run).catch(e=>this.fail(e));return b;}
 async load(more=false){const generation=this.generation=(this.generation||0)+1,result=await this.api.history({...this.scope,kind:this.kind,...historyRange(this.from,this.to),...more?{after:this.next}:{}});if(!this.alive||generation!==this.generation)return;
  this.result=result;this.error=null;this.next=result.next;this.pages=(more?[...this.pages,...result.items]:result.items).slice(0,1000);this.render();
 }
 render(){
  this.chart?.destroy();this.chart=null;const tools=el('div','view-controls');tools.append(el('h2','','Timeline'));
  const kind=selectControl({label:'Timeline event type',name:'kind',value:this.kind,items:[{value:'',label:'All changes'},{value:'create',label:'Created'},{value:'edit',label:'Edited'},{value:'rename',label:'Moved'},{value:'delete',label:'Removed'}]});kind.addEventListener('change',()=>{this.kind=kind.querySelector('input').value;this.selectedDay=null;this.load().catch(e=>this.fail(e));});tools.append(kind);
  for(const [key,label]of [['from','From date'],['to','To date']]){const input=el('input','timeline-date');input.type='date';input.setAttribute('aria-label',label);input.value=this[key]||'';input.onchange=()=>{this[key]=input.value;this.selectedDay=null;this.load().catch(e=>this.fail(e));};tools.append(input);}
  tools.append(this.button('Refresh timeline','refresh-cw',()=>this.load()));
  this.dom.replaceChildren(tools,el('p','section-qualification',`${this.scope.root}/${this.scope.path||''} · ${this.result.origin} · ${this.result.coverage}${this.result.error?' '+this.result.error:''}`));
  if(this.error)this.dom.append(el('p','section-qualification warning',this.error+(this.pages.length?' Previously loaded receipts remain below.':'')));
  if(!this.pages.length){this.dom.append(el('p','empty','No recorded changes in this window. This does not prove the folder was unchanged.'));return;}
  const counts=new Map();for(const e of this.pages)counts.set(day(e.at),(counts.get(day(e.at))||0)+1);const labels=[...counts.keys()].reverse(),strip=el('div','timeline-chart'),canvas=el('canvas');canvas.setAttribute('role','img');canvas.setAttribute('aria-label','Activity in loaded timeline pages');strip.append(canvas);this.dom.append(strip,el('p','section-qualification',`${this.pages.length} receipts in loaded pages`));
  this.chart=new Chart(canvas,{type:'bar',data:{labels,datasets:[{data:labels.map(d=>counts.get(d)),backgroundColor:getComputedStyle(document.documentElement).getPropertyValue('--info').trim(),borderRadius:3,maxBarThickness:28}]},options:{responsive:true,maintainAspectRatio:false,animation:false,plugins:{legend:{display:false}},scales:{x:{grid:{display:false},ticks:{color:'#a49c95',maxTicksLimit:12}},y:{beginAtZero:true,ticks:{precision:0,color:'#a49c95'},grid:{color:'#88888816'}}},onClick:(_event,points)=>{this.selectedDay=points.length?labels[points[0].index]:null;this.render();}}});
  if(this.selectedDay){const clear=el('button','base-link',this.selectedDay+' ×');clear.onclick=()=>{this.selectedDay=null;this.render();};this.dom.append(clear);}
  const list=el('div','timeline-events');for(const event of this.pages.filter(e=>!this.selectedDay||day(e.at)===this.selectedDay)){
   const row=el('article','timeline-event'),symbol={create:'file-plus',edit:'pencil',rename:'folder-input',delete:'trash-2'}[event.kind]||'clock';row.append(this.icon(symbol));
   const content=el('div','timeline-event-content'),title=el('div','timeline-event-title'),link=el('button','base-link',event.path||event.current_path||'Source operation');bindNavigation(link,intent=>event.directory?this.folder(this.scope.root,event.current_path||event.target||event.path,intent):this.open(event.current_path||event.target||event.path,this.scope.root,intent));title.append(link,el('span','timeline-state',event.state.replaceAll('_',' ')));content.append(title,el('small','',`${when(event.at)} · ${event.kind}${event.target?' → '+event.target:''}${event.entries?' · '+event.entries+' entries':''}`));if(event.reason)content.append(el('p','section-qualification',event.reason));
   const inspect=el('details','timeline-evidence'),summary=el('summary','','Receipt');inspect.append(summary,el('code','',event.id));if(event.base_id)inspect.append(el('div','section-qualification','Before: '+event.base_id));if(event.result_base_id)inspect.append(el('div','section-qualification','After: '+event.result_base_id));content.append(inspect);
   row.append(content,this.button('Compare retained text','git-compare',()=>this.compare(event,row),event.directory||event.state!=='accepted'||!event.result_base_id));list.append(row);
  }this.dom.append(list);if(this.next){if(this.pages.length<1000)this.dom.append(this.button('Load older changes','chevrons-down',()=>this.load(true)));else this.dom.append(el('p','section-qualification','Showing 1,000 receipts. Choose a narrower date range for older changes.'));}
 }
 async compare(event,row){
  const result=await this.api.history({...this.scope,action:'compare',operation:event.id});if(!this.alive||!row.isConnected)return;
  row.querySelector('.timeline-comparison')?.remove();const panel=el('div','timeline-comparison');if(!result.available)panel.append(el('p','section-qualification',result.reason||'Retained text is unavailable.'));else{
   panel.append(el('div','section-qualification',`${event.base_id} → ${event.result_base_id} · read-only retained evidence`));
   const diff=diffLines(result.before,result.after,{timeout:100,maxEditLength:2000});if(!diff)panel.append(el('p','section-qualification','This comparison exceeds the bounded visual diff. Original versions remain retained.'));else{const pre=el('pre','history-diff');let remaining=4000;for(const part of diff){const lines=part.value.split(/(?<=\n)/);const span=el('span',part.added?'diff-added':part.removed?'diff-removed':'',lines.slice(0,remaining).map(line=>(part.added?'+ ':part.removed?'- ':'  ')+line).join(''));remaining-=Math.min(remaining,lines.length);pre.append(span);if(!remaining){pre.append(el('span','section-qualification','\nComparison display truncated.'));break;}}panel.append(pre);}
  }row.append(panel);
 }
}
