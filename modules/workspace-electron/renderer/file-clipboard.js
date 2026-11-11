const inside=(parent,child)=>child===parent||child.startsWith(parent+'/');
export function clipboardEntries(entries){
 if(!Array.isArray(entries)||!entries.length)throw Error('Select a file or folder first.');
 if(entries.length>40)throw Error('Select at most 40 entries for one operation.');
 const unique=new Map();for(const entry of entries){if(!['box','mac','storage'].includes(entry.root)||!entry.path||entry.member)throw Error('Select ordinary files or folders, not a root or archive attachment.');unique.set(JSON.stringify([entry.root,entry.path]),{root:entry.root,path:entry.path,directory:entry.directory===true});}
 const result=[...unique.values()];if(result.some((a,i)=>result.some((b,j)=>i!==j&&a.root===b.root&&inside(a.path,b.path))))throw Error('Select a folder or its children, not both.');return result;
}
export function pasteTarget(entry,target,cut){
 if(!['box','mac'].includes(target.root))throw Error('Paste into a writable Main or Mac Box folder.');
 if(cut&&entry.root!==target.root)throw Error('Moving between machines needs an explicit copy; originals are never deleted by cross-machine paste.');
 if(entry.root===target.root&&entry.directory&&inside(entry.path,target.path))throw Error('A folder cannot be pasted inside itself.');
 const parent=entry.path.split('/').slice(0,-1).join('/');let name=entry.path.split('/').pop();
 if(entry.root===target.root&&parent===target.path&&!cut){const dot=name.lastIndexOf('.');name=dot>0&&!entry.directory?`${name.slice(0,dot)} copy${name.slice(dot)}`:`${name} copy`;}
 const path=[target.path,name].filter(Boolean).join('/');if(cut&&entry.root===target.root&&path===entry.path)throw Error('The entry is already in this folder.');return {path,name};
}
