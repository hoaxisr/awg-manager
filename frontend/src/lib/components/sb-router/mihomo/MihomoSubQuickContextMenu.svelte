<script lang="ts">
  import { untrack } from 'svelte';
  import { Zap, Search, RefreshCw, Layers, Check, Radio, SlidersHorizontal } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { singboxDelayHistory, triggerDelayCheck } from '$lib/stores/singbox';
  import { runWithConcurrency } from '$lib/utils/runWithConcurrency';
  import type { Subscription, MihomoNativeSubscription, MihomoRuntimeProxy } from '$lib/types';
  import { pluralize } from '$lib/utils/pluralize';

  interface Props {
    open: boolean;
    subRef: string;
    triggerEl: HTMLElement | null;
    mihomoSubscriptions?: MihomoNativeSubscription[];
    runtimeProxies?: MihomoRuntimeProxy[];
    onClose: () => void;
    onUpdated?: () => void;
  }

  let {
    open,
    subRef,
    triggerEl,
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

  let menuEl = $state<HTMLDivElement | null>(null);
  let menuTop = $state(0);
  let menuLeft = $state(0);
  let menuUp = $state(false);

  const allMihomoSubs = $derived(
    loadedMihomoSubs.length > 0 ? loadedMihomoSubs : mihomoSubscriptions
  );
  const allSingboxSubs = $derived(
    loadedSingboxSubs.length > 0 ? loadedSingboxSubs : ($subscriptionsStore.data ?? [])
  );

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
        (s.selectorTag && s.selectorTag === subRef) ||
        s.id === subRef ||
        s.label === subRef ||
        (subRef.startsWith('sub-') && s.id.startsWith(subRef.slice(4))) ||
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

  function updatePosition() {
    if (!triggerEl) return;
    const rect = triggerEl.getBoundingClientRect();
    const width = 310;
    const padding = 8;
    const measuredH = menuEl?.getBoundingClientRect().height ?? 0;
    const menuHeight = measuredH > 0 ? measuredH : 320;

    let left = rect.left;
    if (left + width > window.innerWidth - padding) {
      left = window.innerWidth - width - padding;
    }
    if (left < padding) left = padding;

    const spaceBelow = window.innerHeight - rect.bottom - padding;
    const spaceAbove = rect.top - padding;
    const flip = menuHeight > spaceBelow && spaceAbove > spaceBelow;
    menuUp = flip;

    if (flip) {
      menuTop = Math.max(padding, rect.top - menuHeight - 4);
    } else {
      menuTop = rect.bottom + 4;
    }
    menuLeft = left;
  }

  function handleKeydown(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === 'Escape') {
      onClose();
    }
  }

  $effect(() => {
    if (open) {
      untrack(() => {
        searchQuery = '';
        requestAnimationFrame(updatePosition);

        void (async () => {
          try {
            const [sbSubs, mhSubs, initialProvs] = await Promise.all([
              api.listSubscriptions().catch(() => []),
              api.mihomoNativeSubscriptions().catch(() => []),
              api.mihomoRuntimeProviders().catch(() => null),
            ]);
            if (sbSubs.length > 0) loadedSingboxSubs = sbSubs;
            if (mhSubs.length > 0) loadedMihomoSubs = mhSubs;

            if (initialProvs?.providers) {
              const nextDelays: Record<string, number> = { ...localDelays };
              for (const pName of Object.keys(initialProvs.providers)) {
                const p = initialProvs.providers[pName];
                if (p?.proxies) {
                  for (let i = 0; i < p.proxies.length; i++) {
                    const px = p.proxies[i];
                    const lastHist = px.history?.[px.history.length - 1];
                    const d = (lastHist && typeof lastHist.delay === 'number' && lastHist.delay > 0) ? lastHist.delay : null;
                    if (d !== null) {
                      if (pName === matchedMihomoSub?.providerName && i < servers.length) {
                        nextDelays[servers[i].tag] = d;
                        nextDelays[servers[i].label] = d;
                      }
                      for (const s of servers) {
                        if (s.label === px.name || s.tag === px.name || s.server === px.name) {
                          nextDelays[s.tag] = d;
                          nextDelays[s.label] = d;
                        }
                      }
                    }
                  }
                }
              }
              localDelays = nextDelays;
            }
          } catch {}
          requestAnimationFrame(updatePosition);
        })();

        if (matchedMihomoSub) {
          currentMode = matchedMihomoSub.mode === 'select' ? 'manual' : 'auto';
          const groupTarget = matchedMihomoSub.groupName || matchedMihomoSub.name || `Mihomo: ${matchedMihomoSub.name}`;
          const grp = runtimeProxies.find((p) => p.name === groupTarget || p.name === matchedMihomoSub.groupName || p.name === matchedMihomoSub.name);
          activeMemberTag = grp?.now || matchedMihomoSub.members?.[0]?.label || matchedMihomoSub.members?.[0]?.tag || null;
        } else if (matchedSingboxSub) {
          currentMode = matchedSingboxSub.mode === 'selector' ? 'manual' : 'auto';
          const subTag = `sub-${matchedSingboxSub.id.slice(0, 8)}`;
          const grp = runtimeProxies.find((p) =>
            p.name === subRef ||
            p.name === subTag ||
            (p.name.startsWith('sub-') && matchedSingboxSub.id.startsWith(p.name.slice(4))) ||
            (matchedSingboxSub.label && p.name === matchedSingboxSub.label)
          );
          activeMemberTag = matchedSingboxSub.activeMember || grp?.now || matchedSingboxSub.members?.[0]?.tag || null;
        }
      });

      window.addEventListener('keydown', handleKeydown);
      window.addEventListener('resize', updatePosition);

      return () => {
        delayAbortController?.abort();
        window.removeEventListener('keydown', handleKeydown);
        window.removeEventListener('resize', updatePosition);
      };
    }
  });

  function getMemberDelay(server: DisplayServer): number | null {
    if (server.tag in localDelays && typeof localDelays[server.tag] === 'number') {
      return localDelays[server.tag];
    }
    if (server.label in localDelays && typeof localDelays[server.label] === 'number') {
      return localDelays[server.label];
    }
    // Check Singbox delay history store
    const sbHist = $singboxDelayHistory.get(server.tag) ?? [];
    if (sbHist.length > 0) {
      const last = sbHist[sbHist.length - 1];
      if (typeof last === 'number' && last > 0) {
        return last;
      }
    }
    const rp = runtimeProxies.find((p) => p.name === server.tag || p.name === server.label);
    if (rp?.history && rp.history.length > 0) {
      const last = rp.history[rp.history.length - 1]?.delay;
      if (typeof last === 'number' && last > 0) return last;
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

  let delayAbortController: AbortController | null = null;

  async function updateDelays() {
    if (testing) return;
    testing = true;
    delayAbortController?.abort();
    delayAbortController = new AbortController();
    const signal = delayAbortController.signal;

    try {
      const nextDelays: Record<string, number> = { ...localDelays };
      if (matchedMihomoSub) {
        if (matchedMihomoSub.providerName) {
          await api.mihomoRuntimeProviderHealthcheck(matchedMihomoSub.providerName).catch(() => {});
        }
        if (signal.aborted) return;
        const freshProviders = await api.mihomoRuntimeProviders().catch(() => null);

        const provKey = matchedMihomoSub.providerName;
        const rawProv = (provKey && freshProviders?.providers && typeof freshProviders.providers === 'object')
          ? (freshProviders.providers as Record<string, any>)[provKey]
          : null;
        if (rawProv && Array.isArray(rawProv.proxies) && rawProv.proxies.length > 0) {
          for (let i = 0; i < rawProv.proxies.length; i++) {
            const px = rawProv.proxies[i];
            const lastHist = px.history?.[px.history.length - 1];
            const d = (lastHist && typeof lastHist.delay === 'number' && lastHist.delay > 0) ? lastHist.delay : 0;
            if (i < servers.length) {
              nextDelays[servers[i].tag] = d;
              nextDelays[servers[i].label] = d;
            }
            for (const s of servers) {
              if (s.label === px.name || s.tag === px.name || s.server === px.name) {
                nextDelays[s.tag] = d;
                nextDelays[s.label] = d;
              }
            }
          }
        }
      } else if (matchedSingboxSub) {
        const targets = servers.slice(0, 30);
        await runWithConcurrency(targets, 3, async (s) => {
          if (signal.aborted) return;
          try {
            const d = await api.mihomoRuntimeDelay(s.tag, 'https://www.gstatic.com/generate_204', 2500);
            if (d > 0) {
              nextDelays[s.tag] = d;
              nextDelays[s.label] = d;
            } else {
              nextDelays[s.tag] = 0;
              nextDelays[s.label] = 0;
            }
          } catch {
            nextDelays[s.tag] = 0;
            nextDelays[s.label] = 0;
          }
          if (!signal.aborted) {
            localDelays = { ...nextDelays };
          }
        });
      }
      if (!signal.aborted) {
        localDelays = nextDelays;
      }
    } catch {}
    finally {
      testing = false;
    }
  }

  async function handleModeToggle(nextMode: 'auto' | 'manual') {
    if (nextMode === currentMode || switchingMode) return;
    switchingMode = true;
    currentMode = nextMode;
    notifications.success(
      nextMode === 'auto'
        ? `Автовыбор для «${subName}»`
        : `Ручной выбор для «${subName}»`
    );
    onUpdated?.();

    void (async () => {
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
        } else if (matchedSingboxSub) {
          const sbMode = nextMode === 'auto' ? 'urltest' : 'selector';
          await api.updateSubscription(matchedSingboxSub.id, { mode: sbMode });
        }
      } catch (e) {
        notifications.error(e instanceof Error ? e.message : 'Ошибка смены режима');
      } finally {
        switchingMode = false;
      }
    })();
  }

  async function handleSelectServer(server: DisplayServer) {
    if (selectingTag) return;
    selectingTag = server.tag;
    activeMemberTag = server.tag;
    notifications.success(`Выбран: ${server.label}`);
    onClose();
    onUpdated?.();

    void (async () => {
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
          const groupTarget = matchedMihomoSub.groupName || matchedMihomoSub.name;
          const proxyTarget = server.label || server.tag;
          await api.mihomoRuntimeSelect(groupTarget, proxyTarget);
        } else if (matchedSingboxSub) {
          const subTag = matchedSingboxSub.selectorTag || matchedSingboxSub.id;
          if (currentMode === 'auto') {
            await api.updateSubscription(matchedSingboxSub.id, { mode: 'selector' }).catch(() => {});
            currentMode = 'manual';
          }
          await Promise.allSettled([
            subTag ? api.mihomoRuntimeSelect(subTag, server.tag) : Promise.resolve(),
            api.setSubscriptionActiveMember(matchedSingboxSub.id, server.tag),
          ]);
        }
      } catch (e) {
        notifications.error(e instanceof Error ? e.message : 'Ошибка выбора сервера');
      } finally {
        selectingTag = null;
      }
    })();
  }

  function portal(node: HTMLElement) {
    document.body.appendChild(node);
    return {
      destroy() {
        if (node.parentNode) {
          node.parentNode.removeChild(node);
        }
      },
    };
  }
