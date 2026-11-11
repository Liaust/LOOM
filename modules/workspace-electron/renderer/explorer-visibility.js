const key = 'explorer-hide-readme-agents';
const names = new Set(['readme.md', 'agents.md']);

export function visibleExplorerEntries(entries, hidden) {
  return hidden ? entries.filter(entry => entry.kind !== 'file' || entry.root_ref || !names.has(entry.name.toLowerCase())) : entries;
}

export function loadExplorerVisibility(storage) {
  try { return storage.getItem(key) === 'true'; } catch { return false; }
}

export function saveExplorerVisibility(storage, hidden) {
  storage.setItem(key, String(hidden));
}
