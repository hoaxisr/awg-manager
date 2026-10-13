<script lang="ts">
  import { onMount } from 'svelte';
  import { Modal, Button, Badge, SegmentedControl } from '$lib/components/ui';
  import { Zap, Check, Search, Globe, RefreshCw, Radio, Layers, ShieldCheck, Activity } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import type { Subscription, SubscriptionMember, MihomoNativeSubscription, MihomoRuntimeProxy } from '$lib/types';
  import { pluralize } from '$lib/utils/pluralize';

  interface Props {
    open: boolean;
    subRef: string;
    mihomoSubscriptions?: MihomoNativeSubscription[];
    runtimeProxies?: MihomoRuntimeProxy[];
    onClose: () => void;
    onUpdated?: () => void;
  }

  let {
    open,
    subRef,
    mihomoSubscriptions = [],
    runtimeProxies = [],
    onClose,
    onUpdated,
  }: Props = $props();

  let searchQuery = $state('');
  let testing = $state(false);
  let selectingTag = $state<string | null>(null);
  let localDelays = $state<Record<string, number>>({});
  let activeMemberTag = $state<string | null>(null);
  let currentMode = $state<'auto' | 'manual'>('auto');
  let switchingMode = $state(false);
  let loadedSingboxSubs = $state<Subscription[]>([]);
  let loadedMihomoSubs = $state<MihomoNativeSubscription[]>([]);

  const allMihomoSubs = $derived(
    loadedMihomoSubs.length > 0 ? loadedMihomoSubs : mihomoSubscriptions
  );
  const allSingboxSubs = $derived(
    loadedSingboxSubs.length > 0 ? loadedSingboxSubs : ($subscriptionsStore.data ?? [])
  );

  // Identify subscription (Mihomo or Sing-box)
  const matchedMihomoSub = $derived.by(() => {
    if (!subRef) return null;
    return allMihomoSubs.find(
      (s) =>
        s.id === subRef ||
        s.name === subRef ||
        s.groupName === subRef ||
        s.providerName === subRef ||
        `Mihomo: ${s.name}` === subRef
    ) ?? null;
  });

  const matchedSingboxSub = $derived.by(() => {
    if (!subRef) return null;
    return allSingboxSubs.find(
      (s) =>
        s.id === subRef ||
        s.selectorTag === subRef ||
        s.label === subRef ||
        (s.id && subRef.includes(s.id.slice(0, 8)))
    ) ?? null;
  });

  const subName = $derived(
    matchedMihomoSub?.name ||
    matchedSingboxSub?.label ||
    subRef
  );

  const isMihomo = $derived(matchedMihomoSub !== null);

  interface DisplayServer {
    tag: string;
    label: string;
    server: string;
    port?: number;
    protocol?: string;
    security?: string;
    transport?: string;
  }

  const servers = $derived.by((): DisplayServer[] => {
    if (matchedMihomoSub) {
      if (matchedMihomoSub.members && matchedMihomoSub.members.length > 0) {
        return matchedMihomoSub.members.map((m) => ({
          tag: m.tag,
          label: m.label || m.tag,
          server: m.server || m.tag,
          port: m.port,
          protocol: m.protocol?.toUpperCase() || 'VLESS',
          security: m.security,
          transport: m.transport,
        }));
      }
      // Fallback from runtime proxy group
      const grp = runtimeProxies.find((p) => p.name === matchedMihomoSub.groupName);
      if (grp?.all) {
        return grp.all.map((t) => ({
          tag: t,
          label: t,
          server: t,
          protocol: 'PROXY',
        }));
      }
    }

    if (matchedSingboxSub) {
      const list = matchedSingboxSub.members ?? [];
      return list.map((m) => ({
        tag: m.tag,
        label: m.label || m.tag,
        server: m.server || m.tag,
        port: m.port,
        protocol: m.protocol?.toUpperCase() || 'VLESS',
        security: m.security,
        transport: m.transport,
      }));
    }

    return [];
  });

  const filteredServers = $derived.by(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return servers;
    return servers.filter(
      (s) =>
        s.label.toLowerCase().includes(q) ||
        s.server.toLowerCase().includes(q) ||
        (s.protocol && s.protocol.toLowerCase().includes(q))
    );
  });

  // Sync mode and active member when opening
  $effect(() => {
    if (open) {
      searchQuery = '';
      void (async () => {
        try {
          const [sbSubs, mhSubs] = await Promise.all([
            api.listSubscriptions().catch(() => []),
            api.mihomoNativeSubscriptions().catch(() => []),
          ]);
          if (sbSubs.length > 0) loadedSingboxSubs = sbSubs;
          if (mhSubs.length > 0) loadedMihomoSubs = mhSubs;
        } catch {}
        void updateDelays();
      })();
      if (matchedMihomoSub) {
        currentMode = matchedMihomoSub.mode === 'select' ? 'manual' : 'auto';
        const grp = runtimeProxies.find((p) => p.name === matchedMihomoSub.groupName);
        activeMemberTag = grp?.now || matchedMihomoSub.members?.[0]?.tag || null;
      } else if (matchedSingboxSub) {
        currentMode = matchedSingboxSub.mode === 'selector' ? 'manual' : 'auto';
        activeMemberTag = matchedSingboxSub.activeMember || matchedSingboxSub.members?.[0]?.tag || null;
      }
    }
  });

  function getMemberDelay(tag: string): number | null {
    if (tag in localDelays) return localDelays[tag];
    const rp = runtimeProxies.find((p) => p.name === tag);
    if (rp?.history && rp.history.length > 0) {
      return rp.history[rp.history.length - 1].delay ?? null;
    }
    return null;
  }

  function getDelayTone(delay: number | null): 'good' | 'medium' | 'bad' | 'none' {
    if (delay === null || delay === undefined) return 'none';
    if (delay <= 0) return 'bad';
    if (delay < 200) return 'good';
    if (delay < 600) return 'medium';
    return 'bad';
  }

  async function updateDelays() {
    if (testing) return;
    testing = true;
    try {
      if (matchedMihomoSub) {
        if (matchedMihomoSub.providerName) {
          await api.mihomoRuntimeProviderHealthcheck(matchedMihomoSub.providerName).catch(() => {});
        }
        const fresh = await api.mihomoRuntimeProxies().catch(() => null);
        if (fresh?.proxies) {
          for (const s of servers) {
            const p = fresh.proxies[s.tag];
            if (p?.history && p.history.length > 0) {
              localDelays[s.tag] = p.history[p.history.length - 1].delay ?? 0;
            }
          }
        }
      } else if (matchedSingboxSub) {
        // Measure up to first 25 servers
        const targets = servers.slice(0, 25);
        await Promise.allSettled(
          targets.map(async (s) => {
            try {
              const d = await api.mihomoRuntimeDelay(s.tag, 'https://www.gstatic.com/generate_204', 3000);
              localDelays[s.tag] = d;
            } catch {
              localDelays[s.tag] = 0;
            }
          })
        );
      }
    } catch {
      // Ignored
    } finally {
      testing = false;
    }
  }

  async function handleModeChange(newVal: string) {
    const nextMode = newVal as 'auto' | 'manual';
    if (nextMode === currentMode || switchingMode) return;
    switchingMode = true;
    try {
      if (matchedMihomoSub) {
        const mihomoMode = nextMode === 'auto' ? 'url-test' : 'select';
        await api.mihomoNativeUpdateSubscription(matchedMihomoSub.id, {
          name: matchedMihomoSub.name,
          url: matchedMihomoSub.url,
          format: matchedMihomoSub.format,
          enginePreference: matchedMihomoSub.enginePreference,
          refreshHours: matchedMihomoSub.refreshHours,
          enabled: matchedMihomoSub.enabled,
          mode: mihomoMode,
        });
        currentMode = nextMode;
        notifications.success(
          nextMode === 'auto'
            ? `Включен автовыбор узлов для подписки «${subName}»`
            : `Включен ручной выбор узлов для подписки «${subName}»`
        );
      } else if (matchedSingboxSub) {
        const sbMode = nextMode === 'auto' ? 'urltest' : 'selector';
        await api.updateSubscription(matchedSingboxSub.id, { mode: sbMode });
        currentMode = nextMode;
        notifications.success(
          nextMode === 'auto'
            ? `Включен автовыбор узлов для подписки «${subName}»`
            : `Включен ручной выбор узлов для подписки «${subName}»`
        );
      }
      onUpdated?.();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка переключения режима');
    } finally {
      switchingMode = false;
    }
  }

  async function handleSelectServer(server: DisplayServer) {
    if (selectingTag) return;
    selectingTag = server.tag;
    try {
      if (matchedMihomoSub) {
        if (currentMode === 'auto') {
          await api.mihomoNativeUpdateSubscription(matchedMihomoSub.id, {
            name: matchedMihomoSub.name,
            url: matchedMihomoSub.url,
            format: matchedMihomoSub.format,
            enginePreference: matchedMihomoSub.enginePreference,
            refreshHours: matchedMihomoSub.refreshHours,
            enabled: matchedMihomoSub.enabled,
            mode: 'select',
          }).catch(() => {});
          currentMode = 'manual';
        }
        await api.mihomoRuntimeSelect(matchedMihomoSub.groupName || matchedMihomoSub.name, server.tag);
        activeMemberTag = server.tag;
        notifications.success(`Выбран сервер «${server.label}»`);
      } else if (matchedSingboxSub) {
        if (currentMode === 'auto') {
          await api.updateSubscription(matchedSingboxSub.id, { mode: 'selector' }).catch(() => {});
          currentMode = 'manual';
        }
        await api.setSubscriptionActiveMember(matchedSingboxSub.id, server.tag);
        activeMemberTag = server.tag;
        notifications.success(`Выбран сервер «${server.label}»`);
      }
      onUpdated?.();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка выбора сервера');
    } finally {
      selectingTag = null;
    }
  }
