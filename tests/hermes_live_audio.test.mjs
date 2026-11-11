// Run against the pinned plugin after compat + audio patches; never opens media.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import vm from 'node:vm'

const source = readFileSync(process.env.HERMES_LIVE_PLUGIN_SOURCE || new URL('../.loom-acceptance/hermes-audio-reliability/candidate/desktop/plugin.js', import.meta.url), 'utf8')
const wake = source.slice(source.indexOf('const KEY_WAKE ='), source.indexOf('// END LOOM NATIVE WAKE'))
const call = source.slice(source.indexOf('async function startLive('), source.indexOf('function useLiveState()'))
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const settle = () => new Promise(resolve => setImmediate(resolve))

function fixture(options = {}) {
  const tracks = [], peers = [], contexts = [], players = [], requests = [], intervals = new Map(), loops = []
  const makeTrack = () => {
    const track = new EventTarget()
    Object.assign(track, { kind: 'audio', readyState: 'live', label: 'Test microphone', enabled: true, stops: 0,
      stop() { this.stops++; this.readyState = 'ended' }, getSettings: () => ({ sampleRate: 48000, channelCount: 1, deviceId: 'private-device-id' }) })
    tracks.push(track); return track
  }
  const stream = { getTracks: () => tracks, getAudioTracks: () => tracks }
  makeTrack()
  const node = () => ({ disconnected: 0, connect() {}, disconnect() { this.disconnected++ } })
  class Context {
    constructor() {
      if (options.contextError) throw new Error('context setup failed')
      Object.assign(this, { state: 'running', sampleRate: 48000, destination: {}, nodes: [], closes: 0 })
      contexts.push(this)
    }
    async resume() { this.state = 'running' }
    async close() { this.closes++; this.state = 'closed' }
    createMediaStreamSource() { const n = node(); this.nodes.push(n); return n }
    createGain() { const n = { ...node(), gain: { value: 1 } }; this.nodes.push(n); return n }
    createAnalyser() {
      const n = { ...node(), fftSize: 2048, get frequencyBinCount() { return this.fftSize / 2 },
        getByteFrequencyData(buf) { buf.fill(0) }, getByteTimeDomainData(buf) { buf.fill(128) } }
      this.nodes.push(n); return n
    }
  }
  class Peer {
    constructor() {
      if (options.peerError) throw new Error('peer setup failed')
      this.connectionState = 'new'; this.closes = 0; this.senders = []; this.remoteDescriptions = 0; this.statsRequests = 0
      peers.push(this)
    }
    addTrack(track) { this.senders.push({ track }) }
    getSenders() { return this.senders }
    createDataChannel() { this.dc = { closes: 0, close() { this.closes++ } }; return this.dc }
    async createOffer() { return { sdp: 'offer' } }
    async setLocalDescription() {}
    async setRemoteDescription() { this.remoteDescriptions++; if (options.remoteError) throw new Error('answer rejected') }
    close() { this.closes++; this.connectionState = 'closed'; this.onconnectionstatechange?.() }
    getStats() { this.statsRequests++; return options.stats || Promise.resolve([]) }
  }
  class Player {
    constructor() {
      this.paused = true; this.srcObject = null; this.events = []
      if (options.noSinkSelection) this.setSinkId = undefined
      players.push(this)
    }
    async setSinkId() { this.events.push('sink'); if (options.sinkError) throw new Error('sink lost'); await options.sink }
    async play() { this.events.push('play'); this.paused = false }
    pause() { this.events.push('pause'); this.paused = true }
  }
  const bus = { live: false, stage: 'idle', set(next) { Object.assign(this, next) }, emit() {} }
  let micRequests = 0
  const context = vm.createContext({
    bus, window: {}, sdk: {}, AudioContext: Context, RTCPeerConnection: Peer, Audio: Player,
    MediaStream: class { constructor(tracks) { this.tracks = tracks } },
    navigator: { mediaDevices: { async getUserMedia() { micRequests++; if (options.micError) throw options.micError; return options.microphone || stream } } },
    setInterval(fn, ms) { const id = {}; intervals.set(id, { fn, ms }); return id }, clearInterval(id) { intervals.delete(id) },
    setTimeout, clearTimeout, performance: { now: () => 1000 },
    makeLoop(tick) { const loop = { tick, disposed: false }; loops.push(loop); return loop }, disposeLoop(loop) { loop.disposed = true },
    tr: (es, en) => en, EN: true, clearTranscript() {}, transcript: [], _bumpTranscript() {}, IDLE_MS: 2600,
    liveAudioMs: 0, _liveRefs: null, currentBotTurnId: '', _botSpeakingSince: 0, handoffMode: 'client',
    handleRealtimeEvent() {}, doBargeIn() {}, pushTranscript() {}, playChime() {}
  })
  vm.runInContext(`${wake}\n${call}\nthis.start = startLive; this.Client = NativeWakeClient; this.diagnostics = typeof audioDiagnostics === 'function' ? audioDiagnostics : null;
    this.unload = () => { pluginDisposed = true; activeCall?.close() };
    this.setWake = value => { wakeClient = value }; this.setMicTest = value => { micTestStop = value }`, context)
  const ctx = { storage: { get() { return '' } }, async rest(path, body) {
    requests.push({ path, body })
    if (path === '/codexlive/session') return options.session || { answer: 'answer', threadId: 'test-thread' }
    return {}
  } }
  return { context, bus, tracks, peers, contexts, players, requests, intervals, loops, stream,
    micRequests: () => micRequests, start: (opts = {}) => context.start(ctx, { engine: 'codex', log() {}, ...opts }) }
}

