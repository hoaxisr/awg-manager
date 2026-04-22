<script lang="ts">
    import { Modal } from '$lib/components/ui';
    import { ServiceIcon } from '$lib/components/dnsroutes';
    import { SERVICE_PRESETS, type ServicePreset } from '$lib/data/presets';
    import type { RoutingTunnel } from '$lib/types';

    interface Props {
        open: boolean;
        existingNames: string[];
        tunnels: RoutingTunnel[];
        isOS5?: boolean;
        hydrarouteInstalled?: boolean;
        onclose: () => void;
        oncreate: (presets: ServicePreset[], tunnelId: string, backend: 'ndms' | 'hydraroute') => void;
    }

    let {
        open = $bindable(false),
        existingNames,
        tunnels,
        isOS5 = false,
        hydrarouteInstalled = false,
        onclose,
        oncreate,
    }: Props = $props();

    let selected = $state<Set<string>>(new Set());
    let defaultTunnelId = $state('');
    let backend = $state<'ndms' | 'hydraroute'>('ndms');
    let creating = $state(false);
    let wasOpen = $state(false);

    let showBackendSelector = $derived(isOS5 && hydrarouteInstalled);

    let userTunnels = $derived(tunnels.filter(t => t.type === 'managed' && t.available));
    let systemTunnels = $derived(tunnels.filter(t => t.type === 'system' && t.available));
    let wanTunnels = $derived(tunnels.filter(t => t.type === 'wan' && t.available));
    let noTunnels = $derived(tunnels.filter(t => t.available).length === 0);
    let existingLower = $derived(existingNames.map(n => n.toLowerCase()));

    $effect(() => {
        if (open && !wasOpen) {
            selected = new Set();
            defaultTunnelId = tunnels.find(t => t.available)?.id ?? '';
            backend = isOS5 ? 'ndms' : (hydrarouteInstalled ? 'hydraroute' : 'ndms');
            creating = false;
        }
        wasOpen = open;
    });

    // IDs covered by a selected "covers" preset (e.g. "all-blocked" covers youtube, discord, etc.)
    let coveredIds = $derived.by(() => {
        const ids = new Set<string>();
        for (const id of selected) {
            const preset = SERVICE_PRESETS.find(p => p.id === id);
            if (preset?.covers) {
                for (const c of preset.covers) ids.add(c);
            }
        }
        return ids;
    });

    function isAdded(preset: ServicePreset): boolean {
        return existingLower.includes(preset.name.toLowerCase());
    }

    function isCovered(preset: ServicePreset): boolean {
        return coveredIds.has(preset.id);
    }

    function toggle(presetId: string) {
        const next = new Set(selected);
        if (next.has(presetId)) {
            next.delete(presetId);
        } else {
            next.add(presetId);
            // If this preset covers others, deselect them
            const preset = SERVICE_PRESETS.find(p => p.id === presetId);
            if (preset?.covers) {
                for (const c of preset.covers) next.delete(c);
            }
        }
        selected = next;
    }

    function handleCreate() {
        if (selected.size === 0 || !defaultTunnelId) return;
        creating = true;
        const presets = SERVICE_PRESETS.filter(p => selected.has(p.id));
        oncreate(presets, defaultTunnelId, backend);
    }
</script>

