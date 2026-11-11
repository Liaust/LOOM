import {syntaxTree} from '@codemirror/language';
import {frontmatter} from './frontmatter.js';
const cached=new WeakMap();
export function attachmentSpans(state){
  const tree=syntaxTree(state),prior=cached.get(state.doc);if(prior?.tree===tree)return prior.spans;
  const text=state.doc.toString(),metadata=frontmatter(text),excluded=[],spans=[];
  if(metadata.present&&metadata.error&&!metadata.end)return [];
  if(metadata.end)excluded.push([0,metadata.end]);
  syntaxTree(state).iterate({enter(node){if(['FencedCode','CodeBlock','InlineCode','HTMLBlock','HTMLTag'].includes(node.name)){excluded.push([node.from,node.to]);return false;}}});
  for(const match of text.matchAll(/(?<!\\)!\[\[([^\]\n]+)\]\]|(?<!\\)!\[([^\]\n]*)\]\(([^\n]+?)\)/g)){
    const from=match.index,to=from+match[0].length;
    if(excluded.some(([a,b])=>from<b&&to>a))continue;
    const reference=match[1]||match[3],line=state.doc.lineAt(from),end=state.doc.lineAt(to);
    if(line.number!==end.number||text.slice(line.from,from).trim()||text.slice(to,line.to).trim())continue;
    spans.push({from:line.from,to:line.to,reference});
  }
  cached.set(state.doc,{tree,spans});return spans;
}
