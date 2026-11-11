import test from 'node:test';
import assert from 'node:assert/strict';
import { hasExactAncestry, readRemoteHistory } from './history.mjs';

test('a truncated boundary does not establish root or direct-parent ancestry', () => {
    assert.equal(hasExactAncestry({ ancestryAvailable: true, ancestryCompleteToRoot: false, ancestors: [] }), false);
    assert.equal(hasExactAncestry({ ancestryAvailable: false, ancestors: ['2-parent'] }), false);
    assert.equal(hasExactAncestry({ ancestryAvailable: true, ancestryCompleteToRoot: true, ancestors: [] }), true);
    assert.equal(hasExactAncestry({ ancestryAvailable: true, ancestryCompleteToRoot: false, ancestors: ['2-parent'] }), true);
});

test('historical repair requests one exact encrypted revision and closes ownership', async () => {
    let closes = 0;
    const settings = { encrypt: true, couchDB_URI: 'http://localhost', couchDB_DBNAME: 'notes' };
    const exact = { id: 'native-id', path: 'Notes/a.md', revision: '1-old', kind: 'file',
        ancestryAvailable: true, ancestryCompleteToRoot: true, ancestors: [] };
    const replicator = {
        async connectRemoteCouchDBWithSetting(actualSettings, mobile, setup, skipInfo, options) {
            assert.deepEqual(actualSettings, settings);
            assert.equal(mobile, false); assert.equal(setup, false); assert.equal(skipInfo, true);
            assert.equal(options.allowNativeFallback, false); assert.ok(options.signal);
            return { db: { async get(id, options) {
                assert.equal(id, 'native-id');
                assert.deepEqual(options, { rev: '1-old', revs: true });
                return { ...exact, content: 'must never be returned' };
            } } };
        },
        async closeRemoteConnection() { closes++; },
    };
    const core = { services: { setting: { currentSettings: () => settings },
        replication: { replicatorService: { runWithActiveReplicatorContext: run => run({ replicator }) } } } };
    const result = await readRemoteHistory(core, 'Notes/a.md', 'native-id', '1-old', () => exact);
    assert.deepEqual(result, { ...exact, status: 'history' });
    assert.equal(closes, 1);
    for (const bad of [{ path: 'Notes/other.md' }, { revision: '2-new' }, { id: 'other' },
        { kind: 'system' }, { branchDeleted: true }, { ancestryAvailable: false }]) {
        assert.equal((await readRemoteHistory(core, exact.path, exact.id, exact.revision,
            () => ({ ...exact, ...bad }))).reason, 'historical_metadata_mismatch');
    }
    replicator.connectRemoteCouchDBWithSetting = async () => ({ db: { async get() { throw Error('private endpoint'); } } });
    assert.deepEqual(await readRemoteHistory(core, exact.path, exact.id, exact.revision, () => exact),
        { status: 'held', reason: 'historical_metadata_unavailable' });
    assert.equal(closes, 8);
    replicator.connectRemoteCouchDBWithSetting = async () => ({ db: { async get() { throw { status: 404 }; } } });
    assert.deepEqual(await readRemoteHistory(core, exact.path, exact.id, exact.revision, () => exact),
        { status: 'held', reason: 'revision_unavailable' });
    assert.equal(closes, 9);
});

test('historical repair has no guessed or latest-revision fallback', async () => {
    const core = { services: { replication: {} } };
    assert.deepEqual(await readRemoteHistory(core, 'Notes/a.md', 'native-id', '1-old', () => {}),
        { status: 'held', reason: 'revision_unavailable' });
});
