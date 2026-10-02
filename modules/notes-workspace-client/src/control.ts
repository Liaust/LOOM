import { createTextBlob, readContent } from "@vrtmrz/livesync-commonlib/compat/common/utils";
import type { DatabaseFileAccess } from "@vrtmrz/livesync-commonlib/compat/interfaces/DatabaseFileAccess";
import type { FilePathWithPrefix, UXFileInfo } from "@vrtmrz/livesync-commonlib/compat/common/types";
import { controlPath, decode, encode, MAX_CONTROL_BYTES, token, type Control } from "./protocol";

// Ordinary native note payloads go through native splitting/encryption. No
// source path/base/intent is added as a plaintext custom PouchDB field.
export class ControlTransport {
    constructor(private db: DatabaseFileAccess,
        private ready: (path: string, revision: string) => Promise<boolean> = async () => true) {}
    async put(record: Control): Promise<string> {
        const text = encode(record),
            path = controlPath(record) as FilePathWithPrefix;
        return this.putImmutable(path, text);
    }
    async putPayload(id: string, text: string): Promise<string> {
        if (!token(id) || new TextEncoder().encode(text).length > MAX_CONTROL_BYTES)
            throw new Error("invalid_payload");
        return this.putImmutable(`LOOM-Control-v1/payload/${id}.md` as FilePathWithPrefix, text);
    }
    private async putImmutable(path: FilePathWithPrefix, text: string): Promise<string> {
        const old = await this.db.fetchEntryMeta(path, undefined, true);
        if (old) {
            if (old.deleted || old._deleted || (await this.db.getConflictedRevs(path)).length)
                throw new Error("control_collision");
            const entry = await this.db.fetchEntry(path, old._rev, false, true, true);
            if (!entry || readContent(entry) !== text) throw new Error("control_collision");
            return old._rev!;
        }
        const now = Date.now();
        const file = {
            path,
            name: path.split("/").pop()!,
            body: createTextBlob(text),
            stat: {
                type: "file",
                ctime: now,
                mtime: now,
                size: new TextEncoder().encode(text).length,
            },
        } as UXFileInfo;
        const rev = await this.db.storeIndependentRevision(file, true);
        if (!rev) throw new Error("control_write_failed");
        return rev;
    }
    async get(path: string, revision: string): Promise<Control> {
        if (!await this.ready(path, revision)) throw new Error("control_pending_chunks");
        if ((await this.db.getConflictedRevs(path as FilePathWithPrefix)).length)
            throw new Error("control_collision");
        const entry = await this.db.fetchEntry(path as FilePathWithPrefix, revision, false, true, true);
        if (!entry || entry._deleted || entry.deleted) throw new Error("control_unavailable");
        const text = readContent(entry);
        if (typeof text !== "string") throw new Error("control_not_text");
        return decode(path, text);
    }
}
