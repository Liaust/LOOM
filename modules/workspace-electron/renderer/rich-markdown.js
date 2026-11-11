import {StateField} from '@codemirror/state';
import {Decoration,EditorView,WidgetType} from '@codemirror/view';
import {syntaxTree} from '@codemirror/language';
import katex from 'katex';
import {frontmatter} from './frontmatter.js';
import {bindNavigation} from './navigation-intent.js';

const overlaps=(state,from,to)=>state.selection.ranges.some(r=>r.from<=to&&r.to>=from);
export function richSpans(state,tree){
  const spans=[],excluded=[],metadata=frontmatter(state.doc.toString());
  if(metadata.present&&metadata.error&&!metadata.end)return [];
  if(metadata.end)excluded.push([0,metadata.end]);
  tree.iterate({enter(node){
    if(node.name==='HorizontalRule'&&!excluded.some(([a,b])=>node.from<b&&node.to>a)){spans.push({from:node.from,to:node.to,kind:'divider',block:true});return false;}
    if(['FencedCode','CodeBlock','InlineCode','HTMLBlock','HTMLTag','Link'].includes(node.name)){excluded.push([node.from,node.to]);return false;}
    if(node.name==='Table'){spans.push({from:node.from,to:node.to,kind:'table',block:true});excluded.push([node.from,node.to]);return false;}
  }});
  const text=state.doc.toString(),math=/(?<!\\)\$\$([\s\S]+?)(?<!\\)\$\$|(?<![\\$])\$(?![\s$])([^\n$]*?\S)(?<!\\)\$(?![\d$])/g;
  for(const match of text.matchAll(math)){
    const from=match.index,to=from+match[0].length;
    if(excluded.some(([a,b])=>from<b&&to>a)||match[0].length>20000)continue;
    const display=match[1]!==undefined,first=state.doc.lineAt(from),last=state.doc.lineAt(to);
    const block=display&&text.slice(first.from,from).trim()===''&&text.slice(to,last.to).trim()==='';
    if(!block&&match[0].includes('\n'))continue;
    spans.push({from,to,kind:'math',source:display?match[1]:match[2],display,block});
  }
  return spans.sort((a,b)=>a.from-b.from);
}
class RichWidget extends WidgetType{
  constructor(span,source,owner){super();Object.assign(this,{span,source,owner});}
  eq(other){return this.source===other.source&&this.span.kind===other.span.kind&&this.span.block===other.span.block&&this.owner===other.owner;}
  toDOM(view){
    const dom=document.createElement(this.span.block?'div':'span');dom.className=`lp-rendered-${this.span.kind}`;
    if(this.span.kind==='divider'){
      const rule=document.createElement('hr');rule.className='note-divider';dom.append(rule);
    }else if(this.span.kind==='math'){
      katex.render(this.span.source,dom,{displayMode:this.span.display,throwOnError:false,trust:false,strict:'ignore',maxSize:20,maxExpand:500,output:'htmlAndMathml'});
    }else{
      dom.innerHTML=DOMPurify.sanitize(marked.parse(this.source),{ALLOWED_TAGS:['table','thead','tbody','tr','th','td','strong','em','del','code','a','br'],ALLOWED_ATTR:['href','align']});
      for(const link of dom.querySelectorAll('a')){const ref=link.dataset.noteReference||link.getAttribute('href');link.removeAttribute('href');link.dataset.noteReference=ref||'';bindNavigation(link,(intent,e)=>{e.stopPropagation();if(ref)this.owner.follow(ref,intent);});}
    }
    dom.dataset.tooltip='Click to edit source';
    dom.onmousedown=event=>{if(event.target.closest('a')||event.button!==0)return;event.preventDefault();const position=view.posAtDOM(dom);view.focus();view.dispatch({selection:{anchor:position},scrollIntoView:true});};
    return dom;
  }
  ignoreEvent(){return true;}
}
export function richMarkdown(owner){
  const build=state=>Decoration.set(richSpans(state,syntaxTree(state)).filter(span=>!overlaps(state,span.from,span.to)).map(span=>Decoration.replace({widget:new RichWidget(span,state.doc.sliceString(span.from,span.to),owner),block:span.block}).range(span.from,span.to)),true);
  return StateField.define({create:build,update:(old,tr)=>tr.docChanged||tr.selection||tr.reconfigured||syntaxTree(tr.startState)!==syntaxTree(tr.state)?build(tr.state):old,provide:field=>EditorView.decorations.from(field)});
}
