import { createHash, randomUUID } from 'node:crypto';
import path from 'node:path';
import {AsyncLocalStorage} from 'node:async_hooks';
import {BoxRoots,isBoxRef} from './box-roots.mjs';

const TTL = 30_000;
const LIMIT = 50;
const text = (value, limit = 240) => typeof value === 'string' ? value.replace(/[\x00-\x1f\x7f]/g, ' ').trim().slice(0, limit) : '';
const prose = value => {
  if(typeof value!=='string')return '';
  const clean=value.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g,'').trim();
  return clean.length>16_000?`${clean.slice(0,16_000)}\n\n[Display limited to 16,000 characters; open the project source for the remainder.]`:clean;
};
const state = value => text(value, 80) || 'unknown';
const at = value => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? value : null;
const count = value => Number.isSafeInteger(value) && value >= 0 ? value : 'unknown';
const rows = value => {
  if (value == null) return [];
  if (!Array.isArray(value)) throw new Error('Expected an inventory array.');
  return value;
};
const object = value => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Expected an owner response.');
  return value;
};
const reference = value => {
  if (typeof value !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(value) || value.includes('..')) throw new Error('Invalid resource reference.');
  return value;
};
const required = (value, label, limit = 160) => {
  if (typeof value !== 'string' || !value.trim() || value.length > limit || /[\x00-\x1f\x7f]/.test(value)) throw new Error(`${label} is required and must be at most ${limit} characters.`);
  return value.trim();
};
const abort = () => Object.assign(new Error('Overview cancelled.'), { name: 'AbortError' });
const summary = entries => entries.filter(([, value]) => value !== undefined && value !== null && value !== '').map(([name, value]) => `${name}: ${text(String(value), 400)}`).join('\n');
const fingerprint = (kind, input) => createHash('sha256').update(JSON.stringify([kind, input])).digest('hex');
export function resourceDescription(resource){
  const kind=text(resource.binding?.kind)||'Declared resource',known=value=>value&&!['unknown','unavailable'].includes(value);
  const applied=resource.readiness?.applied?.state,health=resource.readiness?.healthy?.state;
  const observations=[known(applied)&&`Applied: ${text(applied)}`,known(health)&&`Health: ${text(health)}`].filter(Boolean);
  return [kind,...observations,...!observations.length&&['application','service'].includes(kind)?['No runtime health observation']:[]].join(' · ');
}
export function evidenceText(value){
  const clean=(v,depth=0)=>depth>5?'[Nested evidence omitted]':Array.isArray(v)?v.slice(0,50).map(x=>clean(x,depth+1)):v&&typeof v==='object'?Object.fromEntries(Object.entries(v).slice(0,60).map(([k,x])=>[k,/password|secret|token|credential|api.?key/i.test(k)?'[Redacted]':clean(x,depth+1)])):typeof v==='string'?prose(v).slice(0,8000):v;
  return (typeof value==='string'?prose(value):JSON.stringify(clean(value),null,2)).slice(0,16000);
}

function boxPath(absolute, box) {
  if (!box || typeof absolute !== 'string' || !path.posix.isAbsolute(absolute) || absolute.includes('\0')) return undefined;
  const relative = path.posix.relative(box, absolute);
  return relative === '..' || relative.startsWith('../') || path.posix.isAbsolute(relative) ? undefined : relative;
}

function resourcePath(root, relative, box,rootRef='box') {
  if (typeof root !== 'string' || !path.posix.isAbsolute(root) || typeof relative !== 'string' || !relative || path.posix.isAbsolute(relative) || relative.split('/').includes('..')) return {};
  const resolved = boxPath(path.posix.join(root, relative), box);
  return resolved === undefined ? {} : { root: rootRef, path: resolved, kind: 'directory' };
}

function projectRow(p) {
  const status = state(p.status);
  return {
    id: text(p.project_id, 128), name: text(p.name, 160) || text(p.slug), description: text(p.description, 320),
    owner: text(p.home_node_id || p.owner_actor_id) || 'loom', status,
    posture: status === 'archived' ? 'archived' : ['working', 'passive'].includes(p.metadata?.work_state) ? p.metadata.work_state : 'unspecified',
    legacyActive: status === 'active' && !Object.hasOwn(p.metadata || {}, 'work_state'),
    at: at(p.updated_at),
  };
}

function loomSchedule(s) {
  return {
    id: text(s.schedule_id, 128), name: text(s.display_name) || text(s.schedule_key), owner: 'loom',
    status: state(s.status), enabled: s.status === 'active', expression: text(s.schedule_expr), timezone: text(s.timezone) || null,
    next: at(s.next_fire_at), last: at(s.last_fire_at), result: 'unknown',
  };
}

function hermesSchedule(s, source = {}) {
  const schedule = s.schedule || {};
  const profile=text(s.profile||source.profile,128)||'unknown profile',host=text(s.host||source.host,128)||'unknown host';
  return {
    id: text(s.id, 128), name: text(s.display_name) || `${profile} \u00b7 ${text(s.id, 128)}`, owner: 'hermes', host, profile, project_id:text(s.project_id,128)||null, status: state(s.native_state),
    enabled: typeof s.enabled === 'boolean' ? s.enabled : null,
    expression: text(schedule.expression) || (Number.isSafeInteger(schedule.minutes) ? `every ${schedule.minutes}m` : text(schedule.run_at)) || 'unknown',
    timezone: text(schedule.timezone) || null, next: at(s.next_run_at), last: at(s.last_run_at), result: state(s.last_result),
  };
}

function qualified(value, now) {
  if (!value || value.available !== true) return 'unavailable';
  const freshness = value.freshness || value;
  const source = state(freshness.source_state);
  if (source !== 'live') return source;
  const observed = at(freshness.source_updated_at || freshness.captured_at);
  if (!observed || !Number.isFinite(freshness.stale_after_seconds) || freshness.stale_after_seconds <= 0) return 'unknown';
  return now - Date.parse(observed) > freshness.stale_after_seconds * 1000 ? 'stale' : state(value.state || value.database_state || 'live');
}

