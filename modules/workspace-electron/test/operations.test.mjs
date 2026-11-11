import test from 'node:test';
import assert from 'node:assert/strict';
import { Operations } from '../src/operations.mjs';

const time = '2026-11-01T12:00:00Z';
const privatePayload = 'private-payload-must-not-reach-renderer';
const schedule = { schedule_id: 'schedule_one', schedule_key: 'one', display_name: 'One', status: 'paused', schedule_kind: 'cron', schedule_expr: '0 9 * * *', timezone: 'Europe/Amsterdam' };
const native = { owner: 'hermes', source: { profile: 'mina' }, availability: 'available', observed_at: time, jobs: [{ id: 'native_one', enabled: true, native_state: 'scheduled', schedule: { kind: 'cron', expression: '0 7 * * *', timezone: null }, next_run_at: time, last_run_at: null, last_result: 'unknown', prompt: privatePayload, name: privatePayload }] };

function mockRemote(routes) {
  const calls = [];
  return {
    calls,
    async api(route, query = {}, options = {}) {
      calls.push({ route, query, ...options });
      const handler = routes[`${options.method || 'GET'} ${route}`];
      assert.notEqual(handler, undefined, `Unexpected request: ${options.method || 'GET'} ${route}`);
      const data = typeof handler === 'function' ? await handler({ route, query, ...options }) : handler;
      return { data: structuredClone(data), ms: 1 };
    },
  };
}

test('project list is compact, bounded, classified and cached for 30 seconds', async t => {
  let now = Date.now();
  t.mock.method(Date, 'now', () => now);
  const remote = mockRemote({ 'GET /v1/projects': [
    { project_id: 'project_one', name: 'One', status: 'active', metadata: { work_state: 'working', secret: privatePayload }, updated_at: time },
    { project_id: 'project_two', name: 'Two', status: 'archived', metadata: { work_state: 'working' } },
    { project_id: 'project_three', name: 'Three', status: 'active', metadata: {} },
  ] });
  const operations = new Operations(remote);
  const first = await operations.overview({ section: 'projects' });
  assert.deepEqual(first.items.map(p => p.posture), ['working', 'archived', 'unspecified']);
  assert.equal(first.items[2].legacyActive,true);
  assert.equal(remote.calls[0].query.limit, 50);
  assert.ok(!JSON.stringify(first).includes(privatePayload));
  first.items[0].name = 'renderer mutation';
  assert.equal((await operations.overview({ section: 'projects' })).items[0].name, 'One');
  assert.equal(remote.calls.length, 1);
  now += 30_001;
  await operations.overview({ section: 'projects' });
  assert.equal(remote.calls.length, 2);
  await operations.overview({ section: 'projects', refresh: true });
  assert.equal(remote.calls.length, 3);
});

test('project detail uses exact Box containment, bounded resources and no guessed Basecamp link', async () => {
  const remote = mockRemote({
    'GET /v1/projects/project_one': { project: { project_id: 'project_one', name: 'One', status: 'active', metadata: { basecamp: 'https://example.invalid/guess' } }, workspace_view: { root_path: '/wrong/path' } },
    'GET /v1/projects/project_one/declaration-status': { target: { project_root: '/srv/loom/box/Projects/one' }, lifecycle: 'active', work_state: 'passive', development: { posture: 'present', purpose: 'A purpose', current_focus: 'A focus', progress: 'A complete paragraph.\n'.repeat(60), private: privatePayload }, resources: { files: { path: '.', binding: { kind: 'knowledge' }, readiness: { applied: { state: 'satisfied' } }, secret: privatePayload } } },
    'GET /v1/projects/project_one/repos': { repositories: [{ key: 'source', relative_path: 'repos/source', role: 'primary', observation_posture: 'present' }, { key: 'escape', relative_path: '../escape' }] },
    'GET /v1/services': [],
    'GET /v1/files/roots': [{ ref: 'box', source_path: '/srv/loom/box' }],
  });
  const operations = new Operations(remote);
  const result = await operations.overview({ section: 'project', ref: 'project_one' });
  assert.equal(result.name, 'One');
  assert.equal(result.path, 'Projects/one');
  assert.equal(result.facts.Focus, 'A focus');
  assert.equal(result.facts.Progress,'A complete paragraph.\n'.repeat(60).trim());
  assert.equal(result.facts['Work state'], 'passive');
  assert.equal(result.groups[0].items[0].path, 'Projects/one/repos/source');
  assert.equal(result.groups[0].items[1].path, undefined);
  assert.equal(result.groups[1].items[0].path, undefined);
  assert.equal(result.basecamp, undefined);
  assert.ok(!JSON.stringify(result).includes(privatePayload));
  assert.equal(remote.calls.find(c => c.route.endsWith('/repos')).query.limit, 20);
  assert.equal(remote.calls.find(c => c.route === '/v1/services').query.project, 'project_one');
  await operations.overview({ section: 'projects' }).catch(() => {});
  assert.equal((await operations.overview({ section: 'project', ref: 'project_one' })).cached, true);
});

