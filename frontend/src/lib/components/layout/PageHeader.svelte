<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        title: string;
        description?: string;
        actions?: Snippet;
    }

    let { title, description, actions }: Props = $props();
</script>

<div class="page-header">
    <div>
        <h1>{title}</h1>
        {#if description}
            <p class="description">{description}</p>
        {/if}
    </div>

    {#if actions}
        <div class="actions">
            {@render actions()}
        </div>
    {/if}
</div>

<style>
    .page-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 1rem;
        margin-bottom: 1.5rem;
    }

    .page-header h1 {
        font-size: 1.5rem;
        font-weight: 600;
        color: var(--text-primary);
        margin: 0;
    }

    .description {
        color: var(--text-muted);
        font-size: 0.875rem;
        margin: 0.25rem 0 0;
    }

    .actions {
        display: flex;
        gap: 0.5rem;
        flex-shrink: 0;
    }
</style>
