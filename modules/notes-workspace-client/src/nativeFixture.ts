import { afterEach, describe, expect, it, vi } from "vitest";
import PouchDB from "pouchdb-core";
import MemoryAdapter from "pouchdb-adapter-memory";
import HttpAdapter from "pouchdb-adapter-http";
import replication from "pouchdb-replication";
import type {
    EntryDoc,
    FilePathWithPrefix,
    UXFileInfo,
} from "@vrtmrz/livesync-commonlib/compat/common/types";
import { DEFAULT_SETTINGS } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { compareMTime, createTextBlob, readContent } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import { createLiveSyncEventHub } from "@vrtmrz/livesync-commonlib/context";
import {
    LiveSyncLocalDB,
    type LiveSyncLocalDBEnv,
} from "@vrtmrz/livesync-commonlib/compat/pouchdb/LiveSyncLocalDB";
import {
    ServiceDatabaseFileAccessBase,
    type ServiceDatabaseFileAccessDependencies,
} from "@vrtmrz/livesync-commonlib/compat/serviceModules/ServiceDatabaseFileAccessBase";
import type { ServiceFileHandlerDependencies } from "@vrtmrz/livesync-commonlib/compat/serviceModules/ServiceFileHandlerBase";
import { ServiceFileHandler } from "../serviceModules/FileHandler";
PouchDB.plugin(MemoryAdapter).plugin(HttpAdapter).plugin(replication);
const path = "multi-device.txt" as FilePathWithPrefix;
const old = "Original content\n";
const oldTime = 1_000_000;
class TestHandler extends ServiceFileHandler {}

export function makeFile(body: string, mtime = oldTime, filePath = path): UXFileInfo {
    return {
        name: filePath,
        path: filePath,
        stat: { type: "file", ctime: oldTime, mtime, size: new Blob([body]).size },
        body: createTextBlob(body),
    };
}

export async function makeDevice(name: string, encrypted = false) {
    const db = new PouchDB<EntryDoc>(name, { adapter: "memory" });
    const reflection = new Map<FilePathWithPrefix, { revision: string; observedStorageMtime?: number }>();
    let storage = makeFile(old);
    const settings = {
        ...DEFAULT_SETTINGS,
        encrypt: encrypted,
        passphrase: encrypted ? "ephemeral-test-only" : "",
        useOnlyLocalChunk: true,
        writeDocumentsIfConflicted: false,
    };
    const setting = { currentSettings: () => settings };
    const pathService = {
        path2id: (value: string) => Promise.resolve(value),
        id2path: (id: string, entry?: { path?: string }) => entry?.path ?? id,
        getPath: (entry: { path: FilePathWithPrefix }) => entry.path,
        compareFileFreshness: (file: UXFileInfo, entry: { mtime: number }) =>
            compareMTime(file.stat.mtime, entry.mtime),
        markChangesAreSame: vi.fn(),
    };
    const events = createLiveSyncEventHub();
    const API = { addLog: vi.fn() };
    const localDatabase = new LiveSyncLocalDB(name, {
        services: {
            API,
            setting,
            path: pathService,
            context: { events },
            database: { createPouchDBInstance: () => db },
            databaseEvents: {
                onDatabaseInitialisation: () => Promise.resolve(true),
                onDatabaseHasReady: () => Promise.resolve(true),
                onCloseDatabase: () => Promise.resolve(true),
                onUnloadDatabase: () => Promise.resolve(true),
            },
            replicator: { onCloseActiveReplication: () => Promise.resolve(true) },
        },
    } as unknown as LiveSyncLocalDBEnv);
    await expect(localDatabase.initializeDatabase()).resolves.toBe(true);
    const storageAccess = {
        getStub: () => Promise.resolve(storage),
        getFileStub: () => Promise.resolve(storage),
        readStubContent: () => Promise.resolve(storage),
        ensureDir: () => Promise.resolve(true),
        writeFileAuto: vi.fn((_path: string, body: string, times: { mtime: number }) => {
            storage = makeFile(body, times.mtime, _path as FilePathWithPrefix);
            return Promise.resolve(true);
        }),
        stat: () => Promise.resolve(storage.stat),
        touched: () => Promise.resolve(),
        triggerFileEvent: vi.fn(),
    };
    const conflict = { queueCheckFor: vi.fn(), queueCheckForIfOpen: vi.fn() };
    const services = {
        API,
        path: pathService,
        setting,
        events,
        database: { localDatabase },
        vault: { isTargetFile: () => Promise.resolve(true), isFileSizeTooLarge: () => false },
        storageAccess,
        conflict,
        fileReflectionProvenance: {
            get: (value: FilePathWithPrefix) => Promise.resolve(reflection.get(value)),
            set: (value: FilePathWithPrefix, record: { revision: string }) => {
                reflection.set(value, record);
                return Promise.resolve();
            },
            delete: (value: FilePathWithPrefix) => {
                reflection.delete(value);
                return Promise.resolve();
            },
        },
        fileProcessing: { processFileEvent: { addHandler: vi.fn() } },
        replication: { processSynchroniseResult: { addHandler: vi.fn() } },
    } as unknown as ServiceFileHandlerDependencies & ServiceDatabaseFileAccessDependencies;
    const access = new ServiceDatabaseFileAccessBase(services);
    (services as ServiceFileHandlerDependencies).databaseFileAccess = access;
    const handler = new TestHandler(services);
    return {
        db,
        localDatabase,
        access,
        handler,
        conflict,
        reflection,
        storageAccess,
        settings,
        services,
        getStorage: () => storage,
        setStorage: (file: UXFileInfo) => {
            storage = file;
        },
    };
}
