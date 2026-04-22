import { writable, get, type Readable } from 'svelte/store';

export type PollingState<T> = {
    data: T | null;
    status: 'idle' | 'loading' | 'fresh' | 'stale' | 'error';
    error: string | null;
    lastFetchedAt: number; // ms epoch; 0 if never
    consecutiveFailures: number;
};

export interface PollingStore<T> extends Readable<PollingState<T>> {
    subscribe: (run: (value: PollingState<T>) => void, invalidate?: any) => () => void;
    refetch: () => Promise<void>;
    invalidate: () => void;
    applyMutationResponse: (data: T) => void;
}

export interface PollingOptions {
    staleTime: number;
    pollInterval: number;
    // Error threshold before badge shows (default 3).
    errorThreshold?: number;
}

/**
 * Polling store with reference-counted subscribers, visibility-aware
 * polling, stale-while-revalidate, and mutation-response fast-path.
 *
 * Semantics:
 *  - First subscribe: immediate fetch unless cache is within staleTime.
 *  - Poll interval: periodic refetch while subscribers > 0 and tab visible.
 *  - Last unsubscribe: stop polling (data preserved in memory).
 *  - Tab hidden: pause polling. On visible: immediate refetch.
 *  - invalidate(): immediate refetch if subscribed, else mark stale.
 *  - applyMutationResponse(data): update cache, reset error counter.
 *  - Failed fetches: increment consecutiveFailures; stay in 'error' status
 *    once threshold is crossed. Cached data is NOT discarded.
 */
export function createPollingStore<T>(
    fetcher: () => Promise<T>,
    opts: PollingOptions
): PollingStore<T> {
    const threshold = opts.errorThreshold ?? 3;
    const state = writable<PollingState<T>>({
        data: null,
        status: 'idle',
        error: null,
        lastFetchedAt: 0,
        consecutiveFailures: 0,
    });

    let subCount = 0;
    let pollTimer: ReturnType<typeof setInterval> | null = null;
    let inflight: Promise<void> | null = null;

    async function doFetch(): Promise<void> {
        if (inflight) return inflight;
        state.update(s => ({ ...s, status: s.data ? 'stale' : 'loading' }));
        inflight = (async () => {
            try {
                const data = await fetcher();
                state.set({
                    data,
                    status: 'fresh',
                    error: null,
                    lastFetchedAt: Date.now(),
                    consecutiveFailures: 0,
                });
            } catch (e) {
                state.update(s => {
                    const fails = s.consecutiveFailures + 1;
                    const message = e instanceof Error ? e.message : String(e);
                    let status: PollingState<T>['status'];
                    if (!s.data) {
                        // No cached data — any failure is user-visible.
                        status = 'error';
                    } else if (fails >= threshold) {
                        // Tier 2: exceeded silent threshold, show badge.
                        status = 'error';
                    } else {
                        // Tier 1: keep showing cached data, no badge.
                        status = 'stale';
                    }
                    return {
                        ...s,
                        status,
                        error: message,
                        consecutiveFailures: fails,
                    };
                });
            } finally {
                inflight = null;
            }
        })();
        return inflight;
    }

    function startPoll() {
        if (pollTimer !== null) return;
        pollTimer = setInterval(() => {
            if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return;
            void doFetch();
        }, opts.pollInterval);
    }

    function stopPoll() {
        if (pollTimer !== null) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
    }

    let visibilityHandler: (() => void) | null = null;

    function attachVisibilityHandler() {
        if (typeof document === 'undefined' || visibilityHandler !== null) return;
        visibilityHandler = () => {
            if (document.visibilityState === 'visible' && subCount > 0) {
                void doFetch();
            }
        };
        document.addEventListener('visibilitychange', visibilityHandler);
    }

    function detachVisibilityHandler() {
        if (typeof document === 'undefined' || visibilityHandler === null) return;
        document.removeEventListener('visibilitychange', visibilityHandler);
        visibilityHandler = null;
    }

    return {
        subscribe(run, invalidate) {
            subCount++;
            if (subCount === 1) {
                const s = get(state);
                const age = Date.now() - s.lastFetchedAt;
                if (s.lastFetchedAt === 0 || age > opts.staleTime) {
                    void doFetch();
                }
                startPoll();
                attachVisibilityHandler();
            }
            const unsub = state.subscribe(run, invalidate);
            return () => {
                unsub();
                subCount--;
                if (subCount === 0) {
                    stopPoll();
                    detachVisibilityHandler();
                }
            };
        },
        async refetch() {
            await doFetch();
        },
        invalidate() {
            if (subCount > 0) {
                void doFetch();
            } else {
                state.update(s => ({ ...s, lastFetchedAt: 0 }));
            }
        },
        applyMutationResponse(data: T) {
            state.set({
                data,
                status: 'fresh',
                error: null,
                lastFetchedAt: Date.now(),
                consecutiveFailures: 0,
            });
        },
    };
}