test('long project detail uses only captured prose with matching project and development digest',async()=>{
  const snapshot={project_id:'project_one',context:{development:{source_digest:'old',purpose:'Detailed purpose. '.repeat(60),private:privatePayload}}};
  const remote=mockRemote({
    'GET /v1/projects/project_one':{project:{name:'One',status:'active'}},
    'GET /v1/projects/project_one/declaration-status':{target:{project_root:'/srv/loom/box/Projects/one'},development:{posture:'present',source_digest:'current',truncated_fields:['purpose'],purpose:'Summary...'}},
    'GET /v1/projects/project_one/repos':{repositories:[]},'GET /v1/services':[],
    'GET /v1/files/roots':[{ref:'box',source_path:'/srv/loom/box'}],
    'GET /v1/provenance/projects/project_one':()=>snapshot,
  });
  const operations=new Operations(remote),first=await operations.overview({section:'project',ref:'project_one'});
  assert.equal(first.facts.Purpose,'Summary...');assert.match(first.facts['Context scope'],/Summary only/);
  snapshot.context.development.source_digest='current';
  const second=await operations.overview({section:'project',ref:'project_one',refresh:true});
  assert.equal(second.facts.Purpose,snapshot.context.development.purpose.trim());assert.ok(!JSON.stringify(second).includes(privatePayload));
});

test('unmapped or outside-Box project roots never become reconstructed navigation paths', async () => {
  const remote = mockRemote({
    'GET /v1/projects/project_one': { project: { name: 'One', status: 'active' } },
    'GET /v1/projects/project_one/declaration-status': { target: { project_root: '/srv/loom/box-other/one' } },
    'GET /v1/projects/project_one/repos': { repositories: [{ key: 'source', relative_path: 'repo' }] },
    'GET /v1/services': [], 'GET /v1/files/roots': [{ ref: 'box', source_path: '/srv/loom/box' }],
  });
  const result = await new Operations(remote).overview({ section: 'project', ref: 'project_one' });
  assert.equal(result.path, undefined);
  assert.equal(result.facts.Status,'active');
  assert.equal(result.groups[0].items[0].path, undefined);
});

test('project context reads exact captured provenance and a bounded verified Basecamp mapping',async()=>{
  const remote=mockRemote({
    'GET /v1/projects/project_one':{project:{project_id:'project_one',name:'One',status:'active',metadata:{v0_3_project_contract:{project_root:'/srv/loom/box/Projects/one'}}}},
    'GET /v1/provenance/projects/project_one':{project_id:'project_one',snapshot_id:'snapshot_one',observed_at:time,context:{development:{purpose:'A purpose',current_focus:'A focus',documents:[{content:privatePayload}]}}},
    'GET /v1/files/roots':[{ref:'box',source_path:'/srv/loom/box'}],
  });
  remote.smallText=async(root,path)=>{assert.equal(root,'box');assert.equal(path,'Projects/one/.basecamp/project-url');return 'https://app.basecamp.com/300001/buckets/48479304';};
  const result=await new Operations(remote).inspect({kind:'project-context',ref:'project_one'});
  assert.match(result.summary,/A purpose/);assert.match(result.summary,/not a fresh observation/);
  assert.equal(result.basecamp,'https://app.basecamp.com/300001/buckets/48479304');
  assert.ok(!JSON.stringify(result).includes(privatePayload));
  remote.smallText=async()=> 'https://app.basecamp.com.evil.invalid/300001/buckets/48479304';
  assert.equal((await new Operations(remote).inspect({kind:'project-context',ref:'project_one'})).basecamp,undefined);
});

