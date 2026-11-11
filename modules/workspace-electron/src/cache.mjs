import { createWriteStream, createReadStream } from 'node:fs';
import { mkdir, readFile, writeFile, rename, stat, unlink, readdir } from 'node:fs/promises';
import path from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { crc32 } from 'node:zlib';

export class FileCache {
  constructor(directory, { maxBytes = 1024 ** 3, maxFileBytes = 256 * 1024 ** 2, maxEntries = 4096 } = {}) {
    this.directory = directory;
    this.maxBytes = maxBytes;
    this.maxFileBytes = maxFileBytes;
    this.maxEntries = maxEntries;
    this.entries = new Map();
    this.inflight = new Map();
    this.readers = new Map();
    this.saving = Promise.resolve();
    this.budget = Promise.resolve();
    this.reserved = 0;
    this.retired = new Set();
    this.generations = new Map();
  }

  key(root, relative) { return createHash('sha256').update(JSON.stringify([root, relative])).digest('hex'); }

  async init() {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const decoded = await readFile(path.join(this.directory, 'index.json'), 'utf8').then(JSON.parse).catch(() => []);
    const manifest = Array.isArray(decoded) ? decoded : [];
    for (const entry of manifest.slice(0, this.maxEntries)) {
      if (!/^[a-f0-9]{64}$/.test(entry.key) || !/^[a-f0-9-]+\.bin$/.test(entry.file) || !Number.isSafeInteger(entry.size) || entry.size < 0) continue;
      const size = await stat(path.join(this.directory, entry.file)).then(info => info.size).catch(() => -1);
      if (size === entry.size) this.entries.set(entry.key, entry);
    }
    const retained = new Set([...this.entries.values()].map(entry => entry.file));
    for (const file of await readdir(this.directory)) {
      if (/^[a-f0-9-]+\.bin(?:\.part)?$/.test(file) && !retained.has(file)) await unlink(path.join(this.directory, file));
    }
    await this.evict();
  }

  get(root, relative, revision) {
    const value = this.entries.get(this.key(root, relative));
    if (!value || (revision !== undefined && value.revision !== revision)) return null;
    value.used = Date.now();
    return { ...value, diskPath: path.join(this.directory, value.file) };
  }

  summary() {
    const values = [...this.entries.values()];
    return { bytes: values.reduce((sum, entry) => sum + entry.size, 0), maxBytes: this.maxBytes, files: values.length, pinned: values.filter(entry => entry.pinned).length, inflight: this.inflight.size };
  }

  persist() {
    const snapshot = JSON.stringify([...this.entries.values()]);
    this.saving = this.saving.catch(() => {}).then(async () => {
      const temporary = path.join(this.directory, 'index.json.tmp');
      await writeFile(temporary, snapshot, { mode: 0o600 });
      await rename(temporary, path.join(this.directory, 'index.json'));
    });
    return this.saving;
  }

  async pin(root, relative, value) {
    const entry = this.entries.get(this.key(root, relative));
    if (!entry) throw new Error('Download the complete file before keeping it offline.');
    entry.pinned = Boolean(value);
    await this.persist();
    return this.get(root, relative);
  }

  async retarget(root,from,to,operation){
    const key=this.key(root,from),next=to?this.key(root,to):this.key('retained',operation);
    this.generations.set(key,(this.generations.get(key)||0)+1);
    const entry=this.entries.get(key);if(!entry)return;
    if(this.entries.has(next))throw Error('The destination has an independent cached copy. Both copies were preserved.');
    this.entries.delete(key);this.entries.set(next,{...entry,key:next,...(to?{relative:to}:{root:'retained',relative:operation,originalRoot:root,originalPath:from})});
    await this.persist();
  }

