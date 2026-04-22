<script lang="ts">
    import type { PollingStore } from '$lib/stores/polling';

    interface Props {
        store: PollingStore<unknown>;
        /**
         * Must match the `errorThreshold` passed to createPollingStore for this store.
         * Default 3 matches the createPollingStore default. If the store was created
         * with a custom errorThreshold, pass the same value here or the badge will
         * never render (or will render early).
         */
        threshold?: number;
    }

    let { store, threshold = 3 }: Props = $props();

    let s = $derived($store);

    function humanAge(ms: number): string {
        if (ms === 0) return 'никогда';
        const secs = Math.floor((Date.now() - ms) / 1000);
        if (secs < 60) return `${secs}с назад`;
        return `${Math.floor(secs / 60)}мин назад`;
    }

    async function retry() {
        await store.refetch();
    }
</script>

{#if s.status === 'error' && s.consecutiveFailures >= threshold}
    <div class="badge badge-error" role="status" aria-live="polite">
        <span>обновлено {humanAge(s.lastFetchedAt)}</span>
        <button type="button" onclick={retry}>повторить</button>
    </div>
{/if}

<style>
    button {
        background: transparent;
        border: none;
        color: var(--error);
        cursor: pointer;
        padding: 0;
        text-decoration: underline;
        font-size: 0.75rem;
    }
    button:hover {
        opacity: 0.8;
    }
</style>
