import { Document, parseDocument, isMap, isSeq, isScalar } from 'yaml';

export function frontmatter(text) {
  const opening=/^(\uFEFF?)---[ \t]*(\r?\n)/.exec(text);
  if(!opening)return {present:false,end:0,body:text,fields:[]};
  const closing=/^(---|\.\.\.)[ \t]*(?:\r?\n|$)/m.exec(text.slice(opening[0].length));
  if(!closing)return {present:true,end:0,body:text,fields:[],error:'Unclosed YAML frontmatter. Edit source to repair it.'};
  const end=opening[0].length+closing.index+closing[0].length;
  const raw=text.slice(opening[0].length,opening[0].length+closing.index);
  const document=parseDocument(raw,{keepSourceTokens:true,uniqueKeys:true});
  const problem=[...document.errors,...document.warnings][0];
  if(problem||document.contents&&!isMap(document.contents))return {present:true,end,body:text.slice(end),fields:[],error:problem?.message||'Properties must be a YAML mapping.'};
  const fields=(document.contents?.items||[]).map(pair=>{
    const key=isScalar(pair.key)&&typeof pair.key.value==='string'?pair.key.value:null,value=pair.value;
    const scalar=value===null||isScalar(value)&&(value.value===null||['string','boolean'].includes(typeof value.value)||typeof value.value==='number'&&Number.isFinite(value.value));
    const list=isSeq(value)&&value.items.every(item=>isScalar(item)&&typeof item.value==='string'&&!item.anchor&&!item.tag);
    const editable=key!==null&&(scalar||list)&&!value?.tag&&!value?.anchor;
    return {key:key??String(pair.key),value:list?value.items.map(item=>item.value):scalar?value?.value??null:String(value),
      type:list?'list':scalar&&typeof value?.value==='boolean'?'boolean':scalar&&typeof value?.value==='number'?'number':'text',editable};
  });
  return {present:true,end,body:text.slice(end),raw,document,fields,eol:opening[2],bom:opening[1],closing:closing[1]};
}

export function changeProperty(text,key,value,{remove=false,add=false}={}) {
  if(typeof key!=='string'||!key.trim()||key.length>200||/[\r\n\0]/.test(key))throw Error('Enter a valid property name.');
  const model=frontmatter(text);if(model.error)throw Error(model.error);
  if(model.fields.some(field=>field.key===key&&!field.editable))throw Error('Edit this structured property in source mode.');
  const doc=model.document||new Document({});
  if(add&&doc.has(key))throw Error('That property already exists.');
  if(remove)doc.delete(key);else {
    if(!(value===null||typeof value==='string'||typeof value==='boolean'||typeof value==='number'&&Number.isFinite(value)||Array.isArray(value)&&value.every(v=>typeof v==='string')))throw Error('Edit this property in source mode.');
    doc.set(key,value);
  }
  return serializePropertyChange(model,doc,text);
}

export function renameProperty(text,from,to){
  to=to.trim();if(!to||to.length>200||/[\r\n\0]/.test(to))throw Error('Enter a valid property name.');
  const model=frontmatter(text);if(model.error)throw Error(model.error);
  const doc=model.document;if(!doc||!model.fields.some(field=>field.key===from&&field.editable))throw Error('Edit this property in source mode.');
  if(to!==from&&doc.has(to))throw Error('That property already exists.');
  const pair=doc.contents.items.find(pair=>isScalar(pair.key)&&pair.key.value===from);pair.key.value=to;
  return serializePropertyChange(model,doc,text);
}
function serializePropertyChange(model,doc,text){
  const eol=model.eol||(text.includes('\r\n')?'\r\n':'\n');
  const yaml=doc.toString({lineWidth:0}).replace(/\r?\n/g,eol);
  const insert=`${model.bom||''}---${eol}${yaml}${model.closing||'---'}${eol}`;
  return {from:0,to:model.end,insert,text:insert+model.body};
}
