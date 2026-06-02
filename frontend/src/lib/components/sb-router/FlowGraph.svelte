<!--
  Hero-баннер beginner-вида. Концепт «Весь роутер → sing-box → развилка»:
  выход по умолчанию (Напрямую) и туннельный выход, DNS показан по каждой ветке.
-->
<script lang="ts">
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { singboxStatus } from '$lib/stores/singbox';
  import { systemInfo } from '$lib/stores/system';
  import { openDrawer } from './drawerStore';
  import { deriveRoutingSummary } from './flowData';

  const status = singboxRouterStore.status;
  const rulesStore = singboxRouterStore.rules;
  const dnsServersStore = singboxRouterStore.dnsServers;
  const dnsGlobalsStore = singboxRouterStore.dnsGlobals;
  const options = singboxRouterStore.options;

  let s = $derived($status);
  let engineOn = $derived(s?.enabled ?? false);
  let rulesCount = $derived(s?.ruleCount ?? 0);
  let deviceMode = $derived(s?.deviceMode);
  let routeFinal = $derived(s?.final ?? 'direct');
  let policyName = $derived(s?.policyName || '—');

  let singboxInstallStatus = $derived($singboxStatus.data);
  let singboxVersion = $derived((
    singboxInstallStatus?.version ?? singboxInstallStatus?.currentVersion ?? $systemInfo.data?.singbox?.version ?? ''
  ).trim());

  let summary = $derived(
    deriveRoutingSummary($rulesStore ?? [], routeFinal, $dnsServersStore ?? [], $dnsGlobalsStore, $options),
  );

  let sourceTitle = $derived(deviceMode === 'all' ? 'Весь роутер' : 'Устройства');
  let sourceSub = $derived(deviceMode === 'all' ? 'все устройства' : `policy: ${policyName}`);
  let engineSub = $derived.by(() => {
    if (!engineOn) return 'выключен';
    const parts = ['first-match'];
    if (singboxVersion) parts.push(`v${singboxVersion}`);
    return parts.join(' · ');
  });
  let hasTunnel = $derived(summary.tunnels.length > 0);
  let tunnelTitle = $derived(
    summary.tunnels.length <= 1 ? (summary.tunnels[0] ?? '—') : `${summary.tunnels.length} туннелей`,
  );
  function pluralSvc(n: number): string {
    if (n === 1) return 'сервис';
    if (n >= 2 && n <= 4) return 'сервиса';
    return 'сервисов';
  }
</script>

<div class="flow">
  <div class="row">
    <div class="node">
      <div class="cap">Источник</div>
      <div class="node-title">{sourceTitle}</div>
      <div class="node-sub">{sourceSub}</div>
    </div>

    <div class="arrow">›</div>

    <button type="button" class="node engine" class:glow={engineOn} onclick={openDrawer}>
      <div class="cap acc">sing-box</div>
      <div class="node-sub light">{engineSub}</div>
      <div class="node-sub">{rulesCount} правил{deviceMode === 'all' ? ' · весь роутер' : ''}</div>
    </button>

    <div class="arrow">›</div>

    <div class="branch">
      <div class="out">
        <div class="out-line"><span class="dot muted"></span><span class="mut">по умолчанию →</span> <b>{summary.defaultLabel}</b></div>
        <div class="dns">DNS: {summary.defaultDnsLabel}</div>
      </div>
      {#if hasTunnel}
        <div class="out tun">
          <div class="out-line"><span class="dot"></span>через туннель → <span class="acc">{tunnelTitle}</span> <span class="mut">· {summary.tunneledRuleCount} {pluralSvc(summary.tunneledRuleCount)}</span></div>
          <div class="dns">DNS: {summary.tunnelDnsLabel ? `через туннель · ${summary.tunnelDnsLabel}` : 'через туннель'}</div>
        </div>
      {/if}
    </div>
  </div>
</div>

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
    grid-template-columns: 0.9fr auto 1.1fr auto 1.6fr;
    align-items: center;
    gap: 14px;
  }
  .node {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 14px;
    text-align: left;
  }
  button.node { font-family: inherit; color: inherit; cursor: pointer; width: 100%; }
  .node.engine { border-color: var(--accent-line); }
  .node.engine.glow { box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 25%, transparent); }
  .cap { font-size: 10px; letter-spacing: 0.06em; text-transform: uppercase; color: var(--text-muted); }
  .cap.acc { color: var(--accent); font-weight: 600; }
  .node-title { margin-top: 4px; font-weight: 600; }
  .node-sub { font-size: 11px; color: var(--text-muted); margin-top: 2px; }
  .node-sub.light { color: var(--text-secondary); font-size: 12px; }
  .arrow { color: var(--text-muted); font-size: 18px; text-align: center; }
  .branch { display: flex; flex-direction: column; gap: 7px; }
  .out { padding: 9px 12px; border-radius: 8px; background: var(--bg-primary); border: 1px solid var(--border); }
  .out.tun { border-color: var(--accent-line); }
  .out-line { font-size: 13px; }
  .dns { font-size: 11px; color: var(--text-muted); margin-top: 5px; padding-top: 5px; border-top: 1px dashed var(--border); }
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