function miniGroups(data, now) {
  const d = object(data), runtime = d.runtime || {}, network = d.network || {};
  const protection = (name, value) => ({
    name, status: qualified(value, now), evidenceAt: at(value?.last_success_at), evidenceLabel: 'Last success', detail: value?.available === true
      ? `Last success: ${at(value.last_success_at) || 'unknown'}; verification: ${state(value.verification_state)}; next: ${at(value.next_run_at) || 'unknown'}` : 'No current owner evidence.',
  });
  return [
    { key: 'overview', title: 'Runtime', items: [
      { name: 'Runtime summary', status: qualified(runtime, now), detail: runtime.available === true ? `Queued: ${count(runtime.queued)}; running: ${count(runtime.running)}; failed: ${count(runtime.failed)}; workers: ${count(runtime.healthy_workers)}/${count(runtime.enabled_workers)}` : 'Runtime evidence unavailable; connection health is separate.' },
      { name: 'Network', status: qualified(network, now), detail: network.available === true ? `${rows(network.nodes).filter(n => n.online === true).length} reported online; cloud: ${state(network.cloud?.state)}` : 'Network evidence unavailable.' },
    ] },
    { key: 'protection', title: 'Protection', items: [
      protection('Local backup', d.local_backup), protection('Cloud snapshot', d.cloud_snapshot),
      { name: 'Coverage', status: qualified(d.coverage, now), detail: d.coverage?.available === true ? `Captured: ${at(d.coverage.captured_at) || 'unknown'}` : 'Coverage evidence unavailable; no healthy inference.' },
    ] },
  ];
}

export class Operations {
  constructor(remote,{db,boxes=new BoxRoots()}={}) {
    this.remote = remote;
    this.cache = new Map();
    this.intents = new Map();
    this.inflightActions = new Map();
    this.pendingAction = Promise.resolve();
    this.revision = 0;
    this.retainedDB=db;
    this.boxes=boxes;this.requestScope=new AsyncLocalStorage();
    db?.exec('CREATE TABLE IF NOT EXISTS retained_moves(id TEXT PRIMARY KEY,record TEXT NOT NULL)');
  }

  setBoxes(boxes){this.boxes=boxes instanceof BoxRoots?boxes:new BoxRoots(boxes);}
  selection(input={}) { return this.boxes.selection({...input,root:isBoxRef(input.root)?input.root:undefined}); }
  cancel(input) { if(input&&JSON.stringify(this.selection(input))!==this.controllerScope)return;this.controller?.abort(); this.controller = null; }

  async read(route, query = {}, signal) {
    const scoped=/^\/v1\/(?:project|jobs(?:\/|$)|schedules(?:\/|$)|schedule-fires(?:\/|$)|automations(?:\/|$)|invocations(?:\/|$)|knowledge\/|provenance\/|files\/|storage\/workspace-archive\/)/.test(route);
    const result = await this.remote.api(route, {...(scoped?this.requestScope.getStore():{}),...query}, signal ? { signal } : {});
    if (signal?.aborted) throw abort();
    return result.data;
  }

  overview(input={}) {
    const selection=this.selection(input);
    return this.requestScope.run(input.section==='system'?{}:selection,()=>this.readOverview(input,selection));
  }
  async readOverview({ section, ref, refresh = false } = {},selection={}) {
    if (!['projects', 'project', 'activity', 'schedules', 'system'].includes(section)) throw new Error('Unsupported section.');
    if (section === 'project' || ref !== undefined) reference(ref);
    const key = JSON.stringify([section,ref||'',selection]);
    this.cancel();
    const previous = this.cache.get(key);
    if (!refresh && previous && Date.now() - previous.savedAt < TTL) return { ...structuredClone(previous.dto), cached: true };
    const controller = new AbortController();
    this.controller = controller;
    this.controllerScope=JSON.stringify(selection);
    const revision = this.revision, sources = {}, errors = [];
    let successes = 0, failures = 0, networkFailures = 0, reused = false;
    const load = async (label, get) => {
      try {
        const data = await get(controller.signal);
        if (controller.signal.aborted) throw abort();
        sources[label] = { data, at: new Date().toISOString() };
        successes++;
        return data;
      } catch (error) {
        if (controller.signal.aborted || error.name === 'AbortError') throw abort();
        failures++;
        if (!Number.isInteger(error.status)) networkFailures++;
        errors.push(`${label} unavailable${Number.isInteger(error.status) ? ` (HTTP ${error.status})` : ''}.${previous?.sources[label] ? ' Showing its previous snapshot.' : ''}`);
        if (previous?.sources[label]) {
          sources[label] = previous.sources[label]; reused = true;
          return structuredClone(previous.sources[label].data);
        }
        return null;
      }
    };
    try {
      const dto = await this[section](load, ref);
      if (controller.signal.aborted) throw abort();
      const times = Object.values(sources).map(source => source.at).sort();
      Object.assign(dto, { ...(selection.box_id?{box_id:selection.box_id}:{}),...(selection.root?{activeBox:selection.root}:{}),scope:section==='system'?'system':'box',observedAt: times[0] || null, errors: [...errors, ...(dto.errors || [])], cached: reused, offline: successes === 0 && failures > 0 && failures === networkFailures });
      if (this.revision === revision && Object.keys(sources).length) {
        this.cache.delete(key);
        this.cache.set(key, { dto: structuredClone(dto), sources, savedAt: failures ? 0 : Date.now() });
        if (this.cache.size > 32) this.cache.delete(this.cache.keys().next().value);
      }
      return dto;
    } finally {
      if (this.controller === controller) this.controller = null;
    }
  }

  async projects(load) {
    const items = await load('Projects', async signal => rows(await this.read('/v1/projects', { limit: LIMIT }, signal)).slice(0, LIMIT).map(projectRow));
    return { items: items || [], limited: items?.length === LIMIT };
  }

