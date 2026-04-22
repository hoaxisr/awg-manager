import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { get } from 'svelte/store';
import { createPollingStore } from './polling';

describe('createPollingStore', () => {
    beforeEach(() => vi.useFakeTimers());
    afterEach(() => vi.useRealTimers());

    it('fetches once on first subscribe, reuses cache within staleTime', async () => {
        const fetcher = vi.fn().mockResolvedValue({ v: 1 });
        const s = createPollingStore(fetcher, { staleTime: 1000, pollInterval: 10_000 });

        const unsub1 = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(fetcher).toHaveBeenCalledTimes(1);
        unsub1();

        const unsub2 = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(fetcher).toHaveBeenCalledTimes(1); // cached
        unsub2();
    });

    it('re-fetches after staleTime on new subscribe', async () => {
        const fetcher = vi.fn().mockResolvedValue({ v: 1 });
        const s = createPollingStore(fetcher, { staleTime: 100, pollInterval: 10_000 });

        const u1 = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        u1();

        vi.advanceTimersByTime(150);

        const u2 = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(fetcher).toHaveBeenCalledTimes(2);
        u2();
    });

    it('polls at pollInterval while subscribed', async () => {
        const fetcher = vi.fn().mockResolvedValue({ v: 1 });
        const s = createPollingStore(fetcher, { staleTime: 0, pollInterval: 1000 });

        const u = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(fetcher).toHaveBeenCalledTimes(1);

        await vi.advanceTimersByTimeAsync(1000);
        expect(fetcher).toHaveBeenCalledTimes(2);

        await vi.advanceTimersByTimeAsync(1000);
        expect(fetcher).toHaveBeenCalledTimes(3);

        u();
    });

    it('stops polling when last subscriber unsubscribes', async () => {
        const fetcher = vi.fn().mockResolvedValue({ v: 1 });
        const s = createPollingStore(fetcher, { staleTime: 0, pollInterval: 1000 });

        const u = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        u();

        await vi.advanceTimersByTimeAsync(5000);
        expect(fetcher).toHaveBeenCalledTimes(1);
    });

    it('invalidate() triggers immediate refetch when subscribed', async () => {
        const fetcher = vi.fn().mockResolvedValue({ v: 1 });
        const s = createPollingStore(fetcher, { staleTime: 10_000, pollInterval: 10_000 });

        const u = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(fetcher).toHaveBeenCalledTimes(1);

        s.invalidate();
        await vi.advanceTimersByTimeAsync(0);
        expect(fetcher).toHaveBeenCalledTimes(2);

        u();
    });

    it('applyMutationResponse updates value without fetch', async () => {
        const fetcher = vi.fn().mockResolvedValue({ v: 1 });
        const s = createPollingStore<{ v: number }>(fetcher, { staleTime: 10_000, pollInterval: 10_000 });

        const u = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);

        s.applyMutationResponse({ v: 42 });
        expect(get(s).data).toEqual({ v: 42 });
        expect(fetcher).toHaveBeenCalledTimes(1);
        u();
    });

    it('error counter increments on failed fetch, enters stale-errored after threshold', async () => {
        const fetcher = vi.fn().mockRejectedValue(new Error('boom'));
        const s = createPollingStore(fetcher, { staleTime: 0, pollInterval: 1000 });

        const u = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        expect(get(s).status).toBe('error'); // first fail is still errored (no prev data)

        await vi.advanceTimersByTimeAsync(1000);
        await vi.advanceTimersByTimeAsync(1000);

        expect(get(s).consecutiveFailures).toBeGreaterThanOrEqual(3);
        u();
    });

    it('preserves cached data as stale under threshold, flips to error at threshold', async () => {
        let callCount = 0;
        const fetcher = vi.fn().mockImplementation(() => {
            callCount++;
            if (callCount === 1) return Promise.resolve({ v: 'cached' });
            return Promise.reject(new Error('boom'));
        });
        const s = createPollingStore<{ v: string }>(fetcher, {
            staleTime: 0,
            pollInterval: 1000,
            errorThreshold: 3,
        });

        const u = s.subscribe(() => {});
        await vi.advanceTimersByTimeAsync(0);
        // Seeded successful fetch — data present, status fresh.
        expect(get(s).data).toEqual({ v: 'cached' });
        expect(get(s).status).toBe('fresh');
        expect(get(s).consecutiveFailures).toBe(0);

        // Fail 1: below threshold — status stale, data retained, no badge.
        await vi.advanceTimersByTimeAsync(1000);
        expect(get(s).status).toBe('stale');
        expect(get(s).data).toEqual({ v: 'cached' });
        expect(get(s).consecutiveFailures).toBe(1);

        // Fail 2: still below threshold — status stale.
        await vi.advanceTimersByTimeAsync(1000);
        expect(get(s).status).toBe('stale');
        expect(get(s).consecutiveFailures).toBe(2);

        // Fail 3: at threshold — status flips to error (Tier 2 badge visible).
        await vi.advanceTimersByTimeAsync(1000);
        expect(get(s).status).toBe('error');
        expect(get(s).consecutiveFailures).toBe(3);
        // Data still preserved.
        expect(get(s).data).toEqual({ v: 'cached' });

        u();
    });
});
