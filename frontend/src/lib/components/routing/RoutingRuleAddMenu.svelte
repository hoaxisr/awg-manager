<script lang="ts">
    import { m } from '$lib/i18n';
    import DropdownMenu from '$lib/components/ui/DropdownMenu.svelte';
    import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
    import { LayoutGrid, Plus, Upload } from 'lucide-svelte';

    interface Props {
        label?: string;
        disabled?: boolean;
        oncatalog?: () => void;
        onmanual: () => void;
        importEnabled?: boolean;
        importLabel?: string;
        onimport?: () => void;
    }

    let {
        label,
        disabled = false,
        oncatalog,
        onmanual,
        importEnabled = false,
        importLabel,
        onimport,
    }: Props = $props();
</script>

{#snippet createIcon()}
    <CreateIcon />
{/snippet}

<DropdownMenu label={label ?? m.common_add()} size="sm" {disabled} iconBefore={createIcon}>
    {#snippet menu(close)}
        {#if oncatalog}
            <button
                type="button"
                class="dropdown-item"
                onclick={() => {
                    close();
                    oncatalog();
                }}
            >
                <LayoutGrid size={16} style="flex-shrink:0;color:var(--text-muted)" aria-hidden="true" />
                {m.routing_from_catalog()}
            </button>
        {/if}
        <button
            type="button"
            class="dropdown-item"
            onclick={() => {
                close();
                onmanual();
            }}
        >
            <Plus size={16} style="flex-shrink:0;color:var(--text-muted)" aria-hidden="true" />
            {m.routing_create_manual()}
        </button>
        {#if importEnabled && onimport}
            <div class="dropdown-sep"></div>
            <button
                type="button"
                class="dropdown-item"
                onclick={() => {
                    close();
                    onimport();
                }}
            >
                <Upload size={16} style="flex-shrink:0;color:var(--text-muted)" aria-hidden="true" />
                {importLabel ?? m.routing_load_config()}
            </button>
        {/if}
    {/snippet}
</DropdownMenu>

<style>
    .dropdown-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 0.5rem 0.75rem;
        border-radius: 4px;
        cursor: pointer;
        font-size: 0.8125rem;
        color: var(--text-secondary);
        border: none;
        background: none;
        width: 100%;
        text-align: left;
        font-family: inherit;
        transition: background 0.1s;
    }

    .dropdown-item:hover {
        background: var(--bg-hover);
        color: var(--text-primary);
    }

    .dropdown-sep {
        height: 1px;
        background: var(--border);
        margin: 4px 8px;
    }
</style>
