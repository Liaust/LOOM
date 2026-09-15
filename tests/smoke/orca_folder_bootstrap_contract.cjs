#!/usr/bin/env node
'use strict';

// Source contract only. CLI cases stub the existing runtime client. Wrapper
// cases also substitute the two executable boundaries because Linux Electron
// cannot execute on the Darwin worker. Routing and CLI parsing are real source.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { createHash } = require('node:crypto');
const vm = require('node:vm');
const cliAt = appDir => path.join(appDir, 'resources/app.asar.unpacked/out/cli');

async function child(appDir, input) {
  const cli = fs.realpathSync(cliAt(appDir));
  const calls = [];
  const clients = [];
  const types = require(path.join(cli, 'runtime/types.js'));
  class RuntimeClient {
    constructor(...args) { this.isRemote = !!input.remote; clients.push(args); }
    async call(method, params) {
      calls.push({ method, bytes: JSON.stringify(params) });
      return { ok: true, result: { fixture: true } };
    }
  }
  const filename = require.resolve(path.join(cli, 'runtime-client.js'));
  require.cache[filename] = { id: filename, filename, loaded: true,
    exports: { ...types, RuntimeClient } };
  if (input.handler) {
    await require(path.join(cli, 'handlers/repo.js')).REPO_HANDLERS['repo add']({
      flags: new Map([['path', input.cwd], ['kind', 'folder']]),
      cwd: input.cwd, client: new RuntimeClient(), json: true,
    });
  } else {
    await require(path.join(cli, 'index.js')).main(input.args, input.cwd);
  }
  fs.writeSync(3, JSON.stringify({ calls, clients }));
}

// Read the pinned archive without running Electron's main bundle.
function archive(appDir) {
  const fd = fs.openSync(path.join(appDir, 'resources/app.asar'), 'r');
  const prefix = Buffer.alloc(16);
  fs.readSync(fd, prefix, 0, 16, 0);
  const headerBytes = Buffer.alloc(prefix.readUInt32LE(12));
  fs.readSync(fd, headerBytes, 0, headerBytes.length, 16);
  const header = JSON.parse(headerBytes);
  const entry = name => name.split('/').reduce((tree, part) => tree.files[part], header);
  return {
    entry,
    read(name) {
      const e = entry(name);
      assert(!e.unpacked);
      const bytes = Buffer.alloc(e.size);
      fs.readSync(fd, bytes, 0, bytes.length, 8 + prefix.readUInt32LE(4) + Number(e.offset));
      return bytes.toString('utf8');
    },
    close() { fs.closeSync(fd); },
  };
}

function dispatcherProof(appDir) {
  const asar = archive(appDir);
  try {
    assert.equal(JSON.parse(asar.read('package.json')).version, '1.4.191');
    assert.equal(asar.entry('out/cli/index.js').unpacked, true);
    assert.equal(asar.entry('out/cli/handlers/repo.js').unpacked, true);
    const main = asar.read('out/main/index.js');
    const commandsStart = main.indexOf('var APPIMAGE_CLI_COMMAND_NAMES = [');
    assert(commandsStart >= 0);
    const commands = JSON.parse(main.slice(main.indexOf('[', commandsStart), main.indexOf('];', commandsStart) + 1));
    assert(commands.includes('repo'));
    assert(!commands.includes('project'));
    assert(!commands.includes('skills'));
    const start = main.indexOf('function maybeRedirectAppImageCliLaunch(');
    assert(start >= 0);
    assert.equal(main.indexOf('function maybeRedirectAppImageCliLaunch(', start + 1), -1);
    // Its next top-level function bounds this compiled, unmodified function.
    const end = main.indexOf('\nfunction ', start + 1);
    assert(end > start);
    const source = main.slice(start, end);
    assert.match(source, /join\)\(resourcesPath, "app.asar.unpacked", "out", "cli", "index.js"\)/);
    assert.match(source, /spawn\$28\(execPath, \[cliEntryPath, \.\.\.cliArgs\]/);
    assert.match(source, /buildElectronRunAsNodeEnv\$1\(env\)/);
    return 'pinned ASAR routes repo to unpacked CLI; project/skills checks use Node directly; ELF execution remains separate';
  } finally { asar.close(); }
}