test('archived project opens its verified current payload, not the old signed path',async()=>{
  for(const match of [true,false]){
    const remote=mockRemote({
      'GET /v1/projects/project_one':{project:{project_id:'project_one',name:'One',status:'archived',archive_state:{schema_version:'project.physical_archive_state.v1',project_id:match?'project_one':'other',operation_id:'archive_one',phase:'complete',status:'archived',archive_path:'/srv/loom/storage/archive/projects/one/project'}}},
      'GET /v1/projects/project_one/archive/inspect':{physical:{project_id:'project_one',archive_operation_id:'archive_one',status:'archived',evidence_status:'verified',current_payload:{root:'storage',relative_path:'Projects/one'},relocation_id:'retained_relocation_one'}},
      'GET /v1/files/roots':[{ref:'storage',source_path:'/srv/loom/storage',available:true,read_only:true}],
    });
    const result=await new Operations(remote).overview({section:'project',ref:'project_one'});
    assert.equal(result.facts.Status,'archived');assert.equal(result.path,match?'Projects/one':undefined);
    assert.equal(result.facts.Folder,match?'/srv/loom/storage/Projects/one':'Archive location unavailable');
    assert.equal(remote.calls.length,3);assert.ok(!remote.calls.some(c=>/repos|declaration/.test(c.route)));
  }
});

test('archive navigation refuses mismatched, unverified and unsafe current locations',async()=>{
  const physical={project_id:'project_one',archive_operation_id:'archive_one',status:'archived',evidence_status:'verified',current_payload:{root:'storage',relative_path:'Projects/one'}};
  for(const change of [{project_id:'other'},{archive_operation_id:'archive_old'},{evidence_status:'pending'},{status:'restored'},{current_payload:{root:'box',relative_path:'Projects/one'}},{current_payload:{root:'storage',relative_path:'../outside'}},{current_payload:{root:'storage',relative_path:'/outside'}}]){
    const remote=mockRemote({
      'GET /v1/projects/project_one':{project:{project_id:'project_one',name:'One',status:'archived',archive_state:{schema_version:'project.physical_archive_state.v1',project_id:'project_one',operation_id:'archive_one',phase:'complete',status:'archived',archive_path:'/srv/loom/storage/archive/projects/one/project'}}},
      'GET /v1/projects/project_one/archive/inspect':{physical:{...physical,...change}},
      'GET /v1/files/roots':[{ref:'storage',source_path:'/srv/loom/storage',available:true,read_only:true}],
    });
    const result=await new Operations(remote).overview({section:'project',ref:'project_one'});
    assert.equal(result.path,undefined);assert.ok(result.errors.length);
  }
});

test('completed inactive restore is shown as inactive rather than an unavailable active declaration',async()=>{
  const remote=mockRemote({
    'GET /v1/projects/project_one':{project:{project_id:'project_one',name:'One',status:'active',metadata:{v0_3_project_contract:{project_root:'/srv/loom/box/Projects/one'}},archive_state:{schema_version:'project.physical_archive_state.v1',project_id:'project_one',restore:{schema_version:'project.physical_restore_state.v1',phase:'complete',activation_state:'inactive',operation_id:'restore_one',restored_at:time}}}},
    'GET /v1/files/roots':[{ref:'box',source_path:'/srv/loom/box',available:true}],
  });
  const result=await new Operations(remote).overview({section:'project',ref:'project_one'});
  assert.equal(result.path,'Projects/one');assert.equal(result.facts.Runtime,'Restored; inactive');assert.deepEqual(result.errors,[]);
  assert.ok(!remote.calls.some(c=>/declaration|reactivate|repos/.test(c.route)));
});

