import { createHash, randomUUID } from 'node:crypto';
import { constants } from 'node:fs';
import { link, lstat, open, realpath, unlink } from 'node:fs/promises';
import path from 'node:path';
import { addAbortSignal } from 'node:stream';

const MAX_BYTES = 512 * 1024 * 1024;
const MAX_SUMMARY_HEADER = 16 * 1024;

function validateRequest(project, mode, output) {
  if (typeof project !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(project) || project.includes('..')) throw Error('Invalid project reference.');
  if (!['human', 'portable', 'archival'].includes(mode)) throw Error('Invalid project export mode.');
  if (typeof output !== 'string' || output.includes('\0') || !path.isAbsolute(output) || path.resolve(output) !== output || output === path.parse(output).root) throw Error('Export output must be an exact absolute file path.');
}

function readSummary(response, mode, source) {
  if (response?.statusCode !== 200) throw Error('Main did not return a complete project export.');
  const encoded = response.headers?.['x-loom-project-export-summary'];
  const length = response.headers?.['content-length'];
  if (typeof encoded !== 'string' || encoded.length > MAX_SUMMARY_HEADER || !/^[A-Za-z0-9_-]+$/.test(encoded)) throw Error('Invalid or oversized project export summary header.');
  const raw = Buffer.from(encoded, 'base64url');
  if (raw.toString('base64url') !== encoded) throw Error('Invalid project export summary encoding.');
  let summary;
  try { summary = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(raw)); }
  catch { throw Error('Invalid project export summary JSON.'); }
  if (!summary || Array.isArray(summary) || summary.schema_version !== (source?'loom.directory_export.v1':'loom.project_export.v1') || summary.mode !== mode ||
      typeof summary.project_slug !== 'string' || !/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(summary.project_slug)) throw Error('Project export summary identity or mode mismatch.');
  if (!Number.isSafeInteger(summary.archive_bytes) || summary.archive_bytes <= 0 || summary.archive_bytes > MAX_BYTES ||
      typeof length !== 'string' || !/^[1-9][0-9]{0,9}$/.test(length) || Number(length) !== summary.archive_bytes) throw Error('Project export length is invalid, mismatched or exceeds 512 MiB.');
  if (typeof summary.archive_checksum !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(summary.archive_checksum)) throw Error('Invalid project export checksum.');
  for (const value of [summary.included, summary.ignored]) {
    if (!value || Array.isArray(value) || !Number.isSafeInteger(value.count) || value.count < 0 || !Number.isSafeInteger(value.bytes) || value.bytes < 0) throw Error('Invalid project export count summary.');
  }
  if(source&&(summary.source_root!==source.root||summary.source_path!==source.path||summary.source_revision!==source.revision))throw Error('Directory snapshot source identity mismatch.');
  return {
    schema_version: summary.schema_version, project_slug: summary.project_slug, mode: summary.mode,
    archive_bytes: summary.archive_bytes, archive_checksum: summary.archive_checksum,
    included: { count: summary.included.count, bytes: summary.included.bytes },
    ignored: { count: summary.ignored.count, bytes: summary.ignored.bytes },
    ...(source?{source_root:summary.source_root,source_path:summary.source_path,source_revision:summary.source_revision}:{}),
  };
}

async function removeOwnPartial(temporary, identity) {
  if (!identity) return;
  let info;
  try { info = await lstat(temporary); }
  catch (error) { if (error.code === 'ENOENT') return; throw error; }
  if (info.isFile() && info.dev === identity.dev && info.ino === identity.ino) await unlink(temporary);
}

// This is an explicit policy-filtered snapshot download, not a working-copy
// importer or a filesystem backup. Publication links only our verified partial.
export async function exportProjectSnapshot(remote, { project, mode, output, signal }) {
  validateRequest(project, mode, output);
  return downloadSnapshot(remote,{mode,output,signal,body:{project_ref:project,mode,max_bytes:MAX_BYTES},route:'/v1/project-exports'});
}

export async function exportDirectorySnapshot(remote,{source,planHash,output,signal}){
  validateRequest('directory-snapshot','portable',output);
  if(!source||!['box','storage'].includes(source.root)||typeof source.path!=='string'||!source.path||path.posix.isAbsolute(source.path)||path.posix.normalize(source.path)!==source.path||source.path.split('/').includes('..')||typeof source.revision!=='string'||!/^sha256:[a-f0-9]{64}$/.test(planHash))throw Error('Review an exact directory source before copying.');
  return downloadSnapshot(remote,{mode:'portable',output,signal,source,route:'/v1/files/snapshot',body:{...source,action:'apply',plan_hash:planHash}});
}

async function downloadSnapshot(remote,{mode,output,signal,source,route,body}){
  if (typeof remote?.apiRequest !== 'function') throw Error('Main export transport is unavailable.');
  signal?.throwIfAborted();
  const parent = await realpath(path.dirname(output));
  const destination = path.join(parent, path.basename(output));
  try {
    await lstat(destination);
    throw Object.assign(Error('Export destination already exists.'), { code: 'EEXIST' });
  } catch (error) { if (error.code !== 'ENOENT') throw error; }

  const temporary = path.join(parent, `.loom-project-export-${randomUUID()}.partial`);
  let directory, file, identity, response;
  try {
    directory = await open(parent, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
    const parentIdentity = await directory.stat();
    file = await open(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    identity = await file.stat();
    await file.chmod(0o600);
    signal?.throwIfAborted();
    response = await remote.apiRequest(route, {}, {
      lane: 'content', method: 'POST', body, signal,
    });
    signal?.throwIfAborted();
    const summary = readSummary(response, mode, source);
    if (signal) addAbortSignal(signal, response);
    const hash = createHash('sha256');
    let received = 0;
    for await (const chunk of response) {
      signal?.throwIfAborted();
      if (!(chunk instanceof Uint8Array)) throw Error('Project export stream returned non-binary data.');
      received += chunk.byteLength;
      if (received > MAX_BYTES || received > summary.archive_bytes) throw Error('Project export stream exceeds its declared byte limit.');
      hash.update(chunk);
      await file.writeFile(chunk);
    }
    if (received !== summary.archive_bytes) throw Error('Project export stream length does not match its summary.');
    if (`sha256:${hash.digest('hex')}` !== summary.archive_checksum) throw Error('Project export checksum does not match its summary.');
    signal?.throwIfAborted();
    await file.sync();
    const saved = await file.stat();
    const named = await lstat(temporary);
    const currentParent = await lstat(parent);
    if (!named.isFile() || named.dev !== saved.dev || named.ino !== saved.ino || named.size !== received || (named.mode & 0o777) !== 0o600 ||
        !currentParent.isDirectory() || currentParent.dev !== parentIdentity.dev || currentParent.ino !== parentIdentity.ino) throw Error('Export destination or private partial changed during download.');
    await file.close();
    file = undefined;
    signal?.throwIfAborted();
    await link(temporary, destination); // Atomic no-replace; never rename over user output.
    await removeOwnPartial(temporary, identity);
    await directory.sync();
    return { path: destination, summary };
  } finally {
    response?.destroy();
    try {
      await file?.close();
    } finally {
      try { await removeOwnPartial(temporary, identity); }
      finally { await directory?.close(); }
    }
  }
}