function unchangedPayload(original, patched) {
  const mutable = new Set(['handlers/repo.js', 'handlers/project.js', 'specs/core.js',
    'help.js', 'bundled-skill-guides.js'].map(name => `resources/app.asar.unpacked/out/cli/${name}`));
  const added = 'resources/app.asar.unpacked/out/cli/repo-kind-flag.js';
  const inventory = root => {
    const entries = new Map();
    function walk(directory) {
      for (const name of fs.readdirSync(path.join(root, directory))) {
        const relative = path.join(directory, name);
        const stat = fs.lstatSync(path.join(root, relative));
        if (stat.isDirectory()) walk(relative);
        else if (stat.isSymbolicLink()) entries.set(relative, `link:${fs.readlinkSync(path.join(root, relative))}`);
        else entries.set(relative, createHash('sha256').update(fs.readFileSync(path.join(root, relative))).digest('hex'));
      }
    }
    walk('');
    return entries;
  };
  const before = inventory(original), after = inventory(patched);
  assert(!before.has(added));
  assert(after.has(added));
  assert.equal(after.size, before.size + 1);
  for (const [name, hash] of before) {
    assert(after.has(name), `removed payload: ${name}`);
    if (mutable.has(name)) assert.notEqual(after.get(name), hash, `missing change: ${name}`);
    else assert.equal(after.get(name), hash, `unrelated payload changed: ${name}`);
  }
  return { filesCompared: before.size, nativeModulesCompared: [...before.keys()].filter(name => name.endsWith('.node')).length };
}

