import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { lstat, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { PassThrough, Readable } from 'node:stream';
import { exportProjectSnapshot } from '../src/project-export.mjs';

const MAX_BYTES = 512 * 1024 * 1024;
const project = 'project_01ARZ3NDEKTSV4RRFFQ69G5FAV';
const payload = Buffer.from('owner-produced policy-filtered tar stream\0\n');
const checksum = `sha256:${createHash('sha256').update(payload).digest('hex')}`;
const userPartial = '.loom-project-export-user.partial';

async function fixture(t) {
  const directory = await realpath(await mkdtemp(path.join(os.tmpdir(), 'loom-project-export-')));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await writeFile(path.join(directory, userPartial), 'user partial');
  return { directory, output: path.join(directory, 'snapshot.tar') };
}

function reply(changes = {}, headers = {}) {
  const summary = {
    schema_version: 'loom.project_export.v1', project_slug: 'demo-project', mode: 'portable',
    archive_bytes: payload.length, archive_checksum: checksum,
    included: { count: 3, bytes: 15 }, ignored: { count: 2, bytes: 999999999 },
    ...changes,
  };
  const response = Readable.from([payload.subarray(0, 5), payload.subarray(5, 11), payload.subarray(11)]);
  response.statusCode = 200;
  response.headers = {
    'content-length': String(summary.archive_bytes),
    'x-loom-project-export-summary': Buffer.from(JSON.stringify(summary)).toString('base64url'),
    ...headers,
  };
  return response;
}

test('exports each exact mode using the owner summary and private no-replace publication', async t => {
  for (const mode of ['human', 'portable', 'archival']) await t.test(mode, async t => {
    const { directory, output } = await fixture(t);
    const source = path.join(directory, 'source.tar');
    await writeFile(source, payload, { mode: 0o640 });
    const before = await lstat(source);
    const controller = new AbortController();
    let requests = 0;
    const remote = { async apiRequest(route, query, options) {
      requests++;
      assert.equal(route, '/v1/project-exports');
      assert.deepEqual(query, {});
      assert.deepEqual(options, { lane: 'content', method: 'POST', body: { project_ref: project, mode, max_bytes: MAX_BYTES }, signal: controller.signal });
      const partials = (await readdir(directory)).filter(name => name.endsWith('.partial') && name !== userPartial);
      assert.equal(partials.length, 1);
      assert.equal((await lstat(path.join(directory, partials[0]))).mode & 0o777, 0o600);
      await assert.rejects(lstat(output), { code: 'ENOENT' });
      return reply({ mode, output_path: '/untrusted/server/path.tar' });
    } };
    const result = await exportProjectSnapshot(remote, { project, mode, output, signal: controller.signal });
    assert.equal(requests, 1);
    assert.equal(result.path, output);
    assert.equal(result.summary.project_slug, 'demo-project'); // A slug is not the requested stable ID.
    assert.equal(result.summary.mode, mode);
    assert.equal(result.summary.archive_checksum, checksum);
    assert.equal(result.summary.output_path, undefined);
    assert.deepEqual(result.summary.ignored, { count: 2, bytes: 999999999 });
    assert.deepEqual(await readFile(output), payload);
    const saved = await lstat(output);
    assert.equal(saved.mode & 0o777, 0o600);
    assert.equal(saved.nlink, 1);
    assert.deepEqual(await readdir(directory), [userPartial, 'snapshot.tar', 'source.tar']);
    const after = await lstat(source);
    assert.deepEqual(await readFile(source), payload);
    assert.equal(after.ino, before.ino);
    assert.equal(after.mode, before.mode);
    assert.equal(after.mtimeMs, before.mtimeMs);
  });
});

test('invalid references, modes and output paths are rejected before HTTP or temporary writes', async t => {
  const { directory, output } = await fixture(t);
  let requests = 0;
  const remote = { async apiRequest() { requests++; throw Error('must not request'); } };
  for (const invalid of [
    { project: '' }, { project: '../escape' }, { project: '/source/path' }, { project: ' name' }, { project: 'one..two' },
    { project: 'a'.repeat(129) }, { project: { id: project } },
    { mode: 'Portable' }, { mode: ' portable' }, { mode: 'copy' }, { mode: undefined },
    { output: 'relative.tar' }, { output: '/' }, { output: `${output}\0` }, { output: null },
  ]) await assert.rejects(exportProjectSnapshot(remote, { project, mode: 'portable', output, ...invalid }), /Invalid project|exact absolute/);
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(exportProjectSnapshot(remote, { project, mode: 'portable', output, signal: controller.signal }), { name: 'AbortError' });
  assert.equal(requests, 0);
  assert.deepEqual(await readdir(directory), [userPartial]);
});

test('malformed, mismatched and oversized response headers fail without publishing', async t => {
  const cases = [
    ['schema', { schema_version: 'other.v1' }],
    ['mode', { mode: 'archival' }],
    ['unsafe slug', { project_slug: '../source' }],
    ['unsafe checksum', { archive_checksum: 'sha256:bad' }],
    ['oversized archive', { archive_bytes: MAX_BYTES + 1 }],
    ['fractional size', { archive_bytes: 1.5 }],
    ['zero size', { archive_bytes: 0 }],
    ['missing counts', { included: null }],
    ['negative counts', { ignored: { count: -1, bytes: 0 } }],
    ['unsafe integer', { ignored: { count: 0, bytes: Number.MAX_SAFE_INTEGER + 1 } }],
    ['length mismatch', {}, { 'content-length': String(payload.length + 1) }],
    ['missing length', {}, { 'content-length': undefined }],
    ['duplicate length', {}, { 'content-length': [String(payload.length), String(payload.length)] }],
    ['missing summary', {}, { 'x-loom-project-export-summary': undefined }],
    ['bad encoding', {}, { 'x-loom-project-export-summary': 'bad+base64' }],
    ['bad JSON', {}, { 'x-loom-project-export-summary': Buffer.from('{bad').toString('base64url') }],
    ['oversized header', {}, { 'x-loom-project-export-summary': 'a'.repeat(16 * 1024 + 1) }],
  ];
  for (const [name, summary, headers] of cases) await t.test(name, async t => {
    const { directory, output } = await fixture(t);
    const response = reply(summary, headers);
    let requests = 0;
    await assert.rejects(exportProjectSnapshot({ async apiRequest() { requests++; return response; } }, { project, mode: 'portable', output }), /summary|length|checksum|encoding/);
    assert.equal(requests, 1);
    assert.equal(response.destroyed, true);
    assert.deepEqual(await readdir(directory), [userPartial]);
  });
});

test('stream checksum, truncation and streamed overrun are verified before publication', async t => {
  for (const kind of ['checksum', 'truncated', 'overrun']) await t.test(kind, async t => {
    const { directory, output } = await fixture(t);
    let response;
    if (kind === 'checksum') response = reply({ archive_checksum: `sha256:${'0'.repeat(64)}` });
    else {
      const template = reply();
      response = Readable.from(kind === 'truncated' ? [payload.subarray(0, -1)] : [payload, Buffer.from('extra')]);
      response.statusCode = template.statusCode;
      response.headers = template.headers;
      template.destroy();
    }
    await assert.rejects(exportProjectSnapshot({ async apiRequest() { return response; } }, { project, mode: 'portable', output }), /checksum|length|byte limit/);
    assert.equal(response.destroyed, true);
    assert.deepEqual(await readdir(directory), [userPartial]);
  });
});

test('an existing output file, directory or dangling symlink refuses without a request', async t => {
  for (const kind of ['file', 'directory', 'symlink']) await t.test(kind, async t => {
    const { directory, output } = await fixture(t);
    if (kind === 'file') await writeFile(output, 'user output');
    else if (kind === 'directory') await mkdir(output);
    else await symlink('missing-user-target', output);
    const before = await lstat(output);
    let requests = 0;
    await assert.rejects(exportProjectSnapshot({ async apiRequest() { requests++; return reply(); } }, { project, mode: 'portable', output }), { code: 'EEXIST' });
    assert.equal(requests, 0);
    assert.equal((await lstat(output)).ino, before.ino);
    if (kind === 'file') assert.equal(await readFile(output, 'utf8'), 'user output');
    assert.deepEqual(await readdir(directory), [userPartial, 'snapshot.tar']);
  });
});

test('a late output collision is never replaced and only the private partial is removed', async t => {
  const { directory, output } = await fixture(t);
  let requests = 0;
  const remote = { async apiRequest() {
    requests++;
    await writeFile(output, 'late user output', { flag: 'wx', mode: 0o640 });
    return reply();
  } };
  await assert.rejects(exportProjectSnapshot(remote, { project, mode: 'portable', output }), { code: 'EEXIST' });
  assert.equal(requests, 1);
  assert.equal(await readFile(output, 'utf8'), 'late user output');
  assert.equal((await lstat(output)).mode & 0o777, 0o640);
  assert.deepEqual(await readdir(directory), [userPartial, 'snapshot.tar']);
});

test('request and stream failures clean up without retry or changes to unrelated partials', async t => {
  for (const kind of ['request', 'stream']) await t.test(kind, async t => {
    const { directory, output } = await fixture(t);
    const failure = Error('connection lost');
    let requests = 0;
    const remote = { async apiRequest() {
      requests++;
      if (kind === 'request') throw failure;
      const template = reply();
      const response = Readable.from((async function* () { yield payload.subarray(0, 5); throw failure; })());
      response.statusCode = 200;
      response.headers = template.headers;
      template.destroy();
      return response;
    } };
    await assert.rejects(exportProjectSnapshot(remote, { project, mode: 'portable', output }), error => error === failure);
    assert.equal(requests, 1);
    assert.deepEqual(await readdir(directory), [userPartial]);
    assert.equal(await readFile(path.join(directory, userPartial), 'utf8'), 'user partial');
  });
});

test('cancellation interrupts a stalled stream and removes its private partial', async t => {
  const { directory, output } = await fixture(t);
  const controller = new AbortController();
  const response = new PassThrough();
  const template = reply();
  response.statusCode = 200;
  response.headers = template.headers;
  template.destroy();
  let timer;
  t.after(() => clearTimeout(timer));
  const remote = { async apiRequest() {
    response.write(payload.subarray(0, 5));
    timer = setTimeout(() => controller.abort(), 20);
    return response;
  } };
  await assert.rejects(exportProjectSnapshot(remote, { project, mode: 'portable', output, signal: controller.signal }), { name: 'AbortError' });
  assert.equal(response.destroyed, true);
  assert.deepEqual(await readdir(directory), [userPartial]);
});

test('failure cleanup preserves a replacement at the private partial name', async t => {
  const { directory, output } = await fixture(t);
  let replacement;
  const remote = { async apiRequest() {
    replacement = (await readdir(directory)).find(name => name !== userPartial);
    await rename(path.join(directory, replacement), path.join(directory, 'retained-owned-file'));
    await writeFile(path.join(directory, replacement), 'external replacement', { flag: 'wx' });
    return reply();
  } };
  await assert.rejects(exportProjectSnapshot(remote, { project, mode: 'portable', output }), /private partial changed/);
  assert.equal(await readFile(path.join(directory, replacement), 'utf8'), 'external replacement');
  assert.equal(await readFile(path.join(directory, userPartial), 'utf8'), 'user partial');
  await assert.rejects(lstat(output), { code: 'ENOENT' });
});