  async evict(reserve = 0, replaceKey) {
    let bytes = this.summary().bytes;
    const candidates = [...this.entries.values()].filter(entry => !entry.pinned && !this.readers.has(entry.key) && !this.inflight.has(entry.key) && entry.key !== replaceKey).sort((a, b) => a.used - b.used);
    for (const entry of candidates) {
      if (bytes + reserve <= this.maxBytes && this.entries.size < this.maxEntries) break;
      await unlink(path.join(this.directory, entry.file)).catch(error => { if (error.code !== 'ENOENT') throw error; });
      this.entries.delete(entry.key);
      bytes -= entry.size;
    }
    if (bytes + reserve > this.maxBytes || this.entries.size >= this.maxEntries) throw new Error('Cache is full of pinned or open files.');
  }

  async fetch(root, relative, metadata, loader) {
    const key = this.key(root, relative);
    const existing = this.get(root, relative, metadata.revision);
    if (existing) return existing;
    if (!Number.isSafeInteger(metadata.size) || metadata.size < 0 || metadata.size > this.maxFileBytes || metadata.size > this.maxBytes) throw new Error('This file is stream-only: it exceeds the cache-file ceiling.');
    if (this.inflight.has(key)) {
      await this.inflight.get(key);
      const completed = this.get(root, relative, metadata.revision);
      if (completed) return completed;
    }
    const operation = this.download(key, root, relative, metadata, loader);
    this.inflight.set(key, operation);
    try { return await operation; } finally { this.inflight.delete(key); }
  }

  async download(key, root, relative, metadata, loader) {
    const generation=this.generations.get(key)||0;
    this.budget = this.budget.catch(() => {}).then(async () => {
      await this.evict(metadata.size + this.reserved, key);
      this.reserved += metadata.size;
    });
    await this.budget;
    const old = this.entries.get(key);
    const file = `${randomUUID()}.bin`;
    const diskPath = path.join(this.directory, file);
    const temporary = `${diskPath}.part`;
    let received = 0;
    let checksum = 0;
    try {
      const response = await loader();
      if (response.headers?.etag && response.headers.etag !== metadata.revision) { response.destroy(); throw new Error('The remote file changed. Refresh before opening it.'); }
      const counter = new Transform({ transform(chunk, _encoding, callback) {
        received += chunk.length;
        if(metadata.crc32!==undefined)checksum=crc32(chunk,checksum);
        callback(received > metadata.size ? new Error('Remote size changed during download.') : null, chunk);
      } });
      await pipeline(response, counter, createWriteStream(temporary, { flags: 'wx', mode: 0o600 }));
      if (received !== metadata.size) throw new Error('Incomplete download; no cache entry was published.');
      if(metadata.crc32!==undefined&&checksum!==metadata.crc32)throw new Error('Attachment checksum failed; no cache entry was published.');
      if((this.generations.get(key)||0)!==generation)throw new Error('The source path moved while downloading. Reopen its accepted destination.');
      await rename(temporary, diskPath);
      this.entries.set(key, { key, root, relative, revision: metadata.revision, size: received, modifiedAt: metadata.modified_at, file, used: Date.now(), pinned: old?.pinned || false });
      await this.persist();
      if (old) {
        if (!this.readers.has(key)) await unlink(path.join(this.directory, old.file)).catch(() => {});
        else this.retired.add(path.join(this.directory, old.file));
      }
      return this.get(root, relative, metadata.revision);
    } catch (error) {
      await unlink(temporary).catch(() => {});
      throw error;
    } finally { this.reserved -= metadata.size; }
  }

  stream(entry, options) {
    this.readers.set(entry.key, (this.readers.get(entry.key) || 0) + 1);
    const stream = createReadStream(entry.diskPath, options);
    stream.once('close', () => {
      const count = this.readers.get(entry.key) - 1;
      if (count > 0) this.readers.set(entry.key, count); else this.readers.delete(entry.key);
      if (!this.readers.size) for (const file of this.retired) {
        this.retired.delete(file);
        unlink(file).catch(() => {});
      }
    });
    return stream;
  }
}