  async project(load, ref) {
    const rootRef=this.requestScope.getStore()?.root||'box';
    const base = `/v1/projects/${ref}`;
    const p = await load('Project', async signal => {
      const d = object(await this.read(base, {}, signal));
      const p = object(d.project);
      return { ...projectRow(p), location: text(p.metadata?.v0_3_project_contract?.project_root || d.workspace_view?.root_path, 4096),
        restore: p.status==='active'&&p.archive_state?.schema_version==='project.physical_archive_state.v1'&&p.archive_state.project_id===p.project_id&&p.archive_state.restore?.schema_version==='project.physical_restore_state.v1'&&p.archive_state.restore.phase==='complete'&&p.archive_state.restore.activation_state==='inactive'
          ? {operation:text(p.archive_state.restore.operation_id),at:at(p.archive_state.restore.restored_at)}:null,
        archive: p.archive_state?.schema_version === 'project.physical_archive_state.v1' && p.archive_state.project_id === p.project_id && p.archive_state.phase === 'complete' && p.archive_state.status === 'archived'
          ? { path: text(p.archive_state.archive_path,4096), operation:text(p.archive_state.operation_id), at:at(p.archive_state.archived_at) } : null };
    });
    if(p?.status === 'archived') {
      const archive=await load('Archive location',async signal=>{
        const current=object(await this.read(`${base}/archive/inspect`,{},signal)).physical;
        if(!p.archive||current?.project_id!==p.id||current.archive_operation_id!==p.archive.operation||current.status!=='archived'||current.evidence_status!=='verified')throw Error('Verified current archive location unavailable.');
        const payload=current.current_payload;
        if(payload?.root!=='storage'||typeof payload.relative_path!=='string'||!payload.relative_path||path.posix.isAbsolute(payload.relative_path)||path.posix.normalize(payload.relative_path)!==payload.relative_path||payload.relative_path.split('/').includes('..'))throw Error('Invalid current archive location.');
        return {path:payload.relative_path,relocation:text(current.relocation_id,128)};
      });
      const storage=await load('Storage root',async signal=>rows(await this.read('/v1/files/roots',{},signal)).find(r=>r.ref==='storage'&&r.available===true&&r.read_only===true));
      const relative=archive&&path.posix.isAbsolute(storage?.source_path||'')?archive.path:undefined;
      return {name:p.name,root:'storage',...(relative!==undefined?{path:relative}:{}),facts:{Status:'archived',Folder:relative!==undefined?path.posix.join(storage.source_path,relative):'Archive location unavailable',
        'Archive operation':p.archive?.operation||'Unverified','Archived at':p.archive?.at||'Unknown',...(archive?.relocation?{'Location operation':archive.relocation}:{}),'Evidence':'Verified current payload location with retained archive history; not a fresh cloud verification'},groups:[],
        errors:relative===undefined?['The verified current archive location cannot be mapped to available read-only Storage.']:[]};
    }
    if(p?.restore){
      const box=await load('Box root',async signal=>rows(await this.read('/v1/files/roots',{},signal)).find(r=>r.ref===rootRef&&r.available===true));
      const relative=boxPath(p.location,box?.source_path);
      return {name:p.name,root:rootRef,...(relative!==undefined?{path:relative}:{}),facts:{Status:'active','Runtime':'Restored; inactive',Folder:p.location,'Restore operation':p.restore.operation,'Restored at':p.restore.at||'Unknown','Context scope':'Source files are available. Runtime and declaration activation remain inactive; no automatic reactivation was requested.'},groups:[],errors:relative===undefined?['The restored path could not be mapped to the current Box root.']:[]};
    }
    const d = await load('Declaration', async signal => {
      const d = object(await this.read(`${base}/declaration-status`, {}, signal));
      return {
        location: text(d.target?.project_root, 4096), lifecycle: text(d.lifecycle), workState: text(d.work_state),
        development: Object.fromEntries(['posture', 'purpose', 'current_focus', 'progress', 'blockers', 'next_action'].map(key => [key, prose(d.development?.[key])])),
        developmentDigest: text(d.development?.source_digest,128), developmentTruncated: Array.isArray(d.development?.truncated_fields)&&d.development.truncated_fields.length>0,
        resources: Object.entries(d.resources || {}).slice(0, 30).map(([name, r]) => ({ name: text(name), description: resourceDescription(r), relative: r.binding?.kind === 'repository' ? text(r.path, 4096) : '' })),
      };
    });
    const repos = await load('Repositories', async signal => {
      const d = object(await this.read(`${base}/repos`, { limit: 20 }, signal));
      return rows(d.repositories).slice(0, 20).map(r => ({ name: text(r.key || r.repository_id), relative: text(r.relative_path, 4096), description: `${state(r.role)}; ${state(r.observation_posture)}${r.git?.current_branch ? `; branch: ${text(r.git.current_branch)}` : ''}` }));
    });
    const services = await load('Project services', async signal => rows(await this.read('/v1/services', { limit: 20, project: ref }, signal)).slice(0, 20).map(s => ({ name: text(s.display_name || s.provider_key), description: `Registry: ${state(s.registry_state)}; process: ${state(s.process_state)}; health: ${state(s.health_status)}` })));
    const box = await load('Box root', async signal => {
      const root = rows(await this.read('/v1/files/roots', {}, signal)).find(root => root.ref === rootRef);
      return root && path.posix.isAbsolute(root.source_path || '') ? root.source_path : '';
    });
    const location = d?.location || p?.location, relative = boxPath(location, box);
    const basecamp=relative!==undefined?await this.basecampFor(relative,rootRef).catch(()=>null):null;
    let development = d?.development || {}, developmentQualification;
    if(d?.developmentTruncated){
      const captured=await load('Development detail',async signal=>{
        const snapshot=object(await this.read(`/v1/provenance/projects/${ref}`,{},signal)),full=snapshot.context?.development;
        if(snapshot.project_id!==ref||!d.developmentDigest||full?.source_digest!==d.developmentDigest)return null;
        return Object.fromEntries(['purpose','current_focus','progress','blockers','next_action'].filter(key=>typeof full[key]==='string'&&full[key]).map(key=>[key,prose(full[key])]));
      });
      if(captured){development={...development,...captured};developmentQualification='Captured development context matching the current declaration source. Bounded excerpts, not complete project files.';}
      else developmentQualification='Summary only. Matching detailed development evidence is unavailable; open the project source for complete text.';
    }
    return {
      name: p?.name || ref, root: rootRef, ...(relative !== undefined ? { path: relative } : {}),...(basecamp?{basecamp}:{}), limited: true,
      facts: {
        Status: p?.status || d?.lifecycle || 'unavailable', 'Work state': ['working','passive'].includes(d?.workState) ? d.workState : p?.posture || 'unknown',
        ...(location ? { Folder: location } : {}), Description: p?.description || '',
        'Development evidence': development.posture || 'unavailable', Purpose: development.purpose || '',
        Focus: development.current_focus || '', Progress: development.progress || '', Blockers: development.blockers || '', 'Next action': development.next_action || '',
        ...(developmentQualification?{'Context scope':developmentQualification}:{}),
      },
      groups: [
        { title: 'Repositories', items: (repos || []).map(({ relative, ...item }) => ({ ...item, ...resourcePath(location, relative, box,rootRef) })) },
        { title: 'Resources', items: (d?.resources || []).map(({ relative, ...item }) => ({ ...item, ...resourcePath(location, relative, box,rootRef) })) },
        { title: 'Services', items: services || [] },
      ],
    };
  }

