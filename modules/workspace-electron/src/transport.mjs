import { spawn, execFile } from 'node:child_process';
import { mkdir, stat, unlink } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { promisify } from 'node:util';
import {BoxRoots,isBoxRef} from './box-roots.mjs';

const wait = ms => new Promise(resolve => setTimeout(resolve, ms));

export function scopedQuery(boxes,route,query={},body) {
  const result={...query};
  if(route==='/v1/files/roots')result.box_context='v1';
  const object=body&&typeof body==='object'&&!Buffer.isBuffer(body)?body:{};
  const roots=[query.root,object.root,object.from_root].filter(isBoxRef);
  const aliases=[query.box,object.box].filter(value=>value!==undefined&&value!=='');
  const ids=[query.box_id,object.box_id].filter(value=>value!==undefined&&value!=='');
  const all=query.all_boxes===true||query.all_boxes==='true'||object.all_boxes===true;
  if(all&&(roots.length||aliases.length||ids.length))throw Error('All Boxes conflicts with a bound request.');
  if(roots.length||aliases.length||ids.length){
    const selections=[...roots,...aliases,...ids].map(value=>boxes.selection({box:value}));
    if(selections.some(value=>value.root!==selections[0].root))throw Error('Request and saved body belong to different Boxes.');
    if(query.box_key)boxes.identity(selections[0].root,undefined,query.box_key);
    if(selections[0].box_id)result.box_id=selections[0].box_id;
  }
  // Replica/navigation descriptors are not owner API query parameters.
  delete result.box_key;delete result.box;
  if(isBoxRef(result.root)&&!route.startsWith('/v1/files/'))delete result.root;
  if(all)result.all_boxes=true;
  return result;
}

export function queryParameters(query) {
  const values=new URLSearchParams();
  for(const [name,value] of Object.entries(query))if(value!==undefined&&value!==null)for(const item of Array.isArray(value)?value:[value])values.append(name,String(item));
  return values;
}

export class MainFiles {
  constructor(directory, { host = 'loom-main', remoteSocket = '/run/loom/loomd.sock',boxes=new BoxRoots() } = {}) {
    this.directory = directory;
    this.host = host;
    this.remoteSocket = remoteSocket;
    this.lanes = new Map();
    this.closed = false;
    this.applicationViews=new Map();
    this.boxes=boxes;
  }

  async lane(name) {
    if (this.closed) throw new Error('Connection closed.');
    if (this.lanes.has(name)) return this.lanes.get(name);
    const opening = this.startLane(name);
    this.lanes.set(name, opening);
    opening.catch(() => { if (this.lanes.get(name) === opening) this.lanes.delete(name); });
    return opening;
  }

  async startLane(name) {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const socket = path.join(this.directory, `${name}.sock`);
    await unlink(socket).catch(error => { if (error.code !== 'ENOENT') throw error; });
    let diagnostic = '';
    let exit;
    const environment = { ...process.env };
    // GUI apps do not necessarily inherit the login shell's ssh-agent socket.
    // Ask the existing macOS launch agent, never copy credentials into the app.
    if (!environment.SSH_AUTH_SOCK && process.platform === 'darwin') {
      const { stdout } = await promisify(execFile)('/bin/launchctl', ['getenv', 'SSH_AUTH_SOCK']);
      const candidate = stdout.trim();
      if (candidate && await stat(candidate).then(info => info.isSocket()).catch(() => false)) environment.SSH_AUTH_SOCK = candidate;
    }
    const child = spawn('/usr/bin/ssh', [
      '-T', '-N', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
      '-o', 'ExitOnForwardFailure=yes', '-o', 'ConnectTimeout=8',
      '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=3',
      '-o', 'ControlMaster=no', '-S', 'none',
      '-L', `${socket}:${this.remoteSocket}`, this.host,
    ], { stdio: ['ignore', 'ignore', 'pipe'], env: environment });
    child.stderr.on('data', data => { diagnostic = (diagnostic + data).slice(-1200); });
    child.on('error', error => { exit = error.message; });
    const agent = new http.Agent({ keepAlive: true, maxSockets: ['watch','explorer-watch'].includes(name) ? 16 : name === 'metadata' ? 4 : 2, maxFreeSockets: 2 });
    child.on('exit', code => {
      exit = `SSH disconnected (${code}). ${diagnostic.trim()}`;
      agent.destroy();
      this.lanes.delete(name);
    });
    for (let attempt = 0; attempt < 200; attempt++) {
      if (exit || this.closed) break;
      if (await stat(socket).then(value => value.isSocket()).catch(() => false)) return { socket, child, agent };
      await wait(50);
    }
    child.kill();
    throw new Error(exit || `Main connection unavailable. ${diagnostic.trim()}`);
  }

  async request(endpoint, query = {}, options = {}) {
    if(query.root&&this.applicationViews.has(query.root))query={...query,view_revision:this.applicationViews.get(query.root)};
    return this.apiRequest(`/v1/files/${endpoint}`, query, options);
  }

