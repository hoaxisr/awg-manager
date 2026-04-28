<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { saveStatus } from '$lib/stores/saveStatus';

    let unsub: () => void;
    onMount(() => { unsub = saveStatus.subscribe(() => {}); });
    onDestroy(() => unsub?.());

    let s = $derived($saveStatus.data);
    let state = $derived(s?.state ?? 'idle');
    let pending = $derived(s?.pendingCount ?? 0);
    let lastError = $derived(s?.lastError ?? '');

    // Don't render anything in idle (no save activity).
    let visible = $derived(state !== 'idle');

    let label = $derived.by(() => {
        if (state === 'pending') return pending > 0 ? `Сохранение (${pending})` : 'Сохранение';
        if (state === 'saving') return 'Сохранение...';
        if (state === 'error') return 'Ошибка сохранения';
        if (state === 'failed') return 'Сохранение не удалось';
        return '';
    });

    let tone = $derived.by(() => {
        if (state === 'pending' || state === 'saving') return 'pending';
        if (state === 'error' || state === 'failed') return 'error';
        return '';
    });
</script>

{#if visible}
    <span class="indicator indicator-{tone}" title={lastError || label}>{label}</span>
{/if}

<style>
    .indicator {
        font-size: 0.75rem;
        padding: 2px 8px;
        border-radius: var(--radius-sm);
        white-space: nowrap;
    }
    .indicator-pending {
        background: rgba(122, 162, 247, 0.1);
        color: var(--accent);
    }
    .indicator-error {
        background: rgba(239, 68, 68, 0.1);
        color: var(--error);
    }
</style>
