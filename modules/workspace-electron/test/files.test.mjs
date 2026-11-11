import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, mkdir, symlink, rm } from 'node:fs/promises';
import { Readable } from 'node:stream';
import path from 'node:path';
import os from 'node:os';
import { FileCache } from '../src/cache.mjs';
import { LocalFiles, normalize } from '../src/local-files.mjs';

async function temporary(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'loom-files-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return directory;
}

test('local notes list empty folders and reject traversal/symlinks', async t => {
  const directory = await temporary(t);
  await mkdir(path.join(directory, 'empty'));
  await writeFile(path.join(directory, 'note.md'), '# Local');
  await symlink('/etc/passwd', path.join(directory, 'escape.md'));
  const files = new LocalFiles(directory);
  assert.deepEqual((await files.list()).entries.map(value => value.name).sort(), ['empty', 'note.md']);
  assert.throws(() => normalize('../elsewhere'));
  assert.throws(() => normalize('.obsidian/data.json'));
  await assert.rejects(files.read('escape.md'), /Symbolic links/);
  assert.equal((await files.list('empty')).entries.length, 0);
});

test('local save preserves a concurrent edit and accepts the exact base', async t => {
  const directory = await temporary(t);
  const note = path.join(directory, 'note.md');
  await writeFile(note, 'original');
  const files = new LocalFiles(directory);
  const first = await files.read('note.md');
  await writeFile(note, 'newer Obsidian edit');
  await assert.rejects(files.save('note.md', 'stale draft', first.base), /changed on disk/);
  assert.equal(await readFile(note, 'utf8'), 'newer Obsidian edit');
  const fresh = await files.read('note.md');
  const saved = await files.save('note.md', 'accepted edit', fresh.base);
  assert.equal(saved.text, 'accepted edit');
  assert.equal(await readFile(note, 'utf8'), 'accepted edit');
});

test('cache shares downloads, persists complete files and refuses partial content', async t => {
  const directory = await temporary(t);
  const cache = new FileCache(directory);
  await cache.init();
  let calls = 0;
  const load = async () => { calls++; return Readable.from(['hello']); };
  const [one, two] = await Promise.all([cache.fetch('box', 'note.md', { size: 5, revision: 'one' }, load), cache.fetch('box', 'note.md', { size: 5, revision: 'one' }, load)]);
  assert.equal(calls, 1);
  assert.equal(one.diskPath, two.diskPath);
  assert.equal(await readFile(one.diskPath, 'utf8'), 'hello');
  const reloaded = new FileCache(directory);
  await reloaded.init();
  assert.ok(reloaded.get('box', 'note.md', 'one'));
  assert.equal(reloaded.get('box', 'note.md', 'changed'), null);
  await assert.rejects(cache.fetch('box', 'broken.md', { size: 10, revision: 'one' }, load), /Incomplete/);
  assert.equal(cache.get('box', 'broken.md'), null);
});

test('cache is bounded and never evicts pinned entries', async t => {
  const directory = await temporary(t);
  const cache = new FileCache(directory, { maxBytes: 10 });
  await cache.init();
  const load = async () => Readable.from(['hello']);
  await cache.fetch('box', 'one', { size: 5, revision: 'one' }, load);
  await cache.pin('box', 'one', true);
  await cache.fetch('box', 'two', { size: 5, revision: 'two' }, load);
  await cache.fetch('box', 'three', { size: 5, revision: 'three' }, load);
  assert.ok(cache.get('box', 'one'));
  assert.equal(cache.get('box', 'two'), null);
  assert.equal(cache.summary().bytes, 10);
});

test('simultaneous cache downloads reserve their disk budget', async t => {
  const cache = new FileCache(await temporary(t), { maxBytes: 5 });
  await cache.init();
  let complete;
  const gate = new Promise(resolve => { complete = resolve; });
  const first = cache.fetch('box', 'first', { size: 5, revision: 'one' }, async () => { await gate; return Readable.from(['hello']); });
  await new Promise(resolve => setTimeout(resolve, 10));
  await assert.rejects(cache.fetch('box', 'second', { size: 5, revision: 'two' }, async () => Readable.from(['world'])), /Cache is full/);
  complete();
  await first;
  assert.equal(cache.summary().bytes, 5);
  assert.equal(cache.reserved, 0);
});

test('changed response revision cannot replace a complete cached file', async t => {
  const cache = new FileCache(await temporary(t));
  await cache.init();
  await cache.fetch('box', 'note', { size: 5, revision: 'old' }, async () => Readable.from(['hello']));
  await assert.rejects(cache.fetch('box', 'note', { size: 5, revision: 'new' }, async () => {
    const stream = Readable.from(['world']); stream.headers = { etag: 'different' }; return stream;
  }), /remote file changed/);
  assert.ok(cache.get('box', 'note', 'old'));
  assert.equal(cache.get('box', 'note', 'new'), null);
});
