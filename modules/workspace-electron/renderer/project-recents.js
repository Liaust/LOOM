export function recentProjects(storage, root='box') {
  try {
    const saved=storage.getItem(`loom.recentProjects:${root}`);
    const value=JSON.parse(saved??(root==='box'?storage.getItem('loom.recentProjects'):null)??'[]');
    return Array.isArray(value)?value.filter(item=>typeof item.id==='string'&&typeof item.name==='string').slice(0,8):[];
  } catch { return []; }
}

export function rememberProject(storage, root, item) {
  storage.setItem(`loom.recentProjects:${root}`,JSON.stringify([{id:item.id,name:item.name},...recentProjects(storage,root).filter(value=>value.id!==item.id)].slice(0,8)));
}
