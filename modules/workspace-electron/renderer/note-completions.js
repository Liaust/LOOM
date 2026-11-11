import {syntaxTree} from '@codemirror/language';
import {insertCompletionText} from '@codemirror/autocomplete';

export function completionRequest(context){
  for(let node=syntaxTree(context.state).resolveInner(Math.max(0,context.pos-1),-1);node;node=node.parent)if(['FencedCode','CodeBlock','InlineCode','HTMLBlock','HTMLTag','URL'].includes(node.name))return null;
  if(context.state.doc.line(1).text==='---'){let end=Infinity;for(let line=2;line<=context.state.doc.lines;line++){if(/^(---|\.\.\.)$/.test(context.state.doc.line(line).text)){end=context.state.doc.line(line).to;break;}}if(context.pos<=end)return null;}
  const wiki=context.matchBefore(/\[\[[^\]\r\n]*$/);
  if(wiki){if(/[|#]/.test(wiki.text.slice(2))||/\\$/.test(context.state.sliceDoc(0,wiki.from)))return null;return{kind:'link',from:wiki.from+2,query:wiki.text.slice(2),...(context.state.sliceDoc(wiki.from-1,wiki.from)==='!'?{attachment:true}:{})};}
  const tag=context.matchBefore(/(?:^|[\s([])#[\p{L}\p{N}_/-]*$/u);
  if(tag){const at=tag.text.lastIndexOf('#');return{kind:'tag',from:tag.from+at+1,query:tag.text.slice(at+1)};}
  return null;
}
export function noteCompletionSource(owner){return async context=>{
  const request=completionRequest(context);if(!request)return null;
  const result=await owner.suggest(request);if(context.aborted)return null;
  return{from:request.from,filter:false,options:(result.items||[]).map(item=>({
    label:item.name,type:request.kind==='tag'?'keyword':'text',detail:`${result.origin==='main'?'':result.origin+' · '}${item.path||''}`,
    apply(view,completion,from,to){
      const name=request.kind==='tag'?item.name.replace(/^#/,''):request.attachment?item.path:item.path.replace(/\.(md|markdown)$/i,'');
      const suffix=request.kind==='link'&&view.state.sliceDoc(to,to+2)!==']]'?']]':'';
      view.dispatch(insertCompletionText(view.state,name+suffix,from,to));
    },
  }))};
};}