test('early audio and peer setup failures release every acquired microphone/context', async () => {
  for (const option of ['contextError', 'peerError']) {
    const f = fixture({ [option]: true })
    await assert.rejects(f.start(), /setup failed/)
    assert.equal(f.tracks[0].stops, 1)
    assert.equal(f.contexts[0]?.closes || 0, option === 'peerError' ? 1 : 0)
    assert.equal(f.bus.live, false)
    assert.equal(f.intervals.size, 0)
  }
})

test('a rejected answer closes media, data channel and the minted provider session', async () => {
  const f = fixture({ remoteError: true })
  await assert.rejects(f.start(), /answer rejected/)
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.contexts[0].closes, 1)
  assert.equal(f.peers[0].closes, 1)
  assert.equal(f.peers[0].dc.closes, 1)
  assert.equal(f.intervals.size, 0)
  assert.equal(f.requests.filter(r => r.path === '/codexlive/stop').length, 1)
})

test('cancel during microphone acquisition stops its late track without opening WebRTC', async () => {
  const microphone = deferred(), f = fixture({ microphone: microphone.promise })
  const starting = f.start()
  await settle()
  f.context.window.__talkLiveHandle.close()
  microphone.resolve(f.stream)
  await assert.rejects(starting, /cancelled/)
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.contexts.length, 0)
  assert.equal(f.requests.length, 0)
  assert.equal(f.bus.live, false)
})

test('unload during negotiation releases local media promptly and retires a late provider session', async () => {
  const session = deferred(), f = fixture({ session: session.promise })
  const starting = f.start()
  await settle()
  f.context.unload()
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.contexts[0].closes, 1)
  session.resolve({ answer: 'late answer', threadId: 'late-thread' })
  await assert.rejects(starting, /cancelled/)
  assert.equal(f.peers[0].remoteDescriptions, 0)
  assert.equal(f.requests.filter(r => r.path === '/codexlive/stop').length, 1)
  assert.equal(f.intervals.size, 0)
})

test('selected microphone failure does not silently substitute the system default', async () => {
  const f = fixture({ micError: { name: 'OverconstrainedError' } })
  await assert.rejects(f.start({ micId: 'selected' }), /MIC:/)
  assert.equal(f.micRequests(), 1)
  assert.equal(f.requests.length, 0)
})