test('activity reads bounded inventories without pipeline status, logs or raw payloads', async () => {
  const remote = mockRemote({
    'GET /v1/jobs': [{ job_id: 'job_one', job_type: 'script_run', status: 'running', manual_action_required: true, failure_code: 'execution_failed', updated_at: time, input_json: { secret: privatePayload }, output_json: privatePayload }],
    'GET /v1/knowledge/notes/pipelines': [{ knowledge_pipeline_run_id: 'pipeline_one', pipeline_definition_key: 'notes', status: 'processing', current_stage_key: 'ocr', plan_snapshot: privatePayload }],
    'GET /v1/indexes/status': [{ index_status_id: 'index_one', index_type: 'lexical', status: 'queued', metadata: privatePayload }],
  });
  const result = await new Operations(remote).overview({ section: 'activity' });
  assert.equal(result.items.length, 3);
  assert.deepEqual(result.items.map(i => i.owner), ['jobs', 'notes', 'index']);
  assert.equal(result.items[0].status, 'requires_manual_action');
  assert.ok(result.limited);
  assert.deepEqual(remote.calls.map(c => c.query.limit), [30, 10, 10]);
  assert.ok(!JSON.stringify(result).includes(privatePayload));
});

test('schedule owners retain native unknowns without prompt or title reads', async () => {
  const remote = mockRemote({ 'GET /v1/schedules': [schedule], 'GET /v1/schedules/hermes': native });
  const result = await new Operations(remote).overview({ section: 'schedules' });
  assert.equal(result.items[0].owner, 'loom');
  assert.equal(result.items[0].result, 'unknown');
  assert.equal(result.items[1].name, 'mina \u00b7 native_one');
  assert.equal(result.items[1].profile,'mina');
  assert.equal(result.items[1].timezone, null);
  assert.equal(result.items[1].result, 'unknown');
  assert.ok(!JSON.stringify(result).includes(privatePayload));
  assert.deepEqual(remote.calls[1].query, {});
});

test('Hermes rows and inspection preserve profile/host identity when native IDs repeat',async()=>{
  const observation={...native,jobs:[{...native.jobs[0],profile:'mina'},{...native.jobs[0],profile:'reader',native_state:'paused'}]};
  const remote=mockRemote({'GET /v1/schedules':[],'GET /v1/schedules/hermes':observation});
  const operations=new Operations(remote),result=await operations.overview({section:'schedules'});
  assert.equal(result.items[1].name,'reader · native_one');assert.equal(result.items[1].profile,'reader');
  const detail=await operations.inspect({kind:'schedule',owner:'hermes',ref:'native_one',profile:'reader'});
  assert.match(detail.summary,/Profile: reader/);assert.match(detail.summary,/Status: paused/);
  await assert.rejects(operations.inspect({kind:'schedule',owner:'hermes',ref:'native_one'}),/ambiguous/);
});

test('sources fail independently, preserve qualified snapshots and never refresh stale timestamps', async () => {
  let failHermes = false, failAll = false;
  const remote = mockRemote({
    'GET /v1/schedules': () => { if (failAll) throw new Error('offline'); return [{ ...schedule, display_name: failHermes ? 'Fresh LOOM' : 'Original' }]; },
    'GET /v1/schedules/hermes': () => { if (failAll) throw new Error('offline'); if (failHermes) throw Object.assign(new Error(privatePayload), { status: 503 }); return native; },
  });
  const operations = new Operations(remote);
  await operations.overview({ section: 'schedules' });
  failHermes = true;
  const partial = await operations.overview({ section: 'schedules', refresh: true });
  assert.equal(partial.items[0].name, 'Fresh LOOM');
  assert.equal(partial.items[1].id, 'native_one');
  assert.equal(partial.cached, true);
  assert.equal(partial.offline, false);
  assert.match(partial.errors[0], /Hermes schedules unavailable.*503.*previous snapshot/);
  assert.ok(!JSON.stringify(partial).includes(privatePayload));
  failAll = true;
  const offline = await operations.overview({ section: 'schedules', refresh: true });
  assert.equal(offline.offline, true);
  assert.equal(offline.cached, true);
  assert.equal(offline.observedAt, partial.observedAt);
});

test('unavailable Hermes inventory is not represented as an empty healthy source', async () => {
  const remote = mockRemote({ 'GET /v1/schedules': [], 'GET /v1/schedules/hermes': { availability: 'unavailable', jobs: null } });
  const result = await new Operations(remote).overview({ section: 'schedules' });
  assert.deepEqual(result.items, []);
  assert.match(result.errors[0], /Hermes schedules unavailable/);
});