  async activity(load) {
    const jobs = await load('Jobs', async signal => rows(await this.read('/v1/jobs', { limit: 30 }, signal)).slice(0, 30).map(j => ({
      id: text(j.job_id, 128), name: text(j.job_type) || 'Job', owner: 'jobs', status: j.manual_action_required === true ? 'requires_manual_action' : state(j.status), posture: state(j.status),
      description: [text(j.failure_code), text(j.target_kind), text(j.target_id)].filter(Boolean).join(' - '), at: at(j.updated_at),
    })));
    const pipelines = await load('Enrichment', async signal => rows(await this.read('/v1/knowledge/notes/pipelines', { limit: 10 }, signal)).slice(0, 10).map(p => ({
      id: text(p.knowledge_pipeline_run_id, 128), name: text(p.pipeline_definition_key) || 'Enrichment', owner: 'notes', status: state(p.status), posture: state(p.status),
      description: [text(p.current_stage_key), text(p.last_error_code), text(p.knowledge_object_id)].filter(Boolean).join(' - '), at: at(p.updated_at),
    })));
    // Generic object-index inventory has no Box owner. Keep its legacy unbound
    // read, never mix global index rows into an explicitly scoped Activity page.
    const bound=this.requestScope.getStore();
    const indexes = bound?.root||bound?.box_id?[]:await load('Indexing', async signal => rows(await this.read('/v1/indexes/status', { limit: 10 }, signal)).slice(0, 10).map(i => ({
      id: text(i.index_status_id, 128), name: text(i.index_type) || 'Indexing', owner: 'index', status: state(i.status), posture: state(i.status),
      description: text(i.object_id), at: at(i.updated_at),
    })));
    return { items: [...(jobs || []), ...(pipelines || []), ...(indexes || [])].sort((a, b) => (b.at || '').localeCompare(a.at || '')), limited: true };
  }

  async schedules(load) {
    const loom = await load('LOOM schedules', async signal => rows(await this.read('/v1/schedules', { limit: LIMIT }, signal)).slice(0, LIMIT).map(loomSchedule));
    const hermes = await load('Hermes schedules', async signal => {
      const d = object(await this.read('/v1/schedules/hermes', {}, signal));
      if (d.availability !== 'available') throw new Error('Native inventory unavailable.');
      return { items: rows(d.jobs).slice(0, LIMIT).map(job=>hermesSchedule(job,d.source)), observedAt: at(d.observed_at), limited: d.jobs?.length > LIMIT };
    });
    return { items: [...(loom || []), ...(hermes?.items || [])], limited: loom?.length === LIMIT || !!hermes?.limited };
  }

  async system(load, ref) {
    if (ref === 'protection') {
      const mini = await load('Protection summary', async signal => miniGroups(await this.read('/v1/mini-dashboard/status', {}, signal), Date.now()));
      return { limited: true, groups: mini || miniGroups({}, Date.now()) };
    }
    const host = await load('Host metrics', async signal => {
      const data = object(await this.read('/v1/system/host', {}, signal));
      return [['CPU', data.host?.cpu], ['Memory', data.host?.memory], ['Temperature', data.host?.temperature], ['Storage disk used', data.host?.storage], ['System disk used', data.system_disk]].map(([name, metric]) => ({
        name, status: metric?.available === true && Number.isFinite(metric.value) ? `${metric.value.toFixed(1)}${text(metric.unit, 8)}` : 'unavailable', evidenceAt: at(metric?.sampled_at),
        detail: metric?.available === true ? `Sample: ${at(metric.sampled_at) || 'unknown'}; ${state(metric.source_state)}` : 'No current sample',
      }));
    });
    const health = await load('Main health', async signal => {
      const h = object(await this.read('/v1/health', {}, signal));
      return [{ name: 'Main connection', status: state(h.status), detail: `Node: ${text(h.node?.id)}; version: ${text(h.version)}. Health includes a bounded storage write probe.` },
        ...['database', 'storage', 'migrations'].map(key => ({ name: key, status: state(h.checks?.[key]?.status), detail: 'Health check only; independent protection evidence is required.' }))];
    });
    const services = await load('Services', async signal => rows(await this.read('/v1/services', { limit: 20, node: 'main' }, signal)).slice(0, 20).map(s => ({
      name: text(s.display_name || s.provider_key), status: state(s.health_status), evidenceAt: at(s.last_observed_at), detail: `Registry: ${state(s.registry_state)}; process: ${state(s.process_state)}; observed: ${at(s.last_observed_at) || 'unknown'}`,
    })));
    const workers = await load('Workers', async signal => rows(await this.read('/v1/workers', { limit: 30, node: 'main' }, signal)).slice(0, 30).map(w => ({
      name: text(w.display_name || w.worker_key), status: w.enabled === false ? 'disabled' : w.paused === true ? 'paused' : state(w.health_status), evidenceAt: at(w.last_success_at), evidenceLabel: 'Last success',
      detail: `Lifecycle: ${state(w.lifecycle_status)}; last success: ${at(w.last_success_at) || 'unknown'}; last failure: ${at(w.last_failure_at) || 'unknown'}`,
    })));
    const unavailable = name => [{ name, status: 'unavailable', detail: 'No current source evidence.' }];
    return { limited: true, groups: [
      { key: 'overview', title: 'Connection', items: health || unavailable('Main health') },
      { key: 'overview', title: 'Host', items: host || unavailable('Host metrics') },
      { key: 'services', title: 'Services', items: services || unavailable('Services') },
      { key: 'workers', title: 'Workers', items: workers || unavailable('Workers') },
    ] };
  }