test('remote track replacement detaches the old playback graph and selects output before play', async () => {
  const sink = deferred(), f = fixture({ sink: sink.promise })
  const handle = await f.start({ outId: 'selected-output' })
  const track = { kind: 'audio' }, event = { track, streams: [] }
  f.peers[0].ontrack(event)
  const firstNodes = f.contexts[0].nodes.slice(-3)
  assert.equal(f.players[0].autoplay, false)
  assert.deepEqual(f.players[0].events, ['sink'])
  f.peers[0].ontrack(event)
  assert.equal(f.players[0].srcObject, null)
  assert.ok(firstNodes.every(n => n.disconnected === 1))
  sink.resolve(); await settle()
  assert.ok(!f.players[0].events.includes('play'))
  assert.deepEqual(f.players[1].events, ['sink', 'play'])
  handle.close(); handle.close()
  assert.equal(f.players[1].srcObject, null)
  assert.equal(f.contexts[0].closes, 1)
  assert.equal(f.tracks[0].stops, 1)
  assert.ok(f.loops.every(loop => loop.disposed))
  assert.equal(f.intervals.size, 0)
})

test('selected output failure remains paused with an actionable error', async () => {
  const f = fixture({ sinkError: true }), handle = await f.start({ outId: 'missing' })
  f.peers[0].ontrack({ track: { kind: 'audio' }, streams: [f.stream] })
  await settle()
  assert.equal(f.players[0].paused, true)
  assert.match(f.bus.err, /output is unavailable/)
  handle.close()
})

test('clients without output selection keep selected routes paused and permit system default playback', async () => {
  for (const outId of ['selected-output', '']) {
    const f = fixture({ noSinkSelection: true }), handle = await f.start({ outId })
    try {
      f.peers[0].ontrack({ track: { kind: 'audio' }, streams: [f.stream] })
      await settle()
      assert.equal(f.players[0].paused, Boolean(outId))
      assert.equal(f.players[0].events.includes('play'), !outId)
      if (outId) assert.match(f.bus.err, /output selection.*not supported.*system default/i)
      else assert.equal(f.bus.err, '')
    } finally { handle.close() }
  }
})

test('device loss closes capture and wake resumes exactly once after ordinary hangup', async () => {
  const f = fixture(); let pauses = 0, resumes = 0
  f.context.setWake({ pauseForCall: async () => pauses++, resumeAfterCall: async () => resumes++ })
  const handle = await f.start()
  assert.equal(pauses, 1)
  f.tracks[0].dispatchEvent(new Event('ended'))
  handle.close()
  assert.equal(resumes, 1)
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.bus.live, false)
  assert.match(f.bus.err, /microphone disconnected/)
})

test('slow getStats never overlaps polling and late results cannot revive closed meters', async () => {
  const stats = deferred(), f = fixture({ stats: stats.promise }), handle = await f.start()
  const poll = [...f.intervals.values()].find(t => t.ms === 250).fn
  poll(); poll(); poll()
  assert.equal(f.peers[0].statsRequests, 1)
  handle.close()
  stats.resolve([{ type: 'inbound-rtp', kind: 'audio', audioLevel: 1 }]); await settle()
  assert.equal(f.bus.remoteLevel, 0)
  assert.equal(f.intervals.size, 0)
})

function wakeFixture(context, capture) {
  const rpc = { async request(method) {
    if (method === 'wake.status') return { enabled: true, configured_surface: 'live-voice', owned_by_caller: true, frame_length: 1280 }
    if (method === 'wake.start') return { started: true, capture: 'client' }
    return { resumed: true }
  }, close() {} }
  const client = new context.Client({ connect: async () => rpc, capture, onState() {}, onDetect() {} })
  client.setRoute({ connectionId: 'main', profile: 'default' })
  return client
}