test('hidden overview cancellation aborts transport and never stores an offline fallback', async () => {
  let observed;
  const remote = { api: (_route, _query, { signal }) => new Promise((_resolve, reject) => {
    observed = signal;
    signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })), { once: true });
  }) };
  const operations = new Operations(remote);
  const pending = operations.overview({ section: 'projects' });
  assert.ok(observed);
  operations.cancel();
  await assert.rejects(pending, { name: 'AbortError' });
  assert.equal(observed.aborted, true);
  assert.equal(operations.cache.size, 0);
});

test('switching sections cancels the old overview even when transport ignores abort', async () => {
  let finish;
  const remote = mockRemote({
    'GET /v1/projects': () => new Promise(resolve => { finish = resolve; }),
    'GET /v1/schedules': [], 'GET /v1/schedules/hermes': { ...native, jobs: [] },
  });
  const operations = new Operations(remote);
  const first = operations.overview({ section: 'projects' });
  await operations.overview({ section: 'schedules' });
  finish([]);
  await assert.rejects(first, { name: 'AbortError' });
  assert.equal(operations.cache.has('projects:'), false);
});

test('system does not infer runtime or coverage health from successful connection health', async () => {
  const remote = mockRemote({
    'GET /v1/mini-dashboard/status': { runtime: { available: false, state: 'unavailable', queued: 0 }, coverage: { available: false }, network: { available: false }, local_backup: { available: false }, cloud_snapshot: { available: false }, raw: privatePayload },
    'GET /v1/health': { status: 'ok', node: { id: 'main' }, version: '1', checks: { database: { status: 'ok' } } },
    'GET /v1/services': [{ display_name: 'Example', health_status: 'unknown', process_state: 'running', registry_state: 'active', runtime_profile: privatePayload }],
    'GET /v1/workers': [{ display_name: 'Indexer', health_status: 'healthy', lifecycle_status: 'active', enabled: true, paused: true, config_json: privatePayload }],
  });
  const result = await new Operations(remote).overview({ section: 'system' });
  const items = result.groups.flatMap(g => g.items);
  assert.equal(items.find(i => i.name === 'Main connection').status, 'ok');
  assert.ok(!remote.calls.some(c => c.route === '/v1/mini-dashboard/status'));
  const protection = await new Operations(remote).overview({ section: 'system', ref: 'protection' });
  assert.equal(protection.groups.flatMap(g => g.items).find(i => i.name === 'Runtime summary').status, 'unavailable');
  assert.equal(protection.groups.flatMap(g => g.items).find(i => i.name === 'Coverage').status, 'unavailable');
  assert.equal(items.find(i => i.name === 'Indexer').status, 'paused');
  assert.ok(!JSON.stringify(result).includes(privatePayload));
  assert.ok(remote.calls.every(c => c.method === undefined));
  assert.equal(remote.calls.find(c => c.route === '/v1/workers').query.limit, 30);
});

test('system qualifies stale domain evidence and keeps independent service failures visible', async () => {
  const remote = mockRemote({
    'GET /v1/mini-dashboard/status': { runtime: { available: true, state: 'live', freshness: { source_state: 'live', source_updated_at: '2000-01-01T00:00:00Z', stale_after_seconds: 30 } } },
    'GET /v1/health': () => { throw Object.assign(new Error(privatePayload), { status: 503 }); },
    'GET /v1/services': () => { throw Object.assign(new Error(privatePayload), { status: 403 }); },
    'GET /v1/workers': [],
  });
  remote.api = ((original) => async (route, ...args) => route === '/v1/system/host' ? { data: { host: {} } } : original(route, ...args))(remote.api);
  const operations = new Operations(remote);
  const result = await operations.overview({ section: 'system' });
  const protection = await operations.overview({ section: 'system', ref: 'protection' });
  assert.equal(protection.groups.flatMap(g => g.items).find(i => i.name === 'Runtime summary').status, 'stale');
  assert.equal(result.errors.length, 2);
  assert.ok(!JSON.stringify(result).includes(privatePayload));
});

