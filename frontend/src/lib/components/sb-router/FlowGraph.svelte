<!--
  Hero-баннер beginner-вида. Концепт «Весь роутер → sing-box → развилка»:
  выход По умолчанию (Напрямую) и Через туннель, DNS и провайдер — по каждой ветке.
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { singboxStatus } from '$lib/stores/singbox';
  import { systemInfo } from '$lib/stores/system';
  import { openDrawer } from './drawerStore';
  import EngineFatalModal from './EngineFatalModal.svelte';
  import SimpleDnsPickerModal from './SimpleDnsPickerModal.svelte';
  import { openSourceDrawer } from './sourceDrawerStore';
  import { deriveRoutingSummary, resolveDefaultWanLabel } from './flowData';
  import { liveConnectionsTraffic } from './liveConnectionsStore';
  import type { RouterPolicy, SingboxRouterWANInterface, SingboxRouterDNSServer } from '$lib/types';

  interface Props {
    isMihomo?: boolean;
    mihomoRulesCount?: number;
    mihomoGroupsCount?: number;
    mihomoTopGroup?: string;
    mihomoOutbounds?: string[];
  }
  let {
    isMihomo = false,
    mihomoRulesCount = 0,
    mihomoGroupsCount = 0,
    mihomoTopGroup = '',
    mihomoOutbounds = [],
  }: Props = $props();

  let dnsPickerTag = $state<string | null>(null);

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;
  const rulesStore = singboxRouterStore.rules;
  const dnsServersStore = singboxRouterStore.dnsServers;
  const dnsGlobalsStore = singboxRouterStore.dnsGlobals;
  const options = singboxRouterStore.options;

  let engineIsMihomo = $derived(isMihomo || $storeSettings?.routingEngine === 'mihomo');

  let policies = $state<RouterPolicy[]>([]);
  let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);

  async function loadPolicies() {
    try {
      policies = await api.singboxRouterListPolicies();
    } catch {
      policies = [];
    }
  }

  async function loadWanInterfaces() {
    try {
      wanInterfaces = await api.singboxRouterListWANInterfaces();
    } catch {
      wanInterfaces = [];
    }
  }

  let s = $derived($status);
  let engineOn = $derived(s?.enabled ?? true);
  // engineActive = интерцепция реально жива (цепочки + PREROUTING-jump'ы),
  // а не просто «включён в настройках». Узел светится только когда работает.
  let engineActive = $derived(
    engineOn && (Boolean(s?.active) || (Boolean(engineIsMihomo) && ((mihomoRulesCount > 0) || Boolean(s?.ruleCount))))
  );
  let engineFatalOpen = $state(false);
  let activeDnsServer = $derived(
    dnsPickerTag ? (($dnsServersStore ?? []).find((srv) => srv.tag === dnsPickerTag) ?? null) : null,
  );
  // СБОЙ с захваченной причиной → клик по узлу открывает модалку с ошибкой,
  // иначе — обычные настройки движка (StatusDrawer).
  const engineFatal = $derived(engineOn && !engineActive && !!s?.lastError);
  let rulesCount = $derived(s?.ruleCount ?? 0);
  let deviceMode = $derived(s?.deviceMode);
  let routeFinal = $derived(s?.final ?? 'direct');
  let policyName = $derived((s?.policyName ?? '').trim());

  onMount(() => {
    void loadPolicies();
    void loadWanInterfaces();
    void singboxRouterStore.reloadStatus?.();
    void singboxRouterStore.reloadSettings?.();
  });

  let singboxInstallStatus = $derived($singboxStatus.data);
  let singboxVersion = $derived((
    singboxInstallStatus?.version ?? singboxInstallStatus?.currentVersion ?? $systemInfo.data?.singbox?.version ?? ''
  ).trim());

  let summary = $derived(
    deriveRoutingSummary($rulesStore ?? [], routeFinal, $dnsServersStore ?? [], $dnsGlobalsStore, $options),
  );

  let effectiveRulesCount = $derived(engineIsMihomo ? (mihomoRulesCount || s?.ruleCount || 0) : (s?.ruleCount ?? 0));
  let engineDisplayName = $derived(engineIsMihomo ? 'Mihomo' : 'sing-box');

  let currentPolicy = $derived(policies.find((p) => p.name === policyName));

  // В «Политики + tun» deviceMode мёртв (бэкенд ветвится раньше — см.
  // service_lifecycle.go: enable/reconcile уходят в policy-tun до его чтения),
  // а захват задаёт членство в политике. Поэтому узел не зовёт SourceDrawer с
  // его выбором «весь роутер», а ведёт в карточку режима (StatusDrawer).
  let policyTunMode = $derived($storeSettings?.routingMode === 'policy-tun');
  let sourceTitle = $derived(
    policyTunMode
      ? m.sb_router_flow_source_policy()
      : deviceMode === 'all'
        ? m.sb_router_flow_source_all()
        : m.sb_router_flow_source_devices(),
  );
  let sourceSub = $derived.by(() => {
    if (!policyTunMode && deviceMode === 'all') return m.sb_router_flow_sub_all_lan();
    if (!policyName) return m.sb_router_flow_sub_no_policy();
    const label = currentPolicy?.description?.trim() || policyName;
    const devices = s?.deviceCount ?? currentPolicy?.deviceCount ?? 0;
    return m.sb_router_flow_policy_devices({ label, devices: m.sb_router_devices_count({ count: devices }) });
  });

  let engineSub = $derived.by(() => {
    if (!engineOn) return m.sb_router_flow_engine_off();
    if (!engineActive) return m.sb_router_flow_engine_down();
    const parts = ['first-match'];
    if (!engineIsMihomo && singboxVersion) parts.push(`v${singboxVersion}`);
    return parts.join(' · ');
  });

  let hasTunnel = $derived(
    engineIsMihomo
      ? (mihomoOutbounds.length > 0 || mihomoGroupsCount > 0 || !!mihomoTopGroup)
      : summary.tunnels.length > 0,
  );
  let tunnelTitle = $derived.by(() => {
    if (engineIsMihomo) {
      if (mihomoOutbounds.length === 1) return mihomoOutbounds[0];
      if (mihomoOutbounds.length > 1) {
        return m.sb_router_flow_tunnels_count({ count: mihomoOutbounds.length });
      }
      return mihomoTopGroup || (mihomoGroupsCount > 1 ? m.tunnels_tab_proxy_groups() : 'Proxy Group');
    }
    return summary.tunnels.length <= 1
      ? (summary.tunnels[0] ?? '—')
      : m.sb_router_flow_tunnels_count({ count: summary.tunnels.length });
  });
  let tunnelTooltip = $derived.by(() => {
    if (engineIsMihomo && mihomoOutbounds.length > 1) {
      return mihomoOutbounds.join(', ');
    }
    return tunnelTitle;
  });

  let defaultWanLabel = $derived(
    resolveDefaultWanLabel($storeSettings, wanInterfaces, routeFinal),
  );

  let defaultRuleHint = $derived.by(() => {
    if (engineIsMihomo) return m.sb_router_flow_rest_traffic();
    if (summary.bypassRuleCount > 0) return m.routing_rules_count({ count: summary.bypassRuleCount });
    if (routeFinal === 'direct' && summary.tunneledRuleCount > 0) return m.sb_router_flow_rest_traffic();
    return null;
  });

  let trafficText = $derived($liveConnectionsTraffic);