function dispatchMatrix(appDir, wrapperFile, immutableAppDir) {
  const wrapper = fs.readFileSync(wrapperFile, 'utf8');
  assert(immutableAppDir.startsWith('/nix/store/'));
  assert(wrapper.includes(`export APPDIR=${immutableAppDir}`));
  const appRunHash = createHash('sha256').update(fs.readFileSync(path.join(appDir, 'AppRun'))).digest('hex');
  assert.equal(appRunHash, 'f48e4f9b653213fcdf77c91176503f090a06a9d57f8350a440106ba79e989f91');
  const cli = cliAt(appDir);
  const args = require(path.join(cli, 'args.js'));
  assert.deepEqual(args.GLOBAL_FLAGS, ['help', 'json', 'pairing-code', 'environment']);
  const asar = archive(appDir);
  try {
    const main = asar.read('out/main/index.js');
    assert(main.includes('var CLI_FLAGS_WITH_VALUES = new Set(["--environment", "--pairing-code"]);'));
  } finally { asar.close(); }
  // Execute the exact pinned helper, isolated from unrelated launch imports.
  // The Linux gate must still observe real runtime descendants after startup.
  const launch = fs.readFileSync(path.join(cli, 'runtime/launch.js'), 'utf8');
  const stripSource = launch.match(/function stripElectronRunAsNode\(env\) \{[\s\S]*?\n\}/)?.[0];
  assert(stripSource);
  const stripped = vm.runInNewContext(`(${stripSource})`)({ ELECTRON_RUN_AS_NODE: '1', fixture: 'preserved' });
  assert.equal(stripped.ELECTRON_RUN_AS_NODE, undefined);
  assert.equal(stripped.fixture, 'preserved');
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'loom-orca-dispatch-'));
  let cases = 0;
  try {
    fs.symlinkSync(path.join(appDir, 'resources'), path.join(root, 'resources'), 'dir');
    const script = path.join(root, 'wrapper.sh');
    fs.writeFileSync(script, wrapper.split(immutableAppDir).join(root));
    for (const route of ['AppRun', 'orca-ide']) {
      const program = `#!${process.execPath}\n'use strict';
const fs = require('node:fs');
const { spawnSync } = require('node:child_process');
const argv = process.argv.slice(2);
const envKeys = ['APPDIR', 'ELECTRON_RUN_AS_NODE', 'NODE_OPTIONS', 'NODE_REPL_EXTERNAL_MODULE',
  'ORCA_NODE_OPTIONS', 'ORCA_NODE_REPL_EXTERNAL_MODULE', 'ORCA_APPIMAGE_NO_SANDBOX',
  'PATH', 'XDG_DATA_DIRS', 'LD_LIBRARY_PATH', 'GSETTINGS_SCHEMA_DIR'];
fs.writeSync(4, JSON.stringify({ route: ${JSON.stringify(route)}, argv,
  env: Object.fromEntries(envKeys.map(key => [key, process.env[key]])) }));
if (${JSON.stringify(route)} === 'orca-ide') {
  if (fs.realpathSync(argv[0]) !== ${JSON.stringify(fs.realpathSync(path.join(cli, 'index.js')))}) process.exit(91);
  const result = spawnSync(process.execPath, [${JSON.stringify(__filename)}, '--case',
    ${JSON.stringify(appDir)}, JSON.stringify({ args: argv.slice(1), cwd: process.cwd() })],
    { stdio: ['ignore', 'inherit', 'inherit', 3], env: process.env });
  process.exit(result.status ?? 92);
}
`;
      fs.writeFileSync(path.join(root, route), program, { mode: 0o700 });
    }
    const env = { PATH: process.env.PATH, HOME: root, TMPDIR: root,
      XDG_CONFIG_HOME: path.join(root, 'config'), XDG_DATA_HOME: path.join(root, 'data'),
      XDG_CACHE_HOME: path.join(root, 'cache'), XDG_RUNTIME_DIR: path.join(root, 'runtime'),
      NODE_OPTIONS: '--no-warnings', NODE_REPL_EXTERNAL_MODULE: 'fixture-repl',
      ORCA_NODE_OPTIONS: 'older-options', ORCA_NODE_REPL_EXTERNAL_MODULE: 'older-repl',
      XDG_DATA_DIRS: '/fixture/data', LD_LIBRARY_PATH: '/fixture/lib', GSETTINGS_SCHEMA_DIR: '/fixture/schema' };
    function run(argv, route = 'orca-ide') {
      cases++;
      const result = spawnSync('bash', [script, ...argv], { cwd: root, env, encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'pipe', 'pipe', 'pipe'] });
      assert(!result.error, String(result.error));
      assert(result.output[4], `missing executable receipt: ${result.stderr}`);
      const dispatched = JSON.parse(result.output[4]);
      assert.equal(dispatched.route, route, `wrong route for ${JSON.stringify(argv)}`);
      const receipt = result.output[3] ? JSON.parse(result.output[3]) : { calls: [], clients: [] };
      if (route === 'orca-ide') {
        assert.deepEqual(dispatched.argv, [path.join(root, 'resources/app.asar.unpacked/out/cli/index.js'), ...argv]);
        assert.equal(dispatched.env.ELECTRON_RUN_AS_NODE, '1');
        assert.equal(dispatched.env.NODE_OPTIONS, undefined);
        assert.equal(dispatched.env.NODE_REPL_EXTERNAL_MODULE, undefined);
        assert.equal(dispatched.env.ORCA_NODE_OPTIONS, env.NODE_OPTIONS);
        assert.equal(dispatched.env.ORCA_NODE_REPL_EXTERNAL_MODULE, env.NODE_REPL_EXTERNAL_MODULE);
        assert.equal(dispatched.env.PATH, `${root}:${root}/usr/sbin:${env.PATH}`);
        assert.equal(dispatched.env.XDG_DATA_DIRS, `${root}/usr/share/:${env.XDG_DATA_DIRS}:/usr/share/gnome:/usr/local/share/:/usr/share/`);
        assert.equal(dispatched.env.LD_LIBRARY_PATH, `${root}/usr/lib:${env.LD_LIBRARY_PATH}`);
        assert.equal(dispatched.env.GSETTINGS_SCHEMA_DIR, `${root}/usr/share/glib-2.0/schemas:${env.GSETTINGS_SCHEMA_DIR}`);
      } else {
        assert.deepEqual(dispatched.argv, argv);
        assert.equal(dispatched.env.ELECTRON_RUN_AS_NODE, undefined);
        for (const key of ['NODE_OPTIONS', 'NODE_REPL_EXTERNAL_MODULE', 'PATH', 'XDG_DATA_DIRS', 'LD_LIBRARY_PATH', 'GSETTINGS_SCHEMA_DIR']) {
          assert.equal(dispatched.env[key], env[key]);
        }
      }
      assert.equal(dispatched.env.ORCA_APPIMAGE_NO_SANDBOX, undefined);
      assert.equal(dispatched.env.APPDIR, root);
      return { ...result, ...receipt };
    }
    const guide = require(path.join(cli, 'bundled-skill-guides.js')).BUNDLED_SKILL_GUIDES.find(g => g.name === 'orca-cli');
    for (const prefix of [[], ['--json'], ['--json=skills'], ['--pairing-code', 'project']]) {
      const result = run([...prefix, 'skills', 'get', 'orca-cli']);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.deepEqual(result.calls, []);
      if (prefix.some(arg => arg.startsWith('--json'))) assert.equal(JSON.parse(result.stdout).markdown, guide.markdown);
      else assert.equal(result.stdout.trimEnd(), guide.markdown.trimEnd());
    }
    const project = run(['--json', 'project', 'list']);
    assert.equal(project.status, 0, project.stderr);
    assert.deepEqual(project.calls, [{ method: 'project.list' }]);
    const quotedPath = path.join(root, 'ordinary notes');
    const setup = run(['project', 'setup-existing-folder', '--project', 'fixture', '--host', 'local',
      '--path', quotedPath, '--kind', 'folder', '--json']);
    assert.equal(setup.status, 0, setup.stderr);
    assert.deepEqual(setup.calls, [{ method: 'projectHostSetup.setupExistingFolder',
      bytes: JSON.stringify({ projectId: 'fixture', hostId: 'local', path: quotedPath, kind: 'folder' }) }]);
    for (const argv of [['--environment', 'fixture', 'project', '--help'],
      ['--environment=skills', '--pairing-code=project', 'skills', '--help']]) {
      const result = run(argv);
      assert.equal(result.status, 0, result.stderr);
      assert.deepEqual(result.calls, []);
      assert(result.stdout.includes(argv.includes('project') ? 'project' : 'skills'));
    }
    for (let repetition = 0; repetition < 20; repetition++) {
      for (const argv of [['skills', 'get', '--json'], ['project', 'setup-create', '--json'],
        ['project', 'list', '--environment', '--json'], ['--environment=', 'skills', 'get', 'orca-cli', '--json'],
        ['--environment', '--json', 'skills', 'get', 'orca-cli'],
        ['skills', 'get', 'orca-cli', '--unknown', '--json']]) {
        const result = run(argv);
        assert.equal(result.status, 1, result.stdout + result.stderr);
        assert.deepEqual(result.calls, []);
        assert.equal(JSON.parse(result.stderr || result.stdout).error.code, 'invalid_argument');
      }
      for (const argv of [['--environment', 'skills'], ['--pairing-code', 'project'],
        ['--user-data-dir', 'project'], ['--json', '--user-data-dir', 'skills', 'project', 'list'],
        ['--environment', '--user-data-dir', 'project'], ['--pairing-code', '--user-data-dir', 'skills', 'project', 'list']]) {
        assert.equal(run(argv, 'AppRun').status, 0);
      }
    }
    for (const argv of [[], ['serve', '--port', '0', '--no-pairing'], ['repo', 'list'],
      ['--environment', 'skills', 'repo', 'list'], ['--help'], ['help', 'project'], ['-h'],
      ['--no-sandbox'], ['/fixture/project'], ['--environment=skills'], ['--environment']]) {
      assert.equal(run(argv, 'AppRun').status, 0);
    }
    console.log(JSON.stringify({ gate: 'generated-wrapper-source', cases,
      nativeRuntimeAcceptance: 'not exercised; executables substituted', nativeChildModeHelper: 'pinned helper removes RunAsNode' }));
  } finally {
    assert(path.basename(root).startsWith('loom-orca-dispatch-'));
    fs.rmSync(root, { recursive: true });
    assert(!fs.existsSync(root));
  }
}

