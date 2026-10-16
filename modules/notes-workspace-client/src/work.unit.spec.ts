import { describe, it, expect, vi } from "vitest";
import { WakeLoop, PathWork } from "./work";

describe("event-driven client work", () => {
    it("pages dirty work without losing arrivals and reconstructs dependencies after restart", () => {
        const work = new PathWork();
        work.add("a"); work.add("b"); work.add("c");
        expect(work.take(2)).toEqual(["a", "b"]);
        expect(work.runnable).toBe(true);
        work.add("a");
        expect(work.isDirty("a")).toBe(true);
        expect(work.take(2)).toEqual(["c", "a"]);
        work.wait("a", ["old-db-chunk"]);
        expect(work.isWaiting("a")).toBe(true);
        work.restart();
        expect(work.isWaiting("a")).toBe(false);
        expect(work.interested("old-db-chunk")).toBe(false);
        expect(work.take()).toEqual(["a"]);
        expect(work.runnable).toBe(false);
    });
    it("routes chunk arrivals only to their waiting paths", () => {
        const w = new PathWork();
        w.wait("a", ["chunk-a"]); w.wait("b", ["chunk-b"]);
        w.changed("unrelated"); expect(w.take()).toEqual([]);
        w.changed("chunk-a"); expect(w.take()).toEqual(["a"]);
        w.clear("a"); w.changed("chunk-a"); expect(w.take()).toEqual([]);
        w.retry(); expect(w.take()).toEqual(["b"]);
        w.clear("b"); expect(w.pending).toBe(false);
    });
    it("preserves arrivals during active work and is idle afterward", async () => {
        vi.useFakeTimers();
        try {
            let loop: WakeLoop;
            const run = vi.fn(async () => { if (run.mock.calls.length === 1) loop.wake(); });
            loop = new WakeLoop(run, () => false, () => {}, async () => {});
            loop.wake(); loop.wake();
            await vi.runAllTimersAsync();
            expect(run).toHaveBeenCalledTimes(2);
            expect(vi.getTimerCount()).toBe(0);
            await vi.advanceTimersByTimeAsync(60000);
            expect(run).toHaveBeenCalledTimes(2);
            await loop.stop();
        } finally { vi.useRealTimers(); }
    });
    it("backs off pending work and cancels retries when cleared or unloaded", async () => {
        vi.useFakeTimers();
        try {
            let pending = true;
            const run = vi.fn(async () => {}), retry = vi.fn();
            const loop = new WakeLoop(run, () => pending, retry, async () => {});
            await loop.flush();
            await vi.advanceTimersByTimeAsync(1000);
            expect(run).toHaveBeenCalledTimes(2);
            await vi.advanceTimersByTimeAsync(1999); expect(run).toHaveBeenCalledTimes(2);
            pending = false;
            await vi.advanceTimersByTimeAsync(1); expect(run).toHaveBeenCalledTimes(3);
            expect(vi.getTimerCount()).toBe(0);
            pending = true; await loop.flush(); await loop.stop();
            expect(vi.getTimerCount()).toBe(0); expect(retry).toHaveBeenCalledTimes(2);
        } finally { vi.useRealTimers(); }
    });
});