  inspect(input={}) { return this.requestScope.run(this.selection(input),()=>this.inspectScoped(input)); }
  async inspectScoped({ kind, ref, owner, profile, host,evidence=false } = {}) {
    const selection=this.requestScope.getStore();
    if(selection?.root&&selection.root!=='box'&&(['artifact','artifact-content'].includes(kind)||owner==='index'))throw Error('This inspector has no Box-scoped backend. No global content was read.');
    if(kind==='schedule-options'){
      const [projects,capabilities]=await Promise.all([this.read('/v1/projects',{limit:100}),this.read('/v1/capabilities',{status:'active',limit:100})]);
      return {projects:rows(projects).filter(p=>p.status!=='archived').map(p=>({value:p.project_id,label:text(p.name)})),capabilities:rows(capabilities).map(c=>({value:c.compact_address,label:text(c.display_name||c.endpoint_name)||text(c.compact_address)})),qualification:'First 100 registered capabilities and projects; advanced entry remains available.'};
    }
    if(kind==='hermes-options')return this.post('/v1/schedules/hermes/workspace',{operation:'workspace_info',profile:profile||(selection?.root&&selection.root!=='box'?'':'mina')});
    if(kind==='protection'){
      const findings=(await Promise.all(['main_backup','cloud_snapshot_upload'].map(worker=>this.read('/v1/maintenance/findings',{status:'open',worker,limit:50}).then(rows)))).flat();
      return {summary:'Existing owner findings; opening this view does not run or retry a backup.',sections:findings.filter(f=>/backup|cloud|protection|recovery/.test(`${f.worker_kind} ${f.finding_kind} ${f.worker_key}`)).map(f=>({title:text(f.summary,500)||text(f.finding_kind),text:summary([['Severity',f.severity],['Subject',`${f.subject_kind}: ${f.subject_id}`],['Worker',f.worker_key],['First seen',f.first_seen_at],['Last seen',f.last_seen_at]])+'\n\n'+evidenceText(f.details_json)})),qualification:findings.length===50?'First 50 open maintenance findings; this is a bounded inventory.':'Current open protection findings from the maintenance owner.'};
    }
    reference(ref);
    if(kind==='artifact'){
      const d=object(await this.read(`/v1/artifacts/${ref}`)),a=d.artifact||{};
      return {summary:summary([['Title',a.title],['Reference',a.artifact_id],['Type',a.artifact_type],['Status',a.status],['Storage',d.storage_visibility],['Read command',d.read_command]]),sections:[{title:'Retained metadata',text:evidenceText(a.metadata),inspect:{kind:'artifact-content',ref},label:'Read verified text'}],qualification:'Metadata only. Text preview is explicit and scope-verified; binary payloads are not downloaded.'};
    }
    if(kind==='artifact-content'){
      const d=object(await this.read(`/v1/artifacts/${ref}/content`,{max_bytes:16384}));
      return {summary:summary([['Artifact',d.artifact_id],['Size',d.size_bytes],['Version',d.object_version_id]]),sections:[{title:'Verified text',text:d.content||'Empty text artifact'}],qualification:d.truncated?'First 16 KiB; retained payload is unchanged.':'Owner-verified retained text. No command executed.'};
    }
    if(kind==='worker'){
      const d=object(await this.read(`/v1/workers/${ref}`)),w=d.instance||{};
      return {scope:'system',scopeLabel:'Main-wide',summary:summary([['Scope','Main-wide'],['Worker',w.display_name],['Enabled',w.enabled],['Paused',w.paused],['Mode',w.tick_policy_json?.mode||'manual'],['Current run',w.current_run_id],['Next',w.next_run_after],['Last success',w.last_success_at],['Last failure',w.last_failure_at],['Health',d.health?.health_status]])};
    }
    if(kind==='service'){
      const d=object(await this.read(`/v1/services/${ref}`));return {scope:'system',scopeLabel:'Main-wide',summary:summary([['Scope','Main-wide'],['Service',d.display_name],['Registry',d.registry_state],['Process',d.process_state],['Health',d.health_status],['Observed',d.last_observed_at]])};
    }
    if(kind==='project-context'){
      const d=object(await this.read(`/v1/projects/${ref}`)),p=object(d.project),id=reference(p.project_id);
      const entries=[['Project',p.name],['Registry status',p.status]],sections=[];let basecamp;
      try{
        const snapshot=object(await this.read(`/v1/provenance/projects/${id}`));
        if(snapshot.project_id!==id)throw Error('Mismatched project snapshot');
        const development=snapshot.context?.development||{};
        for(const key of ['current_focus','next_action','blockers','progress','purpose'])if(development[key])sections.push({title:key.replaceAll('_',' '),text:prose(development[key])});
        entries.push(['Provenance snapshot',snapshot.snapshot_id],['Captured source',snapshot.observed_at],['Qualification','Captured project context, not a fresh observation or authority'],
          ...['purpose','current_focus','progress','blockers','next_action'].map(key=>[key,text(development[key],800)]));
      }catch{entries.push(['Provenance','No readable captured project context']);}
      try{
        const rootRef=this.requestScope.getStore()?.root||'box',roots=rows(await this.read('/v1/files/roots')),root=roots.find(r=>r.ref===rootRef);
        const location=p.metadata?.v0_3_project_contract?.project_root,relative=boxPath(location,root?.source_path);
        if(relative!==undefined){
          const raw=await this.remote.smallText(rootRef,path.posix.join(relative,'.basecamp/project-url'));
          const url=new URL(raw.trim());
          if(url.protocol==='https:'&&['app.basecamp.com','3.basecamp.com'].includes(url.hostname)&&!url.username&&!url.password&&/^\/[0-9]+\/(projects|buckets)\/[0-9]+(?:\/|$)/.test(url.pathname))basecamp=url.href;
        }
      }catch{entries.push(['Basecamp','No valid project URL mapping found']);}
      return {summary:summary(entries),sections,...(basecamp?{basecamp,links:[{name:'Open Basecamp',url:basecamp}]}:{})};
    }
    if (kind === 'schedules' || kind === 'schedule') {
      if (owner === 'hermes') {
        if(!selection?.all_boxes&&(profile==='mina'||selection?.root&&selection.root!=='box'))return this.post('/v1/schedules/hermes/workspace',{operation:'workspace_detail',profile:required(profile,'Native profile'),id:ref});
        const d = object(await this.read('/v1/schedules/hermes'));
        if (d.availability !== 'available') throw new Error('Hermes inventory unavailable.');
        const matches = rows(d.jobs).filter(job => job.id === ref&&(!profile||(job.profile||d.source?.profile)===profile)&&(!host||(job.host||d.source?.host)===host));
        if (matches.length !== 1) throw new Error('Hermes reference unavailable or ambiguous across profiles.');
        const s = hermesSchedule(matches[0],d.source);
        return { summary: summary([['Owner', 'Hermes (native read-only inventory)'], ['Profile', matches[0].profile || d.source?.profile], ['Status', s.status], ['Enabled', s.enabled ?? 'unknown'], ['Schedule', s.expression], ['Timezone', s.timezone || 'unknown'], ['Next', s.next], ['Last', s.last], ['Result', s.result], ['Observed', d.observed_at]]) };
      }
      if (owner && owner !== 'loom') throw new Error('Unsupported schedule owner.');
      const d = object(await this.read(`/v1/schedules/${ref}`)), s = object(d.schedule);
      const entries = [['Owner', 'LOOM'], ['Name', s.display_name], ['Status', s.status], ['Schedule', s.schedule_expr], ['Timezone', s.timezone], ['Next', s.next_fire_at], ['Last', s.last_fire_at]];
      if (s.last_schedule_fire_id) {
        try {
          const f = object(await this.read(`/v1/schedule-fires/${reference(s.last_schedule_fire_id)}`));
          entries.push(['Last result', f.status], ['Failure', f.failure_code], ['Job', f.job_id], ['Invocation', f.invocation_id], ['Finished', f.completed_at || f.failed_at]);
        } catch { entries.push(['Last result', 'unavailable']); }
      } else entries.push(['Last result', 'unknown']);
      return { summary: summary(entries) };
    }
    if (kind !== 'activity' && kind !== 'job' && kind !== 'jobs') throw new Error('Unsupported inspection.');
    if (owner === 'notes') {
      const d = object(await this.read(`/v1/knowledge/notes/pipelines/${ref}`)), r = object(d.run);
      return { summary: summary([['Owner', 'Notes enrichment'], ['Status', r.status], ['Stage', r.current_stage_key], ['Failure', r.last_error_code], ['Object', r.knowledge_object_id], ['Updated', r.updated_at], ...rows(d.stages).slice(0, 10).map(s => [text(s.stage_key), state(s.status)])]) };
    }
    if (owner === 'index') {
      const d = object(await this.read(`/v1/indexes/queue/${ref}`));
      return { summary: summary([['Owner', 'Indexing'], ['Status', d.status], ['Type', d.index_type], ['Object', d.object_id], ['Updated', d.updated_at]]) };
    }
    if (owner && owner !== 'loom' && owner !== 'jobs') throw new Error('Unsupported activity owner.');
    const d = object(await this.read(`/v1/jobs/${ref}`)), j = object(d.job);
    let name=text(j.job_type),target=text(j.target_id);if(j.target_kind==='script'&&j.target_id)try{const s=await this.read(`/v1/scripts/${reference(j.target_id)}`);name=text(s.script?.display_name||s.script?.name||s.display_name)||name;}catch{}
    const outputs=rows(d.outputs).slice(-40),question=outputs.find(o=>o.output_key==='question'),artifactOutputs=outputs.filter(o=>o.artifact_id||o.value_json?.artifact_id),artifactIDs=new Set(artifactOutputs.map(o=>o.artifact_id||o.value_json.artifact_id));
    const sections=[...(question?[{title:'Agent clarification',text:evidenceText(question.value_json)}]:[]),{title:'Execution details',text:evidenceText(Object.fromEntries(outputs.filter(o=>o!==question&&!artifactOutputs.includes(o)).map(o=>[o.output_key,o.value_json])))},...artifactOutputs.map(o=>({title:text(o.output_key).replaceAll('_',' '),text:evidenceText(o.value_json),inspect:{kind:'artifact',ref:o.artifact_id||o.value_json.artifact_id}})),...rows(d.logs).slice(-10).map(l=>({title:`${text(l.stream)||'Job'} log tail`,text:prose(l.tail_text).slice(-16000)||'No retained log tail'})),...rows(d.artifacts).filter(a=>!artifactIDs.has(a.artifact_id)).slice(0,30).map(a=>({title:text(a.title)||'Artifact',text:summary([['Reference',a.artifact_id],['Type',a.artifact_type],['Status',a.status],['Size',a.size_bytes]]),inspect:{kind:'artifact',ref:a.artifact_id}}))];
    const links=[];for(const o of outputs){const v=o.value_json,candidates=['thread_id','codex_thread_id'].includes(o.output_key)?[v]:v&&typeof v==='object'?[v.thread_id,v.codex_thread_id]:[];for(const id of candidates)if(/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(id||'')&&!links.some(l=>l.url.endsWith(id)))links.push({name:'Open saved Codex conversation',url:`codex://threads/${id}`});}
    return { summary: summary([['Owner', 'LOOM jobs'], ['Task',name],['Status', j.manual_action_required?'Needs your input':j.status], ['Attempts', `${count(j.attempt_count)}/${count(j.max_attempts)}`], ['Failure', j.failure_code==='codex_needs_input'?'The coding agent saved a question requiring clarification. Inspect the retained outputs below.':j.failure_code], ['Target', target], ['Started', j.started_at], ['Finished', j.completed_at || j.failed_at || j.cancelled_at],['Outputs',rows(d.outputs).length],['Artifacts',rows(d.artifacts).length]]),...(evidence?{sections,links,qualification:'Retained output values and log tails, bounded per section. Source files and full histories are unchanged.'}:{}) };
  }