</script>

{#if open}
  <div
    use:portal
    class="sub-menu-backdrop"
    onclick={onClose}
    oncontextmenu={(e) => { e.preventDefault(); onClose(); }}
    role="presentation"
  ></div>

  <div
    use:portal
    bind:this={menuEl}
    class="sub-context-menu"
    class:menu-up={menuUp}
    style="top: {menuTop}px; left: {menuLeft}px;"
  >
    <!-- Header: Name & Mode switcher -->
    <div class="menu-header">
      <div class="menu-title-block">
        <div class="menu-title-row">
          <Layers size={13} class="title-icon" />
          <span class="sub-title" title={subName}>{subName}</span>
          <span class="engine-tag">{isMihomo ? 'Mihomo' : 'Sing-box'}</span>
        </div>
        <span class="server-count-text">
          {pluralize(servers.length, ['сервер', 'сервера', 'серверов'])}
        </span>
      </div>

      <button
        type="button"
        class="ping-test-btn"
        disabled={testing}
        onclick={updateDelays}
        title="Замерить задержки серверов"
      >
        <RefreshCw size={11} class={testing ? 'spin' : ''} />
      </button>
    </div>

    <!-- Mode switch row -->
    <div class="mode-toggle-strip">
      <span class="mode-lbl">Режим:</span>
      <div class="mini-toggle-group">
        <button
          type="button"
          class="toggle-opt"
          class:active={currentMode === 'auto'}
          disabled={switchingMode}
          onclick={() => handleModeToggle('auto')}
        >
          <Zap size={10} />
          <span>Автовыбор</span>
        </button>
        <button
          type="button"
          class="toggle-opt"
          class:active={currentMode === 'manual'}
          disabled={switchingMode}
          onclick={() => handleModeToggle('manual')}
        >
          <SlidersHorizontal size={10} />
          <span>Ручной</span>
        </button>
      </div>
    </div>

    <!-- Search filter if > 4 servers -->
    {#if servers.length > 4}
      <div class="search-box">
        <Search size={12} class="search-ic" />
        <input
          type="text"
          placeholder="Поиск по серверу..."
          bind:value={searchQuery}
          class="search-input"
        />
      </div>
    {/if}

    <!-- Servers List -->
    <div class="servers-scroll">
      {#if filteredServers.length === 0}
        <div class="empty-hint">Серверы не найдены</div>
      {:else}
        {#each filteredServers as s (s.tag)}
          {@const isActive = activeMemberTag === s.tag || activeMemberTag === s.label || activeMemberTag === s.server}
          {@const delay = getMemberDelay(s)}
          {@const tone = getDelayTone(delay)}

          <button
            type="button"
            class="server-item"
            class:active={isActive}
            onclick={() => handleSelectServer(s)}
          >
            <div class="radio-indicator">
              <span class="dot-ring" class:checked={isActive}>
                {#if isActive}
                  <span class="dot-core"></span>
                {/if}
              </span>
            </div>

            <div class="server-text-col">
              <div class="server-main-line">
                <span class="server-label" title={s.label}>{s.label}</span>
              </div>
              <div class="server-sub-line">
                {#if s.protocol}
                  <span class="proto-tag">{s.protocol}</span>
                {/if}
                <span class="server-host">{s.server}</span>
              </div>
            </div>

            <div class="delay-wrap">
              {#if delay !== null}
                <span class="delay-pill tone-{tone}">
                  {delay > 0 ? `${delay}ms` : 'fail'}
                </span>
              {:else}
                <span class="delay-pill tone-none">—</span>
              {/if}
            </div>
          </button>
        {/each}
      {/if}
    </div>
  </div>
{/if}

<style>
  .sub-menu-backdrop {
    position: fixed;
    inset: 0;
    z-index: 999;
    background: transparent;
    cursor: default;
  }

  .sub-context-menu {
    position: fixed;
    z-index: 1000;
    width: 310px;
    background: var(--color-bg-primary, #ffffff);
    border: 1px solid var(--color-border, #cbd5e1);
    border-radius: var(--radius-sm, 6px);
    box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.25), 0 8px 10px -6px rgba(0, 0, 0, 0.15);
    padding: 6px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-family: inherit;
    animation: context-menu-in 120ms ease-out;
  }

  :global(.dark) .sub-context-menu {
    background: #1e293b;
    border-color: #334155;
    box-shadow: 0 12px 30px -4px rgba(0, 0, 0, 0.6), 0 6px 12px -4px rgba(0, 0, 0, 0.4);
  }

  @keyframes context-menu-in {
    from { opacity: 0; transform: scale(0.97) translateY(-3px); }
    to { opacity: 1; transform: scale(1) translateY(0); }
  }

  .menu-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 4px 6px 6px;
    border-bottom: 1px solid var(--color-border, #e2e8f0);
  }

  :global(.dark) .menu-header {
    border-bottom-color: #334155;
  }

  .menu-title-block {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
    flex: 1;
  }

  .menu-title-row {
    display: flex;
    align-items: center;
    gap: 5px;
    min-width: 0;
  }

  :global(.title-icon) {
    color: var(--color-accent, #3b82f6);
    flex-shrink: 0;
  }

  .sub-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--color-text-primary, #0f172a);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  :global(.dark) .sub-title {
    color: #f8fafc;
  }

  .engine-tag {
    font-size: 10px;
    font-weight: 600;
    color: var(--color-text-muted, #64748b);
    background: var(--color-bg-secondary, #f1f5f9);
    padding: 1px 4px;
    border-radius: 3px;
    flex-shrink: 0;
  }

  :global(.dark) .engine-tag {
    background: #0f172a;
    color: #94a3b8;
  }

  .server-count-text {
    font-size: 11px;
    color: var(--color-text-muted, #64748b);
    padding-left: 18px;
  }

  .ping-test-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: 4px;
    border: 1px solid var(--color-border, #cbd5e1);
    background: var(--color-bg-secondary, #f8fafc);
    color: var(--color-text-primary, #334155);
    cursor: pointer;
    transition: all 0.15s ease;
    flex-shrink: 0;
  }

  :global(.dark) .ping-test-btn {
    background: #0f172a;
    border-color: #334155;
    color: #cbd5e1;
  }

  .ping-test-btn:hover:not(:disabled) {
    border-color: var(--color-accent, #3b82f6);
    color: var(--color-accent, #3b82f6);
  }

  .mode-toggle-strip {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 3px 6px;
    background: var(--color-bg-secondary, #f8fafc);
    border-radius: 4px;
    border: 1px solid var(--color-border, #e2e8f0);
  }

  :global(.dark) .mode-toggle-strip {
    background: #0f172a;
    border-color: #334155;
  }

  .mode-lbl {
    font-size: 11px;
    font-weight: 500;
    color: var(--color-text-muted, #64748b);
  }

  .mini-toggle-group {
    display: flex;
    align-items: center;
    background: var(--color-bg-primary, #ffffff);
    border: 1px solid var(--color-border, #cbd5e1);
    border-radius: 4px;
    padding: 1px;
    gap: 1px;
  }

  :global(.dark) .mini-toggle-group {
    background: #1e293b;
    border-color: #334155;
  }

  .toggle-opt {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 7px;
    font-size: 11px;
    font-weight: 500;
    border: none;
    border-radius: 3px;
    background: transparent;
    color: var(--color-text-muted, #64748b);
    cursor: pointer;
    transition: all 0.12s ease;
  }

  .toggle-opt:hover:not(:disabled) {
    color: var(--color-text-primary, #0f172a);
  }

  .toggle-opt.active {
    background: var(--color-accent, #3b82f6);
    color: #ffffff;
    font-weight: 600;
  }

  .search-box {
    position: relative;
    padding: 0 2px;
  }

  :global(.search-ic) {
    position: absolute;
    left: 9px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--color-text-muted, #94a3b8);
    pointer-events: none;
  }

  .search-input {
    width: 100%;
    height: 24px;
    padding: 2px 6px 2px 24px;
    font-size: 11px;
    border-radius: 4px;
    border: 1px solid var(--color-border, #cbd5e1);
    background: var(--color-bg-secondary, #f8fafc);
    color: var(--color-text-primary, #0f172a);
    outline: none;
    transition: border-color 0.12s ease;
  }

  :global(.dark) .search-input {
    background: #0f172a;
    border-color: #334155;
    color: #f8fafc;
  }

  .search-input:focus {
    border-color: var(--color-accent, #3b82f6);
  }

  .servers-scroll {
    max-height: 220px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 1px;
  }

  .server-item {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 4px 6px;
    border-radius: 4px;
    border: 1px solid transparent;
    background: transparent;
    color: var(--color-text-primary, #0f172a);
    cursor: pointer;
    text-align: left;
    transition: all 0.12s ease;
  }

  :global(.dark) .server-item {
    color: #f1f5f9;
  }

  .server-item:hover {
    background: var(--color-bg-hover, #f1f5f9);
  }

  :global(.dark) .server-item:hover {
    background: #334155;
  }

  .server-item.active {
    background: rgba(59, 130, 246, 0.08);
    border-color: rgba(59, 130, 246, 0.25);
  }

  :global(.dark) .server-item.active {
    background: rgba(59, 130, 246, 0.18);
    border-color: rgba(59, 130, 246, 0.4);
  }

  .radio-indicator {
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .dot-ring {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    border: 1.5px solid var(--color-border, #94a3b8);
    display: flex;
    align-items: center;
    justify-content: center;
    transition: border-color 0.12s ease;
  }

  .dot-ring.checked {
    border-color: var(--color-accent, #3b82f6);
  }

  .dot-core {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--color-accent, #3b82f6);
  }

  .server-text-col {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }

  .server-main-line {
    display: flex;
    align-items: center;
  }

  .server-label {
    font-size: 12px;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .server-item.active .server-label {
    font-weight: 600;
    color: var(--color-accent, #3b82f6);
  }

  .server-sub-line {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 10px;
    color: var(--color-text-muted, #64748b);
  }

  .proto-tag {
    font-weight: 600;
    font-size: 9px;
    background: var(--color-bg-secondary, #f1f5f9);
    padding: 0 3px;
    border-radius: 2px;
  }

  :global(.dark) .proto-tag {
    background: #0f172a;
    color: #94a3b8;
  }

  .server-host {
    font-family: var(--font-mono, monospace);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .delay-wrap {
    flex-shrink: 0;
  }

  .delay-pill {
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    font-weight: 500;
    font-variant-numeric: tabular-nums;
    line-height: 1;
    padding: 1px 4px;
    border-radius: 4px;
    background: transparent;
    border: 1px solid transparent;
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

  .empty-hint {
    padding: 16px;
    text-align: center;
    font-size: 11px;
    color: var(--color-text-muted, #64748b);
  }

  :global(.spin) {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
</style>
