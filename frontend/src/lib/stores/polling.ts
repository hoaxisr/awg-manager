import { writable, get, type Readable } from 'svelte/store';

/** Matches Svelte's store invalidation callback (`(value?: T) => void`). */
type Invalidator<T> = (value?: T) => void;

export type PollingState<T> = {
    data: T | null;
    status: 'idle' | 'loading' | 'fresh' | 'stale' | 'error';
    error: string | null;
    lastFetchedAt: number; // ms epoch; 0 if never
    consecutiveFailures: number;
};

export interface PollingStore<T> extends Readable<PollingState<T>> {
    subscribe: (
        run: (value: PollingState<T>) => void,
        invalidate?: Invalidator<PollingState<T>>,
    ) => () => void;
    refetch: () => Promise<void>;
    invalidate: () => void;
    applyMutationResponse: (data: T) => void;
    /**
     * Текущее состояние БЕЗ подписки. Нужен тем, кто заглядывает в снимок из
     * обработчика события, а не из разметки.
     *
     * `get(store)` из svelte/store для этого не годится: он подписывается и
     * тут же отписывается, а у нас на переходе subCount 0→1 висит `doFetch()`
     * по истёкшему staleTime плюс запуск и остановка таймера. Заглядывание
     * превращалось в полный запрос к бэкенду.
     */
    peek: () => PollingState<T>;
}

export interface PollingOptions {
    staleTime: number;
    /**
     * Период фонового опроса в мс. `0` — таймера нет: стор обновляется
     * только по инвалидации (SSE `resource:invalidated`), по подписке с
     * истёкшим staleTime и по возврату фокуса вкладки. Ставить 0 можно
     * ТОЛЬКО стору, чей ресурс бэкенд реально публикует, иначе он замрёт.
     */
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
 *  - Poll interval: periodic refetch while subscribers > 0 and tab visible;
 *    `pollInterval: 0` отключает таймер — обновление только по инвалидации.
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
        state.update(s => {
            const newStatus = s.data ? 'stale' : 'loading';
            // Return same reference if nothing changed — writable skips
            // subscriber notification when old === new, avoiding a wasted re-render.
            if (s.status === newStatus) return s;
            return { ...s, status: newStatus };
        });
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
                        status = 'error';
                    } else if (fails >= threshold) {
                        status = 'error';
                    } else {
                        status = 'stale';
                    }
                    if (s.status === status && s.error === message && s.consecutiveFailures === fails) return s;
                    return { ...s, status, error: message, consecutiveFailures: fails };
                });
            } finally {
                inflight = null;
            }
        })();
        return inflight;
    }

    function startPoll() {
        if (opts.pollInterval <= 0) return;
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
        peek() {
            return get(state);
        },
        async refetch() {
            await doFetch();
        },
        invalidate() {
            if (subCount > 0) {
                void doFetch();
            } else {
                state.update(s => {
                    if (s.lastFetchedAt === 0) return s;
                    return { ...s, lastFetchedAt: 0 };
                });
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