test('job and pipeline inspectors select concise evidence, never logs or input/output values', async () => {
  const remote = mockRemote({
    'GET /v1/jobs/job_one': { job: { status: 'failed', failure_code: 'runner_failed', attempt_count: 1, max_attempts: 3, input_json: privatePayload }, outputs: [{ value_json: privatePayload }], logs: [{ tail_text: privatePayload }] },
    'GET /v1/knowledge/notes/pipelines/pipeline_one': { run: { status: 'failed', current_stage_key: 'ocr', last_error_code: 'ocr_failed', plan_snapshot: privatePayload }, stages: [{ stage_key: 'ocr', status: 'failed', error: privatePayload }] },
  });
  const operations = new Operations(remote);
  const job = await operations.inspect({ kind: 'activity', ref: 'job_one', owner: 'jobs' });
  const pipeline = await operations.inspect({ kind: 'activity', ref: 'pipeline_one', owner: 'notes' });
  assert.match(job.summary, /runner_failed/);
  assert.match(pipeline.summary, /ocr_failed/);
  assert.ok(!JSON.stringify([job, pipeline]).includes(privatePayload));
  assert.deepEqual(Object.keys(job), ['summary']);
});

test('schedule inspection resolves the actual last fire without inventory fan-out', async () => {
  const remote = mockRemote({
    'GET /v1/schedules/schedule_one': { schedule: { ...schedule, last_schedule_fire_id: 'fire_one', input_json: privatePayload } },
    'GET /v1/schedule-fires/fire_one': { status: 'failed', failure_code: 'target_unavailable', job_id: 'job_one', failure_message: privatePayload },
  });
  const result = await new Operations(remote).inspect({ kind: 'schedules', ref: 'schedule_one', owner: 'loom' });
  assert.match(result.summary, /Last result: failed/);
  assert.match(result.summary, /Job: job_one/);
  assert.ok(!result.summary.includes(privatePayload));
  assert.equal(remote.calls.length, 2);
});

test('Hermes inspection never requests a prompt or native mutation', async () => {
  const remote = mockRemote({ 'GET /v1/schedules/hermes': native });
  const result = await new Operations(remote).inspect({ kind: 'schedules', ref: 'native_one', owner: 'hermes' });
  assert.match(result.summary, /Timezone: unknown/);
  assert.ok(!result.summary.includes(privatePayload));
  assert.equal(remote.calls.length, 1);
});

test('project creation previews before publishing and checks retained registration state', async () => {
  const remote = mockRemote({ 'POST /v1/project-scaffolds': ({ body }) => ({ ok: true, validation: body.dry_run ? {state:'planned_only',errors:0,warnings:0} : {state:'passed',ok:true,errors:0,warnings:0}, project_root: '/srv/loom/box/Projects/example', project_id: 'project_new', ...(body.dry_run ? {} : { context_state: 'registered_refresh_pending' }) }) });
  const result = await new Operations(remote).action({ kind: 'project-create', name: 'Example', slug: 'example' });
  assert.equal(result.ref, 'project_new');
  assert.deepEqual(remote.calls.map(c => c.body.dry_run), [true, false]);
  assert.deepEqual(remote.calls[0].body, { mode: 'declaration', name: 'Example', slug: 'example', dry_run: true });
});

test('invalid project inputs and failed preview cannot publish', async () => {
  const remote = mockRemote({ 'POST /v1/project-scaffolds': { ok: false, validation: { ok: false } } });
  const operations = new Operations(remote);
  for (const input of [{ name: '', slug: 'example' }, { name: 'Example', slug: '../elsewhere' }, { name: 'Example', slug: 'example', description: 'Unsupported description' }]) await assert.rejects(operations.action({ kind: 'project-create', ...input }));
  assert.equal(remote.calls.length, 0);
  await assert.rejects(operations.action({ kind: 'project-create', name: 'Example', slug: 'example' }), /preview did not validate/);
  assert.equal(remote.calls.length, 1);
});

