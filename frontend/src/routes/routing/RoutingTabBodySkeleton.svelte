<script lang="ts">
    interface Props {
        /** Cards grid (NDMS / IP / client VPN) vs stacked rows (политики). */
        variant?: 'cards' | 'rows';
    }

    let { variant = 'cards' }: Props = $props();
</script>

<div class="sk-root" aria-hidden="true">
    {#if variant === 'rows'}
        <div class="sk-rows">
            {#each [1, 2, 3, 4, 5] as _}
                <div class="sk-row"></div>
            {/each}
        </div>
    {:else}
        <div class="route-grid sk-grid">
            {#each [1, 2, 3, 4, 5, 6] as _}
                <div class="sk-card">
                    <div class="sk-card-top">
                        <div class="sk-dot"></div>
                        <div class="sk-card-text">
                            <div class="sk-title"></div>
                            <div class="sk-line sk-line-stat"></div>
                            <div class="sk-line sk-line-stat sk-line-narrow"></div>
                            <div class="sk-line sk-line-route"></div>
                        </div>
                    </div>
                </div>
            {/each}
        </div>
    {/if}
</div>

<style>
    @keyframes sk-pulse {
        0%,
        100% {
            opacity: 0.35;
        }
        50% {
            opacity: 0.65;
        }
    }

    .sk-root {
        animation: sk-pulse 1.2s ease-in-out infinite;
    }

    .sk-grid {
        margin-top: 0.25rem;
    }

    .sk-card {
        min-height: 158px;
        border-radius: 8px;
        border: 1px solid var(--border);
        background: var(--bg-secondary, var(--bg-card, #1a1b2e));
        padding: 14px;
        display: flex;
        flex-direction: column;
        justify-content: flex-start;
    }

    .sk-card-top {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        flex: 1;
        min-height: 0;
    }

    .sk-dot {
        width: 36px;
        height: 36px;
        border-radius: 8px;
        flex-shrink: 0;
        background: var(--bg-hover, rgba(255, 255, 255, 0.06));
    }

    .sk-card-text {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding-top: 2px;
    }

    .sk-title {
        height: 16px;
        width: 62%;
        max-width: 100%;
        border-radius: 4px;
        background: var(--bg-hover, rgba(255, 255, 255, 0.08));
    }

    .sk-line {
        height: 9px;
        border-radius: 3px;
        background: var(--bg-hover, rgba(255, 255, 255, 0.05));
    }

    .sk-line-stat {
        width: 88%;
    }

    .sk-line-narrow {
        width: 52%;
    }

    .sk-line-route {
        width: 72%;
        margin-top: 4px;
        height: 10px;
    }

    .sk-rows {
        display: flex;
        flex-direction: column;
        gap: 8px;
        margin-top: 0.25rem;
    }

    .sk-row {
        height: 58px;
        border-radius: 8px;
        border: 1px solid var(--border);
        background: var(--bg-secondary, var(--bg-card, #1a1b2e));
    }
</style>
