<script lang="ts">
    import { onMount } from 'svelte';
    import type { SubscriptionMember } from '$lib/types';
    import { singboxDelayHistory } from '$lib/stores/singbox';

    interface Props {
        members: SubscriptionMember[];
        activeMemberTag: string;
        onPick: (memberTag: string) => Promise<void>;
        onClose: () => void;
    }
    let { members, activeMemberTag, onPick, onClose }: Props = $props();

    let switching = $state<string | null>(null);
    let pickError = $state('');
    let popoverEl: HTMLDivElement | undefined = $state();

    function delayFor(tag: string): { text: string; cls: string } {
        const h = $singboxDelayHistory.get(tag) ?? [];
        if (h.length === 0) return { text: '—', cls: 'unknown' };
        const last = h[h.length - 1];
        if (last <= 0) return { text: 'timeout', cls: 'fail' };
        if (last < 200) return { text: `${last}ms`, cls: 'ok' };
        if (last < 500) return { text: `${last}ms`, cls: 'slow' };
        return { text: `${last}ms`, cls: 'fail' };
    }

    async function pick(tag: string): Promise<void> {
        if (tag === activeMemberTag || switching !== null) return;
        switching = tag;
        pickError = '';
        try {
            await onPick(tag);
            onClose();
        } catch (e) {
            pickError = e instanceof Error ? e.message : 'Не удалось переключить';
        } finally {
            switching = null;
        }
    }

    function handleOutsideClick(e: MouseEvent): void {
        if (!popoverEl) return;
        if (!popoverEl.contains(e.target as Node)) onClose();
    }

    function handleKey(e: KeyboardEvent): void {
        if (e.key === 'Escape') onClose();
    }

    onMount(() => {
        // Defer one tick so the click that opened the popover doesn't immediately close it.
        const t = setTimeout(() => {
            window.addEventListener('click', handleOutsideClick);
            window.addEventListener('keydown', handleKey);
        }, 0);
        return () => {
            clearTimeout(t);
            window.removeEventListener('click', handleOutsideClick);
            window.removeEventListener('keydown', handleKey);
        };
    });
</script>

<div class="popover" bind:this={popoverEl} role="listbox">
    {#each members as m (m.tag)}
        {@const isActive = m.tag === activeMemberTag}
        {@const isSwitching = switching === m.tag}
        {@const d = delayFor(m.tag)}
        <button
            type="button"
            role="option"
            aria-selected={isActive}
            class="row"
            class:active={isActive}
            class:switching={isSwitching}
            disabled={switching !== null}
            onclick={() => pick(m.tag)}
        >
            <span class="led" class:on={isActive}></span>
            <span class="server">{m.server}:{m.port}</span>
            <span class="proto">{m.protocol.toUpperCase()}</span>
            <span class="delay {d.cls}">{d.text}</span>
        </button>
    {/each}
    {#if pickError}
        <div class="err">{pickError}</div>
    {/if}
</div>

<style>
    .popover {
        position: absolute;
        top: calc(100% + 4px);
        left: 0;
        right: 0;
        max-height: 280px;
        overflow-y: auto;
        background: var(--color-bg-secondary);
        border: 1px solid var(--color-border);
        border-radius: 6px;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
        z-index: var(--z-page-overlay);
    }
    .row {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        width: 100%;
        padding: 0.5rem 0.75rem;
        background: transparent;
        border: 0;
        border-bottom: 1px solid var(--color-border);
        font: inherit;
        color: var(--color-text-primary);
        cursor: pointer;
        text-align: left;
    }
    .row:last-of-type { border-bottom: 0; }
    .row:hover:not(:disabled):not(.active) { background: var(--color-bg-tertiary); }
    .row.active { background: rgba(63, 185, 80, 0.08); }
    .row.switching { opacity: 0.7; cursor: wait; }
    .row:disabled { cursor: wait; }
    .led {
        width: 8px; height: 8px;
        border-radius: 999px;
        background: var(--color-bg-tertiary);
        flex-shrink: 0;
    }
    .led.on {
        background: var(--color-success);
        box-shadow: 0 0 0 2px rgba(63, 185, 80, 0.22);
    }
    .server {
        flex: 1;
        font-size: 0.85rem;
        font-family: var(--font-mono, ui-monospace, monospace);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .proto {
        font-size: 0.68rem;
        font-weight: 600;
        color: var(--color-accent);
    }
    .delay {
        font-size: 0.72rem;
        font-family: var(--font-mono, ui-monospace, monospace);
        min-width: 60px;
        text-align: right;
    }
    .delay.ok      { color: var(--latency-color-ok); }
    .delay.slow    { color: var(--latency-color-slow); }
    .delay.fail    { color: var(--latency-color-fail); }
    .delay.unknown { color: var(--color-text-muted); }
    .err {
        padding: 0.5rem 0.75rem;
        color: var(--color-error);
        font-size: 0.78rem;
        border-top: 1px solid var(--color-border);
    }
</style>
