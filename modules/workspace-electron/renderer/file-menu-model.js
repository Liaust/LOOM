const sections = [
  ['new-note','new-folder','new-canvas','new-base'],
  ['focus','find','open','open-tab','open-background','browse-attachments','reveal-tree'],
  ['rename','duplicate','move','copy','cut','paste'],
  ['properties','copy-path','copy-local-path','copy-member','reveal-finder'],
  ['offline','save-copy','working-copy','upload','copy-main'],
  ['timeline','enrich','webdav-files','export-project','move-retained','project-lifecycle'],
  ['trash'],
];
const known = new Set(sections.flat());

export function groupFileMenu(actions) {
  const selected = new Map();
  for (const action of actions) {
    if (!known.has(action.id) || selected.has(action.id)) throw Error(`Invalid file menu action: ${action.id}`);
    selected.set(action.id, action);
  }
  const result = [];
  for (const section of sections) {
    const entries = section.filter(id => selected.has(id)).map(id => selected.get(id));
    if (!entries.length) continue;
    if (result.length) result.push({separator:true});
    result.push(...entries);
  }
  return result;
}
