<!--
  Компактный индикатор живых соединений для шапки sb-router. WS открыт только
  когда движок работает. Клик → полный вид (?sub=connections).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { isMockDevMode } from '$lib/env';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { liveConnectionsSnapshot, liveConnectionsWsStatus } from './liveConnectionsStore';

  const status = singboxRouterStore.status;
  // Жив перехват, а не просто persisted-тумблер: без активных jump'ов живых
  // соединений через движок нет, чип скрываем.
  let engineOn = $derived(($status?.enabled ?? false) && ($status?.active ?? false));
  let isActive = $derived($page.url.searchParams.get('sub') === 'connections');

  let snapshot = $derived($liveConnectionsSnapshot);
  let wsStatus = $derived($liveConnectionsWsStatus);

  const count = $derived(snapshot.connectionsTotal);
  const countLabel = $derived(m.sb_router_live_count({ count }));
  let visible = $derived(engineOn || isMockDevMode());
  let isStale = $derived(wsStatus !== 'open');
  let stateTitle = $derived.by(() => {
    if (isActive) return m.sb_router_live_title_close();
    if (!engineOn && isMockDevMode()) return m.sb_router_live_title_open_mock();
    if (wsStatus === 'open') {
      return count > 0 ? m.sb_router_live_title_open() : m.sb_router_live_title_none();
    }
    if (wsStatus === 'connecting') return m.sb_router_live_title_connecting();
    if (wsStatus === 'closed') return m.sb_router_live_title_reconnecting();
    return m.sb_router_live_unavailable();
  });
  let stateAria = $derived.by(() => {
    if (isActive) return m.sb_router_live_aria_active({ count });
    if (wsStatus === 'open') return m.sb_router_live_aria_open({ count });
    return m.sb_router_live_unavailable();
  });

  function toggleConnections() {
    const url = new URL(window.location.href);
    if (isActive) {
      url.searchParams.delete('sub');
    } else {
      url.searchParams.set('tab', 'singbox');
      url.searchParams.set('sub', 'connections');
    }
    void goto(`${url.pathname}${url.search}`, { keepFocus: true, noScroll: true });
  }
</script>

{#if visible}
  <button
    type="button"
    class="live-chip"
    class:active={isActive}
    aria-pressed={isActive}
    onclick={toggleConnections}
    title={stateTitle}
    aria-label={stateAria}
  >
    <span class="dot" data-stale={isStale}></span>
    <span class="label">{countLabel}</span>
  </button>
{/if}

<style>
  /* Не .chip: имя занято утилитой Skeleton и app.css — их свойства протекали сюда. */
  .live-chip {
    display: inline-flex; align-items: center; gap: 6px;
    height: 32px;
    padding: 0 10px;
    box-sizing: border-box;
    border-radius: 999px;
    background: var(--color-bg-secondary, var(--bg-primary));
    border: 1px solid var(--color-border, var(--border));
    color: var(--color-text-secondary, var(--text-secondary));
    font-size: 12px;
    font-weight: 600;
    line-height: calc(1 / 0.75);
    font-family: inherit;
    cursor: pointer;
    white-space: nowrap;
    transition: background var(--t-fast) ease, color var(--t-fast) ease, border-color var(--t-fast) ease;
  }
  .live-chip:hover { border-color: var(--accent); color: var(--color-text-primary); }
  .live-chip.active {
    background: color-mix(in srgb, var(--color-success, #22c55e) 12%, var(--bg-primary));
    border-color: color-mix(in srgb, var(--color-success, #22c55e) 45%, var(--border));
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-success, #22c55e) 20%, transparent);
  }
  .live-chip.active:hover {
    border-color: var(--color-success, #22c55e);
  }
  .live-chip.active .label { color: var(--color-success, #22c55e); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-success, #22c55e); flex-shrink: 0; }
  .dot[data-stale='true'] { background: var(--color-warning, #dab856); }
  .label { color: var(--text-primary); font-weight: 500; }
</style>
