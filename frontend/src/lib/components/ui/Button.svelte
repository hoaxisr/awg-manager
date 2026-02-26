<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        variant?: 'filled' | 'ghost';
        preset?: 'primary' | 'secondary' | 'success' | 'warning' | 'danger';
        size?: 'sm' | 'md' | 'lg';
        loading?: boolean;
        disabled?: boolean;
        type?: 'button' | 'submit' | 'reset';
        onclick?: (e: MouseEvent) => void;
        children: Snippet;
    }

    let {
        variant = 'filled',
        preset = 'primary',
        size = 'md',
        loading = false,
        disabled = false,
        type = 'button',
        onclick,
        children
    }: Props = $props();

    const sizeClasses = {
        sm: 'btn-sm',
        md: '',
        lg: 'btn-lg'
    };

    const presetClasses: Record<string, string> = {
        primary: 'btn-primary',
        secondary: 'btn-secondary',
        success: 'btn-success',
        warning: 'btn-warning',
        danger: 'btn-danger'
    };

    let variantClass = $derived(variant === 'ghost' ? 'btn-icon' : presetClasses[preset]);
</script>

<button
    {type}
    class="btn {variantClass} {sizeClasses[size]}"
    disabled={disabled || loading}
    onclick={onclick}
>
    {#if loading}
        <span class="spinner"></span>
    {/if}
    {@render children()}
</button>
