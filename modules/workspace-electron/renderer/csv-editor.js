import {CsvModel} from '../src/csv-model.mjs';
import {selectControl} from './choice-controls.js';

const el=(tag,className,text)=>{const n=document.createElement(tag);n.className=className||'';if(text!==undefined)n.textContent=text;return n;};
export class CsvEditor {
  constructor({text,editable,onChange,icon,notice,path}){
    Object.assign(this,{editable,onChange,icon,notice,path});this.header=true;this.query='';this.sort=null;this.text=text;this.columnOffset=0;
    this.dom=el('div','csv-editor');const tools=el('div','view-controls');
    const search=el('input','table-filter');search.placeholder='Filter rows';search.setAttribute('aria-label','Filter table rows');search.oninput=()=>{clearTimeout(this.timer);this.timer=setTimeout(()=>{this.query=search.value;this.viewport.scrollTop=0;this.render();},100);};
    const header=el('label','table-header-option'),check=el('input');check.type='checkbox';check.checked=true;check.onchange=()=>{this.header=check.checked;this.render();};header.append(check,document.createTextNode('Header'));
    const add=el('button','icon');add.append(icon('list-plus'));add.setAttribute('aria-label','Add table row');add.dataset.tooltip='Add row';add.disabled=!editable;add.onclick=()=>{try{this.flush();this.model.addRow();this.changed();this.render();}catch(e){this.notice(e.message);}};
    this.delimiterHost=el('span');tools.append(search,header,this.delimiterHost,add);this.count=el('span','section-qualification');tools.append(this.count);
    for(const [label,symbol,step]of [['Previous columns','chevron-left',-20],['Next columns','chevron-right',20]]){const b=el('button','icon');b.append(icon(symbol));b.setAttribute('aria-label',label);b.onclick=()=>{this.flush();const columns=this.columnCount||1;this.columnOffset=Math.max(0,Math.min(Math.floor((columns-1)/20)*20,this.columnOffset+step));this.render();};tools.append(b);if(step<0)this.previous=b;else this.next=b;}
    this.viewport=el('div','csv-viewport');this.viewport.tabIndex=0;this.viewport.setAttribute('aria-label','CSV table');this.viewport.onscroll=()=>{if(!this.input)this.render();};
    this.tools=tools;this.dom.append(tools,this.viewport);this.parse(text);
    this.dom.onkeydown=e=>{if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='z'&&!this.input&&editable&&this.model){e.preventDefault();if(e.shiftKey?this.model.redo():this.model.undo()){this.changed();this.render();}}};
  }
  changed(){this.text=this.model.text();this.onChange(this.text);}
  flush(){this.commit?.();}
  mount(){if(!this.model&&!this.worker)this.parse(this.text);}
  destroy(){this.flush();this.worker?.terminate();this.worker=null;clearTimeout(this.timer);}
  parse(text){
    this.worker?.terminate();this.worker=null;const delimiter=/\.tsv$/i.test(this.path)?'\t':undefined;
    if(text.length<=128*1024){this.model=new CsvModel(text,{delimiter});this.render();return;}
    this.model=null;this.viewport.replaceChildren(el('p','section-qualification','Reading table…'));for(const input of this.tools.querySelectorAll('input,button'))input.disabled=true;
    const worker=this.worker=new Worker(new URL('csv-worker.js',import.meta.url),{type:'module'});
    const fail=error=>{if(this.worker!==worker)return;worker.terminate();this.worker=null;this.viewport.replaceChildren(el('p','section-qualification warning',error));};
    worker.onmessage=({data})=>{if(this.worker!==worker)return;if(data.error)return fail(data.error);worker.terminate();this.worker=null;this.model=Object.assign(Object.create(CsvModel.prototype),data.model,{undoStack:[],redoStack:[]});for(const input of this.tools.querySelectorAll('input,button'))input.disabled=false;this.tools.querySelector('[aria-label="Add table row"]').disabled=!this.editable;this.render();};worker.onerror=e=>fail(e.message||'Table parsing failed. The original source is preserved.');worker.postMessage({text,delimiter});
  }
  replaceText(text){if(text===this.text)return;this.flush();this.text=text;this.parse(text);}
  render(){
    if(!this.model)return;
    const indexKey=JSON.stringify([this.model.revision,this.header,this.query,this.sort]);if(this.indexModel!==this.model||indexKey!==this.indexKey){this.indexModel=this.model;this.indexKey=indexKey;this.rowIndices=this.model.indices(this);}const ids=this.rowIndices,columns=this.model.columnCount,height=32,start=Math.max(0,Math.min(ids.length-1,Math.floor(this.viewport.scrollTop/height)-5)),visible=ids.slice(start,start+55);
    this.columnCount=columns;this.columnOffset=Math.min(this.columnOffset,Math.floor((columns-1)/20)*20);const end=Math.min(columns,this.columnOffset+20);this.previous.disabled=!this.columnOffset;this.next.disabled=end>=columns;
    this.count.textContent=`${ids.length.toLocaleString()} ${ids.length===1?'row':'rows'} · ${columns} ${columns===1?'column':'columns'}${columns>20?' · '+(this.columnOffset+1)+'–'+end:''}`;
    if(this.delimiterHost.dataset.value!==this.model.delimiter){const select=selectControl({label:'CSV delimiter',name:'delimiter',value:this.model.delimiter,items:[{value:',',label:'Comma'},{value:';',label:'Semicolon'},{value:'\t',label:'Tab'},{value:'|',label:'Pipe'}]});select.addEventListener('change',()=>{if(!this.editable)return;this.flush();if(this.model.setDelimiter(select.querySelector('input').value)){this.changed();this.render();}});select.querySelector('button').disabled=!this.editable;this.delimiterHost.replaceChildren(select);this.delimiterHost.dataset.value=this.model.delimiter;}
    const table=el('table','csv-table'),head=el('thead'),hr=el('tr');hr.append(el('th','csv-row-number','#'));
    for(let c=this.columnOffset;c<end;c++){const th=el('th'),b=el('button','',this.header?this.model.rows[0]?.[c]||`Column ${c+1}`:`Column ${c+1}`);b.onclick=()=>{this.flush();this.sort={column:c,direction:this.sort?.column===c?-this.sort.direction:1};this.render();};th.append(b);hr.append(th);}head.append(hr);table.append(head);
    const body=el('tbody'),spacer=pixels=>{const tr=el('tr','csv-spacer'),td=el('td');td.colSpan=end-this.columnOffset+1;td.style.height=`${pixels}px`;tr.append(td);return tr;};
    if(start)body.append(spacer(start*height));
    for(const row of visible){const tr=el('tr');tr.append(el('td','csv-row-number',row+1));for(let c=this.columnOffset;c<end;c++){const td=el('td','csv-cell',this.model.rows[row][c]||'');td.tabIndex=this.editable?0:-1;td.dataset.row=row;td.dataset.column=c;td.ondblclick=()=>this.edit(td,row,c);td.onkeydown=e=>{if(e.key==='Enter'&&this.editable){e.preventDefault();this.edit(td,row,c);}};tr.append(td);}body.append(tr);}
    if(start+visible.length<ids.length)body.append(spacer((ids.length-start-visible.length)*height));table.append(body);this.viewport.replaceChildren(table);
  }
  edit(td,row,column){
    if(!this.editable||this.input)return;const input=el('textarea','csv-cell-input');input.value=this.model.rows[row][column]||'';input.setAttribute('aria-label',`Row ${row+1}, column ${column+1}`);td.replaceChildren(input);this.input=input;
    let done=false;this.commit=()=>{if(done)return;done=true;this.input=null;this.commit=null;if(this.model.set(row,column,input.value))this.changed();this.render();};
    input.onblur=this.commit;input.onkeydown=e=>{e.stopPropagation();if(e.key==='Escape'){e.preventDefault();input.value=this.model.rows[row][column]||'';this.commit();}else if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();this.commit();}};input.focus();input.select();
  }
}
