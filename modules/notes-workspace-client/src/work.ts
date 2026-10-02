// Coalesce transient wakeups, never durable edit records.
export class WakeLoop {
    private timer?: ReturnType<typeof setTimeout>;
    private running?: Promise<void>;
    private requested = false;
    private stopped = false;
    private delay = 1000;
    constructor(private run: () => Promise<void>, private retryNeeded: () => boolean,
        private retry: () => void, private failed: () => Promise<void>) {}
    wake() {
        if (this.stopped) return;
        this.requested = true;
        if (this.running) return;
        if (this.timer) clearTimeout(this.timer);
        // Leave native path locks before entering reflection.
        this.timer = setTimeout(() => { this.timer = undefined; void this.flush(); }, 0);
    }
    async flush() {
        if (this.stopped) return;
        if (this.running) return this.running;
        if (this.timer) clearTimeout(this.timer);
        this.timer = undefined;
        this.requested = true;
        this.running = Promise.resolve().then(async () => {
            try {
                do { this.requested = false; await this.run(); }
                while (this.requested && !this.stopped);
            } catch { if (!this.stopped) await this.failed(); }
        }).finally(() => {
            this.running = undefined;
            if (!this.stopped && this.retryNeeded()) {
                this.timer = setTimeout(() => {
                    this.timer = undefined;
                    this.retry();
                    void this.flush();
                }, this.delay);
                this.delay = Math.min(this.delay * 2, 60000);
            } else this.delay = 1000;
        });
        return this.running;
    }
    async stop() {
        this.stopped = true;
        this.requested = false;
        if (this.timer) clearTimeout(this.timer);
        this.timer = undefined;
        await this.running;
    }
}

export class PathWork {
    private dirty = new Set<string>();
    private waiting = new Map<string, Set<string>>();
    private dependencies = new Map<string, Set<string>>();
    add(path: string) { this.dirty.add(path); }
    take() { const paths = [...this.dirty]; this.dirty.clear(); return paths; }
    wait(path: string, chunks: string[]) {
        this.clear(path);
        this.waiting.set(path, new Set(chunks));
        for (const id of chunks) {
            const paths = this.dependencies.get(id) ?? new Set<string>();
            paths.add(path);
            this.dependencies.set(id, paths);
        }
    }
    clear(path: string) {
        for (const id of this.waiting.get(path) ?? []) {
            const paths = this.dependencies.get(id)!;
            paths.delete(path);
            if (!paths.size) this.dependencies.delete(id);
        }
        this.waiting.delete(path);
    }
    changed(id: string, path?: string) {
        for (const p of this.dependencies.get(id) ?? []) this.add(p);
        if (path) this.add(path);
    }
    interested(id: string) { return this.dependencies.has(id); }
    retry() { for (const p of this.waiting.keys()) this.add(p); }
    get pending() { return this.waiting.size > 0; }
}