  async basecampFor(relative,root='box'){
    const raw=await this.remote.smallText(root,path.posix.join(relative,'.basecamp/project-url')),url=new URL(raw.trim());
    if(url.protocol==='https:'&&['app.basecamp.com','3.basecamp.com'].includes(url.hostname)&&!url.username&&!url.password&&/^\/[0-9]+\/(projects|buckets)\/[0-9]+(?:\/|$)/.test(url.pathname))return url.href;
    return null;
  }

  action(input = {}) {
    // Capture IPC input before queueing; hidden-page cancellation never aborts a mutation.
    const captured = structuredClone(input);
    const selection=this.selection(captured);
    const id = fingerprint('action', [captured,selection]);
    if (this.inflightActions.has(id)) return this.inflightActions.get(id);
    const run = this.pendingAction.then(() => this.requestScope.run(selection,()=>this.execute(captured)));
    this.pendingAction = run.catch(() => {});
    const pending = run.finally(() => this.inflightActions.delete(id));
    this.inflightActions.set(id, pending);
    return pending;
  }

  intent(kind, input) {
    const id = fingerprint(kind, [input,this.requestScope.getStore()||{}]);
    let value = this.intents.get(id);
    if (!value || Date.now() - value.at > 86_400_000) {
      value = { key: `desktop-${randomUUID()}`, at: Date.now() };
      this.intents.set(id, value);
      if (this.intents.size > 64) this.intents.delete(this.intents.keys().next().value);
    }
    return value.key;
  }