function matrix(mode, appDir, original) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'loom-orca-folder-contract-'));
  let cases = 0;
  try {
    const cwd = path.join(root, 'ordinary folder');
    fs.mkdirSync(cwd);
    function run(args, extra = {}) {
      cases++;
      const result = spawnSync(process.execPath, [__filename, '--case', appDir,
        JSON.stringify({ args, cwd, ...extra })], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe', 'pipe'],
        env: { PATH: process.env.PATH, HOME: root, TMPDIR: root,
          XDG_CONFIG_HOME: path.join(root, 'config'), XDG_DATA_HOME: path.join(root, 'data'),
          XDG_CACHE_HOME: path.join(root, 'cache'), XDG_RUNTIME_DIR: path.join(root, 'runtime') },
      });
      assert(!result.error, String(result.error));
      assert(result.output[3], `Missing client receipt: ${result.stderr}`);
      return { ...result, ...JSON.parse(result.output[3]) };
    }
    function ok(args, params, method = 'repo.add', extra = {}) {
      const r = run([...args, '--json'], extra);
      assert.equal(r.status, 0, r.stdout + r.stderr);
      assert.equal(r.stderr, '');
      assert.deepEqual(JSON.parse(r.stdout), { ok: true, result: { fixture: true } });
      assert.deepEqual(r.calls, [{ method, bytes: JSON.stringify(params) }]);
      return r;
    }
    function bad(args, code = 'invalid_argument', extra = {}) {
      const r = run([...args, '--json'], extra);
      assert.equal(r.status, 1, r.stdout + r.stderr);
      assert.deepEqual(r.calls, [], 'invalid input reached RPC');
      const output = JSON.parse(r.stderr || r.stdout);
      assert.equal(output.error.code, code);
      return r;
    }
    const add = ['repo', 'add', '--path', '.'];
    ok(add, { path: cwd });
    if (mode === 'baseline') {
      const r = run([], { handler: true });
      assert.equal(r.status, 0, r.stderr);
      assert.deepEqual(r.calls, [{ method: 'repo.add', bytes: JSON.stringify({ path: cwd }) }]);
      const rejected = run([...add, '--kind', 'folder', '--json']);
      assert.equal(rejected.status, 1);
      assert.deepEqual(rejected.calls, []);
      assert.match(rejected.stderr + rejected.stdout, /kind/);
    } else {
      // Same public positive is also the reverse-patch regression sentinel.
      ok([...add, '--kind', 'folder'], { path: cwd, kind: 'folder' });
      ok([...add, '--kind', 'git'], { path: cwd, kind: 'git' });
      ok(['repo', 'add', '--path', cwd, '--kind=folder'], { path: cwd, kind: 'folder' });
      const remote = ok(['repo', 'add', '--path', '/remote/notes', '--kind', 'folder',
        '--pairing-code', 'fixture-pairing'], { path: '/remote/notes', kind: 'folder' },
        'repo.add', { remote: true });
      assert.equal(remote.clients[0][2], 'fixture-pairing');
      for (let repetition = 0; repetition < 20; repetition++) {
        for (const flags of [['--kind'], ['--kind='], ['--kind', ''], ['--kind', 'other'], ['--kind', 'Folder']]) {
          bad([...add, ...flags]);
        }
        bad([...add, '--kind', 'folder'], 'invalid_argument', { remote: true });
        // Repeated requests only; native duplicate/persistence semantics require runtime acceptance.
        ok([...add, '--kind', 'folder'], { path: cwd, kind: 'folder' });
      }
      bad(['repo', 'add', '--kind', 'folder']);
      const setup = ['project', 'setup-existing-folder', '--project', 'fixture-project',
        '--host', 'local', '--path', '.', '--display-name', 'Notes'];
      const params = { projectId: 'fixture-project', hostId: 'local', path: cwd, displayName: 'Notes' };
      // Compare original setup bytes, including absent kind and other option order.
      const originalCli = appDir;
      for (const kind of [undefined, 'git', 'folder']) {
        const args = kind === undefined ? setup : [...setup, '--kind', kind];
        const expected = { projectId: params.projectId, hostId: params.hostId, path: params.path,
          ...(kind === undefined ? {} : { kind }), displayName: params.displayName };
        ok(args, expected, 'projectHostSetup.setupExistingFolder');
        if (original) {
          appDir = original;
          ok(args, expected, 'projectHostSetup.setupExistingFolder');
          appDir = originalCli;
        }
      }
      for (const command of [setup,
        ['project', 'setup-create', '--project', 'fixture-project', '--host', 'local'],
        ['project', 'setup-create', '--project', 'fixture-project', '--host', 'ssh:fixture'],
        ['project', 'setup-update', '--setup', 'fixture-setup']]) {
        for (const flags of [['--kind'], ['--kind='], ['--kind', 'other']]) bad([...command, ...flags]);
      }
      const otherSetups = [
        { args: ['project', 'setup-create', '--project', 'fixture-project', '--host', 'local',
          '--path', '.', '--kind', 'folder', '--display-name', 'Notes', '--state', 'ready', '--method', 'provisioned'],
          method: 'projectHostSetup.create', params: { projectId: 'fixture-project', hostId: 'local',
            path: cwd, kind: 'folder', displayName: 'Notes', setupState: 'ready', setupMethod: 'provisioned' } },
        { args: ['project', 'setup-update', '--setup', 'fixture-setup', '--path', '.', '--kind', 'git',
          '--display-name', 'Notes', '--worktree-base-path', '/fixture/worktrees', '--git-username', 'Fixture',
          '--state', 'ready', '--method', 'legacy-repo'], method: 'projectHostSetup.update',
          params: { setupId: 'fixture-setup', updates: { displayName: 'Notes', path: cwd,
            worktreeBasePath: '/fixture/worktrees', gitUsername: 'Fixture', kind: 'git',
            setupState: 'ready', setupMethod: 'legacy-repo' } } },
        { args: ['project', 'setup-clone', '--project', 'fixture-project', '--host', 'local',
          '--url', 'https://example.invalid/repo.git', '--destination', '.', '--display-name', 'Notes'],
          method: 'projectHostSetup.clone', params: { projectId: 'fixture-project', hostId: 'local',
            url: 'https://example.invalid/repo.git', destination: cwd, displayName: 'Notes' } },
      ];
      for (const setupCase of otherSetups) {
        ok(setupCase.args, setupCase.params, setupCase.method);
        if (original) {
          appDir = original;
          ok(setupCase.args, setupCase.params, setupCase.method);
          appDir = originalCli;
        }
      }
      for (const args of [['repo', 'add', '--help'], ['--help']]) {
        const r = run(args);
        assert.equal(r.status, 0, r.stderr);
        assert.deepEqual(r.calls, []);
        assert.match(r.stdout, /repo add --path <path> \[--kind git\|folder\]/);
      }
      const guides = require(path.join(cliAt(appDir), 'bundled-skill-guides.js')).BUNDLED_SKILL_GUIDES;
      const guide = guides.find(g => g.name === 'orca-cli');
      assert.match(guide.fullMarkdown, /ORCA repo add --path \/abs\/notes --kind folder --json/);
      assert.equal(guide.markdown, guide.fullMarkdown);
      const publicGuide = run(['skills', 'get', 'orca-cli', '--full', '--json']);
      assert.equal(publicGuide.status, 0, publicGuide.stdout + publicGuide.stderr);
      assert.deepEqual(publicGuide.calls, []);
      assert.deepEqual(JSON.parse(publicGuide.stdout), { name: 'orca-cli', full: true, markdown: guide.fullMarkdown });
      if (original) {
        const before = require(path.join(cliAt(original), 'bundled-skill-guides.js')).BUNDLED_SKILL_GUIDES;
        assert.deepEqual(guides.filter(g => g.name !== 'orca-cli'), before.filter(g => g.name !== 'orca-cli'));
        assert.equal(guide.fullMarkdown.replace('ORCA repo add --path /abs/notes --kind folder --json\n', ''),
          before.find(g => g.name === 'orca-cli').fullMarkdown);
      }
    }
    console.log(JSON.stringify({ gate: 'source-contract', mode, cases, dispatcher: dispatcherProof(appDir),
      payload: mode === 'patched' && original ? unchangedPayload(original, appDir) : 'not compared',
      nativeRuntimeAcceptance: 'not exercised' }));
  } finally {
    assert(path.basename(root).startsWith('loom-orca-folder-contract-'));
    fs.rmSync(root, { recursive: true });
    assert(!fs.existsSync(root));
  }
}

if (process.argv[2] === 'dispatch') {
  dispatchMatrix(fs.realpathSync(process.argv[3]), path.resolve(process.argv[4]), process.argv[5]);
} else if (process.argv[2] === '--case') {
  child(path.resolve(process.argv[3]), JSON.parse(process.argv[4])).catch(error => {
    console.error(error); process.exitCode = 2;
  });
} else {
  const [mode, directory, original] = process.argv.slice(2);
  assert(['baseline', 'patched'].includes(mode), 'usage: contract.cjs baseline|patched APPDIR [ORIGINAL_APPDIR]');
  matrix(mode, path.resolve(directory), original && path.resolve(original));
}