  setApplicationViews(roots){this.applicationViews=new Map(roots.filter(root=>root.source_kind==='application_data'&&root.view_revision).map(root=>[root.ref,root.view_revision]));}
  setBoxes(boxes){this.boxes=boxes instanceof BoxRoots?boxes:new BoxRoots(boxes);return this.boxes;}

  async apiRequest(route, query = {}, { lane = 'metadata', headers = {}, signal, method = 'GET', body } = {}) {
    if (!/^\/v1\/[a-z0-9_./-]+$/i.test(route) || route.includes('..')) throw new Error('Invalid API route.');
    // Resolve before the first await: sidebar/catalog changes cannot retarget
    // queued requests while a transport lane is opening.
    query=scopedQuery(this.boxes,route,query,body);
    const parameters=queryParameters(query);
    const binary = Buffer.isBuffer(body);
    const payload = body === undefined ? null : binary ? body : JSON.stringify(body);
    if (payload && Buffer.byteLength(payload) > 4 * 1024 * 1024) throw new Error('Request exceeds 4 MiB.');
    const started = performance.now();
    const connection = await this.lane(lane);
    if (signal?.aborted) throw new Error('Request cancelled.');
    return new Promise((resolve, reject) => {
      const request = http.request({
        socketPath: connection.socket, path: route + (parameters.size ? `?${parameters}` : ''),
        method, agent: connection.agent, headers: { ...(payload ? { 'Content-Type': binary ? 'application/octet-stream' : 'application/json', 'Content-Length': Buffer.byteLength(payload) } : {}), ...headers }, signal,
      }, response => {
        if (['watch','explorer-watch'].includes(lane) && response.statusCode === 200) request.setTimeout(0);
        response.loomMs = performance.now() - started;
        if (response.statusCode >= 200 && response.statusCode < 300) return resolve(response);
        let body = '';
        response.on('data', data => { body += data; if (body.length > 65536) request.destroy(new Error('Oversized error response.')); });
        response.on('end', () => {
          let message, envelope;
          try { envelope = JSON.parse(body); message = envelope.error?.summary; } catch { /* Not all transport failures contain JSON. */ }
          const error = new Error(message || `Main returned HTTP ${response.statusCode}.`);
          error.status = response.statusCode;
          error.code = envelope?.error?.code;
          if (envelope?.data) error.receipt = envelope.data;
          reject(error);
        });
      });
      request.on('error', reject);
      const timeout = method === 'POST' && (route==='/v1/files/workspace-path'||route==='/v1/files/snapshot'||route==='/v1/files/retained-move'||route==='/v1/project-exports'||/^\/v1\/projects\/[A-Za-z0-9_.-]+\/archive\/(restore\/)?(apply|recover)$/.test(route)) ? 195000 : 30000;
      request.setTimeout(timeout, () => request.destroy(new Error(`Main did not respond within ${timeout / 1000} seconds. Inspect the saved operation before recovery.`)));
      request.end(payload);
    });
  }

  async json(endpoint, query, options) {
    const response = await this.request(endpoint, query, options);
    return this.readJSON(response);
  }

  async api(route, query, options) {
    return this.readJSON(await this.apiRequest(route, query, options));
  }

  async readJSON(response) {
    let size = 0;
    const chunks = [];
    for await (const chunk of response) {
      size += chunk.length;
      if (size > 2 * 1024 * 1024) { response.destroy(); throw new Error('Metadata page exceeded 2 MiB.'); }
      chunks.push(chunk);
    }
    const envelope = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    if (!envelope.ok) throw new Error(envelope.error?.summary || 'Main refused the request.');
    return { data: envelope.data, ms: response.loomMs };
  }

  async content(root, relative, options = {}) {
    return this.request('content', { root, path: relative }, { lane: 'content', ...options });
  }

  async smallText(root,relative,limit=4096){
    const {data:metadata}=await this.json('stat',{root,path:relative});
    if(metadata.kind!=='file'||metadata.size>limit)throw Error('Project mapping is missing or exceeds its small-file limit.');
    const response=await this.content(root,relative);let size=0;const chunks=[];
    for await(const chunk of response){size+=chunk.length;if(size>limit){response.destroy();throw Error('Project mapping exceeded its limit.');}chunks.push(chunk);}
    if(size!==metadata.size||response.headers.etag!==metadata.revision)throw Error('Project mapping changed during read.');
    return new TextDecoder('utf-8',{fatal:true}).decode(Buffer.concat(chunks));
  }

  async close() {
    this.closed = true;
    const lanes = await Promise.allSettled([...this.lanes.values()]);
    for (const result of lanes) if (result.status === 'fulfilled') {
      result.value.agent.destroy();
      result.value.child.kill();
    }
    this.lanes.clear();
  }
}
