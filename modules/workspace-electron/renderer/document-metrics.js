import { parser,GFM } from '@lezer/markdown';
import { frontmatter } from './frontmatter.js';

const markdown=parser.configure(GFM);
export function documentMetrics(text,{links=true}={}){
  const metadata=frontmatter(text),body=metadata.body,references=new Set();
  if(links){const tree=markdown.parse(body);
  tree.iterate({enter(node){
    if(['FencedCode','CodeBlock','InlineCode','HTMLBlock'].includes(node.name))return false;
    if(node.name==='Link'||node.name==='Image'){const url=node.node.getChild('URL');if(url)references.add(body.slice(url.from,url.to));}
    if(node.name==='Paragraph'||/^ATXHeading/.test(node.name)){
      const inline=[];node.node.toTree().iterate({enter(child){if(child.name==='InlineCode')inline.push([node.from+child.from,node.from+child.to]);}});
      for(const match of body.slice(node.from,node.to).matchAll(/!?\[\[([^\]\n]+)\]\]/g))if(!inline.some(([from,to])=>node.from+match.index>=from&&node.from+match.index<to))references.add(match[1].split('|')[0]);
    }
  }});
  for(const field of metadata.fields)for(const value of Array.isArray(field.value)?field.value:[field.value])if(typeof value==='string')for(const match of value.matchAll(/\[\[([^\]\n]+)\]\]/g))references.add(match[1].split('|')[0]);}
  return {properties:metadata.fields.length,propertyError:metadata.error,words:(body.match(/[\p{L}\p{N}]+(?:['’_-][\p{L}\p{N}]+)*/gu)||[]).length,characters:[...body].length,references:[...references]};
}
