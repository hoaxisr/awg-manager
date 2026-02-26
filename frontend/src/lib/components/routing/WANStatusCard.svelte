<script lang="ts">
    import type { WANStatus, WANInterface } from '$lib/types';

    interface Props {
        status: WANStatus;
        wanInterfaces: WANInterface[];
    }

    let { status, wanInterfaces }: Props = $props();

    function isUp(name: string): boolean {
        return status.interfaces[name]?.up ?? false;
    }
</script>

<div>
    <div class="section-label">WAN-подключения</div>
    <div class="card">
        <div class="wan-list">
            {#each wanInterfaces as iface}
                <div class="wan-item">
                    <div class="wan-left">
                        <div class="wan-info">
                            <span class="wan-name">{iface.label || iface.name}</span>
                            <span class="wan-sysname">{iface.name}</span>
                        </div>
                        <span class="wan-status" class:up={isUp(iface.name)} class:down={!isUp(iface.name)}>
                            {isUp(iface.name) ? 'Активен' : 'Не активен'}
                        </span>
                    </div>
                </div>
            {/each}
        </div>
    </div>
</div>

<style>
    .wan-list {
        display: flex;
        flex-direction: column;
    }

    .wan-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0.75rem 0;
        gap: 1rem;
    }

    .wan-item + .wan-item {
        border-top: 1px solid var(--border);
    }

    .wan-item:first-child {
        padding-top: 0;
    }

    .wan-item:last-child {
        padding-bottom: 0;
    }

    .wan-left {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
        min-width: 0;
    }

    .wan-info {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        flex-wrap: wrap;
    }

    .wan-name {
        font-weight: 500;
        color: var(--text-primary);
        font-size: 0.875rem;
    }

    .wan-sysname {
        color: var(--text-muted);
        font-size: 0.75rem;
        font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
    }

    .wan-status {
        font-size: 0.8125rem;
        font-weight: 500;
    }

    .wan-status.up {
        color: var(--success);
    }

    .wan-status.down {
        color: var(--text-muted);
    }
</style>
