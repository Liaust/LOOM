import { describe, it, expect, vi } from "vitest";
import { WakeLoop, PathWork } from "./work";

describe("event-driven client work", () => {
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
