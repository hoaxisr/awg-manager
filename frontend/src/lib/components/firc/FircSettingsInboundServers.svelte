<script lang="ts">
  import { m } from '$lib/i18n';
  import type { FircInboundServerItem } from '$lib/types/firc';
  import { ChevronDown, ChevronUp } from 'lucide-svelte';
  import { StatusDot, Button } from '$lib/components/ui';

  interface Props {
    /** Каталог от бэкенда; null — ещё не пришёл, показываем встроенный. */
    availableServers: FircInboundServerItem[] | null;
    enabledServers: string[];
    ontoggle: (id: string) => void;
  }

  let { availableServers, enabledServers, ontoggle }: Props = $props();

  let serverPickerOpen = $state(false);

  // Встроенный каталог — функция, а не константа: имена берутся из словаря
  // на языке текущего рендера.
  function fallbackCatalog(): FircInboundServerItem[] {
    return [
      { id: 'wdtt_server', name: m.firc_settings_srv_wdtt(), category: 'awgm', running: false, detail: 'opkgtun17/19 · 10.66.0.0/16' },
      { id: 'vless_xhttp_server', name: m.firc_settings_srv_vless_xhttp(), category: 'awgm', running: false, detail: 'router · XHTTP' },
      { id: 'openflux_server', name: m.firc_settings_srv_openflux(), category: 'awgm', running: false, detail: 'opkgtun · 10.77.0.0/16' },
      { id: 'keenetic_sstp', name: 'Keenetic SSTP', category: 'system', running: false, detail: 'sstp+ · 172.16.7.0/24' },
      { id: 'keenetic_l2tp', name: 'Keenetic L2TP/IPsec', category: 'system', running: false, detail: 'l2tp+ · 172.16.2.0/24' },
      { id: 'keenetic_ikev2', name: 'Keenetic IKEv2/IPsec', category: 'system', running: false, detail: 'xfrms2 · 172.20.8.0/24' },
      { id: 'keenetic_ikev1', name: 'Keenetic Virtual IP (IKEv1)', category: 'system', running: false, detail: 'xfrms1 · 172.20.0.0/24' },
      { id: 'keenetic_pptp', name: 'Keenetic PPTP', category: 'system', running: false, detail: 'vpn+ · 172.16.1.0/24' },
      { id: 'keenetic_oc', name: 'Keenetic OpenConnect', category: 'system', running: false, detail: 'oc+ · 172.16.3.0/24' },
    ];
  }

  const servers = $derived(availableServers ?? fallbackCatalog());

  function isInboundServerSelected(id: string): boolean {
    return enabledServers.includes(id);
  }

  function getServerById(id: string): FircInboundServerItem {
    const found = servers.find((s) => s.id === id);
    if (found) return found;
    if (id.startsWith('managed:')) {
      const ndmsName = id.slice('managed:'.length);
      return { id, name: m.firc_settings_srv_awg_managed({ name: ndmsName }), category: 'awgm', running: false, detail: '' };
    }
    if (id.startsWith('wg:')) {
      const ndmsName = id.slice('wg:'.length);
      return { id, name: `Keenetic WireGuard (${ndmsName})`, category: 'system', running: false, detail: '' };
    }
    return {
      id,
      name: id,
      category: 'system',
      running: false,
      detail: '',
    };
  }
</script>

