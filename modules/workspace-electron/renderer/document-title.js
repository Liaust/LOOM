import {StateField} from '@codemirror/state';
import {Decoration,EditorView,WidgetType} from '@codemirror/view';
import {isMarkdown} from '../src/file-formats.mjs';

export function fileTitle(path){const name=path.split('/').pop();return isMarkdown(path)?name.replace(/\.[^.]+$/,''):name;}
export function titleFilename(path,title){
  const value=title.trim();if(!value||value==='.'||value==='..'||/[\/\\\0\r\n]/.test(value))throw Error('Enter a filename without path separators.');
  const extension=path.split('/').pop().match(/\.[^.]+$/)?.[0]||'';
  const name=isMarkdown(path)?value.toLowerCase().endsWith(extension.toLowerCase())?value:value+extension:value.includes('.')?value:value+extension;
  return name===path.split('/').pop()?null:name;
}
class Title extends WidgetType {
  constructor(owner,path){super();Object.assign(this,{owner,path,canEdit:owner.editable});}
  eq(other){return this.path===other.path&&this.owner===other.owner&&this.canEdit===other.canEdit;}
  ignoreEvent(){return true;}
  toDOM(){
    const row=document.createElement('div');row.className='inline-title';
    const input=document.createElement('input');input.className='document-title';input.value=fileTitle(this.path);input.setAttribute('aria-label','File title');input.dataset.tooltip='Rename file on Enter';input.readOnly=!this.canEdit;input.spellcheck=false;
    let busy=false;input.onkeydown=async event=>{
      event.stopPropagation();
      if(event.key==='Escape'){event.preventDefault();input.value=fileTitle(this.owner.path);input.blur();}
      if(event.key!=='Enter'||busy||!this.owner.editable)return;event.preventDefault();
      try{const name=titleFilename(this.owner.path,input.value);if(!name)return input.blur();busy=true;input.disabled=true;await this.owner.rename(name);input.value=fileTitle(this.owner.path);input.blur();}
      catch(error){this.owner.notice(error.message);}
      finally{busy=false;input.disabled=false;}
    };
    input.onblur=()=>{if(!busy)input.value=fileTitle(this.owner.path);};row.append(input);return row;
  }
}
export function titleExtension(owner,path){return StateField.define({
  create:()=>Decoration.set([Decoration.widget({widget:new Title(owner,path),block:true,side:-3}).range(0)]),
  update:(value,transaction)=>value.map(transaction.changes),provide:field=>EditorView.decorations.from(field),
});}
