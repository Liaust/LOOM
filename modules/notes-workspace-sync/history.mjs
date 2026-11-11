const held = (reason) => ({ status: 'held', reason });

export function hasExactAncestry(metadata) {
    return metadata.ancestryAvailable === true &&
        (metadata.ancestryCompleteToRoot === true || metadata.ancestors?.length > 0);
}

// Native local revision trees are bounded. Fetch only the requested historical
// metadata through the existing encrypted connection, never a current winner.
export async function readRemoteHistory(core, path, id, revision, metadata) {
    const owner = core.services.replication.replicatorService;
    if (!owner?.runWithActiveReplicatorContext) return held('revision_unavailable');
    const settings = core.services.setting.currentSettings();
    if (!settings.couchDB_URI || !settings.couchDB_DBNAME) return held('revision_unavailable');
    try {
        return await owner.runWithActiveReplicatorContext(async ({ replicator }) => {
            if (!replicator.connectRemoteCouchDBWithSetting || !replicator.closeRemoteConnection)
                return held('revision_unavailable');
            const connection = await replicator.connectRemoteCouchDBWithSetting(
                settings, false, false, true,
                { signal: AbortSignal.timeout(2000), allowNativeFallback: false },
            );
            if (typeof connection === 'string') return held('historical_metadata_unavailable');
            try {
                const doc = await connection.db.get(id, { rev: revision, revs: true });
                const exact = await metadata(doc);
                if (exact.id !== id || exact.path !== path || exact.revision !== revision ||
                    exact.kind !== 'file' || exact.branchDeleted || !hasExactAncestry(exact))
                    return held('historical_metadata_mismatch');
                return { ...exact, status: 'history' };
            } catch (error) {
                if (error.status === 404) return held('revision_unavailable');
                throw error;
            } finally {
                await replicator.closeRemoteConnection(connection);
            }
        }) ?? held('revision_unavailable');
    } catch {
        return held('historical_metadata_unavailable');
    }
}
