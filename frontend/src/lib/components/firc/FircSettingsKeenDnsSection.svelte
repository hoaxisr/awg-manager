<script lang="ts">
  import { m } from '$lib/i18n';
  import type { FircExtConfig, FircInterfaceOption } from '$lib/types/firc';
  import { fircTunnelsState } from '$lib/stores/firc';
  import { friendlyIfaceLabel, selectSelectableFircFallbackInterfaces } from '$lib/utils/fircFallback';
  import { Toggle, Button } from '$lib/components/ui';
  import { formatIfaceLabel } from './fircSettingsIface';

  interface Props {
    extConfig: FircExtConfig;
    keenDnsSyncError: string;
    interfaces: FircInterfaceOption[];
    onopenproxygroups: () => void;
  }

  let {
    extConfig = $bindable(),
    keenDnsSyncError = $bindable(),
    interfaces,
    onopenproxygroups,
  }: Props = $props();

  let selectableInterfaces = $derived(selectSelectableFircFallbackInterfaces(interfaces));

  function withPreservedSelectedIface(
    base: FircInterfaceOption[],
    all: FircInterfaceOption[],
    selectedId: string,
  ): FircInterfaceOption[] {
    if (!selectedId || base.some((o) => o.id === selectedId)) return base;
    const found = all.find((o) => o.id === selectedId) ?? { id: selectedId, role: 'tunnel' };
    return [...base, found];
  }

  let selectedGlobalIfaceId = $derived(
    extConfig.globalTarget?.startsWith('iface:') ? extConfig.globalTarget.slice(6) : '',
  );
  let globalInterfaces = $derived(
    withPreservedSelectedIface(selectableInterfaces, interfaces, selectedGlobalIfaceId),
  );
  let selectedKeenDnsIfaceId = $derived(
    !extConfig.keenDnsCloud.proxyGroupId ? (extConfig.keenDnsCloud.interfaces?.[0] ?? '') : '',
  );
  let keenDnsInterfaces = $derived(
    withPreservedSelectedIface(selectableInterfaces, interfaces, selectedKeenDnsIfaceId),
  );

  function keenDnsTargetSelectValue(): string {
    if (extConfig.keenDnsCloud.proxyGroupId) {
      return `pg:${extConfig.keenDnsCloud.proxyGroupId}`;
    }
    const firstIface = extConfig.keenDnsCloud.interfaces?.[0] ?? '';
    return firstIface ? `iface:${firstIface}` : '';
  }

  function handleKeenDnsTargetChange(val: string) {
    if (val.startsWith('pg:')) {
      const pgId = val.slice(3);
      const pg = extConfig.proxyGroups.find((x) => x.id === pgId);
      extConfig.keenDnsCloud.proxyGroupId = pgId;
      extConfig.keenDnsCloud.interfaces = pg ? [...pg.interfaces] : [];
    } else if (val.startsWith('iface:')) {
      const iface = val.slice(6);
      extConfig.keenDnsCloud.proxyGroupId = '';
      extConfig.keenDnsCloud.interfaces = iface ? [iface] : [];
    } else {
      extConfig.keenDnsCloud.proxyGroupId = '';
      extConfig.keenDnsCloud.interfaces = [];
    }
  }
</script>

<section class="sec">
  <label class="sec-cap" for="firc-global">{m.firc_global_routing_mode()}</label>
  <select id="firc-global" class="inp" bind:value={extConfig.globalTarget}>
    <option value="">{m.firc_global_mode_rules()}</option>
    {#each extConfig.proxyGroups as pg (pg.id)}
      <option value={'pg:' + pg.id}>Global · {pg.name}</option>
    {/each}
    {#each globalInterfaces as opt (opt.id)}
      <option value={'iface:' + opt.id}>Global · {formatIfaceLabel(opt)}</option>
    {/each}
  </select>
</section>

<!-- KeenDNS / Облако Keenetic через туннель / Fallback -->
<section class="sec">
  <div class="sec-cap">{m.firc_settings_keendns_title()}</div>
  <span class="hint">
    {m.firc_settings_keendns_hint()}
  </span>

  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_settings_keendns_enable_label()}</span>
      <span class="hint">
        {m.firc_settings_keendns_enable_hint()}
      </span>
    </div>
    <Toggle
      checked={extConfig.keenDnsCloud.enabled}
      onchange={(v) => {
        extConfig.keenDnsCloud.enabled = v;
        if (!v) keenDnsSyncError = '';
      }}
    />
  </div>
  {#if extConfig.keenDnsCloud.enabled && keenDnsSyncError}
    <div class="restart-banner" role="alert">
      <span>{m.firc_keendns_sync_error({ error: keenDnsSyncError })}</span>
    </div>
  {/if}

  <div class="field">
    <label class="lbl" for="firc-keendns-target">{m.firc_settings_keendns_target_label()}</label>
    <select
      id="firc-keendns-target"
      class="inp"
      value={keenDnsTargetSelectValue()}
      onchange={(e) => handleKeenDnsTargetChange((e.currentTarget as HTMLSelectElement).value)}
    >
      <option value="">{m.firc_settings_keendns_target_placeholder()}</option>
      {#if extConfig.proxyGroups.length > 0}
        <optgroup label={m.firc_fallback_panel_title()}>
          {#each extConfig.proxyGroups as pg (pg.id)}
            <option value="pg:{pg.id}">
              {pg.name} ({pg.interfaces.map((id) => friendlyIfaceLabel(id, interfaces, $fircTunnelsState)).join(' → ')})
            </option>
          {/each}
        </optgroup>
      {/if}
      {#if keenDnsInterfaces.length > 0}
        <optgroup label={m.firc_settings_keendns_target_ifaces()}>
          {#each keenDnsInterfaces as opt (opt.id)}
            <option value="iface:{opt.id}">{formatIfaceLabel(opt)}</option>
          {/each}
        </optgroup>
      {/if}
    </select>
  </div>
  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_keendns_cloud_check_label()}</span>
      <span class="hint">{m.firc_keendns_cloud_check_hint()}</span>
    </div>
    <Toggle
      checked={Boolean(extConfig.keenDnsCloud.cloudHealthCheck)}
      disabled={!extConfig.keenDnsCloud.enabled}
      onchange={(v) => {
        extConfig.keenDnsCloud.cloudHealthCheck = v;
      }}
    />
  </div>
  <div style="display: flex; justify-content: flex-start;">
    <Button variant="secondary" size="sm" onclick={onopenproxygroups}>
      {m.firc_settings_keendns_configure_groups({ count: extConfig.proxyGroups.length })}
    </Button>
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

  .restart-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border-radius: 8px;
    background: var(--color-warning-tint);
    border: 1px solid var(--color-warning-border);
    color: var(--text-primary);
    font-size: 12px;
    flex-wrap: wrap;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 0;
    max-width: 100%;
  }

  .field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid rgba(255, 255, 255, 0.04);
    min-width: 0;
    max-width: 100%;
  }

  .field-row-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    flex: 1 1 auto;
  }

  .lbl {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
  }

  .inp {
    padding: 7px 10px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-primary);
    font-size: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    transition: border-color 0.15s;
  }

  .inp:focus {
    outline: none;
    border-color: var(--accent);
  }

  .hint {
    font-size: 11px;
    color: var(--text-secondary);
    line-height: 1.4;
  }
</style>
