<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        title: string;
        description?: string;
        icon?: Snippet;
        action?: Snippet;
    }

    let { title, description, icon, action }: Props = $props();
</script>

<div class="empty-state">
    {#if icon}
        <div class="empty-state-icon">
            {@render icon()}
        </div>
    {/if}

    <h3 class="empty-state-title">{title}</h3>

    {#if description}
        <p class="empty-state-description">{description}</p>
    {/if}

    {#if action}
        <div class="empty-state-action">
            {@render action()}
        </div>
    {/if}
</div>

<style>
    .empty-state {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        padding: 3rem 1rem;
        text-align: center;
    }

    .empty-state-icon {
        margin-bottom: 1rem;
        color: var(--text-muted);
        opacity: 0.6;
    }

    .empty-state-icon :global(svg) {
        width: 48px;
        height: 48px;
    }

    .empty-state-title {
        font-size: 1.125rem;
        font-weight: 600;
        margin-bottom: 0.5rem;
        color: var(--text-primary);
    }

    .empty-state-description {
        color: var(--text-muted);
        max-width: 24rem;
        margin-bottom: 1.5rem;
    }

    .empty-state-action {
        margin-top: 0.5rem;
    }
</style>
