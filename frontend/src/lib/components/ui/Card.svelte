<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        padding?: 'none' | 'sm' | 'md' | 'lg';
        hover?: boolean;
        children: Snippet;
        header?: Snippet;
        footer?: Snippet;
    }

    let {
        padding = 'md',
        hover = false,
        children,
        header,
        footer
    }: Props = $props();

    const paddingClasses = {
        none: '',
        sm: 'p-2',
        md: 'p-4',
        lg: 'p-6'
    };
</script>

<div class="card {paddingClasses[padding]} {hover ? 'card-hover' : ''}">
    {#if header}
        <header class="card-header">
            {@render header()}
        </header>
    {/if}

    <section class="card-body">
        {@render children()}
    </section>

    {#if footer}
        <footer class="card-footer">
            {@render footer()}
        </footer>
    {/if}
</div>
