import Papa from 'papaparse';

export class CsvModel {
  constructor(text,{delimiter}={}){
    const result=Papa.parse(text,{delimiter,skipEmptyLines:false,dynamicTyping:false});
    const errors=result.errors.filter(e=>e.code!=='UndetectableDelimiter');if(errors.length)throw Error(`CSV: ${errors[0].message}`);
    this.delimiter=delimiter||result.meta.delimiter||',';this.newline=result.meta.linebreak||'\n';this.terminal=/\r?\n$/.test(text);this.bom=text.startsWith('\uFEFF');
    this.rows=result.data;if(this.terminal&&this.rows.at(-1)?.length===1&&this.rows.at(-1)[0]==='')this.rows.pop();
    if(this.rows.length>100000||this.rows.some(r=>r.length>1000))throw Error('This table exceeds the visual editor limit. Open source mode instead.');
    if(!this.rows.length)this.rows=[['']];this.revision=0;this.measure();this.original=text;this.undoStack=[];this.redoStack=[];
  }
  measure(){this.columnCount=this.rows.reduce((n,row)=>Math.max(n,row.length),1);this.revision=(this.revision||0)+1;}
  snapshot(){return JSON.stringify({rows:this.rows,delimiter:this.delimiter});}
  change(fn){const previous=this.snapshot();fn(this.rows);if(previous===this.snapshot())return false;this.measure();this.undoStack.push(previous);this.redoStack=[];while(this.undoStack.length>30||this.undoStack.reduce((n,s)=>n+s.length,0)>8*1024*1024)this.undoStack.shift();return true;}
  setDelimiter(value){if(![',',';','\t','|'].includes(value))throw Error('Unsupported delimiter.');return this.change(()=>{this.delimiter=value;});}
  set(row,column,value){if(!Number.isInteger(row)||!this.rows[row]||!Number.isInteger(column)||column<0||column>=1000)throw Error('Invalid table cell.');return this.change(rows=>{while(rows[row].length<=column)rows[row].push('');rows[row][column]=String(value);});}
  addRow(){if(this.rows.length>=100000)throw Error('This table reached the 100,000-row visual editor limit.');return this.change(rows=>rows.push(Array.from({length:this.columnCount},()=>'')));}
  undo(){if(!this.undoStack.length)return false;this.redoStack.push(this.snapshot());Object.assign(this,JSON.parse(this.undoStack.pop()));this.measure();return true;}
  redo(){if(!this.redoStack.length)return false;this.undoStack.push(this.snapshot());Object.assign(this,JSON.parse(this.redoStack.pop()));this.measure();return true;}
  text(){return(this.bom?'\uFEFF':'')+Papa.unparse(this.rows,{delimiter:this.delimiter,newline:this.newline,skipEmptyLines:false})+(this.terminal?this.newline:'');}
  indices({header=true,query='',sort=null}={}){
    const q=query.toLocaleLowerCase();const ids=this.rows.map((_,i)=>i).filter(i=>(!header||i>0)&&(!q||this.rows[i].some(v=>v.toLocaleLowerCase().includes(q))));
    if(sort)ids.sort((a,b)=>(this.rows[a][sort.column]||'').localeCompare(this.rows[b][sort.column]||'',undefined,{numeric:true})*sort.direction||a-b);return ids;
  }
}
