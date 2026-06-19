<!--
  Hero-баннер beginner-вида. Концепт «Весь роутер → sing-box → развилка»:
  выход По умолчанию (Напрямую) и Через туннель, DNS и провайдер — по каждой ветке.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { singboxStatus } from '$lib/stores/singbox';
  import { systemInfo } from '$lib/stores/system';
  import { openDrawer } from './drawerStore';
  import EngineFatalModal from './EngineFatalModal.svelte';
  import { openSourceDrawer } from './sourceDrawerStore';
  import { deriveRoutingSummary, resolveDefaultWanLabel } from './flowData';
  import { liveConnectionsTraffic } from './liveConnectionsStore';
  import { pluralize, RULE_WORDS, TUNNEL_WORDS, DEVICE_WORDS } from '$lib/utils/pluralize';
  import type { RouterPolicy, SingboxRouterWANInterface } from '$lib/types';

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;
  const rulesStore = singboxRouterStore.rules;
  const dnsServersStore = singboxRouterStore.dnsServers;
  const dnsGlobalsStore = singboxRouterStore.dnsGlobals;
  const options = singboxRouterStore.options;

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
  let engineOn = $derived(s?.enabled ?? false);
  // engineActive = интерцепция реально жива (цепочки + PREROUTING-jump'ы),
  // а не просто «включён в настройках». Узел светится только когда работает.
  let engineActive = $derived(engineOn && (s?.active ?? false));
  let engineFatalOpen = $state(false);
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
  });

  let singboxInstallStatus = $derived($singboxStatus.data);
  let singboxVersion = $derived((
    singboxInstallStatus?.version ?? singboxInstallStatus?.currentVersion ?? $systemInfo.data?.singbox?.version ?? ''
  ).trim());

  let summary = $derived(
    deriveRoutingSummary($rulesStore ?? [], routeFinal, $dnsServersStore ?? [], $dnsGlobalsStore, $options),
  );

  let currentPolicy = $derived(policies.find((p) => p.name === policyName));

  let sourceTitle = $derived(deviceMode === 'all' ? 'Весь роутер' : 'Устройства в политике');
  let sourceSub = $derived.by(() => {
    if (deviceMode === 'all') return 'весь LAN-трафик';
    if (!policyName) return 'политика не выбрана';
    const label = currentPolicy?.description?.trim() || policyName;
    const devices = s?.deviceCount ?? currentPolicy?.deviceCount ?? 0;
    return `${label} · ${pluralize(devices, DEVICE_WORDS)}`;
  });

  let engineSub = $derived.by(() => {
    if (!engineOn) return 'выключен';
    if (!engineActive) return 'не работает';
    const parts = ['first-match'];
    if (singboxVersion) parts.push(`v${singboxVersion}`);
    return parts.join(' · ');
  });

  let hasTunnel = $derived(summary.tunnels.length > 0);
  let tunnelTitle = $derived(
    summary.tunnels.length <= 1 ? (summary.tunnels[0] ?? '—') : pluralize(summary.tunnels.length, TUNNEL_WORDS),
  );

  let defaultWanLabel = $derived(
    resolveDefaultWanLabel($storeSettings, wanInterfaces, routeFinal),
  );

  let defaultRuleHint = $derived.by(() => {
    if (summary.bypassRuleCount > 0) return pluralize(summary.bypassRuleCount, RULE_WORDS);
    if (routeFinal === 'direct' && summary.tunneledRuleCount > 0) return 'остальной трафик';
    return null;
  });

  let trafficText = $derived($liveConnectionsTraffic);
</script>

<div class="flow">
  <div class="row">
    <button type="button" class="node source" onclick={openSourceDrawer} aria-label="Настроить источник трафика">
      <div class="cap">Источник</div>
      <div class="node-title">{sourceTitle}</div>
      <div class="node-sub">{sourceSub}</div>
    </button>

    <div class="arrow">›</div>

    <button type="button" class="node engine" class:glow={engineActive} class:offline={!engineActive} onclick={() => (engineFatal ? (engineFatalOpen = true) : openDrawer())} aria-label="Настройки движка sing-box">
      <div class="cap acc">Движок sing-box</div>
      <div class="node-title">{engineSub}</div>
      <div class="node-sub">
        {pluralize(rulesCount, RULE_WORDS)}
        {#if deviceMode === 'all'}
          {' · '}весь роутер
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
          <span class="out-prefix"><span class="mut">По умолчанию →</span></span>
          <span class="out-target" title={summary.defaultLabel}><b>{summary.defaultLabel}</b></span>
          {#if defaultRuleHint}
            <span class="out-hint mut">{' · '}{defaultRuleHint}</span>
          {/if}
        </div>
        <div class="dns">
          {#if defaultWanLabel}
            {defaultWanLabel}{' · '}DNS: {summary.defaultDnsLabel}
          {:else}
            DNS: {summary.defaultDnsLabel}
          {/if}
        </div>
      </div>
      {#if hasTunnel}
        <div class="out tun">
          <div class="out-line">
            <span class="dot"></span>
            <span class="out-prefix"><span class="mut">Через туннель →</span></span>
            <span class="out-target acc" title={tunnelTitle}>{tunnelTitle}</span>
            {#if summary.tunneledRuleCount > 0}
              <span class="out-hint mut">{' · '}{pluralize(summary.tunneledRuleCount, RULE_WORDS)}</span>
            {/if}
          </div>
          <div class="dns">
            {#if summary.tunnelDnsLabel}
              DNS: {summary.tunnelDnsLabel} (через туннель)
            {:else}
              DNS: через туннель
            {/if}
          </div>
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
  .dns { font-size: 11px; color: var(--text-muted); margin-top: 5px; padding-top: 5px; border-top: 1px dashed var(--border); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
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