</script>

{#snippet dnsLine(text: string, tag: string | null)}
  {#if tag}
    <button
      type="button"
      class="dns-btn"
      title={m.sb_router_flow_dns_change()}
      onclick={() => (dnsPickerTag = tag)}
    >{text}</button>
  {:else}
    <div class="dns">{text}</div>
  {/if}
{/snippet}

<div class="flow">
  <div class="row">
    <button
      type="button"
      class="node source"
      onclick={policyTunMode ? openDrawer : openSourceDrawer}
      aria-label={policyTunMode ? m.sb_router_flow_source_aria_policy() : m.sb_router_flow_source_aria()}
    >
      <div class="cap">{m.sb_router_flow_cap_source()}</div>
      <div class="node-title">{sourceTitle}</div>
      <div class="node-sub">{sourceSub}</div>
    </button>

    <div class="arrow">›</div>

    <button type="button" class="node engine" class:glow={engineActive} class:offline={!engineActive} onclick={() => (engineFatal ? (engineFatalOpen = true) : openDrawer())} aria-label={m.sb_router_flow_engine_aria()}>
      <div class="cap acc">{m.sb_router_flow_cap_engine()} {engineDisplayName}</div>
      <div class="node-title">{engineSub}</div>
      <div class="node-sub">
        {m.routing_rules_count({ count: effectiveRulesCount })}
        {#if !policyTunMode && deviceMode === 'all'}
          {' · '}{m.sb_router_flow_whole_router()}
        {/if}
        {#if trafficText}
          {' · '}<span class="traffic">{trafficText}</span>
        {/if}
      </div>
    </button>

    <div class="arrow">›</div>

    <div class="branch">
      <div class="out">
        <div class="out-line">
          <span class="dot muted"></span>
          <span class="out-prefix"><span class="mut">{m.sb_router_flow_default_prefix()}</span></span>
          <span class="out-target" title={engineIsMihomo ? 'DIRECT' : summary.defaultLabel}><b>{engineIsMihomo ? m.sb_router_flow_default_direct() : summary.defaultLabel}</b></span>
          {#if defaultRuleHint}
            <span class="out-hint mut">{' · '}{defaultRuleHint}</span>
          {/if}
        </div>
        {@render dnsLine(
          defaultWanLabel
            ? m.sb_router_flow_dns_wan({ wan: defaultWanLabel, dns: summary.defaultDnsLabel })
            : m.sb_router_flow_dns({ dns: summary.defaultDnsLabel }),
          summary.defaultDnsTag,
        )}
      </div>
      {#if hasTunnel}
        <div class="out tun">
          <div class="out-line">
            <span class="dot"></span>
            <span class="out-prefix"><span class="mut">{m.sb_router_flow_tunnel_prefix()}</span></span>
            <span class="out-target acc" title={tunnelTooltip}>{tunnelTitle}</span>
            {#if (engineIsMihomo ? effectiveRulesCount : summary.tunneledRuleCount) > 0}
              <span class="out-hint mut">{' · '}{m.routing_rules_count({ count: engineIsMihomo ? effectiveRulesCount : summary.tunneledRuleCount })}</span>
            {/if}
          </div>
          {@render dnsLine(
            summary.tunnelDnsLabel
              ? m.sb_router_flow_dns_tunnel_named({ dns: summary.tunnelDnsLabel })
              : m.sb_router_flow_dns_tunnel(),
            summary.tunnelDnsTag,
          )}
        </div>
      {/if}
    </div>
  </div>
</div>

<EngineFatalModal
  open={engineFatalOpen}
  lastError={s?.lastError ?? ''}
  onclose={() => (engineFatalOpen = false)}
/>

{#if activeDnsServer}
  <SimpleDnsPickerModal
    server={activeDnsServer}
    allowProtocol={true}
    onclose={() => (dnsPickerTag = null)}
    onsaved={() => (dnsPickerTag = null)}
  />
{/if}

<style>
  .flow {
    position: relative;
    padding: 20px 24px;
    background: linear-gradient(180deg, color-mix(in srgb, var(--accent) 5%, var(--bg-secondary)) 0%, var(--bg-secondary) 100%);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 0.9fr) auto minmax(0, 1.1fr) auto minmax(0, 1.6fr);
    align-items: center;
    gap: 14px;
    min-width: 0;
  }
  .node {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 14px;
    text-align: left;
    min-width: 0;
  }
  button.node { font-family: inherit; color: inherit; cursor: pointer; width: 100%; }
  button.node:hover {
    border-color: var(--border-hover, var(--accent-line));
    background: color-mix(in srgb, var(--accent) 4%, var(--bg-primary));
  }
  .node.engine { border-color: var(--accent-line); }
  .node.engine.glow { box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 25%, transparent); }
  .node.engine.offline {
    border-color: var(--color-error, #dc2626);
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--color-error, #dc2626) 20%, transparent);
  }
  .node.engine.offline .cap.acc { color: var(--color-error, #dc2626); }
  .node.engine.offline:hover {
    border-color: var(--color-error, #dc2626);
    background: color-mix(in srgb, var(--color-error, #dc2626) 4%, var(--bg-primary));
  }
  .cap { font-size: 10px; letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-muted); }
  .cap.acc { color: var(--accent); font-weight: 600; }
  .node-title { margin-top: 4px; font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .node-sub { font-size: 11px; color: var(--text-muted); margin-top: 2px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .traffic { font-family: var(--font-mono); font-size: 10.5px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
  .arrow { color: var(--text-muted); font-size: 18px; text-align: center; flex-shrink: 0; }
  .branch { display: flex; flex-direction: column; gap: 7px; min-width: 0; overflow: hidden; }
  .out { padding: 9px 12px; border-radius: 8px; background: var(--bg-primary); border: 1px solid var(--border); min-width: 0; overflow: hidden; }
  .out.tun { border-color: var(--accent-line); }
  .out-line {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    min-width: 0;
    font-size: 13px;
  }
  .out-prefix,
  .out-hint,
  .dot {
    flex-shrink: 0;
  }
  .out-target {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .out-target b {
    font-weight: 600;
  }
  .dns { font-size: 11px; color: var(--text-muted); margin-top: 6px; padding-top: 5px; border-top: 1px dashed var(--border); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dns-btn {
    display: block;
    width: 100%;
    margin-top: 6px;
    padding: 5px 0 0;
    border: 0;
    border-top: 1px dashed var(--border);
    background: transparent;
    color: var(--text-muted);
    font-family: inherit;
    font-size: 11px;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }
  .dns-btn:hover {
    color: var(--text-primary);
  }
  .out-sub-row {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 6px;
    padding-top: 5px;
    border-top: 1px dashed var(--border);
    font-size: 11px;
    color: var(--text-muted);
    min-width: 0;
    overflow: hidden;
  }
  .wan-link-btn,
  .dns-link-btn {
    background: transparent;
    border: none;
    padding: 0;
    font-family: inherit;
    font-size: 11px;
    color: var(--text-muted);
    cursor: pointer;
    white-space: nowrap;
    text-overflow: ellipsis;
    overflow: hidden;
    text-decoration: underline;
    text-decoration-style: dashed;
    text-underline-offset: 2px;
  }
  .wan-link-btn {
    flex-shrink: 1;
  }
  .wan-link-btn:hover,
  .dns-link-btn:hover {
    color: var(--accent);
  }
  .dns-text {
    white-space: nowrap;
    text-overflow: ellipsis;
    overflow: hidden;
    flex-shrink: 0;
  }
  .sub-sep {
    color: var(--text-muted);
    opacity: 0.6;
    flex-shrink: 0;
  }
  .dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: var(--accent); margin-right: 6px; vertical-align: middle; }
  .dot.muted { background: var(--text-muted); }
  .acc { color: var(--accent); font-weight: 600; }
  .mut { color: var(--text-muted); }

  @media (max-width: 768px) {
    .flow { padding: 14px 16px; }
    .row { display: flex; flex-direction: column; align-items: stretch; gap: 8px; }
    .arrow { transform: rotate(90deg); align-self: center; }
    .node, .branch { width: 100%; box-sizing: border-box; }
  }
</style>
