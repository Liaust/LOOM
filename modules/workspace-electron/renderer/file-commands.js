export function documentTemplate(kind='note') {
  if(kind==='note')return {name:'Untitled.md',extension:'md',text:''};
  if(kind==='base')return {name:'Untitled.base',extension:'base',text:'views:\n  - type: table\n    name: Table\n'};
  if(kind==='canvas')return {name:'Untitled.canvas',extension:'canvas',text:'{\n  "nodes": [],\n  "edges": []\n}\n'};
  throw Error('Unsupported document type.');
}

export function documentName(name,kind='note') {
  const template=documentTemplate(kind),value=name.trim();
  return /\.[^./]+$/.test(value)?value:`${value}.${template.extension}`;
}

// Quick opening is local navigation, not a recursive scan or indexed search.
// An explicit path may address a file that has never been downloaded/indexed.
export function quickOpenPath(query,roots,fallback='box') {
  let value=query.trim(),root=roots.find(r=>r.ref===fallback);
  if(!value.includes('/')||value.endsWith('/')||value.includes('\\')||value.includes('\0'))return null;
  const source=roots.filter(r=>r.source_path).sort((a,b)=>b.source_path.length-a.source_path.length).find(r=>value.startsWith(r.source_path.replace(/\/$/,'')+'/'));
  if(source){root=source;value=value.slice(source.source_path.replace(/\/$/,'').length+1);}
  else{
    const matches=roots.filter(r=>[r.ref,r.label,r.box_key].filter(Boolean).some(label=>value.toLowerCase().startsWith(label.toLowerCase()+'/')));
    if(matches.length>1){const relative=value.slice(value.indexOf('/')+1);return relative.split('/').some(p=>!p||p==='.'||p==='..')?null:{choices:matches.map(r=>({root:r.ref,path:relative}))};}
    const prefix=matches[0];
    if(prefix){root=prefix;value=value.slice(value.indexOf('/')+1);}
    else if(value.startsWith('/'))return null;
  }
  if(!root||value.split('/').some(p=>!p||p==='.'||p==='..'))return null;
  return {root:root.ref,path:value};
}
