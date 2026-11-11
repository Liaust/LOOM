import test from 'node:test';
import assert from 'node:assert/strict';
import { DocumentSaver } from '../renderer/document-saver.js';

test('an older save advances its base without clearing a newer edit or draft', async () => {
  let release, started; const wait = new Promise(resolve => release = resolve), began = new Promise(resolve => started = resolve);
  const drafts = [], writes = [], document = { text: 'first', base: 'base0', dirty: true, editable: true };
  const saver = new DocumentSaver({ persist: async value => drafts.push(value.text), write: async (_, text, base) => { writes.push({ text, base }); if (text === 'first') { started(); await wait; } return { base: `base${writes.length}`, entry: {}, pending: true }; } });
  const saving = saver.save(document); await began; document.text = 'second'; document.dirty = true; release(); await saving;
  assert.equal(document.dirty, true); assert.equal(document.base, 'base1'); assert.equal(drafts.at(-1), 'second');
  await saver.save(document); assert.deepEqual(writes, [{ text: 'first', base: 'base0' }, { text: 'second', base: 'base1' }]); assert.equal(document.dirty, false);
});

test('typing coalesces, with the final text committed through the ordinary writer', async () => {
  const writes = [], document = { text: 'a', base: 'base', dirty: true, editable: true };
  const saver = new DocumentSaver({ delay: 5, persist: async () => {}, write: async (_, text) => { writes.push(text); return { base: 'next', entry: {}, pending: true }; } });
  saver.schedule(document); document.text = 'ab'; saver.schedule(document); document.text = 'abc'; saver.schedule(document);
  await new Promise(resolve => setTimeout(resolve, 20)); assert.deepEqual(writes, ['abc']); assert.equal(document.dirty, false);
});

test('a source-owner refusal leaves the latest draft dirty and does not retry in a loop', async () => {
  let writes = 0, failures = 0; const drafts = [], document = { text: 'keep me', base: 'base', dirty: true, editable: true };
  const saver = new DocumentSaver({ persist: async value => drafts.push(value.text), write: async () => { writes++; throw Error('conflicting local source'); }, failed: () => failures++ });
  assert.equal(await saver.save(document), false); assert.equal(document.dirty, true); assert.equal(document.base, 'base');
  assert.equal(writes, 1); assert.equal(failures, 1); assert.deepEqual(drafts, ['keep me']);
});

test('closing/switching documents flushes their own text, and read-only files never write', async () => {
  const writes = [], a = { path: 'a.md', text: 'a', dirty: true, editable: true }, b = { path: 'b.py', text: 'b', dirty: true, editable: true };
  const saver = new DocumentSaver({ persist: async () => {}, write: async (value, text) => { writes.push([value.path, text]); return { base: 'saved', entry: {} }; } });
  await saver.flush([a, b, { path: 'archive.md', text: 'read only', editable: false, dirty: true }]); assert.deepEqual(writes, [['a.md', 'a'], ['b.py', 'b']]);
});
