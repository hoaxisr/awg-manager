<script lang="ts">
    import type { TabBadgeTone } from './tabs-model';

    interface Props {
        badge: number | string;
        tone?: TabBadgeTone;
        // Accent fill — set on the badge of the selected tab chip.
        onActive?: boolean;
    }

    let { badge, tone, onActive = false }: Props = $props();
</script>

<span
    class="tab-badge"
    class:success={tone === 'success'}
    class:warning={tone === 'warning'}
    class:muted={tone === 'muted'}
    class:on-active={onActive}
>{badge}</span>

<style>
    .tab-badge {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        min-width: 1.25rem;
        height: 1.25rem;
        padding: 0 0.375rem;
        border-radius: var(--radius-pill);
        background: var(--bg-hover);
        color: var(--text-muted);
        font-size: 0.6875rem;
        font-weight: 600;
    }

    .tab-badge.on-active {
        background: var(--accent);
        color: var(--color-accent-contrast, #fff);
    }

    .tab-badge.success {
        background: rgba(158, 206, 106, 0.18);
        color: var(--success);
    }
    .tab-badge.warning {
        background: rgba(224, 175, 104, 0.18);
        color: var(--warning);
    }
    .tab-badge.muted {
        background: var(--bg-hover);
        color: var(--text-muted);
        opacity: 0.75;
    }
    /* Active-tab overrides keep contrast on the selected tab. */
    .tab-badge.on-active.success {
        background: var(--success);
        color: var(--color-success-contrast, #fff);
    }
    .tab-badge.on-active.warning {
        background: var(--warning);
        color: var(--color-warning-contrast, #fff);
    }
</style>
