<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        label: string;
        name: string;
        error?: string;
        hint?: string;
        required?: boolean;
        children: Snippet;
    }

    let {
        label,
        name,
        error,
        hint,
        required = false,
        children
    }: Props = $props();
</script>

<div class="space-y-1">
    <label for={name} class="label">
        <span class="label-text">
            {label}
            {#if required}
                <span class="text-error-500">*</span>
            {/if}
        </span>
    </label>

    {@render children()}

    {#if error}
        <p class="text-sm text-error-500">{error}</p>
    {:else if hint}
        <p class="text-sm text-surface-400">{hint}</p>
    {/if}
</div>
