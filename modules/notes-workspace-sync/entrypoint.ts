#!/usr/bin/env node
import { main } from '../main';
import { NO_INTERACTION } from '@vrtmrz/livesync-commonlib/replication';
import { createAdapter } from './adapter.mjs';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { MAX_BYTES, MAX_REFERENCE_BYTES } from './protocol';

// Deliberately no stock CLI passthrough: daemon, mirror, reset, resolve,
// settings mutation and remote administration are outside this boundary.
const args = process.argv.slice(2);
if (args.length !== 3 || args[1] !== '--settings') {
    process.stderr.write('Usage: notes-workspace-sync <database-directory> --settings <private-settings.json>\n');
    process.exit(2);
}
try {
    const settings = JSON.parse(readFileSync(args[2], 'utf8'));
    if (settings.P2P_Enabled !== false || settings.remoteType !== '') throw new Error('couchdb_only');
    for (const key of ['liveSync', 'syncOnStart', 'periodicReplication', 'syncOnSave',
        'syncOnEditorSave', 'syncOnFileOpen', 'syncAfterMerge']) {
        if (settings[key] !== false) throw new Error('automatic_sync_enabled');
    }
} catch {
    process.stderr.write('Private settings must use CouchDB with P2P and automatic sync explicitly disabled.\n');
    process.exit(2);
}
process.argv = [process.argv[0], process.argv[1], resolve(args[0]), '--settings', resolve(args[2]), 'sync'];
const nativeIo = {
    readStdin: async () => { throw new Error('interactive_input_disabled'); },
    prompt: async () => { throw new Error('interactive_input_disabled'); },
    writeStdout: () => {}, writeStderr: () => {},
};
const reply = (id: unknown, result: unknown) => new Promise<void>((resolve, reject) => {
    let output = JSON.stringify({ id, result });
    if (Buffer.byteLength(output) > 2 * MAX_BYTES) output = JSON.stringify({ id, result: { status: 'error', code: 'response_limit' } });
    process.stdout.write(output + '\n', (error) => error ? reject(error) : resolve());
});

main(nativeIo, async (_options, context) => {
    const dispatch = createAdapter(context.core, async (mode = 'sync') => {
        const replication = context.core.services.replication;
        const outcome = mode === 'start' ? await replication.startContinuous({
            trigger: 'daemon', interaction: NO_INTERACTION,
        }) : mode === 'stop' ? await replication.stopActiveTransfer() : await replication.replicateUserInitiated({
            trigger: 'manual', progressPresentation: 'quiet', interaction: NO_INTERACTION,
        });
        // Only typed status/reason values cross IPC; native detail/error objects
        // may contain endpoint credentials and must stay private.
        return { status: 'replication', outcome: outcome.status,
            ...(outcome.status === 'blocked' ? { reason: outcome.reason } : {}) };
    });
    let pending = Buffer.alloc(0);
    for await (const chunk of process.stdin) {
        pending = Buffer.concat([pending, chunk]);
        let newline: number;
        while ((newline = pending.indexOf(10)) >= 0) {
            if (newline > 2 * MAX_REFERENCE_BYTES) throw new Error('request_limit');
            const line = pending.subarray(0, newline).toString('utf8');
            pending = pending.subarray(newline + 1);
            let request;
            try { request = JSON.parse(line); }
            catch { await reply(null, { status: 'error', code: 'invalid_json' }); continue; }
            if (newline > 2 * MAX_BYTES && request?.request?.method !== 'publish.reference') throw new Error('request_limit');
            const id = typeof request?.id === 'string' || typeof request?.id === 'number' ? request.id : null;
            // Correlation lives outside the native document id namespace.
            await reply(id, await dispatch(request?.request));
        }
        if (pending.length > 2 * MAX_REFERENCE_BYTES) throw new Error('request_limit');
    }
    if (pending.length) await reply(null, { status: 'error', code: 'incomplete_request' });
    return true;
}).catch(() => { process.stderr.write('Native adapter failed.\n'); process.exit(1); });
