import path from 'node:path';
import { normalize } from './local-files.mjs';
import {referenceParts} from './reference-parts.mjs';
import {isBoxRef} from './box-roots.mjs';
export {referenceParts} from './reference-parts.mjs';

const clean = value => normalize(value, { allowHidden: true });
const noteExtensions = ['.md', '.markdown', '.canvas', '.base'];

export async function resolveReference({ root, from, reference,allowCrossBox=false }, { roots, records, stat }) {
  const parsed = referenceParts(reference);
  if (parsed.external) return parsed;
  let { target } = parsed;
  if (!target) return { root, path: clean(from), heading: parsed.heading };
  if (target.startsWith('/')) {
    const owning = roots.filter(r => r.source_path && (target === r.source_path || target.startsWith(r.source_path.replace(/\/$/, '') + '/')))
      .sort((a, b) => b.source_path.length - a.source_path.length)[0];
    if (!owning || owning.hidden) throw Error('The link is outside the exposed Box and Storage roots.');
    const crossBox=isBoxRef(root)&&isBoxRef(owning.ref)&&root!==owning.ref;
    root = owning.ref; target = target.slice(owning.source_path.length).replace(/^\//, '');
    if (!target) throw Error('This link points to a root folder, not a file.');
    target = clean(target);
    if(crossBox&&!allowCrossBox)return {root,path:target,box_id:owning.box_id,heading:parsed.heading,cross_box:true,requires_navigation:true};
    if(owning.available===false)throw Error('The linked Box root is unavailable.');
    const entry = await stat(root, target);
    return { root, path: entry.relative_path, heading: parsed.heading, kind: entry.kind };
  }
  if (path.posix.normalize(path.posix.join(path.posix.dirname(clean(from)), target)).startsWith('../')) throw Error('The link leaves its file root.');
  const candidates = new Set();
  const add = value => {
    const normalized = path.posix.normalize(value);
    if (normalized === '..' || normalized.startsWith('../') || normalized.startsWith('/')) return;
    candidates.add(clean(normalized));
    if (!path.posix.extname(normalized)) for (const extension of noteExtensions) candidates.add(clean(normalized + extension));
  };
  add(path.posix.join(path.posix.dirname(clean(from)), target));
  if (!target.startsWith('.')) add(target);
  // Same-file siblings take priority. A root-relative explicit path follows.
  for (const candidate of candidates) {
    try {
      const entry = await stat(root, candidate);
      if (entry.kind === 'file' || entry.kind === 'directory') return { root, path: candidate, heading: parsed.heading, ...(entry.kind==='directory'?{kind:'directory'}:{}) };
    } catch (error) { if (error.status !== 404 && error.code !== 'ENOENT') throw error; }
  }
  const title = target.replace(/\.(md|markdown)$/i, '').toLocaleLowerCase();
  const matches = [...records].filter(r => !r.deleted && r.root === root && (
    r.path.replace(/\.(md|markdown)$/i, '').toLocaleLowerCase() === title ||
    path.posix.basename(r.path).replace(/\.(md|markdown)$/i, '').toLocaleLowerCase() === title
  )).map(r => ({ root, path: r.path, heading: parsed.heading }));
  if (matches.length === 1) return matches[0];
  if (matches.length > 1) return { choices: matches.slice(0, 100), heading: parsed.heading };
  throw Error('File not found in the linked folders or the local working set. No indexed-search result was substituted.');
}
