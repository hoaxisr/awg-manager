<!--
  Компактный индикатор живых соединений для шапки sb-router. WS открыт только
  когда движок работает. Клик → полный вид (?sub=connections).
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import type { ClashConnectionsRaw, ConnectionsSnapshot } from '$lib/types/singboxConnections';
  import { parseSnapshot } from '$lib/utils/singboxConnections';
  import { createClashWS, type WSStatus } from '$lib/utils/clashWebSocket';
  import { formatBytes } from '$lib/utils/format';

  const status = singboxRouterStore.status;
  let engineOn = $derived($status?.enabled ?? false);

  const EMPTY: ConnectionsSnapshot = { connections: [], downloadTotal: 0, uploadTotal: 0, connectionsTotal: 0 };
  const EMPTY_CLIENTS = new Map<string, string>();
  let snapshot = $state<ConnectionsSnapshot>(EMPTY);
  let wsStatus = $state<WSStatus>('connecting');
  let wsClose: (() => void) | null = null;

  const totalUp = $derived(snapshot.connections.reduce((s, c) => s + c.upload, 0));
  const totalDown = $derived(snapshot.connections.reduce((s, c) => s + c.download, 0));
  const count = $derived(snapshot.connectionsTotal);
  let visible = $derived(engineOn);
  let isStale = $derived(wsStatus !== 'open');
  let stateTitle = $derived.by(() => {
    if (wsStatus === 'open') {
      return count > 0 ? 'Открыть живые соединения' : 'Живые соединения: активных подключений нет';
    }
    if (wsStatus === 'connecting') return 'Живые соединения: подключение…';
    if (wsStatus === 'closed') return 'Живые соединения: переподключение…';
    return 'Живые соединения недоступны';
  });
  let stateAria = $derived.by(() => {
    if (wsStatus === 'open') return `Живые соединения: ${count}`;
    return 'Живые соединения недоступны';
  });
  let trafficText = $derived.by(() => {
    if (wsStatus !== 'open') return '—';
    if (count === 0) return '';
    return `↑ ${formatBytes(totalUp)} ↓ ${formatBytes(totalDown)}`;
  });

  $effect(() => {
    if (engineOn && !wsClose) {
      wsClose = createClashWS<ClashConnectionsRaw>(
        '/api/singbox/clash/connections',
        (raw) => { snapshot = parseSnapshot(raw, EMPTY_CLIENTS); },
        (s) => { wsStatus = s; },
      );
    } else if (!engineOn && wsClose) {
      wsClose();
      wsClose = null;
      snapshot = EMPTY;
    }
  });
  onDestroy(() => { wsClose?.(); });

  function openFull() {
    void goto('/routing?tab=singbox&sub=connections');
  }
</script>

{#if visible}
  <button type="button" class="chip" onclick={openFull} title={stateTitle} aria-label={stateAria}>
    <span class="dot" data-stale={isStale}></span>
    <span class="n">{count}</span> conn
    <span class="b">{trafficText}</span>
  </button>
{/if}

<style>
  .chip {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 5px 10px; border-radius: 999px;
    background: var(--bg-primary); border: 1px solid var(--border);
    color: var(--text-secondary); font-size: 12px; font-family: inherit; cursor: pointer; white-space: nowrap;
  }
  .chip:hover { border-color: var(--accent); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-success, #22c55e); flex-shrink: 0; }
  .dot[data-stale='true'] { background: var(--color-warning, #dab856); }
  .n { font-family: var(--font-mono); font-weight: 600; color: var(--text-primary); }
  .b { font-family: var(--font-mono); color: var(--text-muted); margin-left: 2px; }
</style>
