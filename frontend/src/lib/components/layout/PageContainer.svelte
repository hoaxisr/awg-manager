<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        maxWidth?: 'sm' | 'md' | 'lg' | 'xl' | '2xl' | 'full';
        padding?: boolean;
        children: Snippet;
    }

    let { maxWidth = 'xl', padding = true, children }: Props = $props();

    const maxWidthClasses = {
        sm: 'max-w-screen-sm',
        md: 'max-w-screen-md',
        lg: 'max-w-screen-lg',
        xl: 'max-w-screen-xl',
        '2xl': 'max-w-screen-2xl',
        full: 'max-w-full'
    };
</script>

<div class="page-container {maxWidthClasses[maxWidth]}" class:has-padding={padding}>
    {@render children()}
</div>

<style>
    .page-container {
        margin-left: auto;
        margin-right: auto;
    }
    .has-padding {
        padding-top: 2.5rem;
        padding-bottom: 1.5rem;
    }
</style>