test('repeated wake enable/rearm releases the old capture and retains only one listener', async () => {
  const f = fixture(), microphones = []
  const client = wakeFixture(f.context, async () => {
    const mic = { stops: 0, stop() { this.stops++ } }; microphones.push(mic); return mic
  })
  await client.enable(); await client.enable(); await client.resumeAfterCall()
  assert.equal(microphones.length, 2)
  assert.equal(microphones[0].stops, 1)
  assert.equal(microphones[1].stops, 0)
  client.dispose()
})

test('wake-to-call handoff drains a pending capture before call acquisition begins', async () => {
  const f = fixture(), pending = deferred(), mic = { stops: 0, stop() { this.stops++ } }
  const client = wakeFixture(f.context, () => pending.promise)
  f.context.setWake(client)
  const arming = client.enable(); await settle()
  const starting = f.start(); await settle()
  assert.equal(f.micRequests(), 0)
  pending.resolve(mic); await arming
  const handle = await starting
  assert.equal(mic.stops, 1)
  assert.equal(f.micRequests(), 1)
  await client.disable(); handle.close()
  assert.equal(client.phase, 'off')
})

test('diagnostic snapshot is explicit, excludes identifiers/SDP and never acquires media', async () => {
  const f = fixture()
  assert.equal(f.context.diagnostics().owner, 'none')
  assert.equal(f.micRequests(), 0)
  const handle = await f.start({ micId: 'private-device-id' })
  const status = f.context.diagnostics()
  assert.equal(status.owner, 'call')
  assert.equal(status.input.sampleRate, 48000)
  assert.ok(!JSON.stringify(status).includes('private-device-id'))
  assert.equal(f.micRequests(), 1)
  handle.close()
  assert.equal(f.context.diagnostics().owner, 'none')
})

function microphoneTestForm(f) {
  const section = source.slice(source.indexOf('  const stopMicTest ='), source.indexOf("  return jsxs('div'", source.indexOf('  const stopMicTest =')))
  Object.assign(f.context, { micTestRef: { current: null }, micBarRef: { current: null }, micId: '',
    setMicTest() {}, setDevs() {}, requestAnimationFrame: () => 1, cancelAnimationFrame() {} })
  f.context.navigator.mediaDevices.enumerateDevices = async () => []
  f.context.sdk.host = { notifyError() {} }
  return () => vm.runInContext(`(() => { ${section}; return { toggle: toggleMicTest, stop: stopMicTest } })()`, f.context)
}

test('microphone test stopped from a later render releases its global capture reservation', async () => {
  const f = fixture(), render = microphoneTestForm(f)
  await render().toggle()
  assert.equal(f.context.diagnostics().owner, 'microphone test')
  render().stop()
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.contexts[0].closes, 1)
  assert.equal(f.context.diagnostics().owner, 'none')
})

test('cancelled pending microphone test blocks a competing call until its late track is stopped', async () => {
  const microphone = deferred(), f = fixture({ microphone: microphone.promise }), render = microphoneTestForm(f)
  const testing = render().toggle()
  render().stop()
  await assert.rejects(f.start(), /microphone test/)
  assert.equal(f.micRequests(), 1)
  microphone.resolve(f.stream); await testing
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.contexts.length, 0)
  assert.equal(f.context.diagnostics().owner, 'none')
})

test('microphone test setup failure releases its stream and capture reservation', async () => {
  const f = fixture({ contextError: true }), render = microphoneTestForm(f)
  await render().toggle()
  assert.equal(f.tracks[0].stops, 1)
  assert.equal(f.context.diagnostics().owner, 'none')
})

test('user off while wake pause is pending remains off after the late pause reply', async () => {
  const f = fixture(), pause = deferred(), client = wakeFixture(f.context, async () => ({ stop() {} }))
  await client.enable()
  const original = client.rpc.request
  client.rpc.request = async method => method === 'wake.pause' ? pause.promise : original(method)
  const pausing = client.pauseForCall(); await settle()
  await client.disable()
  pause.resolve({}); await pausing
  assert.equal(client.wanted, false)
  assert.equal(client.phase, 'off')
})