test('schedule creation is paused, previewed and stable across a lost response', async () => {
  let loseResponse = true;
  const remote = mockRemote({ 'POST /v1/schedules': ({ body }) => {
    if (!body.dry_run && loseResponse) { loseResponse = false; throw new Error('connection lost'); }
    return { schedule: { ...schedule, schedule_id: 'schedule_new', status: body.status } };
  } });
  const operations = new Operations(remote);
  const input = { kind: 'schedule-create', name: 'Future example', capability: 'main.example.query', expression: '0 9 * * *', timezone: 'Europe/Amsterdam', input: '{"count":1}', project: 'project_one' };
  await assert.rejects(operations.action(input), /connection lost/);
  const result = await operations.action(input);
  assert.equal(result.ref, 'schedule_new');
  const writes = remote.calls.filter(c => !c.body.dry_run);
  assert.equal(writes[0].headers['X-Loom-Idempotency-Key'], writes[1].headers['X-Loom-Idempotency-Key']);
  assert.deepEqual(writes[0].body, writes[1].body);
  assert.equal(writes[0].body.status, 'paused');
  assert.equal(writes[0].body.schedule_kind, 'cron');
  assert.equal(writes[0].body.project_ref, 'project_one');
  assert.deepEqual(writes[0].body.input_json, { count: 1 });
  assert.ok(remote.calls.every(c => !c.signal));
});

test('invalid schedule input and unbound Hermes actions perform no writes', async () => {
  const remote = mockRemote({});
  const operations = new Operations(remote);
  const input = { kind: 'schedule-create', name: 'Example', capability: 'example.query', expression: '0 9 * * *', timezone: 'UTC' };
  for (const override of [{ input: '[]' }, { input: 'bad-json' }, { timezone: 'Not/AZone' }, { expression: 'bad' }]) await assert.rejects(operations.action({ ...input, ...override }));
  await assert.rejects(operations.action({ kind: 'schedule-resume', owner: 'hermes', ref: 'native_one' }), /exact registered native profile/);
  await assert.rejects(operations.action({ kind: 'arbitrary', route: '/v1/workers/repair-stale' }), /Unsupported operation/);
  await assert.rejects(operations.inspect({ kind: 'activity', ref: '../secrets' }), /Invalid resource/);
  assert.equal(remote.calls.length, 0);
});

test('timing edit requires a freshly read paused cron and preserves paused state', async () => {
  let current = { ...schedule };
  const remote = mockRemote({
    'GET /v1/schedules/schedule_one': () => ({ schedule: current }),
    'POST /v1/schedules/schedule_one/timing': ({ body }) => ({ schedule: { ...schedule, schedule_expr: body.cron, timezone: body.timezone } }),
  });
  const operations = new Operations(remote);
  const input = { kind: 'schedule-timing', ref: 'schedule_one', expression: '0 10 * * *', timezone: 'Europe/Amsterdam', misfirePolicy: 'run_if_late_within', latenessWindowSeconds: 120 };
  await operations.action(input);
  const write = remote.calls[1];
  assert.equal(write.body.lateness_window_seconds, 120);
  assert.equal(write.body.misfire_policy, 'run_if_late_within');
  assert.ok(write.headers['X-Loom-Idempotency-Key']);
  current = { ...schedule, status: 'active' };
  await assert.rejects(operations.action(input), /Only a paused LOOM cron/);
  current = { ...schedule, schedule_kind: 'interval' };
  await assert.rejects(operations.action(input), /Only a paused LOOM cron/);
  assert.equal(remote.calls.filter(c => c.method === 'POST').length, 1);
});

test('new timing edits do not replay a prior confirmed edit to the same expression', async () => {
  let current = { ...schedule };
  const remote = mockRemote({
    'GET /v1/schedules/schedule_one': () => ({ schedule: current }),
    'POST /v1/schedules/schedule_one/timing': ({ body }) => ({ schedule: current = { ...current, schedule_expr: body.cron } }),
  });
  const operations = new Operations(remote);
  for (const expression of ['0 10 * * *', '0 11 * * *', '0 10 * * *']) await operations.action({ kind: 'schedule-timing', ref: 'schedule_one', expression, timezone: 'Europe/Amsterdam' });
  const keys = remote.calls.filter(c => c.method === 'POST').map(c => c.headers['X-Loom-Idempotency-Key']);
  assert.equal(new Set(keys).size, 3);
  assert.equal(current.schedule_expr, '0 10 * * *');
});

