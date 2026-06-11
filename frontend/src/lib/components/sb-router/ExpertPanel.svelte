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
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { notifications } from '$lib/stores/notifications';
  import { api } from '$lib/api/client';
  import { computeRuleSetUsage, DNSGlobalsEditModal } from '$lib/components/routing/singboxRouter';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import type {
    CatalogPreset,
    SingboxRouterRule,
    SingboxRouterRuleSet,
    SingboxRouterOutbound,
    SingboxRouterDNSServer,
    SingboxRouterDNSRule,
    SingboxRouterDNSStrategy,
    DeviceProxyInstance,
  } from '$lib/types';
  import { newDeviceProxyInstance } from '$lib/utils/deviceProxyInstance';
  import { deleteDeviceProxyInstanceWithNotice } from '$lib/utils/deviceProxyDeleteNotice';
  import { pluralize, SET_WORDS } from '$lib/utils/pluralize';

  import StatStrip, { type StatCellData } from './StatStrip.svelte';
  import SidePanel from './SidePanel.svelte';
  import RoutingTable from './RoutingTable.svelte';
  import RuleSetsTable from './RuleSetsTable.svelte';
  import SbRouterRuleSetCatalogModal from './SbRouterRuleSetCatalogModal.svelte';
  import { applyCatalogPresetsAsRuleSets } from './rulesetCatalogActions';
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
  import { ConfirmModal, Dropdown, Button, type DropdownOption } from '$lib/components/ui';
  import { LayoutGrid } from 'lucide-svelte';

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
  // route-final: direct + все outbounds, кроме группы «Специальные»
  const routeFinalOptions = $derived<DropdownOption[]>([
    { value: 'direct', label: 'direct (мимо VPN)' },
    ...$storeOptions
      .filter((g) => g.group !== 'Специальные')
      .flatMap((g) => g.items.map((i) => ({ value: i.value, label: i.label, group: g.group }))),
  ]);

  let draftRouteFinal = $state('direct');
  let routeFinalBusy = $state(false);

  // draft синхронизируется со стором
  $effect(() => {
    draftRouteFinal = $storeStatus?.final || 'direct';
  });

  const routeFinalDirty = $derived(draftRouteFinal !== ($storeStatus?.final || 'direct'));

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

  function openDnsGlobalsModal() {
    dnsGlobalsModalOpen = true;
  }

  let activeProxyCount = $state<number | null>(null);
  let totalProxyCount = $state<number | null>(null);
  let deviceProxyInstances = $state<DeviceProxyInstance[]>([]);

  async function loadActiveProxyCount() {
    try {
      const proxyInstances = await api.listDeviceProxyInstances();
      deviceProxyInstances = proxyInstances;
      totalProxyCount = proxyInstances.length;

        const runtimeEntries = await Promise.all(
          proxyInstances.map(async (in_) => {
            const runtime = await api.getDeviceProxyInstanceRuntime(in_.id).catch(() => null);
            return { instance: in_, runtime };
        }),
      );

        activeProxyCount = runtimeEntries.filter(({ instance, runtime }) => {
          return instance.enabled && runtime?.alive === true;
        }).length;
      } catch {
        activeProxyCount = null;
      totalProxyCount = null;
      deviceProxyInstances = [];
    }
  }

  const outboundUsageContext = $derived({
    rules: $storeRules,
    routeFinal: $storeStatus?.final || 'direct',
    outbounds: $storeOutbounds,
    dnsServers: $storeDnsServers,
    ruleSets: $storeRuleSets,
    deviceProxyOutbounds: deviceProxyInstances
      .map((in_) => in_.selectedOutbound)
      .filter((tag): tag is string => !!tag),
  });

  const dnsServerUsageContext = $derived({
    rules: $storeDnsRules,
    servers: $storeDnsServers,
    dnsFinal: $storeDnsGlobals.final || '',
  });

  const activeProxyCountLabel = $derived(
    activeProxyCount === null || totalProxyCount === null ? '—' : `${activeProxyCount}/${totalProxyCount}`,
  );

  // Modal state
  let ruleEditIdx = $state<number | null>(null);
  let ruleAddOpen = $state(false);
  let rewriteAddMode = $state(false);
  let rsEditTag = $state<string | null>(null);
  let rsAddOpen = $state(false);
  let rsCatalogOpen = $state(false);
  let rsCatalogBusy = $state(false);
  let outboundEditTag = $state<string | null>(null);
  let outboundAddOpen = $state(false);
  let dnsServerEditTag = $state<string | null>(null);
  let dnsServerAddOpen = $state(false);
  let dnsRuleEditIdx = $state<number | null>(null);
  let dnsRuleAddOpen = $state(false);
  let dnsGlobalsModalOpen = $state(false);

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
    void loadActiveProxyCount();
  }
  function deleteInbound(in_: DeviceProxyInstance) {
    pendingConfirm = {
      title: 'Удалить inbound',
      message: `Удалить inbound «${in_.name || in_.id}»?`,
      run: async () => {
        try {
          await deleteDeviceProxyInstanceWithNotice(in_.id, {
            successMessage: 'Inbound удалён',
            pendingApplyMessage:
              'Inbound удалён из конфига, но sing-box ещё не обновлён — изменение применится, когда сервис снова будет доступен.',
          });
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

  // Engine badge keys on the live interception state, not the persisted
  // toggle: enabled+active → работает (ON); enabled but jumps gone → СБОЙ;
  // disabled → OFF.
  const engineStat = $derived.by<{ value: string; tone: StatCellData['tone'] }>(() => {
    if (!$storeStatus?.enabled) return { value: 'OFF', tone: 'muted' };
    return $storeStatus.active
      ? { value: 'ON', tone: 'success' }
      : { value: 'СБОЙ', tone: 'error' };
  });

  const statCells: StatCellData[] = $derived([
    {
      label: 'Движок',
      value: engineStat.value,
      tone: engineStat.tone,
    },
    {
      label: 'Правил',
      value: String($storeRules.length),
      helpTitle: 'Правила маршрутизации',
      helpText: 'Количество route rules. Они проверяются сверху вниз: первое совпадение выбирает outbound.',
      helpItems: [
        'Порядок важен.',
        'Если ничего не подошло — используется default outbound в панели правил.',
      ],
    },
    {
      label: 'Rule-sets',
      value: String($storeRuleSets.length),
      helpTitle: 'Наборы правил',
      helpText: 'Списки доменов и IP, на которые ссылаются route rules и DNS rules.',
      helpItems: [
        'Remote — скачивается и обновляется.',
        'Local — файл на роутере.',
        'Inline — правила хранятся прямо в конфиге.',
      ],
    },
    {
      label: 'OUTBOUNDS',
      value: String($storeOutbounds.length),
      helpTitle: 'Outbounds',
      helpText: 'Доступные направления трафика: direct, reject, VPN/selector/composite и подписочные группы.',
      helpItems: [
        'Route rule выбирает outbound.',
        'DNS server тоже может ходить через outbound.',
      ],
    },
    {
      label: 'DNS',
      value: String($storeDnsRules.length),
      helpTitle: 'DNS-правила',
      helpText: 'Правила выбора DNS-сервера по доменам, rule-set, типам запросов и другим условиям.',
      helpItems: [
        'Работают отдельно от route rules.',
        'Могут направлять конкретные домены на нужный DNS-сервер.',
      ],
    },
    {
      label: 'Rewrite',
      value: String($storeDnsRewrites.length),
      helpTitle: 'DNS-перезаписи',
      helpText: 'Статические DNS-ответы: домен или шаблон получает заданный IP.',
      helpItems: [
        'Полезно для локальных override.',
        'Срабатывает до обычного DNS-резолва.',
      ],
    },
      {
        label: 'Прокси',
        value: activeProxyCountLabel,
        helpTitle: 'Device Proxy / Inbounds',
        helpText: 'Количество активных локальных inbound-прокси для устройств.',
        helpItems: [
          'active — inbound запущен и принимает подключения.',
          'выкл — запись есть, но runtime не активен.',
      ],
    },
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

  function handleDeleteDNSRule(idx: number) {
    pendingConfirm = {
      title: 'Удалить DNS-правило',
      message: `Удалить DNS-правило #${idx + 1}?`,
      run: async () => {
        try {
          await api.singboxRouterDeleteDNSRule(idx);
          await singboxRouterStore.loadAll();
          notifications.success('DNS-правило удалено');
        } catch (e) {
          notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
        }
      },
    };
  }

  function handleDeleteDnsServer(tag: string) {
    pendingConfirm = {
      title: 'Удалить DNS-сервер',
      message: `Удалить DNS-сервер «${tag}»?`,
      run: async () => {
        try {
          await api.singboxRouterDeleteDNSServer(tag);
          await singboxRouterStore.loadAll();
          notifications.success('DNS-сервер удалён');
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

  async function handleRsCatalogConfirm(presets: CatalogPreset[]) {
    if (rsCatalogBusy || presets.length === 0) return;
    rsCatalogBusy = true;
    try {
      const result = await applyCatalogPresetsAsRuleSets(presets, $storeRuleSets);
      await singboxRouterStore.loadAll();

      if (result.added.length > 0) {
        notifications.success(`Добавлено ${pluralize(result.added.length, SET_WORDS)} из каталога`);
      } else if (result.failures.length === 0 && result.emptyPresets.length > 0) {
        notifications.error('У выбранных сервисов нет sing-box наборов');
      } else if (result.failures.length === 0) {
        notifications.info('Выбранные наборы уже есть в конфиге');
      }

      if (result.failures.length > 0) {
        const msg = result.failures.map((f) => `${f.tag}: ${f.error}`).join('; ');
        notifications.error(`Не удалось добавить: ${msg}`);
      } else if (result.added.length > 0 || result.emptyPresets.length === 0) {
        rsCatalogOpen = false;
      }
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      rsCatalogBusy = false;
    }
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

  function handleDeleteOutbound(tag: string) {
    const outbound = $storeOutbounds.find((o) => o.tag === tag);
    if (!outbound) return;
    pendingConfirm = {
      title: 'Удалить outbound',
      message: `Удалить outbound «${tag}»?`,
      run: async () => {
        try {
          await api.singboxRouterDeleteOutbound(tag);
          await singboxRouterStore.loadAll();
          notifications.success('Outbound удалён');
        } catch (e) {
          notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
        }
      },
    };
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

  async function handleDnsGlobalsSave(globals: { final: string; strategy: SingboxRouterDNSStrategy }) {
    await api.singboxRouterPutDNSGlobals(globals);
    dnsGlobalsModalOpen = false;
    await singboxRouterStore.loadAll();
  }
</script>


<div class="wrap">
  <StatStrip cells={statCells} />

  <div class="main-grid">
    <div class="col-main">
      <SidePanel
        section="rules"
        title="Правила маршрутизации"
        count={String($storeRules.length)}
        actionLabel="+ Правило"
        actionVariant="filled"
        onAction={() => (ruleAddOpen = true)}
      >
        <div class="globals-bar">
          <span class="gb-label gb-label-full">first-match-wins · если ничего не подошло →</span>
          <span class="gb-label gb-label-mobile">если не подошло →</span>
          <div class="route-final-select">
            <Dropdown bind:value={draftRouteFinal} options={routeFinalOptions} fullWidth />
          </div>
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
          outboundOptions={$storeOptions}
          subscriptions={$subscriptionsStore.data}
          proxyGroups={$singboxProxies.data ?? []}
          singboxTunnels={$singboxTunnels.data ?? []}
          onEdit={(idx) => (ruleEditIdx = idx)}
          onDelete={handleDeleteRule}
          onMove={handleMoveRule}
        />
      </SidePanel>

      <SidePanel
        section="ruleSets"
        title="Rule-sets"
        count={String($storeRuleSets.length)}
      >
        {#snippet actions()}
          <div class="rs-head-actions">
            <Button variant="secondary" size="sm" onclick={() => (rsCatalogOpen = true)}>
              {#snippet iconBefore()}
                <LayoutGrid size={14} aria-hidden="true" />
              {/snippet}
              Каталог
            </Button>
            <Button variant="primary" size="sm" onclick={() => (rsAddOpen = true)}>+ Набор</Button>
          </div>
        {/snippet}
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
        section="outbounds"
        title="Outbounds"
        count={String($storeOutbounds.length)}
        actionLabel="+ Outbound"
        actionVariant="filled"
        onAction={() => (outboundAddOpen = true)}
      >
        <OutboundsCompact
          outbounds={$storeOutbounds}
          subscriptions={$subscriptionsStore.data ?? []}
          usage={outboundUsageContext}
          onEdit={(tag) => (outboundEditTag = tag)}
          onDelete={handleDeleteOutbound}
        />
      </SidePanel>

      <SidePanel
        section="dnsServers"
        title="DNS-серверы"
        count={String($storeDnsServers.length)}
        actionLabel="+ Сервер"
        actionVariant="filled"
        onAction={() => (dnsServerAddOpen = true)}
      >
        <button
          type="button"
          class="globals-summary"
          onclick={openDnsGlobalsModal}
        >
          <div>
            <span class="gb-label">DNS по умолчанию</span>
            <div class="globals-summary-values">
              <span>Final: <strong>{$storeDnsGlobals.final || '—'}</strong></span>
              <span>Strategy: <strong>{$storeDnsGlobals.strategy || 'default'}</strong></span>
            </div>
          </div>
          <span class="globals-summary-action">Настроить</span>
        </button>
        <DnsServersCompact
          servers={$storeDnsServers}
          rules={$storeDnsRules}
          outbounds={$storeOutbounds}
          outboundOptions={$storeOptions}
          subscriptions={$subscriptionsStore.data}
          proxyGroups={$singboxProxies.data ?? []}
          singboxTunnels={$singboxTunnels.data ?? []}
          dnsUsage={dnsServerUsageContext}
          onEditServer={(tag) => (dnsServerEditTag = tag)}
          onDeleteServer={handleDeleteDnsServer}
          onEditRule={(idx) => (dnsRuleEditIdx = idx)}
          onDeleteRule={handleDeleteDNSRule}
          onAddRule={() => (dnsRuleAddOpen = true)}
        />
      </SidePanel>

      <SidePanel
        section="dnsRewrite"
        title="DNS Rewrite"
        count={String($storeDnsRewrites.length)}
        actionLabel="+ Добавить"
        actionVariant="filled"
        onAction={() => (rewriteAddMode = true)}
      >
        <DNSRewritesList
          rewrites={$storeDnsRewrites}
          onChange={() => singboxRouterStore.loadAll()}
          showHeader={false}
          hideColumnHeader={true}
          bind:addMode={rewriteAddMode}
        />
      </SidePanel>

        <SidePanel
          section="inbounds"
          title="Inbounds"
          count={activeProxyCountLabel}
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

<SbRouterRuleSetCatalogModal
  open={rsCatalogOpen}
  existingRuleSetTags={$storeRuleSets.map((rs) => rs.tag)}
  submitting={rsCatalogBusy}
  onclose={() => {
    if (!rsCatalogBusy) rsCatalogOpen = false;
  }}
  onconfirm={handleRsCatalogConfirm}
/>

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

{#if dnsGlobalsModalOpen}
  <DNSGlobalsEditModal
    servers={$storeDnsServers}
    final={$storeDnsGlobals.final}
    strategy={$storeDnsGlobals.strategy}
    onClose={() => (dnsGlobalsModalOpen = false)}
    onSave={handleDnsGlobalsSave}
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
  }
  /* Caption внутри SidePanel body — sub-title строкой над контентом */
  .rs-head-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
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
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 8px;
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
  .gb-label-mobile {
    display: none;
  }
  .route-final-select {
    min-width: 0;
    width: 100%;
  }
  /* Globals-секция DNS (шапка панели «DNS-серверы») */
  .globals-summary {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.75rem 0.875rem;
    background: var(--bg-tertiary);
    border: 0;
    border-bottom: 1px solid var(--border);
    color: inherit;
    text-align: left;
    cursor: pointer;
  }
  .globals-summary-values {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem 0.75rem;
    margin-top: 0.25rem;
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .globals-summary-action {
    flex: 0 0 auto;
    font-size: 0.72rem;
    font-weight: 700;
    color: var(--accent);
    text-transform: uppercase;
    letter-spacing: 0.05em;
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
    .globals-bar {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      gap: 0.45rem;
      padding: 0.625rem 0.875rem;
    }
    .gb-label-full {
      display: none;
    }
    .gb-label-mobile {
      display: block;
      min-width: 0;
      font-size: 10px;
      line-height: 1.2;
      letter-spacing: 0.04em;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .route-final-select {
      width: 100%;
      min-width: 0;
    }
    .globals-summary {
      align-items: flex-start;
    }
    .globals-summary-values {
      display: grid;
      gap: 0.25rem;
    }
    .globals-summary-action {
      padding-top: 0.1rem;
    }
  }
</style>