<!-- Маршрутизация входных VPN-серверов через FIRC -->
<section class="sec">
  <div class="sec-cap">{m.firc_settings_srv_title()}</div>
  <span class="hint">
    {m.firc_settings_srv_hint()}
  </span>

  <div class="srv-picker-wrap">
    <div class="srv-chips-row">
      {#if enabledServers.length === 0}
        <span class="srv-empty-text">{m.firc_settings_srv_empty()}</span>
      {:else}
        {#each enabledServers as srvId (srvId)}
          {@const srv = getServerById(srvId)}
          <span class="srv-chip" title="{srv.name} ({srv.detail})">
            <StatusDot variant={srv.running ? 'success' : 'muted'} size="sm" />
            <span class="srv-chip-name">{srv.name}</span>
            {#if srv.detail}
              <span class="srv-chip-meta">{srv.detail.split('·')[0].trim()}</span>
            {/if}
            <!-- Крестик внутри чипа: IconButton (28×28) раздул бы чип, как и в FircGroupDevicesModal. -->
            <button
              type="button"
              class="srv-chip-remove"
              title={m.firc_settings_srv_remove()}
              onclick={() => ontoggle(srvId)}
            >
              ×
            </button>
          </span>
        {/each}
      {/if}

      <span class="srv-picker-slot">
        <Button variant="secondary" size="sm" onclick={() => (serverPickerOpen = !serverPickerOpen)}>
          {#snippet iconAfter()}
            {#if serverPickerOpen}
              <ChevronUp size={12} />
            {:else}
              <ChevronDown size={12} />
            {/if}
          {/snippet}
          {m.firc_settings_srv_pick({ count: enabledServers.length })}
        </Button>
      </span>
    </div>

    {#if serverPickerOpen}
      <div class="srv-popover">
        <div class="srv-popover-head">
          <span>{m.firc_settings_srv_available()}</span>
          <Button variant="ghost" size="sm" onclick={() => (serverPickerOpen = false)}>
            {m.common_done()}
          </Button>
        </div>

        <div class="srv-popover-group">{m.firc_settings_srv_group_awgm()}</div>
        {#each servers.filter((s) => s.category === 'awgm') as srv (srv.id)}
          {@const checked = isInboundServerSelected(srv.id)}
          <button
            type="button"
            class="srv-popover-item"
            class:selected={checked}
            title="{srv.name} — {srv.detail}"
            onclick={() => ontoggle(srv.id)}
          >
            <span class="srv-check" class:checked>{checked ? '✓' : ''}</span>
            <StatusDot variant={srv.running ? 'success' : 'muted'} size="sm" />
            <span class="srv-item-name">{srv.name}</span>
            <span class="srv-item-detail">{srv.detail.split('·')[0].trim()}</span>
          </button>
        {/each}

        <div class="srv-popover-group">{m.firc_settings_srv_group_system()}</div>
        {#each servers.filter((s) => s.category === 'system') as srv (srv.id)}
          {@const checked = isInboundServerSelected(srv.id)}
          <button
            type="button"
            class="srv-popover-item"
            class:selected={checked}
            title="{srv.name} — {srv.detail}"
            onclick={() => ontoggle(srv.id)}
          >
            <span class="srv-check" class:checked>{checked ? '✓' : ''}</span>
            <StatusDot variant={srv.running ? 'success' : 'muted'} size="sm" />
            <span class="srv-item-name">{srv.name}</span>
            <span class="srv-item-detail">{srv.detail.split('·')[0].trim()}</span>
          </button>
        {/each}
      </div>
    {/if}
  </div>
</section>

<style>
  .sec {
    padding: 14px 14px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-secondary);
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    overflow: hidden;
  }

  .sec-cap {
    font-size: 11px;
    font-weight: 700;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .hint {
    font-size: 11px;
    color: var(--text-secondary);
    line-height: 1.4;
  }

  .srv-picker-wrap {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-chips-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    padding: 8px 10px;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-empty-text {
    font-size: 12px;
    color: var(--text-tertiary);
    padding: 2px 4px;
  }

  .srv-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 8px;
    border-radius: 6px;
    background: color-mix(in srgb, var(--color-accent) 12%, var(--bg-secondary));
    border: 1px solid color-mix(in srgb, var(--color-accent) 32%, var(--border));
    font-size: 12px;
    color: var(--text-primary);
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-chip-name {
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  .srv-chip-meta {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--text-secondary);
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    flex-shrink: 0;
  }

  .srv-chip-remove {
    border: none;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    font-size: 13px;
    line-height: 1;
    padding: 0 2px;
    flex-shrink: 0;
  }

  .srv-chip-remove:hover {
    color: var(--color-danger);
  }

  .srv-picker-slot {
    margin-left: auto;
    max-width: 100%;
  }

  .srv-popover {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow);
    padding: 6px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 320px;
    overflow-y: auto;
    overflow-x: hidden;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-popover-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 4px 6px 6px;
    border-bottom: 1px solid var(--border);
    font-size: 11px;
    font-weight: 600;
    color: var(--text-secondary);
    min-width: 0;
  }

  .srv-popover-group {
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    padding: 7px 6px 2px;
  }

  .srv-popover-item {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    text-align: left;
    padding: 5px 7px;
    border-radius: 6px;
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-primary);
    font-size: 12px;
    cursor: pointer;
    transition: background 0.12s;
  }

  .srv-popover-item:hover {
    background: var(--bg-tertiary);
  }

  .srv-popover-item.selected {
    background: var(--color-accent-tint, color-mix(in srgb, var(--color-accent) 10%, var(--bg-tertiary)));
    border-color: var(--color-accent-border, color-mix(in srgb, var(--color-accent) 28%, var(--border)));
  }

  .srv-check {
    width: 15px;
    height: 15px;
    border-radius: 4px;
    border: 1px solid var(--border-hover, var(--border));
    background: var(--bg-tertiary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 10px;
    font-weight: 700;
    color: transparent;
    flex-shrink: 0;
  }

  .srv-check.checked {
    background: color-mix(in srgb, var(--color-accent) 18%, var(--bg-tertiary));
    border-color: var(--color-accent);
    color: var(--color-accent);
  }

  .srv-item-name {
    font-weight: 500;
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .srv-item-detail {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--text-muted);
    background: var(--bg-tertiary);
    padding: 1px 5px;
    border-radius: 4px;
    flex-shrink: 0;
    max-width: 92px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