test('concurrent identical create requests share one mutation, subsequent requests get new intent', async () => {
  const remote = mockRemote({ 'POST /v1/schedules': ({ body }) => ({ schedule: { ...schedule, schedule_id: 'schedule_new', status: body.status } }) });
  const operations = new Operations(remote);
  const input = { kind: 'schedule-create', name: 'Example', capability: 'example.query', expression: '0 9 * * *', timezone: 'UTC' };
  await Promise.all([operations.action(input), operations.action(input)]);
  assert.equal(remote.calls.length, 2);
  const firstKey = remote.calls[1].headers['X-Loom-Idempotency-Key'];
  await operations.action(input);
  assert.notEqual(remote.calls[3].headers['X-Loom-Idempotency-Key'], firstKey);
});

test('schedule lifecycle checks actual state, supports pause/resume and refuses completed', async () => {
  let current = { ...schedule };
  const remote = mockRemote({
    'GET /v1/schedules/schedule_one': () => ({ schedule: current }),
    'POST /v1/schedules/schedule_one/resume': () => ({ schedule: current = { ...current, status: 'active' } }),
    'POST /v1/schedules/schedule_one/pause': () => ({ schedule: current = { ...current, status: 'paused' } }),
  });
  const operations = new Operations(remote);
  await operations.action({ kind: 'schedule-resume', ref: 'schedule_one' });
  await operations.action({ kind: 'schedule-pause', ref: 'schedule_one' });
  await operations.action({ kind: 'schedule-pause', ref: 'schedule_one' });
  assert.equal(remote.calls.filter(c => c.method === 'POST').length, 2);
  current.status = 'completed';
  await assert.rejects(operations.action({ kind: 'schedule-resume', ref: 'schedule_one' }), /does not support/);
});

test('job retry and cancellation check fresh supported state, not cached renderer claims', async () => {
  let status = 'running';
  const remote = mockRemote({
    'GET /v1/jobs/job_one': () => ({ job: { status, attempt_count: 1, max_attempts: 2 } }),
    'POST /v1/jobs/job_one/retry': { status: 'queued' },
    'POST /v1/jobs/job_one/cancel': { status: 'cancelled' },
  });
  const operations = new Operations(remote);
  await assert.rejects(operations.action({ kind: 'job-cancel', ref: 'job_one', status: 'queued' }), /Only queued/);
  await assert.rejects(operations.action({ kind: 'job-retry', ref: 'job_one' }), /Only failed/);
  status = 'failed';
  await operations.action({ kind: 'job-retry', ref: 'job_one' });
  status = 'queued';
  await operations.action({ kind: 'job-cancel', ref: 'job_one' });
  assert.deepEqual(remote.calls.filter(c => c.method === 'POST').map(c => c.route), ['/v1/jobs/job_one/retry', '/v1/jobs/job_one/cancel']);
});

test('exhausted job attempts require an explicit boolean force', async () => {
  const remote = mockRemote({
    'GET /v1/jobs/job_one': { job: { status: 'timed_out', attempt_count: 2, max_attempts: 2 } },
    'POST /v1/jobs/job_one/retry': { status: 'queued' },
  });
  const operations = new Operations(remote);
  await assert.rejects(operations.action({ kind: 'job-retry', ref: 'job_one', force: 'true' }), /exhausted/);
  await operations.action({ kind: 'job-retry', ref: 'job_one', force: true });
  assert.deepEqual(remote.calls.at(-1).body, { force: true });
});

test('mutations invalidate cached overviews and hidden cancellation never cancels their writes', async () => {
  const remote = mockRemote({
    'GET /v1/projects': [],
    'GET /v1/jobs/job_one': { job: { status: 'queued' } },
    'POST /v1/jobs/job_one/cancel': { status: 'cancelled' },
  });
  const operations = new Operations(remote);
  await operations.overview({ section: 'projects' });
  const result = operations.action({ kind: 'job-cancel', ref: 'job_one' });
  operations.cancel();
  await result;
  await operations.overview({ section: 'projects' });
  assert.equal(remote.calls.filter(c => c.route === '/v1/projects').length, 2);
  assert.equal(remote.calls.find(c => c.method === 'POST').signal, undefined);
});
