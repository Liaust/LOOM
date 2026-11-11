import { StateField,Text } from '@codemirror/state';
import { Decoration, WidgetType, EditorView } from '@codemirror/view';
import { frontmatter,changeProperty,renameProperty } from './frontmatter.js';
import { choiceMenu, attachSuggestions } from './choice-controls.js';
import { propertyTypes, convertProperty } from './property-types.js';
import {bindNavigation} from './navigation-intent.js';

const element=(tag,className,text)=>{const el=document.createElement(tag);el.className=className;if(text!==undefined)el.textContent=text;return el;};
class Properties extends WidgetType {
  constructor(model,owner,canEdit){super();Object.assign(this,{model,owner,canEdit});}
  eq(other){return this.model.raw===other.model.raw&&this.model.present===other.model.present&&this.canEdit===other.canEdit;}
  ignoreEvent(){return true;}
  toDOM(view){
    const panel=element('section','note-properties');panel.setAttribute('aria-label','Properties');
    panel.dataset.collapsed=document.documentElement.dataset.propertiesCollapsed||'false';
    if(this.model.fields.length){const heading=element('button','properties-heading properties-toggle','Properties');heading.type='button';heading.setAttribute('aria-expanded',String(panel.dataset.collapsed!=='true'));heading.onclick=()=>{panel.dataset.collapsed=String(panel.dataset.collapsed!=='true');heading.setAttribute('aria-expanded',String(panel.dataset.collapsed!=='true'));view.requestMeasure();};panel.append(heading);}
    const apply=(key,value,options)=>{
      try{const change=options?.rename?renameProperty(view.state.doc.toString(),key,value):changeProperty(view.state.doc.toString(),key,value,options);view.dispatch({changes:{from:change.from,to:change.to,insert:Text.of(change.insert.split('\n'))},userEvent:'input'});return true;}
      catch(error){this.owner.notice(error.message);}
    };
    for(const field of this.model.fields){
      const row=element('div','property-row');row.dataset.property=field.key;
      const label=element('div','property-key');
      const typeButton=element('button','property-type-button');typeButton.type='button';typeButton.dataset.tooltip=`${field.key}: ${propertyTypes.find(type=>type.value===field.type)?.label}. Change type`;typeButton.setAttribute('aria-label',typeButton.dataset.tooltip);typeButton.setAttribute('aria-haspopup','menu');typeButton.setAttribute('aria-expanded','false');
      typeButton.append(this.owner.icon(field.key==='tags'?'tags':propertyTypes.find(type=>type.value===field.type)?.icon||'align-left'));typeButton.disabled=!this.canEdit||!field.editable;
      typeButton.onclick=()=>choiceMenu(typeButton,propertyTypes.map(type=>({...type,selected:type.value===field.type})),type=>{if(type===field.type)return;try{apply(field.key,convertProperty(field.value,type));}catch(error){this.owner.notice(error.message);}},{label:`${field.key} type`,icon:this.owner.icon});label.append(typeButton);
      const key=element('input','property-key-input');key.value=field.key;key.readOnly=!this.canEdit||!field.editable;key.setAttribute('aria-label',`${field.key} property name`);
      key.onkeydown=event=>{event.stopPropagation();if(event.key==='Escape'){key.value=field.key;key.blur();}if(event.key==='Enter'){event.preventDefault();if(key.value.trim()!==field.key)apply(field.key,key.value,{rename:true});else key.blur();}};key.onblur=()=>{key.value=field.key;};label.append(key);row.append(label);
      const values=element('div','property-values');row.append(values);
      if(!field.editable){const value=element('span','property-unsupported',field.value);value.dataset.tooltip='Structured YAML; edit in source mode';values.append(value);}
      else if(field.type==='list'){
        for(const [index,value] of field.value.entries()){
          const chip=element('span','property-chip');
          if(/^\[\[.*\]\]$/.test(value)){const link=element('button','property-link',value.slice(2,-2));link.type='button';bindNavigation(link,intent=>this.owner.follow(value,intent));chip.append(link);}
          else if(field.key==='tags'){const tag=element('button','property-link',value);tag.type='button';tag.onclick=()=>this.owner.openTag(value.replace(/^#/,''));chip.append(tag);}
          else chip.append(element('span','',value));
          if(this.canEdit){const remove=element('button','property-remove');remove.append(this.owner.icon('x'));remove.type='button';remove.dataset.tooltip=`Remove ${value}`;remove.setAttribute('aria-label',remove.dataset.tooltip);remove.onclick=()=>apply(field.key,field.value.filter((_,i)=>i!==index));chip.append(remove);}
          values.append(chip);
        }
        if(this.canEdit){const input=element('input','property-list-input');input.placeholder=field.key==='tags'?'Add tag':'Add value';input.setAttribute('aria-label',`Add ${field.key}`);
          let committed=false;const commit=()=>{if(!committed&&input.value.trim()){committed=true;apply(field.key,[...field.value,input.value.trim()]);}};
          input.onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();commit();}event.stopPropagation();};input.onchange=commit;values.append(input);}
      }else{
        const multiline=typeof field.value==='string'&&field.value.includes('\n');
        const input=element(multiline?'textarea':'input','property-input');if(!multiline)input.type=field.type==='boolean'?'checkbox':field.type==='number'?'number':'text';if(field.type==='number')input.step='any';input.disabled=!this.canEdit;
        input.setAttribute('aria-label',field.key);if(field.type==='boolean')input.checked=field.value;else input.value=field.value??'';
        input.onchange=()=>{const value=field.type==='boolean'?input.checked:field.type==='number'?input.value===''?null:Number(input.value):input.value;if(value!==field.value)apply(field.key,value);};
        input.onkeydown=event=>{if(event.key==='Enter'&&(!multiline||event.metaKey||event.ctrlKey)){event.preventDefault();input.blur();}event.stopPropagation();};input.id=`property-${this.owner.id}-${this.model.fields.indexOf(field)}`;values.append(input);
        if(typeof field.value==='string'&&/^\[\[.*\]\]$/.test(field.value)){const link=element('button','property-link');link.append(this.owner.icon('external-link'));link.type='button';link.dataset.tooltip='Open linked note';link.setAttribute('aria-label','Open linked note');bindNavigation(link,intent=>this.owner.follow(field.value,intent));values.append(link);}
      }
      if(this.canEdit&&field.editable){const remove=element('button','property-delete');remove.dataset.tooltip=`Remove property ${field.key}`;remove.setAttribute('aria-label',remove.dataset.tooltip);remove.append(this.owner.icon('x'));remove.onclick=()=>apply(field.key,null,{remove:true});row.append(remove);}
      panel.append(row);
    }
    if(this.canEdit){
      const add=element('button','property-add');add.append(this.owner.icon('plus'),element('span','','Add property'));add.onclick=()=>{
        if(panel.querySelector('.property-new'))return;
        const form=element('div','property-row property-new'),key=element('input','property-key-input'),typeButton=element('button','property-type-button'),values=element('div','property-values');
        key.placeholder='Name';key.setAttribute('aria-label','Property name');typeButton.type='button';typeButton.setAttribute('aria-haspopup','menu');typeButton.setAttribute('aria-expanded','false');
        let type='text',value,explicitType=false;
        const renderValue=(previous='')=>{
          value=element('input','property-input');value.type=type==='boolean'?'checkbox':type==='number'?'number':'text';if(type==='number')value.step='any';
          value.placeholder=type==='list'?'Add comma-separated values':'Empty';value.setAttribute('aria-label','Property value');
          if(type==='boolean')value.checked=previous===true;else value.value=Array.isArray(previous)?previous.join(', '):previous;
          if(type==='boolean')value.onchange=commit;
          else value.onblur=event=>{if(event.relatedTarget&&(form.contains(event.relatedTarget)||event.relatedTarget.closest?.('.choice-popup')))return;commit();};values.replaceChildren(value);
          const definition=propertyTypes.find(item=>item.value===type);typeButton.replaceChildren(this.owner.icon(definition.icon));typeButton.dataset.tooltip=`${definition.label}. Change property type`;typeButton.setAttribute('aria-label',typeButton.dataset.tooltip);view.requestMeasure();
        };
        let committed=false;const commit=()=>{
          if(committed||!key.value.trim())return;const name=key.value.trim();
          let parsed;try{parsed=type==='boolean'?value.checked:type==='list'?value.value.split(',').map(item=>item.trim()).filter(Boolean):type==='number'?convertProperty(value.value,'number'):value.value;}catch(error){this.owner.notice(error.message);return;}
          committed=true;if(apply(name,parsed,{add:true})){queueMicrotask(()=>[...view.dom.querySelectorAll('.property-row')].find(row=>row.dataset.property===name)?.querySelector('.property-input,.property-list-input')?.focus());}else committed=false;
        };
        const setType=next=>{try{const current=type==='boolean'?value.checked:type==='list'?value.value.split(',').map(item=>item.trim()).filter(Boolean):value.value;const converted=current===''?(next==='boolean'?false:next==='list'?[]:''):convertProperty(current,next);type=next;renderValue(converted);return true;}catch(error){this.owner.notice(error.message);return false;}};
        const inferType=()=>{if(!explicitType){const next=['tags','aliases','related'].includes(key.value)?'list':'text';if(next!==type)setType(next);}};
        key.oninput=inferType;
        attachSuggestions(key,{items:()=>['aliases','tags','title','type','status','created_at','created_by','updated_at','updated_by','related','owners',...this.model.fields.map(field=>field.key)],load:async query=>(await this.owner.suggest({kind:'property',query})).items?.map(item=>item.name)||[],onChoose:()=>{inferType();value.focus();}});
        key.onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();event.stopPropagation();value.focus();}};
        typeButton.onclick=()=>choiceMenu(typeButton,propertyTypes.map(item=>({...item,selected:item.value===type})),next=>{if(setType(next))explicitType=true;value.focus();},{label:'Property type',icon:this.owner.icon});
        form.onkeydown=event=>{event.stopPropagation();if(event.key==='Escape'){event.preventDefault();committed=true;form.remove();view.requestMeasure();add.focus();}if(event.key==='Enter'){event.preventDefault();commit();}};
        const keyCell=element('div','property-key');keyCell.append(typeButton,key);form.append(keyCell,values);renderValue();panel.insertBefore(form,add);view.requestMeasure();key.focus();
      };panel.append(add);
    }
    return panel;
  }
}

export function propertiesExtension(owner){
  const build=state=>{
    const model=frontmatter(state.doc.toString());if(model.error)return Decoration.none;
    const widget=new Properties(model,owner,!state.readOnly);
    const end=model.end&&state.doc.sliceString(model.end-1,model.end)==='\n'?model.end-1:model.end;
    return Decoration.set([end?Decoration.replace({widget,block:true}).range(0,end):Decoration.widget({widget,block:true,side:-1}).range(0)]);
  };
  return StateField.define({create:build,update:(value,transaction)=>transaction.docChanged||transaction.reconfigured?build(transaction.state):value,provide:field=>[EditorView.decorations.from(field),EditorView.atomicRanges.of(view=>view.state.field(field))]});
}
