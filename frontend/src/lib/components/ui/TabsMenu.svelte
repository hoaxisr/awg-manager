<script lang="ts">
    import TabBadge from './TabBadge.svelte';
    import type { TabChild } from './tabs-model';

    interface Props {
        items: TabChild[];
        active: string;
        align: 'left' | 'right';
        onselect: (id: string) => void;
    }

    let { items, active, align, onselect }: Props = $props();
</script>

<div class="dropdown" class:align-left={align === 'left'} class:align-right={align === 'right'}>
    {#each items as item (item.id)}
        <button
            class="dropdown-item"
            class:active={item.id === active}
            onclick={() => onselect(item.id)}
        >
            {item.label}
            {#if item.badge !== undefined}
                <TabBadge badge={item.badge} tone={item.badgeTone} />
            {/if}
        </button>
    {/each}
</div>

<style>
    .dropdown {
        position: absolute;
        top: calc(100% + 6px);
        background: var(--bg-tertiary);
        border: 1px solid var(--border-bright, var(--border));
        border-radius: var(--radius);
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
        min-width: 180px;
        z-index: var(--z-page-overlay);
        overflow: hidden;
    }

    .dropdown.align-left {
        left: 0;
    }

    .dropdown.align-right {
        right: 0;
    }

    @media (max-width: 768px) {
        .dropdown {
            max-height: calc(100vh - 200px);
            overflow-y: auto;
        }
    }

    .dropdown-item {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.625rem 0.875rem;
        width: 100%;
        background: none;
        border: none;
        color: var(--text-secondary, var(--text-primary));
        font-size: 0.8125rem;
        cursor: pointer;
        text-align: left;
        transition: background 0.1s;
    }

    .dropdown-item:hover {
        background: var(--bg-hover);
    }

    .dropdown-item.active {
        color: var(--accent);
        font-weight: 600;
    }
</style>
