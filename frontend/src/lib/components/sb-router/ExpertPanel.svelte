<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (MainExpertScreen)
  Главная композиция Expert вида (полный набор: правила, rule-sets, outbounds, DNS, движок, прокси).

  Адаптации от шаблона:
  - onSaved → onSave (реальный prop у всех 5 модалов)
  - Все модалы требуют outboundOptions: OutboundGroup[] — берём из store.options
  - RuleEditModal требует availableRuleSets + ruleSetUsage (excludeIndex для edit)
  - DNSServerEditModal требует servers: SingboxRouterDNSServer[]
  - DNSRuleEditModal требует servers + availableRuleSets + ruleSetUsage
  - DNS данные берём из store (dnsServers/dnsRules), не грузим отдельно
  - RuleSetAddModal поддерживает edit-mode через prop ruleSet (необязательный)
  - CompositeOutboundEditModal edit-mode через prop outbound (необязательный)
-->

<script lang="ts">
  import { onMount } from 'svelte';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { notifications } from '$lib/stores/notifications';
  import { api } from '$lib/api/client';
  import { computeRuleSetUsage } from '$lib/components/routing/singboxRouter';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import type {
    SingboxRouterRule,
    SingboxRouterRuleSet,
    SingboxRouterOutbound,
    SingboxRouterDNSServer,
    SingboxRouterDNSRule,
    SingboxRouterDNSStrategy,
    DeviceProxyInstance,
  } from '$lib/types';
  import { newDeviceProxyInstance } from '$lib/utils/deviceProxyInstance';

  import StatStrip, { type StatCellData } from './StatStrip.svelte';
  import SidePanel from './SidePanel.svelte';
  import RoutingTable from './RoutingTable.svelte';
  import RuleSetsTable from './RuleSetsTable.svelte';
  import OutboundsCompact from './OutboundsCompact.svelte';
  import DnsServersCompact from './DnsServersCompact.svelte';
  import DeviceProxyCompact from './DeviceProxyCompact.svelte';
  import InboundSettingsDrawer from './InboundSettingsDrawer.svelte';

  import RuleEditModal from '$lib/components/routing/singboxRouter/RuleEditModal.svelte';
  import RuleSetAddModal from '$lib/components/routing/singboxRouter/RuleSetAddModal.svelte';
  import CompositeOutboundEditModal from '$lib/components/routing/singboxRouter/CompositeOutboundEditModal.svelte';
  import DNSServerEditModal from '$lib/components/routing/singboxRouter/DNSServerEditModal.svelte';
  import DNSRuleEditModal from '$lib/components/routing/singboxRouter/DNSRuleEditModal.svelte';
  import { DNSRewritesList } from '$lib/components/routing/singboxRouter';
  import { ConfirmModal, Dropdown, type DropdownOption } from '$lib/components/ui';

  // Store subscriptions
  const storeStatus = singboxRouterStore.status;
  const storeRules = singboxRouterStore.rules;
  const storeRuleSets = singboxRouterStore.ruleSets;
  const storeOutbounds = singboxRouterStore.outbounds;
  const storeDnsServers = singboxRouterStore.dnsServers;
  const storeDnsRules = singboxRouterStore.dnsRules;
  const storeDnsRewrites = singboxRouterStore.dnsRewrites;
  const storeDnsGlobals = singboxRouterStore.dnsGlobals;
  const storeOptions = singboxRouterStore.options;

  // ── Globals (route-final + DNS final/strategy) ──────────────────────
  const STRATEGY_OPTIONS: DropdownOption<SingboxRouterDNSStrategy>[] = [
    { value: '', label: '— default —' },
    { value: 'ipv4_only', label: 'ipv4_only' },
    { value: 'ipv6_only', label: 'ipv6_only' },
    { value: 'prefer_ipv4', label: 'prefer_ipv4' },
    { value: 'prefer_ipv6', label: 'prefer_ipv6' },
  ];

  // route-final: direct + все outbounds, кроме группы «Специальные»
  const routeFinalOptions = $derived<DropdownOption[]>([
    { value: 'direct', label: 'direct (мимо VPN)' },
    ...$storeOptions
      .filter((g) => g.group !== 'Специальные')
      .flatMap((g) => g.items.map((i) => ({ value: i.value, label: i.label, group: g.group }))),
  ]);

  // DNS-final: серверы из стора
  const dnsFinalOptions = $derived<DropdownOption[]>([
    { value: '', label: '— не задан —' },
    ...$storeDnsServers.map((s) => ({ value: s.tag, label: s.tag })),
  ]);

  let draftRouteFinal = $state('direct');
  let draftDnsFinal = $state('');
  let draftDnsStrategy = $state<SingboxRouterDNSStrategy>('');
  let routeFinalBusy = $state(false);
  let dnsGlobalsBusy = $state(false);

  // draft синхронизируется со стором (как в исходных DNSGlobals/RouteGlobals)
  $effect(() => {
    draftRouteFinal = $storeStatus?.final || 'direct';
  });
  $effect(() => {
    draftDnsFinal = $storeDnsGlobals.final;
    draftDnsStrategy = $storeDnsGlobals.strategy;
  });

  const routeFinalDirty = $derived(draftRouteFinal !== ($storeStatus?.final || 'direct'));
  const dnsGlobalsDirty = $derived(
    draftDnsFinal !== $storeDnsGlobals.final || draftDnsStrategy !== $storeDnsGlobals.strategy,
  );

  async function saveRouteFinal() {
    if (!routeFinalDirty || routeFinalBusy) return;
    routeFinalBusy = true;
    try {
      await api.singboxRouterPutRouteFinal(draftRouteFinal);
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      routeFinalBusy = false;
    }
  }

  async function saveDnsGlobals() {
    if (!dnsGlobalsDirty || dnsGlobalsBusy) return;
    dnsGlobalsBusy = true;
    try {
      await api.singboxRouterPutDNSGlobals({ final: draftDnsFinal, strategy: draftDnsStrategy });
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      dnsGlobalsBusy = false;
    }
  }

  let activeProxyCount = $state<number | null>(null);

  async function loadActiveProxyCount() {
    try {
      const proxyInstances = await api.listDeviceProxyInstances();
      activeProxyCount = proxyInstances.filter((in_) => in_.enabled).length;
    } catch {
      activeProxyCount = null;
    }
  }

  // Modal state
  let ruleEditIdx = $state<number | null>(null);
  let ruleAddOpen = $state(false);
  let rewriteAddMode = $state(false);
  let rsEditTag = $state<string | null>(null);
  let rsAddOpen = $state(false);
  let outboundEditTag = $state<string | null>(null);
  let outboundAddOpen = $state(false);
  let dnsServerEditTag = $state<string | null>(null);
  let dnsServerAddOpen = $state(false);
  let dnsRuleEditIdx = $state<number | null>(null);
  let dnsRuleAddOpen = $state(false);

  let inboundDrawerInstance = $state<DeviceProxyInstance | null>(null);
  let inboundDrawerOpen = $state(false);
  let dpReloadKey = $state(0);

  // Унифицированное подтверждение удаления (rule / rule-set / inbound)
  let pendingConfirm = $state<{ title: string; message: string; run: () => Promise<void> } | null>(null);
  let confirmBusy = $state(false);

  async function runConfirm() {
    if (!pendingConfirm) return;
    confirmBusy = true;
    try {
      await pendingConfirm.run();
      pendingConfirm = null;
    } finally {
      confirmBusy = false;
    }
  }

  function openInbound(in_: DeviceProxyInstance) {
    inboundDrawerInstance = in_;
    inboundDrawerOpen = true;
  }
  async function addInbound() {
    let existing: DeviceProxyInstance[] = [];
    try {
      existing = await api.listDeviceProxyInstances();
    } catch {
      existing = [];
    }
    inboundDrawerInstance = newDeviceProxyInstance(existing);
    inboundDrawerOpen = true;
  }
  function onInboundSaved() {
    inboundDrawerOpen = false;
    dpReloadKey += 1;
  }
  function deleteInbound(in_: DeviceProxyInstance) {
    if (in_.id === 'default') return;
    pendingConfirm = {
      title: 'Удалить inbound',
      message: `Удалить inbound «${in_.name || in_.id}»?`,
      run: async () => {
        try {
          await api.deleteDeviceProxyInstance(in_.id);
          notifications.success('Inbound удалён');
          dpReloadKey += 1;
          await loadActiveProxyCount();
        } catch (e) {
          notifications.error(`Не удалось удалить: ${e instanceof Error ? e.message : String(e)}`);
        }
      },
    };
  }

  onMount(() => {
    void singboxRouterStore.loadAll();
    void loadActiveProxyCount();
  });

  // Derived modal targets
  const ruleEditTarget = $derived<SingboxRouterRule | undefined>(
    ruleEditIdx !== null ? $storeRules[ruleEditIdx] : undefined
  );
  const rsEditTarget = $derived<SingboxRouterRuleSet | undefined>(
    rsEditTag !== null ? $storeRuleSets.find((rs) => rs.tag === rsEditTag) : undefined
  );
  const outboundEditTarget = $derived<SingboxRouterOutbound | undefined>(
    outboundEditTag !== null ? $storeOutbounds.find((o) => o.tag === outboundEditTag) : undefined
  );
  const dnsServerEditTarget = $derived<SingboxRouterDNSServer | undefined>(
    dnsServerEditTag !== null ? $storeDnsServers.find((s) => s.tag === dnsServerEditTag) : undefined
  );
  const dnsRuleEditTarget = $derived<SingboxRouterDNSRule | undefined>(
    dnsRuleEditIdx !== null ? $storeDnsRules[dnsRuleEditIdx] : undefined
  );

  // ruleSetUsage for RuleEditModal: exclude currently edited index
  const ruleSetUsageForRuleAdd = $derived(computeRuleSetUsage($storeRules));
  const ruleSetUsageForRuleEdit = $derived(
    ruleEditIdx === null
      ? new Map<string, number>()
      : computeRuleSetUsage($storeRules, ruleEditIdx)
  );
  // ruleSetUsage for DNSRuleEditModal: exclude currently edited index
  const ruleSetUsageForDnsAdd = $derived(computeRuleSetUsage($storeDnsRules));
  const ruleSetUsageForDnsEdit = $derived(
    dnsRuleEditIdx === null
      ? new Map<string, number>()
      : computeRuleSetUsage($storeDnsRules, dnsRuleEditIdx)
  );

  const statCells: StatCellData[] = $derived([
    {
      label: 'Движок',
      value: $storeStatus?.enabled ? 'ON' : 'OFF',
      tone: $storeStatus?.enabled ? 'success' : 'muted',
    },
    { label: 'Правил', value: String($storeRules.length) },
    { label: 'Rule-sets', value: String($storeRuleSets.length) },
    { label: 'Outbounds', value: String($storeOutbounds.length) },
    { label: 'DNS правил', value: String($storeDnsRules.length) },
    { label: 'Перезаписей', value: String($storeDnsRewrites.length) },
    { label: 'Прокси', value: activeProxyCount === null ? '—' : String(activeProxyCount) },
  ]);

  // Rule handlers
  function handleDeleteRule(idx: number) {
    pendingConfirm = {
      title: 'Удалить правило',
      message: `Удалить правило #${idx}?`,
      run: async () => {
        try {
          await api.singboxRouterDeleteRule(idx);
          await singboxRouterStore.loadAll();
          notifications.success('Правило удалено');
        } catch (e) {
          notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
        }
      },
    };
  }

  async function handleMoveRule(idx: number, dir: 'up' | 'down') {
    const to = dir === 'up' ? idx - 1 : idx + 1;
    if (to < 0 || to >= $storeRules.length) return;
    try {
      await api.singboxRouterMoveRule(idx, to);
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  // Rule save handlers (called by modals)
  async function handleRuleSave(rule: SingboxRouterRule) {
    if (ruleEditIdx !== null) {
      await api.singboxRouterUpdateRule(ruleEditIdx, rule);
    } else {
      await api.singboxRouterAddRule(rule);
    }
    ruleEditIdx = null;
    ruleAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  // RuleSet handlers
  function handleDeleteRs(tag: string) {
    pendingConfirm = {
      title: 'Удалить набор',
      message: `Удалить набор «${tag}»?`,
      run: async () => {
        try {
          await api.singboxRouterDeleteRuleSet(tag);
          await singboxRouterStore.loadAll();
          notifications.success('Набор удалён');
        } catch (e) {
          notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
        }
      },
    };
  }

  async function handleRsAddSave(rs: SingboxRouterRuleSet) {
    await api.singboxRouterAddRuleSet(rs);
    rsAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleRsEditSave(rs: SingboxRouterRuleSet) {
    if (rsEditTag !== null) {
      await api.singboxRouterUpdateRuleSet(rsEditTag, rs);
    }
    rsEditTag = null;
    await singboxRouterStore.loadAll();
  }

  // Outbound handlers
  async function handleOutboundAddSave(o: SingboxRouterOutbound) {
    await api.singboxRouterAddOutbound(o);
    outboundAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleOutboundEditSave(o: SingboxRouterOutbound) {
    if (outboundEditTag !== null) {
      await api.singboxRouterUpdateOutbound(outboundEditTag, o);
    }
    outboundEditTag = null;
    await singboxRouterStore.loadAll();
  }

  // DNS server handlers
  async function handleDnsServerAddSave(server: SingboxRouterDNSServer) {
    await api.singboxRouterAddDNSServer(server);
    dnsServerAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleDnsServerEditSave(server: SingboxRouterDNSServer) {
    if (dnsServerEditTag !== null) {
      await api.singboxRouterUpdateDNSServer(dnsServerEditTag, server);
    }
    dnsServerEditTag = null;
    await singboxRouterStore.loadAll();
  }

  // DNS rule handlers
  async function handleDnsRuleAddSave(rule: SingboxRouterDNSRule) {
    await api.singboxRouterAddDNSRule(rule);
    dnsRuleAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleDnsRuleEditSave(rule: SingboxRouterDNSRule) {
    if (dnsRuleEditIdx !== null) {
      await api.singboxRouterUpdateDNSRule(dnsRuleEditIdx, rule);
    }
    dnsRuleEditIdx = null;
    await singboxRouterStore.loadAll();
  }
</script>


<div class="wrap">
  <StatStrip cells={statCells} />

  <div class="main-grid">
    <div class="col-main">
      <SidePanel
        title="Правила маршрутизации"
        count={String($storeRules.length)}
        actionLabel="+ Правило"
        actionVariant="filled"
        onAction={() => (ruleAddOpen = true)}
      >
        <div class="globals-bar">
          <span class="gb-label">first-match-wins · если ничего не подошло →</span>
          <Dropdown bind:value={draftRouteFinal} options={routeFinalOptions} />
          {#if routeFinalDirty}
            <button class="gb-save" onclick={saveRouteFinal} disabled={routeFinalBusy} type="button">
              Сохранить
            </button>
          {/if}
        </div>
        <RoutingTable
          bare
          rules={$storeRules}
          outbounds={$storeOutbounds}
          onEdit={(idx) => (ruleEditIdx = idx)}
          onDelete={handleDeleteRule}
          onMove={handleMoveRule}
        />
      </SidePanel>

      <SidePanel
        title="Rule-sets"
        count={String($storeRuleSets.length)}
        actionLabel="+ Набор"
        actionVariant="filled"
        onAction={() => (rsAddOpen = true)}
      >
        <div class="panel-cap">наборы доменов и IP, на которые ссылаются правила</div>
        <RuleSetsTable
          bare
          ruleSets={$storeRuleSets}
          onEdit={(tag) => (rsEditTag = tag)}
          onDelete={handleDeleteRs}
        />
      </SidePanel>
    </div>

    <div class="col-sidebar">
      <SidePanel
        title="Outbounds"
        count={String($storeOutbounds.length)}
        actionLabel="+ Outbound"
        actionVariant="filled"
        onAction={() => (outboundAddOpen = true)}
      >
        <OutboundsCompact
          outbounds={$storeOutbounds}
          onEdit={(tag) => (outboundEditTag = tag)}
        />
      </SidePanel>

      <SidePanel
        title="DNS-серверы"
        count={String($storeDnsServers.length)}
        actionLabel="+ Сервер"
        actionVariant="filled"
        onAction={() => (dnsServerAddOpen = true)}
      >
        <div class="globals-col">
          <div class="gb-col-head">
            <span class="gb-label">DNS по умолчанию</span>
            {#if dnsGlobalsDirty}
              <button class="gb-save" onclick={saveDnsGlobals} disabled={dnsGlobalsBusy} type="button">
                Сохранить
              </button>
            {/if}
          </div>
          <label class="gb-field">
            <span class="gb-flabel">Final-сервер</span>
            <Dropdown
              bind:value={draftDnsFinal}
              options={dnsFinalOptions}
              disabled={$storeDnsServers.length === 0}
              fullWidth
            />
          </label>
          <label class="gb-field">
            <span class="gb-flabel">Стратегия</span>
            <Dropdown bind:value={draftDnsStrategy} options={STRATEGY_OPTIONS} fullWidth />
          </label>
        </div>
        <DnsServersCompact
          servers={$storeDnsServers}
          rules={$storeDnsRules}
          onEditServer={(tag) => (dnsServerEditTag = tag)}
          onEditRule={(idx) => (dnsRuleEditIdx = idx)}
          onAddRule={() => (dnsRuleAddOpen = true)}
        />
      </SidePanel>

      <SidePanel
        title="DNS-перезаписи"
        count={String($storeDnsRewrites.length)}
        actionLabel="+ Добавить"
        actionVariant="filled"
        onAction={() => (rewriteAddMode = true)}
      >
        <DNSRewritesList
          rewrites={$storeDnsRewrites}
          onChange={() => singboxRouterStore.loadAll()}
          showHeader={false}
          bind:addMode={rewriteAddMode}
        />
      </SidePanel>

      <SidePanel
        title="Inbounds"
        count=""
        actionLabel="+ Добавить"
        actionVariant="filled"
        onAction={addInbound}
      >
        {#key dpReloadKey}
          <DeviceProxyCompact bare onSelect={openInbound} onDelete={deleteInbound} />
        {/key}
      </SidePanel>
    </div>
  </div>
</div>

<!-- RuleEditModal: add -->
{#if ruleAddOpen}
  <RuleEditModal
    outboundOptions={$storeOptions}
    availableRuleSets={$storeRuleSets}
    ruleSetUsage={ruleSetUsageForRuleAdd}
    onClose={() => (ruleAddOpen = false)}
    onSave={handleRuleSave}
  />
{/if}

<!-- RuleEditModal: edit -->
{#if ruleEditIdx !== null && ruleEditTarget !== undefined}
  <RuleEditModal
    rule={ruleEditTarget}
    outboundOptions={$storeOptions}
    availableRuleSets={$storeRuleSets}
    ruleSetUsage={ruleSetUsageForRuleEdit}
    onClose={() => (ruleEditIdx = null)}
    onSave={handleRuleSave}
  />
{/if}

<!-- RuleSetAddModal: add -->
{#if rsAddOpen}
  <RuleSetAddModal
    outboundOptions={$storeOptions}
    onClose={() => (rsAddOpen = false)}
    onSave={handleRsAddSave}
  />
{/if}

<!-- RuleSetAddModal: edit (ruleSet prop activates edit-mode) -->
{#if rsEditTag !== null && rsEditTarget !== undefined}
  <RuleSetAddModal
    ruleSet={rsEditTarget}
    outboundOptions={$storeOptions}
    onClose={() => (rsEditTag = null)}
    onSave={handleRsEditSave}
  />
{/if}

<!-- CompositeOutboundEditModal: add -->
{#if outboundAddOpen}
  <CompositeOutboundEditModal
    outboundOptions={$storeOptions}
    onClose={() => (outboundAddOpen = false)}
    onSave={handleOutboundAddSave}
  />
{/if}

<!-- CompositeOutboundEditModal: edit -->
{#if outboundEditTag !== null && outboundEditTarget !== undefined}
  <CompositeOutboundEditModal
    outbound={outboundEditTarget}
    outboundOptions={$storeOptions}
    onClose={() => (outboundEditTag = null)}
    onSave={handleOutboundEditSave}
  />
{/if}

<!-- DNSServerEditModal: add -->
{#if dnsServerAddOpen}
  <DNSServerEditModal
    servers={$storeDnsServers}
    outboundOptions={$storeOptions}
    onClose={() => (dnsServerAddOpen = false)}
    onSave={handleDnsServerAddSave}
  />
{/if}

<!-- DNSServerEditModal: edit -->
{#if dnsServerEditTag !== null && dnsServerEditTarget !== undefined}
  <DNSServerEditModal
    server={dnsServerEditTarget}
    servers={$storeDnsServers}
    outboundOptions={$storeOptions}
    onClose={() => (dnsServerEditTag = null)}
    onSave={handleDnsServerEditSave}
  />
{/if}

<!-- DNSRuleEditModal: add -->
{#if dnsRuleAddOpen}
  <DNSRuleEditModal
    servers={$storeDnsServers}
    availableRuleSets={$storeRuleSets}
    ruleSetUsage={ruleSetUsageForDnsAdd}
    onClose={() => (dnsRuleAddOpen = false)}
    onSave={handleDnsRuleAddSave}
  />
{/if}

<!-- DNSRuleEditModal: edit -->
{#if dnsRuleEditIdx !== null && dnsRuleEditTarget !== undefined}
  <DNSRuleEditModal
    rule={dnsRuleEditTarget}
    servers={$storeDnsServers}
    availableRuleSets={$storeRuleSets}
    ruleSetUsage={ruleSetUsageForDnsEdit}
    onClose={() => (dnsRuleEditIdx = null)}
    onSave={handleDnsRuleEditSave}
  />
{/if}

{#if inboundDrawerInstance}
  <InboundSettingsDrawer
    instance={inboundDrawerInstance}
    open={inboundDrawerOpen}
    onClose={() => (inboundDrawerOpen = false)}
    onSaved={onInboundSaved}
  />
{/if}

<ConfirmModal
  open={pendingConfirm !== null}
  title={pendingConfirm?.title ?? ''}
  message={pendingConfirm?.message ?? ''}
  busy={confirmBusy}
  onConfirm={runConfirm}
  onClose={() => { if (!confirmBusy) pendingConfirm = null; }}
/>

<style>
  .wrap {
    max-width: none;
    margin: 0 auto;
    padding: var(--sp-4);
  }
  /* Caption внутри SidePanel body — sub-title строкой над контентом */
  .panel-cap {
    padding: 8px 14px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  /* Globals-бар route-final (шапка панели «Правила») */
  .globals-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 8px 14px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
  }
  .gb-label {
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  /* Globals-секция DNS (шапка панели «DNS-серверы») */
  .globals-col {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 14px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
  }
  .gb-col-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .gb-field {
    display: grid;
    grid-template-columns: 84px 1fr;
    align-items: center;
    gap: 8px;
  }
  .gb-flabel {
    font-size: 11px;
    color: var(--text-muted);
  }
  .gb-save {
    background: transparent;
    border: 0;
    color: var(--accent);
    font-size: 11.5px;
    cursor: pointer;
    font-family: inherit;
    padding: 0;
  }
  .gb-save:hover:not(:disabled) {
    text-decoration: underline;
  }
  .gb-save:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
  .main-grid {
    display: grid;
    grid-template-columns: minmax(0, 8fr) minmax(0, 4fr);
    gap: 14px;
  }
  .col-main {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
  }
  .col-sidebar {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
  }
  @media (max-width: 1023px) {
    .main-grid {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 768px) {
    .wrap {
      padding: var(--sp-2);
    }
  }
</style>
