import {HighlightStyle,syntaxHighlighting,LanguageDescription} from '@codemirror/language';
import {languages} from '@codemirror/language-data';
import {oneDarkHighlightStyle} from '@codemirror/theme-one-dark';
import {tags} from '@lezer/highlight';
import {editorLanguage} from '../src/file-formats.mjs';

export const syntaxThemes=[{id:'one-dark',name:'One Dark'},{id:'contrast',name:'High contrast'}];
const contrast=HighlightStyle.define([
  {tag:tags.keyword,color:'#ff8cb6'},{tag:[tags.name,tags.propertyName],color:'#dce3ed'},
  {tag:[tags.function(tags.variableName),tags.labelName],color:'#8dccff'},
  {tag:[tags.string,tags.special(tags.string)],color:'#b5e69d'},
  {tag:[tags.number,tags.bool,tags.null],color:'#ffd08c'},
  {tag:[tags.typeName,tags.className],color:'#e4c2ff'},
  {tag:tags.comment,color:'#919ca9',fontStyle:'italic'},
  {tag:tags.invalid,color:'#ff817e',textDecoration:'underline'},
]);
const proseTags=new Set([tags.heading,tags.heading1,tags.heading2,tags.heading3,tags.heading4,tags.heading5,tags.heading6,tags.link,tags.url,tags.emphasis,tags.strong,tags.strikethrough,tags.monospace,tags.contentSeparator,tags.list]);
export function syntaxTheme(id,rich=false){const style=id==='contrast'?contrast:oneDarkHighlightStyle;return syntaxHighlighting(rich?HighlightStyle.define(style.specs.flatMap(spec=>{const tag=(Array.isArray(spec.tag)?spec.tag:[spec.tag]).filter(t=>!proseTags.has(t));return tag.length?[{...spec,tag}]:[];})):style);}
export async function languageSupport(path){
  const language=editorLanguage(path),aliases={yaml:'YAML',json:'JSON',csharp:'C#',cpp:'C++',c:'C',shell:'Shell',objectivec:'Objective-C',plaintext:''};
  const description=LanguageDescription.matchFilename(languages,path.split('/').pop())||languages.find(item=>item.name.toLowerCase()===(aliases[language]||language).toLowerCase());
  return description?description.load():[];
}
