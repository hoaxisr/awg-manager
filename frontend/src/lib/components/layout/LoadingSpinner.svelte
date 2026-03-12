<script lang="ts">
    interface Props {
        size?: 'sm' | 'md' | 'lg';
        message?: string;
    }

    let { size = 'md', message }: Props = $props();

    const sizeClasses = {
        sm: 'w-4 h-4',
        md: 'w-8 h-8',
        lg: 'w-12 h-12'
    };
</script>

<div class="flex flex-col items-center justify-center gap-2">
    <div class="{sizeClasses[size]} border-2 border-primary-500 border-t-transparent rounded-full animate-spin"></div>
    {#if message}
        <p class="text-surface-400 text-sm">{message}</p>
    {/if}
</div>
