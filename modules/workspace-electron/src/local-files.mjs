import fs from 'node:fs';
import { open, lstat, realpath, readdir, rename, unlink } from 'node:fs/promises';
import path from 'node:path';
import { createHash, randomUUID } from 'node:crypto';

export const TEXT_LIMIT = 4 * 1024 * 1024;
export const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const hidden = name => name.startsWith('.') || ['node_modules', 'credentials', 'private_no_index'].includes(name);

export function normalize(relative = '', { allowHidden = false } = {}) {
  if (typeof relative !== 'string' || relative.length > 8192 || relative.includes('\0') || relative.includes('\\') || path.posix.isAbsolute(relative)) throw new Error('Invalid file path.');
  if (relative.split('/').some(part => part === '..' || !allowHidden && hidden(part))) throw new Error('That path is outside the visible workspace.');
  return relative.split('/').filter(part => part && part !== '.').join('/');
}

export class LocalFiles {
  constructor(root) { this.root = root; }

  async resolve(relative) {
    relative = normalize(relative);
    const root = await realpath(this.root);
    let current = root;
    for (const component of relative.split('/').filter(Boolean)) {
      current = path.join(current, component);
      if ((await lstat(current)).isSymbolicLink()) throw new Error('Symbolic links are not followed.');
    }
    return current;
  }

  entry(relative, info) {
    return { name: path.posix.basename(relative) || 'LOOM NOTES', relative_path: relative, kind: info.isDirectory() ? 'directory' : info.isFile() ? 'file' : 'other', size: info.size, modified_at: info.mtime.toISOString(), revision: `${info.dev}:${info.ino}:${info.size}:${info.mtimeMs}` };
  }

  async stat(relative) { const file = await this.resolve(relative); return this.entry(relative, await lstat(file)); }

  async list(relative = '') {
    const directory = await this.resolve(relative);
    const children = (await readdir(directory, { withFileTypes: true })).filter(child => !hidden(child.name) && !child.isSymbolicLink());
    const entries = [];
    // One shallow folder only; avoid recursive traversal or a vault-wide watcher.
    for (let i = 0; i < children.length; i += 32) {
      entries.push(...await Promise.all(children.slice(i, i + 32).map(async child => {
        const childPath = path.posix.join(relative, child.name);
        try { return this.entry(childPath, await lstat(path.join(directory, child.name))); } catch { return null; }
      })));
    }
    return { entries: entries.filter(Boolean), has_more: false, order: 'name' };
  }

  async read(relative) {
    const target = await this.resolve(relative);
    const handle = await open(target, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
    try {
      const info = await handle.stat();
      if (!info.isFile() || info.size > TEXT_LIMIT) throw new Error('Text preview is limited to 4 MiB.');
      const buffer = Buffer.alloc(Math.min(TEXT_LIMIT + 1, info.size + 1));
      let total = 0;
      while (total < buffer.length) {
        const { bytesRead } = await handle.read(buffer, total, buffer.length - total, total);
        if (!bytesRead) break;
        total += bytesRead;
      }
      const bytes = buffer.subarray(0, total);
      const after = await handle.stat();
      if (total !== info.size || after.size !== info.size || after.mtimeMs !== info.mtimeMs) throw new Error('The note changed while being read. Reopen it.');
      if (bytes.length > TEXT_LIMIT || bytes.includes(0)) throw new Error('This is not a supported text file.');
      return { text: new TextDecoder('utf-8', { fatal: true }).decode(bytes), base: digest(bytes), entry: this.entry(relative, info) };
    } finally { await handle.close(); }
  }

  async preview(relative,limit=16384){
    const target=await this.resolve(relative),handle=await open(target,fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW);
    try{const before=await handle.stat();if(!before.isFile())throw Error('Choose a text file.');const bytes=Buffer.alloc(Math.min(before.size,limit));let total=0;while(total<bytes.length){const part=await handle.read(bytes,total,bytes.length-total,total);if(!part.bytesRead)break;total+=part.bytesRead;}const after=await handle.stat();if(after.size!==before.size||after.mtimeMs!==before.mtimeMs)throw Error('The note changed during preview.');if(bytes.includes(0))throw Error('Binary content has no text preview.');return {text:new TextDecoder('utf-8',{fatal:true}).decode(bytes.subarray(0,total),{stream:before.size>total}),truncated:before.size>total};}finally{await handle.close();}
  }

  async save(relative, text, base) {
    if (!/\.(md|markdown|txt|mdown|rst|adoc)$/i.test(relative)) throw new Error('Only local notes can be edited in this increment.');
    if (typeof text !== 'string' || Buffer.byteLength(text) > TEXT_LIMIT) throw new Error('Note exceeds 4 MiB.');
    const target = await this.resolve(relative);
    const before = await this.read(relative);
    if (before.base !== base) throw new Error('The note changed on disk. Your draft is preserved; reopen the current file before saving.');
    const original = await lstat(target);
    const temporary = path.join(path.dirname(target), `.loom-files-${randomUUID()}.tmp`);
    const handle = await open(temporary, 'wx', original.mode & 0o777);
    try {
      await handle.writeFile(text, 'utf8');
      await handle.sync();
      await handle.close();
      // Recheck immediately before replacement, including changes by Obsidian.
      if ((await this.read(relative)).base !== base) throw new Error('The note changed during save. Your draft is preserved.');
      await rename(temporary, target);
    } catch (error) {
      await handle.close().catch(() => {});
      await unlink(temporary).catch(() => {});
      throw error;
    }
    return this.read(relative);
  }
}