  async post(route, body, key) {
    this.cancel(); this.revision++; this.cache.clear();
    return (await this.remote.api(route, this.requestScope.getStore()||{}, { method: 'POST', body, ...(key ? { headers: { 'X-Loom-Idempotency-Key': key } } : {}) })).data;
  }

  async execute(input) {
    const selection=this.requestScope.getStore();
    if(input.kind?.startsWith('retained-')){
      const db=this.retainedDB;if(!db)throw Error('Retained move journal is unavailable.');
      const save=value=>db.prepare('INSERT INTO retained_moves VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record').run(value.id,JSON.stringify(value));
      if(input.kind==='retained-list')return db.prepare('SELECT record FROM retained_moves ORDER BY rowid DESC LIMIT 100').all().map(row=>JSON.parse(row.record));
      if(input.kind==='retained-preview'){
        const selection=this.requestScope.getStore()||{};
        const {data}=await this.remote.api('/v1/files/retained-move',selection, {method:'POST',body:{action:'plan',from:input.from,to:input.to}});
        if(data.state!=='planned'||!data.id||!data.plan_hash||data.source!==input.from||data.destination!==input.to)throw Error('Main did not return the exact retained move review.');const op={...data,selection};save(op);return op;
      }
      if(!['retained-apply','retained-cancel','retained-inspect'].includes(input.kind))throw Error('Unsupported retained operation.');
      const row=db.prepare('SELECT record FROM retained_moves WHERE id=?').get(reference(input.id));if(!row)throw Error('Saved retained move is missing.');
      const op=JSON.parse(row.record);if(['complete','cancelled'].includes(op.state))return op;
      const action=input.kind==='retained-cancel'?'cancel':input.kind==='retained-inspect'?'status':'apply';
      op.state='submitted';save(op);this.cancel();this.revision++;this.cache.clear();
      try{
        const {data}=await this.remote.api('/v1/files/retained-move',op.selection||{}, {method:'POST',body:{action,id:op.id,plan_hash:op.plan_hash,confirm:action!=='status'}});
        if(data.id!==op.id||data.plan_hash!==op.plan_hash||data.source!==op.source||data.destination!==op.destination||data.inventory!==op.inventory||!(action==='status'?['planned','prepared','complete','cancelled']:action==='cancel'?['cancelled']:['complete']).includes(data.state))throw Error('Main did not confirm this exact retained operation.');Object.assign(op,data);delete op.error;delete op.receipt;
      }catch(error){op.state='needs_review';op.error=error.message;if(error.receipt?.id===op.id)op.receipt=error.receipt;}
      save(op);return op;
    }
    if(input.kind==='file-review')return (await this.remote.json('operations',input.input)).data;
    if(input.kind?.startsWith('project-lifecycle-')){
      if(!this.lifecycle)throw Error('Project lifecycle journal is unavailable.');
      const action=input.kind.slice('project-lifecycle-'.length);
      if(action==='preview')return this.lifecycle.preview(reference(input.ref),input.operation,this.requestScope.getStore());
      if(action==='list')return this.lifecycle.list();
      if(action==='inspect')return this.lifecycle.get(input.id);
      if(action==='apply'||action==='recover'){
        this.cancel();this.revision++;this.cache.clear();
        return this.lifecycle.apply(input.id,{recover:action==='recover'});
      }
      throw Error('Unsupported lifecycle operation.');
    }
    if (input.owner === 'hermes') {
      const company=selection?.root&&selection.root!=='box';
      if(selection?.all_boxes||company&&input.profile==='mina'||!company&&input.profile!=='mina')throw Error('Use the exact registered native profile for the selected Box.');
      if(!/^[a-z][a-z0-9_-]{0,62}$/.test(input.profile||'')||input.host!=='main'||!['schedule-create','schedule-edit','schedule-pause','schedule-resume'].includes(input.kind))throw Error('Use the exact registered native profile on Main. Project-declared routines remain managed by their project declaration.');
      const operation='workspace_'+input.kind.replace('schedule-',''),spec=['schedule-create','schedule-edit'].includes(input.kind)?{name:required(input.name,'Schedule name'),prompt:prose(input.prompt).slice(0,32768),schedule:required(input.expression,'Schedule',256)}:undefined;
      if(spec&&!spec.prompt)throw Error('Enter the task for the selected agent.');
      const desired={operation,profile:input.profile,...input.ref?{id:reference(input.ref),expected_revision:required(input.revision,'Revision',80)}:{},...spec?{spec}:{}};
      const token=this.intent('hermes',desired),result=await this.post('/v1/schedules/hermes/workspace',{...desired,token});
      if(!result.job?.id||input.kind==='schedule-create'&&!result.job.paused)throw Error('Native owner did not confirm the requested paused schedule.');
      this.intents.delete(fingerprint('hermes',[desired,this.requestScope.getStore()||{}]));return result;
    }
    if (input.kind === 'project-create') {
      const name = required(input.name, 'Project name'), slug = required(input.slug, 'Folder name', 64);
      if (!/^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(slug)) throw new Error('Folder name must be a lowercase URL-safe slug of 3-64 characters.');
      if (text(input.description)) throw new Error('The scaffold owner does not support descriptions; leave the description empty.');
      const body = { mode: 'declaration', name, slug, dry_run: true };
      const preview = object(await this.post('/v1/project-scaffolds', body));
      if (preview.ok !== true || preview.validation?.state !== 'planned_only' || preview.validation?.errors !== 0 || !preview.project_root) throw new Error('Project preview did not validate; no project was created.');
      const result = object(await this.post('/v1/project-scaffolds', { ...body, dry_run: false }));
      if (result.ok !== true || result.validation?.state !== 'passed' || result.validation?.ok !== true || result.context_state !== 'registered_refresh_pending' || result.project_root !== preview.project_root) throw new Error('Project creation needs owner verification; source may have been retained.');
      return { summary: `Created ${name}. Project context refresh is pending.`, ref: text(result.project_id, 128) };
    }
    if (input.kind === 'schedule-create') {
      const name = required(input.name, 'Schedule name'), capability = required(input.capability, 'Capability', 256);
      const once=input.schedule_kind==='one_shot',timing = once?{cron:required(input.expression,'Run at',256),timezone:'UTC',misfire_policy:'mark_missed'}:this.timing(input), project = input.project ? reference(input.project) : '';
      if(once&&(!Number.isFinite(Date.parse(timing.cron))||Date.parse(timing.cron)<=Date.now()))throw Error('Choose a future date and time.');
      let payload;
      try { payload = typeof input.input === 'string' ? JSON.parse(input.input) : input.input ?? {}; } catch { throw new Error('Input must be a JSON object.'); }
      if (!payload || typeof payload !== 'object' || Array.isArray(payload) || Buffer.byteLength(JSON.stringify(payload)) > 65_536) throw new Error('Input must be a JSON object of at most 64 KiB.');
      const desired = { display_name: name, target_capability: capability, input_json: payload, schedule_kind: once?'one_shot':'cron', schedule_expr: timing.cron, timezone: timing.timezone, misfire_policy: timing.misfire_policy, ...(timing.lateness_window_seconds ? { lateness_window_seconds: timing.lateness_window_seconds } : {}), status: 'paused', ...(project ? { project_ref: project } : {}) };
      const key = this.intent('schedule-create', desired);
      const body = { ...desired, schedule_key: key };
      const preview = object(await this.post('/v1/schedules', { ...body, dry_run: true }));
      if (preview.schedule?.status !== 'paused') throw new Error('Schedule preview did not confirm a paused schedule.');
      const result = object(await this.post('/v1/schedules', body, key));
      if (result.schedule?.status !== 'paused') throw new Error('Schedule owner did not confirm paused creation.');
      const ref = reference(result.schedule.schedule_id);
      this.intents.delete(fingerprint('schedule-create', [desired,this.requestScope.getStore()||{}]));
      return { summary: 'Schedule created paused.', ref };
    }
    if (['schedule-pause', 'schedule-resume', 'schedule-timing', 'schedule-edit'].includes(input.kind)) {
      if (input.owner && input.owner !== 'loom') throw new Error('Unsupported schedule owner.');
      const ref = reference(input.ref), timingEdit = ['schedule-timing', 'schedule-edit'].includes(input.kind);
      const timing = timingEdit ? this.timing(input) : null;
      const d = object(await this.read(`/v1/schedules/${ref}`)), s = object(d.schedule);
      if (timingEdit) {
        if (s.status !== 'paused' || s.schedule_kind !== 'cron') throw new Error('Only a paused LOOM cron schedule can have its timing edited.');
        const basis = { expression: s.schedule_expr, timezone: s.timezone, misfire: s.misfire_profile_json, updatedAt: s.updated_at };
        const intent = { ref, ...timing, basis };
        const result = object(await this.post(`/v1/schedules/${ref}/timing`, timing, this.intent('schedule-timing', intent)));
        if (result.schedule?.status !== 'paused' || result.schedule.schedule_expr !== timing.cron || result.schedule.timezone !== timing.timezone) throw new Error('Timing owner did not confirm the requested paused timing.');
        this.intents.delete(fingerprint('schedule-timing', [intent,this.requestScope.getStore()||{}]));
        return { summary: 'Timing updated; schedule remains paused.' };
      }
      const pause = input.kind === 'schedule-pause', desired = pause ? 'paused' : 'active';
      if (s.status === desired) return { summary: `Schedule is already ${desired}.` };
      if (!(pause ? ['active'] : ['paused', 'disabled']).includes(s.status)) throw new Error('This schedule state does not support that operation.');
      const result = object(await this.post(`/v1/schedules/${ref}/${pause ? 'pause' : 'resume'}`, { reason: 'Requested from desktop workspace.' }));
      if (result.schedule?.status !== desired) throw new Error('Schedule owner did not confirm the requested state.');
      return { summary: pause ? 'Schedule paused.' : 'Schedule resumed.' };
    }
    if (input.kind === 'job-retry' || input.kind === 'job-cancel') {
      if (input.owner && input.owner !== 'loom' && input.owner !== 'jobs') throw new Error('Unsupported job owner.');
      const ref = reference(input.ref), retry = input.kind === 'job-retry';
      const d = object(await this.read(`/v1/jobs/${ref}`)), j = object(d.job);
      if (retry ? !['failed', 'timed_out'].includes(j.status) : j.status !== 'queued') throw new Error(retry ? 'Only failed or timed-out jobs support retry.' : 'Only queued jobs support cancellation.');
      if (retry && input.force !== true && Number.isInteger(j.attempt_count) && Number.isInteger(j.max_attempts) && j.attempt_count >= j.max_attempts) throw new Error('Job attempts exhausted; an explicit forced retry is required.');
      const result = object(await this.post(`/v1/jobs/${ref}/${retry ? 'retry' : 'cancel'}`, retry ? { force: input.force === true } : {}));
      if (result.status !== (retry ? 'queued' : 'cancelled')) throw new Error('Job owner did not confirm the requested state.');
      return { summary: retry ? 'Job requeued.' : 'Queued job cancelled.' };
    }
    throw new Error('Unsupported operation.');
  }

  timing(input) {
    const cron = required(input.expression ?? input.cron, 'Cron expression', 256), timezone = required(input.timezone, 'Timezone', 100);
    if (cron.split(/\s+/).length !== 5) throw new Error('A five-field cron expression is required.');
    try { new Intl.DateTimeFormat('en', { timeZone: timezone }); } catch { throw new Error('Timezone must be an IANA timezone.'); }
    const misfire = input.misfirePolicy ?? input.misfire_policy ?? 'mark_missed';
    const lateness = input.latenessWindowSeconds ?? input.lateness_window_seconds;
    if (!['mark_missed', 'run_if_late_within'].includes(misfire) || (misfire === 'run_if_late_within' && (!Number.isSafeInteger(lateness) || lateness <= 0))) throw new Error('A supported misfire policy and positive lateness window are required.');
    return { cron, timezone, misfire_policy: misfire, ...(misfire === 'run_if_late_within' ? { lateness_window_seconds: lateness } : {}) };
  }
}