</script>

<Modal {open} onclose={onClose} title={`Подписка: ${subName}`} size="lg">
  <div class="sub-picker-container">
    <div class="sub-header-strip">
      <div class="sub-meta">
        <span class="sub-engine-badge">
          <Badge variant={isMihomo ? 'accent' : 'warning'} size="sm">
            {isMihomo ? 'Mihomo Provider' : 'Sing-box'}
          </Badge>
        </span>
        <span class="sub-count">
          {pluralize(servers.length, ['сервер', 'сервера', 'серверов'])}
        </span>
      </div>

      <div class="mode-control-row">
        <span class="control-label">Режим:</span>
        <SegmentedControl
          value={currentMode}
          options={[
            { value: 'auto', label: 'Автовыбор' },
            { value: 'manual', label: 'Ручной выбор' },
          ]}
          ariaLabel="Режим выбора сервера"
          onchange={handleModeChange}
        />
      </div>
    </div>

    <div class="filter-actions-row">
      <div class="search-input-wrapper">
        <Search size={14} class="search-icon" />
        <input
          type="text"
          placeholder="Поиск по названию или серверу..."
          bind:value={searchQuery}
          class="sub-search-input"
        />
      </div>

      <button
        type="button"
        class="test-btn"
        disabled={testing}
        onclick={updateDelays}
        title="Замерить задержки серверов"
      >
        <RefreshCw size={13} class={testing ? 'spin' : ''} />
        <span>{testing ? 'Замер...' : 'Замерить пинг'}</span>
      </button>
    </div>

    <div class="server-list-scroll">
      {#if filteredServers.length === 0}
        <div class="empty-list">
          <span>Серверы не найдены</span>
        </div>
      {:else}
        {#each filteredServers as s (s.tag)}
          {@const isActive = activeMemberTag === s.tag}
          {@const delay = getMemberDelay(s.tag)}
          {@const tone = getDelayTone(delay)}
          {@const isSelecting = selectingTag === s.tag}

          <button
            type="button"
            class="server-row"
            class:active={isActive}
            class:selecting={isSelecting}
            onclick={() => handleSelectServer(s)}
          >
            <div class="radio-col">
              <span class="radio-circle" class:checked={isActive}>
                {#if isActive}
                  <span class="radio-dot"></span>
                {/if}
              </span>
            </div>

            <div class="server-info-col">
              <div class="server-name-row">
                <span class="server-name" title={s.label}>{s.label}</span>
                {#if isActive}
                  <span class="active-badge">Активен</span>
                {/if}
              </div>
              <div class="server-details-row">
                {#if s.protocol}
                  <span class="protocol-chip">{s.protocol}</span>
                {/if}
                {#if s.security}
                  <span class="sec-chip">{s.security}</span>
                {/if}
                {#if s.transport}
                  <span class="sec-chip">{s.transport}</span>
                {/if}
                <span class="host-text">{s.server}{s.port ? `:${s.port}` : ''}</span>
              </div>
            </div>

            <div class="delay-col">
              {#if delay !== null}
                <span class="delay-pill tone-{tone}">
                  {delay > 0 ? `${delay} мс` : 'timeout'}
                </span>
              {:else}
                <span class="delay-pill tone-none">—</span>
              {/if}
            </div>
          </button>
        {/each}
      {/if}
    </div>

    <div class="modal-footer-row">
      <Button variant="secondary" onclick={onClose}>
        Закрыть
      </Button>
    </div>
  </div>
</Modal>

<style>
  .sub-picker-container {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding-top: 4px;
  }

  .sub-header-strip {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    background: var(--bg-card);
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    flex-wrap: wrap;
  }

  .sub-meta {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .sub-count {
    font-size: 13px;
    color: var(--text-secondary);
  }

  .mode-control-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .control-label {
    font-size: 13px;
    color: var(--text-secondary);
    font-weight: 500;
  }

  .filter-actions-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .search-input-wrapper {
    position: relative;
    flex: 1;
  }

  :global(.search-icon) {
    position: absolute;
    left: 10px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--text-muted);
    pointer-events: none;
  }

  .sub-search-input {
    width: 100%;
    padding: 7px 10px 7px 32px;
    font-size: 13px;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    color: var(--text-primary);
    outline: none;
    transition: border-color 0.15s ease;
  }

  .sub-search-input:focus {
    border-color: var(--accent);
  }

  .test-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 7px 12px;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
    background: var(--bg-card);
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    cursor: pointer;
    white-space: nowrap;
    transition: all 0.15s ease;
  }

  .test-btn:hover:not(:disabled) {
    border-color: var(--accent);
    color: var(--accent);
  }

  .test-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .server-list-scroll {
    max-height: 380px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding-right: 2px;
  }

  .server-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    background: var(--bg-card);
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    cursor: pointer;
    text-align: left;
    transition: all 0.15s ease;
  }

  .server-row:hover {
    border-color: var(--border-hover, #64748b);
    background: var(--bg-surface);
  }

  .server-row.active {
    border-color: var(--accent);
    background: rgba(var(--accent-rgb, 59, 130, 246), 0.08);
  }

  .radio-col {
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .radio-circle {
    width: 16px;
    height: 16px;
    border-radius: 50%;
    border: 1.5px solid var(--border-subtle);
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.15s ease;
  }

  .radio-circle.checked {
    border-color: var(--accent);
  }

  .radio-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--accent);
  }

  .server-info-col {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 3px;
  }

  .server-name-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .server-name {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .active-badge {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    color: var(--accent);
    background: rgba(var(--accent-rgb, 59, 130, 246), 0.15);
    padding: 1px 6px;
    border-radius: 4px;
    flex-shrink: 0;
  }

  .server-details-row {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    color: var(--text-muted);
  }

  .protocol-chip {
    font-weight: 600;
    font-size: 10px;
    color: var(--text-secondary);
    background: var(--bg-surface);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border-subtle);
  }

  .sec-chip {
    font-size: 10px;
    color: var(--text-muted);
    background: var(--bg-surface);
    padding: 1px 4px;
    border-radius: 3px;
  }

  .host-text {
    font-family: var(--font-mono, monospace);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .delay-col {
    flex-shrink: 0;
  }

  .delay-pill {
    font-size: 11px;
    font-weight: 500;
    font-variant-numeric: tabular-nums;
    font-family: var(--font-mono, monospace);
    line-height: 1;
    padding: 1px 4px;
    border-radius: 4px;
    background: transparent;
    border: 1px solid transparent;
    white-space: nowrap;
  }

  .tone-good {
    color: var(--color-success, #16a34a);
  }

  :global(.dark) .tone-good {
    color: var(--color-success, #4ade80);
  }

  .tone-medium {
    color: var(--color-warning, #d97706);
  }

  :global(.dark) .tone-medium {
    color: var(--color-warning, #fde047);
  }

  .tone-bad {
    color: var(--color-error, #dc2626);
  }

  :global(.dark) .tone-bad {
    color: var(--color-error, #f87171);
  }

  .tone-none {
    color: var(--color-text-muted, #94a3b8);
  }

  .empty-list {
    padding: 30px;
    text-align: center;
    color: var(--text-muted);
    font-size: 13px;
  }

  .modal-footer-row {
    display: flex;
    justify-content: flex-end;
    padding-top: 6px;
  }

  :global(.spin) {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
</style>
