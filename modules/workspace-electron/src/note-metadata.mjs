import {parser,GFM} from '@lezer/markdown';
import {parseDocument} from 'yaml';

const markdown=parser.configure(GFM),unique=values=>[...new Set(values.filter(value=>typeof value==='string'&&value.trim()).map(value=>value.trim()))];
const strings=value=>Array.isArray(value)?value.filter(item=>typeof item==='string'):typeof value==='string'?[value]:[];
export function noteMetadata(text){
  let body=text,frontmatter={};const start=/^\uFEFF?---[ \t]*\r?\n/.exec(text);
  if(start){const close=/^(?:---|\.\.\.)[ \t]*(?:\r?\n|$)/m.exec(text.slice(start[0].length));if(close){const doc=parseDocument(text.slice(start[0].length,start[0].length+close.index));if(!doc.errors.length&&!doc.warnings.length){try{frontmatter=doc.toJS({maxAliasCount:50})||{};body=text.slice(start[0].length+close.index+close[0].length);}catch{/* Invalid/recursive metadata is not interpreted as navigation authority. */}}}}
  const references=new Set(),tags=new Set(strings(frontmatter.tags).map(tag=>tag.replace(/^#/,''))),tagSpans=[];
  const tree=markdown.parse(body);
  tree.iterate({enter(node){
    if(['FencedCode','CodeBlock','InlineCode','HTMLBlock'].includes(node.name))return false;
    if(node.name==='Link'||node.name==='Image'){const url=node.node.getChild('URL');if(url)references.add(body.slice(url.from,url.to));}
    if(node.name==='Paragraph'||/^(?:ATX|Setext)Heading/.test(node.name)){
      const excluded=[];node.node.toTree().iterate({enter(child){if(['InlineCode','HTMLTag'].includes(child.name)){excluded.push([node.from+child.from,node.from+child.to]);return false;}}});
      const allowed=position=>!excluded.some(([from,to])=>position>=from&&position<to);
      const escaped=position=>{let n=0;while(position>0&&body[--position]==='\\')n++;return n%2===1;};
      const part=body.slice(node.from,node.to);
      for(const match of part.matchAll(/!?\[\[([^\]\n]+)\]\]/g)){const from=node.from+match.index;if(allowed(from)&&!escaped(from))references.add(match[1].split('|')[0]);}
      for(const match of part.matchAll(/(^|[\s\[(])#([\p{L}\p{N}_][\p{L}\p{N}_/-]*)/gu)){const from=node.from+match.index+match[1].length;if(allowed(from)&&!escaped(from)){tags.add(match[2]);tagSpans.push({from:from+text.length-body.length,to:from+text.length-body.length+match[2].length+1,tag:match[2]});}}
    }
  }});
  for(const value of Object.values(frontmatter))for(const text of strings(value))for(const match of text.matchAll(/\[\[([^\]\n]+)\]\]/g))references.add(match[1].split('|')[0]);
  return {aliases:unique([...strings(frontmatter.aliases),...strings(frontmatter.alias)]),tags:unique([...tags]),properties:Object.keys(frontmatter),values:frontmatter,references:[...references],tagSpans};
}