<Modal {open} title="Каталог сервисов" size="lg" {onclose}>
    <div class="preset-grid">
        {#each SERVICE_PRESETS as preset (preset.id)}
            {@const added = isAdded(preset)}
            {@const covered = isCovered(preset)}
            {@const isSelected = selected.has(preset.id)}
            <button
                type="button"
                class="preset-card"
                class:selected={isSelected}
                class:added
                class:covered={covered && !isSelected}
                title={preset.notice || undefined}
                onclick={() => { if (!added) toggle(preset.id); }}
                disabled={added || creating}
            >
                {#if isSelected}
                    <span class="preset-check">&#10003;</span>
                {:else if added}
                    <span class="preset-badge">добавлено</span>
                {:else if covered}
                    <span class="preset-badge">входит в сборник</span>
                {/if}
                {#if preset.notice}
                    <span class="preset-notice-mark" aria-label="warning">⚠</span>
                {/if}
                <ServiceIcon name={preset.name} size={40} />
                <span class="preset-name">{preset.name}</span>
            </button>
        {/each}
    </div>

    {@const selectedWithNotices = SERVICE_PRESETS.filter(p => selected.has(p.id) && p.notice)}
    {#if selectedWithNotices.length > 0}
        <div class="notices-panel">
            {#each selectedWithNotices as p (p.id)}
                <div class="notice-entry">
                    <span class="notice-icon">⚠</span>
                    <div class="notice-body">
                        <strong class="notice-title">{p.name}</strong>
                        <span class="notice-text">{p.notice}</span>
                    </div>
                </div>
            {/each}
        </div>
    {/if}

    <!-- Backend + Tunnel selector -->
    <div class="tunnel-bar">
        {#if showBackendSelector}
            <span class="tunnel-label">Движок:</span>
            <select class="tunnel-select" style="flex: 0 1 auto; max-width: 180px;" bind:value={backend} disabled={creating}>
                <option value="ndms">NDMS</option>
                <option value="hydraroute">HydraRoute Neo</option>
            </select>
        {/if}
        <span class="tunnel-label">Туннель:</span>
        <select class="tunnel-select" bind:value={defaultTunnelId} disabled={creating}>
            {#if userTunnels.length > 0}
                <optgroup label="Пользовательские">
                    {#each userTunnels as t}
                        <option value={t.id}>{t.name}</option>
                    {/each}
                </optgroup>
            {/if}
            {#if systemTunnels.length > 0}
                <optgroup label="Системные">
                    {#each systemTunnels as t}
                        <option value={t.id}>{t.name}</option>
                    {/each}
                </optgroup>
            {/if}
            {#if wanTunnels.length > 0}
                <optgroup label="WAN">
                    {#each wanTunnels as t}
                        <option value={t.id}>{t.name}</option>
                    {/each}
                </optgroup>
            {/if}
        </select>
    </div>

    {#if noTunnels}
        <p class="no-tunnels">Создайте хотя бы один туннель</p>
    {/if}

    {#snippet actions()}
        <button class="btn btn-ghost" onclick={onclose} disabled={creating}>Отмена</button>
        <button
            class="btn btn-primary"
            onclick={handleCreate}
            disabled={creating || selected.size === 0 || noTunnels}
        >
            {creating ? 'Создание...' : `Создать (${selected.size})`}
        </button>
    {/snippet}
</Modal>

<style>
    .preset-grid {
        display: grid;
        grid-template-columns: repeat(4, 1fr);
        gap: 10px;
        margin-bottom: 1rem;
    }

    @media (max-width: 640px) {
        .preset-grid {
            grid-template-columns: repeat(3, 1fr);
        }
    }

    @media (max-width: 420px) {
        .preset-grid {
            grid-template-columns: repeat(2, 1fr);
        }
    }

    .preset-card {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 0.375rem;
        padding: 0.875rem 0.5rem;
        background: var(--bg-primary);
        border: 2px solid var(--border);
        border-radius: 10px;
        cursor: pointer;
        transition: border-color 0.15s;
        position: relative;
    }

    .preset-card:hover:not(.added) {
        border-color: var(--text-muted);
    }

    .preset-card.selected {
        border-color: var(--accent);
    }

    .preset-card.added {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .preset-card.covered {
        opacity: 0.35;
        filter: grayscale(0.5);
    }

    .preset-check {
        position: absolute;
        top: 6px;
        right: 6px;
        width: 18px;
        height: 18px;
        border-radius: 4px;
        background: var(--accent);
        color: #fff;
        font-size: 11px;
        display: flex;
        align-items: center;
        justify-content: center;
    }

    .preset-badge {
        position: absolute;
        top: 6px;
        right: 6px;
        font-size: 0.5625rem;
        color: var(--text-muted);
    }

    .preset-notice-mark {
        position: absolute;
        top: 6px;
        left: 6px;
        font-size: 0.875rem;
        color: var(--warning, #f59e0b);
        cursor: help;
        line-height: 1;
    }

    .preset-name {
        font-size: 0.6875rem;
        font-weight: 500;
        color: var(--text-primary);
        text-align: center;
    }

    .tunnel-bar {
        display: flex;
        align-items: center;
        gap: 0.75rem;
        padding: 0.625rem 0.75rem;
        background: var(--bg-primary);
        border: 1px solid var(--border);
        border-radius: 8px;
        margin-bottom: 0.75rem;
    }

    .tunnel-label {
        color: var(--text-muted);
        font-size: 0.75rem;
        white-space: nowrap;
    }

    .tunnel-select {
        flex: 1;
        background: var(--bg-secondary);
        border: 1px solid var(--border);
        border-radius: 4px;
        padding: 0.375rem 0.5rem;
        color: var(--text-primary);
        font-size: 0.8125rem;
    }

    .no-tunnels {
        color: var(--error);
        font-size: 0.8125rem;
    }

    .notices-panel {
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
        margin-bottom: 1rem;
        padding: 0.625rem 0.75rem;
        background: rgba(245, 158, 11, 0.08);
        border: 1px solid rgba(245, 158, 11, 0.25);
        border-radius: 6px;
    }
    .notice-entry {
        display: flex;
        align-items: flex-start;
        gap: 0.5rem;
    }
    .notice-icon {
        color: var(--warning, #f59e0b);
        font-size: 0.875rem;
        line-height: 1.4;
        flex-shrink: 0;
    }
    .notice-body {
        display: flex;
        flex-direction: column;
        gap: 0.125rem;
        font-size: 0.75rem;
        line-height: 1.4;
        color: var(--text-secondary);
    }
    .notice-title {
        color: var(--text-primary);
        font-weight: 500;
        font-size: 0.75rem;
    }
    .notice-text {
        color: var(--text-secondary);
    }
</style>
