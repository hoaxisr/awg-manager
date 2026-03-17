<script lang="ts">
    import type { TunnelListItem, WANInterface, RouterInterface } from '$lib/types';
    import { Toggle } from '$lib/components/ui';

    interface Props {
        tunnels: TunnelListItem[];
        wanInterfaces: WANInterface[];
        allInterfaces: RouterInterface[];
        showAllInterfaces: boolean;
        loadingAllInterfaces: boolean;
        onToggleAllInterfaces: (checked: boolean) => void;
        onupdate: (tunnelId: string, ispInterface: string) => void;
        savingId: string | null;
    }

    let { tunnels, wanInterfaces, allInterfaces, showAllInterfaces, loadingAllInterfaces, onToggleAllInterfaces, onupdate, savingId }: Props = $props();
</script>

<div>
    <div class="section-label">Маршруты туннелей</div>
    <div class="card">
        <p class="section-hint">
            Через какое подключение соединяться с VPN-сервером для каждого туннеля.
        </p>
        <div class="routes-list">
            {#each tunnels as tunnel}
                <div class="route-item">
                    <span class="route-name">{tunnel.name}</span>
                    <div class="route-control">
                        <select
                            class="route-select"
                            value={tunnel.ispInterface || 'auto'}
                            onchange={(e) => onupdate(tunnel.id, e.currentTarget.value)}
                            disabled={savingId === tunnel.id}
                        >
                            <option value="auto">Автоматически</option>
                            {#if showAllInterfaces}
                                {#each allInterfaces as iface}
                                    <option value={iface.name}>
                                        {iface.label || iface.name} ({iface.name}) {iface.up ? '' : '— не активен'}
                                    </option>
                                {/each}
                            {:else}
                                {#each wanInterfaces as iface}
                                    <option value={iface.name}>
                                        {iface.label || iface.name} ({iface.name})
                                    </option>
                                {/each}
                            {/if}
                            {#if tunnels.length > 1}
                                <optgroup label="Через туннель">
                                    {#each tunnels as other}
                                        {#if other.id !== tunnel.id}
                                            <option value="tunnel:{other.id}">
                                                {other.name}
                                            </option>
                                        {/if}
                                    {/each}
                                </optgroup>
                            {/if}
                        </select>
                    </div>
                </div>
                {#if !tunnel.ispInterface || tunnel.ispInterface === 'auto'}
                    <div class="route-hint">
                        Следует за шлюзом по умолчанию
                    </div>
                {/if}
            {/each}
        </div>
        <div class="advanced-toggle">
            <Toggle
                checked={showAllInterfaces}
                onchange={onToggleAllInterfaces}
                loading={loadingAllInterfaces}
                label="Показать все интерфейсы"
                hint={showAllInterfaces ? 'Для продвинутых пользователей. Используйте, если роутер работает в режиме моста или подключён к интернету через нестандартный интерфейс.' : ''}
                size="sm"
            />
        </div>
    </div>
</div>

<style>
    .section-hint {
        color: var(--text-muted);
        font-size: 0.8125rem;
        margin: 0 0 0.75rem 0;
    }

    .routes-list {
        display: flex;
        flex-direction: column;
    }

    .route-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0.625rem 0;
        gap: 1rem;
    }

    .route-item:first-child {
        padding-top: 0;
    }

    .routes-list > :last-child {
        padding-bottom: 0;
    }

    .route-name {
        font-weight: 500;
        color: var(--text-primary);
        font-size: 0.875rem;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .route-control {
        flex-shrink: 0;
    }

    .route-select {
        min-width: 10rem;
        padding: 0.375rem 0.625rem;
        border: 1px solid var(--border);
        border-radius: 6px;
        background: var(--bg-primary);
        color: var(--text-primary);
        font-size: 0.8125rem;
        cursor: pointer;
    }

    .route-select:focus {
        outline: none;
        border-color: var(--accent);
    }

    .route-select:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .route-hint {
        font-size: 0.75rem;
        color: var(--text-muted);
        padding: 0 0 0.625rem 0;
    }

    .advanced-toggle {
        margin-top: 0.75rem;
        padding-top: 0.75rem;
        border-top: 1px solid var(--border);
    }

    @media (max-width: 480px) {
        .route-item {
            flex-direction: column;
            align-items: flex-start;
            gap: 0.5rem;
        }

        .route-select {
            width: 100%;
        }
    }
</style>
