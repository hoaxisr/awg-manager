<script lang="ts">
  import { onMount } from 'svelte';
  import {
    Plus, Globe, Zap, ShieldOff, Layers, Activity, RefreshCw, Sparkles,
    Edit3, Trash2, ChevronUp, ChevronDown, Check, ExternalLink, FileText, LayoutGrid,
    Search, X, SlidersHorizontal, Columns, Maximize2, Minimize2, GripVertical, RotateCcw
  } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { Button, Badge, ConfirmModal } from '$lib/components/ui';
  import { LoadingSpinner } from '$lib/components/layout';
  import StatStrip, { type StatCellData } from '../StatStrip.svelte';
  import SidePanel from '../SidePanel.svelte';
  import { expertPanelCollapse, setExpertPanelSectionCollapsed, type ExpertPanelSection } from '../expertPanelCollapseStore';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { singboxMemory } from '$lib/stores/singboxMemory';
  import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
  import { formatBytes, formatByteRate } from '$lib/utils/format';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { formatOutboundHumanName } from '$lib/utils/outboundHumanName';
  import { pluralize, RULE_WORDS } from '$lib/utils/pluralize';
  import type {
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeRule,
    MihomoNativeRuleProvider,
    MihomoNativeSubscription,
    MihomoRuntimeProxy,
    MihomoRuntimeProvider,
  } from '$lib/types';
  import { openAddWizard } from '../addWizardStore';
  import MihomoRuleEditModal from './MihomoRuleEditModal.svelte';
  import MihomoGroupEditModal from './MihomoGroupEditModal.svelte';
  import MihomoProviderModal from './MihomoProviderModal.svelte';
  import MihomoTemplateModal from './MihomoTemplateModal.svelte';
  import MihomoRuleSetCatalogModal from './MihomoRuleSetCatalogModal.svelte';
  import MihomoProxyGroupCard from './MihomoProxyGroupCard.svelte';
  import DnsServersCompact from '../DnsServersCompact.svelte';
  import DNSServerEditModal from '$lib/components/routing/singboxRouter/DNSServerEditModal.svelte';
  import DNSRuleEditModal from '$lib/components/routing/singboxRouter/DNSRuleEditModal.svelte';
  import { get } from 'svelte/store';
  import type { SingboxRouterDNSServer, SingboxRouterDNSRule } from '$lib/types';

  interface Props {
    rules: MihomoNativeRule[];
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    ruleProviders: MihomoNativeRuleProvider[];
    runtimeProxies: MihomoRuntimeProxy[];
    runtimeProviders?: Record<string, MihomoRuntimeProvider>;
    loading: boolean;
    onReload: () => void;
  }

  let {
    rules = [],
    groups = [],
    proxies = [],
    subscriptions = [],
    ruleProviders = [],
    runtimeProxies = [],
    runtimeProviders = {},
    loading = false,
    onReload,
  }: Props = $props();

  const storeStatus = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;

  const isEngineActive = $derived(
    Boolean($storeStatus?.active || runtimeProxies.length > 0 || Object.keys(runtimeProviders).length > 0)
  );

  const engineStat = $derived.by<{ value: string; tone: StatCellData['tone'] }>(() => {
    if ($storeStatus && !$storeStatus.enabled) return { value: 'OFF', tone: 'muted' };
    if (loading && !isEngineActive) return { value: '...', tone: 'muted' };
    return isEngineActive
      ? { value: 'ON', tone: 'success' }
      : { value: 'СБОЙ', tone: 'error' };
  });

  const nameContext = $derived({
    awgTags: $awgTagsStore.data,
    subscriptions: $subscriptionsStore.data,
    mihomoSubscriptions: subscriptions,
  });

  function formatOutbound(raw: string): string {
    return formatOutboundHumanName(raw, nameContext);
  }

  export type SectionKey = 'rules' | 'outbounds' | 'ruleSets' | 'dns';

  const LAYOUT_STORAGE_KEY = 'awgm.mihomo.layout-mode';
  const COMPACT_STORAGE_KEY = 'awgm.mihomo.compact-view';
  const SPLIT_STORAGE_KEY = 'awgm.mihomo.col-split';
  const BLOCK_LAYOUT_KEY = 'awgm.mihomo.block-columns-v3';
  const CUSTOM_HEIGHTS_KEY = 'awgm.mihomo.block-heights-v2';
  const DEFAULT_SPLIT_RATIO = 58;

  let layoutMode = $state<'split' | 'stack'>('split');
  let isCompact = $state(false);
  let ruleSearchQuery = $state('');
  let splitRatio = $state(DEFAULT_SPLIT_RATIO);
  let isDraggingSplitter = $state(false);
  let gridContainerEl = $state<HTMLElement | null>(null);
  let maximizedSection = $state<SectionKey | null>(null);

  let col1Sections = $state<SectionKey[]>(['rules']);
  let col2Sections = $state<SectionKey[]>(['outbounds', 'ruleSets', 'dns']);

  let draggedSection = $state<SectionKey | null>(null);
  let dragTarget = $state<{ section: SectionKey; position: 'before' | 'after' } | null>(null);
  let dragOverCol = $state<1 | 2 | null>(null);

  let resizingSection = $state<SectionKey | null>(null);
  let customHeights = $state<Record<SectionKey, number | null>>({
    rules: null,
    outbounds: null,
    ruleSets: null,
    dns: null,
  });

  onMount(() => {
    try {
      const savedLayout = localStorage.getItem(LAYOUT_STORAGE_KEY);
      if (savedLayout === 'split' || savedLayout === 'stack') {
        layoutMode = savedLayout;
      }
      isCompact = localStorage.getItem(COMPACT_STORAGE_KEY) === 'true';

      const savedSplit = localStorage.getItem(SPLIT_STORAGE_KEY);
      if (savedSplit) {
        const parsed = parseInt(savedSplit, 10);
        if (!isNaN(parsed) && parsed >= 25 && parsed <= 75) {
          splitRatio = parsed;
        }
      }

      const savedBlockLayout = localStorage.getItem(BLOCK_LAYOUT_KEY);
      if (savedBlockLayout) {
        try {
          const parsed = JSON.parse(savedBlockLayout);
          const validKeys: SectionKey[] = ['rules', 'outbounds', 'ruleSets', 'dns'];
          if (Array.isArray(parsed.col1) && Array.isArray(parsed.col2)) {
            const combined = [...parsed.col1, ...parsed.col2];
            if (combined.length === 4 && validKeys.every((k) => combined.includes(k))) {
              col1Sections = parsed.col1;
              col2Sections = parsed.col2;
            }
          }
        } catch {}
      }

      const savedHeights = localStorage.getItem(CUSTOM_HEIGHTS_KEY);
      if (savedHeights) {
        try {
          const parsed = JSON.parse(savedHeights);
          if (typeof parsed === 'object' && parsed !== null) {
            customHeights = {
              rules: typeof parsed.rules === 'number' ? parsed.rules : null,
              outbounds: typeof parsed.outbounds === 'number' ? parsed.outbounds : null,
              ruleSets: typeof parsed.ruleSets === 'number' ? parsed.ruleSets : null,
              dns: typeof parsed.dns === 'number' ? parsed.dns : null,
            };
          }
        } catch {}
      }
    } catch {
      // ignore
    }
  });

  function setLayoutMode(mode: 'split' | 'stack') {
    layoutMode = mode;
    try {
      localStorage.setItem(LAYOUT_STORAGE_KEY, mode);
    } catch {}
  }

  function toggleCompact() {
    isCompact = !isCompact;
    try {
      localStorage.setItem(COMPACT_STORAGE_KEY, String(isCompact));
    } catch {}
  }

  function resetSplitter() {
    splitRatio = DEFAULT_SPLIT_RATIO;
    try {
      localStorage.setItem(SPLIT_STORAGE_KEY, String(DEFAULT_SPLIT_RATIO));
    } catch {}
  }

  function handleSplitterPointerDown(e: PointerEvent) {
    if (e.button !== 0 || !gridContainerEl) return;
    e.preventDefault();
    isDraggingSplitter = true;

    const target = e.currentTarget as HTMLElement;
    try {
      target.setPointerCapture(e.pointerId);
    } catch {}

    const onPointerMove = (ev: PointerEvent) => {
      if (!gridContainerEl) return;
      const rect = gridContainerEl.getBoundingClientRect();
      const relativeX = ev.clientX - rect.left;
      const percent = (relativeX / rect.width) * 100;
      splitRatio = Math.min(75, Math.max(25, Math.round(percent)));
    };

    const onPointerUp = (ev: PointerEvent) => {
      isDraggingSplitter = false;
      try {
        target.releasePointerCapture(ev.pointerId);
      } catch {}
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      try {
        localStorage.setItem(SPLIT_STORAGE_KEY, String(splitRatio));
      } catch {}
    };

    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
  }

  function toggleMaximize(sec: SectionKey) {
    if (maximizedSection === sec) {
      maximizedSection = null;
    } else {
      maximizedSection = sec;
      const targetSec: ExpertPanelSection = sec === 'dns' ? 'dnsServers' : sec;
      setExpertPanelSectionCollapsed(targetSec, false);
    }
  }

  function handleBlockDragStart(e: DragEvent, secKey: SectionKey) {
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', secKey);
    }
    draggedSection = secKey;
    if (typeof document !== 'undefined') {
      document.body.classList.add('block-dragging-active');
    }
  }

  function handleBlockDragOver(e: DragEvent, secKey: SectionKey, col: 1 | 2) {
    e.preventDefault();
    if (!draggedSection || draggedSection === secKey) {
      dragTarget = null;
      return;
    }
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = 'move';
    }
    const el = document.getElementById(`section-${secKey}`);
    if (el) {
      const rect = el.getBoundingClientRect();
      const relY = e.clientY - rect.top;
      const position = relY < rect.height / 2 ? 'before' : 'after';
      dragTarget = { section: secKey, position };
      dragOverCol = col;
    }
  }

  function handleBlockDragLeave(e: DragEvent, secKey: SectionKey) {
    const related = e.relatedTarget as Node | null;
    const current = document.getElementById(`section-${secKey}`);
    if (current && related && current.contains(related)) {
      return;
    }
    if (dragTarget?.section === secKey) {
      dragTarget = null;
    }
  }

  function handleBlockDrop(e: DragEvent, targetSecKey: SectionKey, targetCol: 1 | 2) {
    e.preventDefault();
    e.stopPropagation();
    if (!draggedSection || draggedSection === targetSecKey) {
      handleBlockDragEnd();
      return;
    }

    const moving = draggedSection;
    col1Sections = col1Sections.filter((s) => s !== moving);
    col2Sections = col2Sections.filter((s) => s !== moving);

    const targetArr = targetCol === 1 ? col1Sections : col2Sections;
    const idx = targetArr.indexOf(targetSecKey);
    const insertIdx = dragTarget?.position === 'after' ? idx + 1 : idx;

    if (insertIdx < 0 || insertIdx >= targetArr.length) {
      targetArr.push(moving);
    } else {
      targetArr.splice(insertIdx, 0, moving);
    }

    if (targetCol === 1) {
      col1Sections = [...targetArr];
    } else {
      col2Sections = [...targetArr];
    }

    handleBlockDragEnd();
    saveBlockLayout();
  }

  function handleColDragOver(e: DragEvent, col: 1 | 2) {
    e.preventDefault();
    if (!draggedSection) return;
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = 'move';
    }
    dragOverCol = col;
  }

  function handleColDragLeave(e: DragEvent) {
    const related = e.relatedTarget as HTMLElement | null;
    if (related && related.closest('.col-drop-zone')) return;
    dragOverCol = null;
  }

  function handleColDrop(e: DragEvent, col: 1 | 2) {
    e.preventDefault();
    if (!draggedSection) return;
    const moving = draggedSection;
    col1Sections = col1Sections.filter((s) => s !== moving);
    col2Sections = col2Sections.filter((s) => s !== moving);

    if (col === 1) {
      col1Sections = [...col1Sections, moving];
    } else {
      col2Sections = [...col2Sections, moving];
    }

    handleBlockDragEnd();
    saveBlockLayout();
  }

  function handleBlockDragEnd() {
    draggedSection = null;
    dragTarget = null;
    dragOverCol = null;
    if (typeof document !== 'undefined') {
      document.body.classList.remove('block-dragging-active');
    }
  }

  function saveBlockLayout() {
    try {
      localStorage.setItem(BLOCK_LAYOUT_KEY, JSON.stringify({
        col1: col1Sections,
        col2: col2Sections,
      }));
    } catch {}
  }

  function handleStartResize(e: PointerEvent, secKey: SectionKey, dir: 's' | 'e' | 'se', colIndex?: 1 | 2) {
    e.preventDefault();
    e.stopPropagation();
    const targetEl = document.getElementById(`section-${secKey}`);
    if (!targetEl) return;

    const startY = e.clientY;
    const startH = targetEl.getBoundingClientRect().height;
    resizingSection = secKey;

    const handleEl = e.currentTarget as HTMLElement | null;
    if (handleEl && typeof handleEl.setPointerCapture === 'function') {
      try {
        handleEl.setPointerCapture(e.pointerId);
      } catch {}
    }

    function onPointerMove(ev: PointerEvent) {
      if (dir === 's' || dir === 'se') {
        const dy = ev.clientY - startY;
        const minH = 220;
        const maxH = Math.max(minH, window.innerHeight * 2.5);
        const newH = Math.max(minH, Math.min(maxH, Math.round(startH + dy)));
        customHeights = { ...customHeights, [secKey]: newH };
      }
      if ((dir === 'e' || dir === 'se') && layoutMode === 'split' && !maximizedSection && gridContainerEl) {
        const rect = gridContainerEl.getBoundingClientRect();
        const relativeX = ev.clientX - rect.left;
        const percent = (relativeX / rect.width) * 100;
        splitRatio = Math.min(75, Math.max(25, Math.round(percent)));
      }
    }

    function onPointerUp(ev: PointerEvent) {
      resizingSection = null;
      if (handleEl && typeof handleEl.releasePointerCapture === 'function') {
        try {
          handleEl.releasePointerCapture(ev.pointerId);
        } catch {}
      }
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerUp);
      try {
        localStorage.setItem(CUSTOM_HEIGHTS_KEY, JSON.stringify(customHeights));
        if (dir === 'e' || dir === 'se') {
          localStorage.setItem(SPLIT_STORAGE_KEY, String(splitRatio));
        }
      } catch {}
    }

    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    window.addEventListener('pointercancel', onPointerUp);
  }

  function resetSectionHeight(secKey: SectionKey) {
    customHeights = { ...customHeights, [secKey]: null };
    try {
      localStorage.setItem(CUSTOM_HEIGHTS_KEY, JSON.stringify(customHeights));
    } catch {}
  }

  function resetAllLayout() {
    col1Sections = ['rules'];
    col2Sections = ['outbounds', 'ruleSets', 'dns'];
    customHeights = { rules: null, outbounds: null, ruleSets: null, dns: null };
    splitRatio = DEFAULT_SPLIT_RATIO;
    maximizedSection = null;
    try {
      localStorage.removeItem(BLOCK_LAYOUT_KEY);
      localStorage.removeItem(CUSTOM_HEIGHTS_KEY);
      localStorage.removeItem(SPLIT_STORAGE_KEY);
    } catch {}
    notifications.success('Расположение и размеры окон сброшены по умолчанию');
  }

  function scrollToSection(id: string, sectionKey?: ExpertPanelSection) {
    if (sectionKey) {
      setExpertPanelSectionCollapsed(sectionKey, false);
    }
    if (typeof document === 'undefined') return;
    setTimeout(() => {
      const el = document.getElementById(id);
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    }, 60);
  }

  const filteredRules = $derived.by(() => {
    const q = ruleSearchQuery.trim().toLowerCase();
    if (!q) return rules;
    return rules.filter((r) => {
      const type = (r.type || '').toLowerCase();
      const payload = (r.payload || '').toLowerCase();
      const outbound = (r.outbound || '').toLowerCase();
      const formattedOutbound = formatOutbound(r.outbound).toLowerCase();
      return (
        type.includes(q) ||
        payload.includes(q) ||
        outbound.includes(q) ||
        formattedOutbound.includes(q) ||
        `${type}: ${payload}`.includes(q)
      );
    });
  });

  const storeDnsServers = singboxRouterStore.dnsServers;
  const storeDnsRules = singboxRouterStore.dnsRules;
  const storeOptions = singboxRouterStore.options;

  let templateModalOpen = $state(false);
  let providerCatalogOpen = $state(false);

  function handleOpenTemplates() {
    templateModalOpen = true;
  }

  function handleOpenProviderCatalog() {
    providerCatalogOpen = true;
  }

  let dnsServerAddOpen = $state(false);
  let dnsServerEditTag = $state<string | null>(null);
  let dnsRuleAddOpen = $state(false);
  let dnsRuleEditIdx = $state<number | null>(null);

  const editingDnsServer = $derived(
    dnsServerEditTag ? $storeDnsServers.find((s) => s.tag === dnsServerEditTag) : undefined,
  );
  const editingDnsRule = $derived(
    dnsRuleEditIdx !== null ? $storeDnsRules[dnsRuleEditIdx] : undefined,
  );

  async function handleDnsServerAddSave(server: SingboxRouterDNSServer) {
    await api.singboxRouterAddDNSServer(server);
    dnsServerAddOpen = false;
    await singboxRouterStore.loadAll();
    onReload();
  }

  async function handleDnsServerEditSave(server: SingboxRouterDNSServer) {
    if (dnsServerEditTag !== null) {
      await api.singboxRouterUpdateDNSServer(dnsServerEditTag, server);
    }
    dnsServerEditTag = null;
    await singboxRouterStore.loadAll();
    onReload();
  }

  async function handleDeleteDnsServer(tag: string) {
    try {
      await api.singboxRouterDeleteDNSServer(tag);
      await singboxRouterStore.loadAll();
      notifications.success(`DNS-сервер «${tag}» удален`);
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка удаления DNS-сервера');
    }
  }

  async function handleDnsRuleAddSave(rule: SingboxRouterDNSRule) {
    await api.singboxRouterAddDNSRule(rule);
    dnsRuleAddOpen = false;
    await singboxRouterStore.loadAll();
    onReload();
  }

  async function handleDnsRuleEditSave(rule: SingboxRouterDNSRule) {
    if (dnsRuleEditIdx !== null) {
      await api.singboxRouterUpdateDNSRule(dnsRuleEditIdx, rule);
    }
    dnsRuleEditIdx = null;
    await singboxRouterStore.loadAll();
    onReload();
  }

  async function handleDeleteDnsRule(idx: number) {
    try {
      await api.singboxRouterDeleteDNSRule(idx);
      await singboxRouterStore.loadAll();
      notifications.success('DNS-правило удалено');
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка удаления DNS-правила');
    }
  }

  async function handleMoveDnsRule(from: number, to: number) {
    const snapshot = get(singboxRouterStore.dnsRules);
    const next = snapshot.slice();
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    singboxRouterStore.applyDNSRules(next);
    try {
      await api.singboxRouterMoveDNSRule(from, to);
      await singboxRouterStore.loadAll();
      onReload();
    } catch (e) {
      singboxRouterStore.applyDNSRules(snapshot);
      notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  // Modals state
  let ruleModalOpen = $state(false);
  let editingRule = $state<MihomoNativeRule | null>(null);

  let groupModalOpen = $state(false);
  let editingGroup = $state<MihomoNativeGroup | null>(null);

  let providerModalOpen = $state(false);
  let editingProvider = $state<MihomoNativeRuleProvider | null>(null);

  let deleteType = $state<'rule' | 'group' | 'provider' | null>(null);
  let deleteId = $state<string | null>(null);
  let deleteConfirmOpen = $state(false);

  let checkingGroupDelay = $state<string | null>(null);
  let refreshingProvider = $state<string | null>(null);

  // Filter tab for Rule Providers
  let providerTab = $state<'all' | 'http' | 'file'>('all');
  let filteredProviders = $derived.by(() => {
    if (providerTab === 'http') return ruleProviders.filter((p) => p.type === 'http');
    if (providerTab === 'file') return ruleProviders.filter((p) => p.type === 'file');
    return ruleProviders;
  });

  const liveStats = $derived($singboxTrafficLive);
  const memCellValue = $derived(
    isEngineActive && $singboxMemory > 0 ? formatBytes($singboxMemory) : '—',
  );
  const rateCellValue = $derived(
    isEngineActive && liveStats.rate.hasRate
      ? formatByteRate(liveStats.rate.downloadRate)
      : '—',
  );

  // Top metric stat strip
  let statCells = $derived<StatCellData[]>([
    {
      label: 'ДВИЖОК',
      value: engineStat.value,
      tone: engineStat.tone,
      helpTitle: 'Ядро маршрутизации Mihomo',
      helpText: 'Mihomo выполняет интерцепцию TProxy и маршрутизацию сетевых пакетов.',
    },
    {
      label: 'ПАМЯТЬ',
      value: memCellValue,
      tone: isEngineActive && $singboxMemory > 0 ? undefined : 'muted',
      helpTitle: 'Память Mihomo',
      helpText: 'Память Go-рантайма Mihomo по данным Clash API. Обновляется каждые ~2 секунды, пока движок работает.',
    },
    {
      label: 'ТРАФИК ↓',
      value: rateCellValue,
      tone: isEngineActive && liveStats.rate.hasRate ? undefined : 'muted',
      helpTitle: 'Трафик через Mihomo',
      helpText: 'Агрегатная скорость скачивания через Mihomo (кумулятивные счётчики Clash).',
      helpItems: [
        `Отдача: ${isEngineActive && liveStats.rate.hasRate ? formatByteRate(liveStats.rate.uploadRate) : '—'}`,
        `За сессию: ${isEngineActive ? formatBytes(liveStats.totals.downloadBytes + liveStats.totals.uploadBytes) : '—'}`,
      ],
    },
    {
      label: 'ПРАВИЛ',
      value: String(rules.length),
      helpTitle: 'Правила маршрутизации',
      helpText: 'Количество правил в цепочке first-match.',
    },
    {
      label: 'RULE-SETS',
      value: String(ruleProviders.length),
      helpTitle: 'Провайдеры правил',
      helpText: 'Внешние и локальные списки правил (RULE-SET).',
    },
    {
      label: 'OUTBOUNDS',
      value: String(groups.length),
      helpTitle: 'Группы прокси',
      helpText: 'Группы автоматического или ручного выбора узлов.',
    },
    {
      label: 'DNS',
      value: '1',
      helpTitle: 'Встроенный DNS',
      helpText: 'Mihomo DNS (enhanced-mode: redir-host).',
    },
    {
      label: 'ПРОКСИ',
      value: String(proxies.length + subscriptions.length),
      helpTitle: 'Прокси и подписки',
      helpText: 'Всего настроенных прокси-узлов и подписок.',
    },
  ]);

  // Fallback / MATCH default rule
  const matchRule = $derived(rules.find((r) => (r.type || '').toUpperCase() === 'MATCH'));
  const fallbackOutbound = $derived(matchRule ? matchRule.outbound : 'DIRECT');
  let editingIsFallback = $state(false);

  // Actions
  function handleAddRule() {
    editingIsFallback = false;
    editingRule = null;
    ruleModalOpen = true;
  }
  function handleEditDefaultFallback() {
    editingIsFallback = true;
    if (matchRule) {
      editingRule = matchRule;
    } else {
      editingRule = {
        id: '',
        type: 'MATCH',
        payload: '',
        outbound: 'DIRECT',
        enabled: true,
      };
    }
    ruleModalOpen = true;
  }
  function handleEditRule(rule: MihomoNativeRule) {
    editingIsFallback = (rule.type || '').toUpperCase() === 'MATCH';
    editingRule = rule;
    ruleModalOpen = true;
  }
  function handleDeleteRule(id: string) {
    deleteType = 'rule';
    deleteId = id;
    deleteConfirmOpen = true;
  }

  function handleAddGroup() {
    editingGroup = null;
    groupModalOpen = true;
  }
  function handleEditGroup(group: MihomoNativeGroup) {
    editingGroup = group;
    groupModalOpen = true;
  }
  function handleDeleteGroup(id: string) {
    deleteType = 'group';
    deleteId = id;
    deleteConfirmOpen = true;
  }

  function handleAddProvider() {
    editingProvider = null;
    providerModalOpen = true;
  }
  function handleEditProvider(provider: MihomoNativeRuleProvider) {
    editingProvider = provider;
    providerModalOpen = true;
  }
  function handleDeleteProvider(id: string) {
    deleteType = 'provider';
    deleteId = id;
    deleteConfirmOpen = true;
  }

  async function handleMoveRule(fromIdx: number, dir: 'up' | 'down') {
    const toIdx = dir === 'up' ? fromIdx - 1 : fromIdx + 1;
    if (toIdx < 0 || toIdx >= rules.length) return;

    const reordered = [...rules];
    const [moved] = reordered.splice(fromIdx, 1);
    reordered.splice(toIdx, 0, moved);

    try {
      const ids = reordered.map((r) => r.id);
      await api.mihomoNativeReorderRules(ids);
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Не удалось переместить правило');
    }
  }

  async function handleRefreshProvider(name: string) {
    refreshingProvider = name;
    try {
      await api.mihomoRuntimeRefreshProvider(name);
      notifications.success(`Провайдер «${name}» обновлён`);
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка обновления провайдера');
    } finally {
      refreshingProvider = null;
    }
  }

  async function handleCheckGroupDelay(name: string) {
    checkingGroupDelay = name;
    try {
      const delay = await api.mihomoRuntimeDelay(name);
      notifications.success(`Задержка «${name}»: ${delay} мс`);
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка проверки задержки группы');
    } finally {
      checkingGroupDelay = null;
    }
  }

  async function confirmDelete() {
    if (!deleteId || !deleteType) return;
    try {
      if (deleteType === 'rule') {
        await api.mihomoNativeDeleteRule(deleteId);
        notifications.success('Правило удалено');
      } else if (deleteType === 'group') {
        await api.mihomoNativeDeleteGroup(deleteId);
        notifications.success('Группа прокси удалена');
      } else if (deleteType === 'provider') {
        await api.mihomoNativeDeleteRuleProvider(deleteId);
        notifications.success('Провайдер правил удален');
      }
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка при удалении');
    } finally {
      deleteConfirmOpen = false;
      deleteId = null;
      deleteType = null;
    }
  }
</script>

<div class="expert-view">
  <!-- Top Stat Strip -->
  <StatStrip cells={statCells} />

  <!-- View Toolbar: Search & Density & Layout Toggle -->
  <div class="mihomo-view-toolbar">
    <div class="toolbar-search">
      <Search size={14} class="search-icon" aria-hidden="true" />
      <input
        type="text"
        class="search-input"
        placeholder="Поиск по правилам (домен, IP, geoip, прямой выход)..."
        bind:value={ruleSearchQuery}
      />
      {#if ruleSearchQuery}
        <button
          type="button"
          class="search-clear-btn"
          onclick={() => (ruleSearchQuery = '')}
          title="Очистить поиск"
        >
          <X size={14} />
        </button>
      {/if}
    </div>

    <div class="toolbar-actions">
      <button
        type="button"
        class="toolbar-toggle-btn"
        class:active={isCompact}
        onclick={toggleCompact}
        title={isCompact ? 'Переключить на стандартный вид' : 'Переключить на компактный вид'}
      >
        <SlidersHorizontal size={14} />
        <span>Компактный вид</span>
      </button>

      <div class="layout-toggle-group">
        <button
          type="button"
          class="layout-btn"
          class:active={layoutMode === 'split'}
          onclick={() => setLayoutMode('split')}
          title="2 колонки (широкий экран)"
        >
          <Columns size={14} />
          <span class="btn-text">2 колонки</span>
        </button>
        <button
          type="button"
          class="layout-btn"
          class:active={layoutMode === 'stack'}
          onclick={() => setLayoutMode('stack')}
          title="1 колонка (стек)"
        >
          <LayoutGrid size={14} />
          <span class="btn-text">1 колонка</span>
        </button>
      </div>

      <button
        type="button"
        class="toolbar-toggle-btn"
        onclick={resetAllLayout}
        title="Сбросить расположение и размеры всех окон по умолчанию"
      >
        <RotateCcw size={14} />
        <span>Сброс окон</span>
      </button>
    </div>
  </div>

    {#snippet renderRules()}
    <SidePanel
            title="Правила маршрутизации"
            count={ruleSearchQuery.trim() ? `${filteredRules.length} из ${rules.length}` : String(rules.length)}
            section="rules"
          >
            {#snippet headerBefore()}
              <div
                class="block-drag-handle"
                draggable="true"
                ondragstart={(e) => handleBlockDragStart(e, 'rules')}
                ondragend={handleBlockDragEnd}
                title="Зажмите и перетащите блок «Правила» мышкой в любое место"
                role="button"
                tabindex="0"
                aria-label="Перетащить блок «Правила»"
              >
                <GripVertical size={14} class="drag-grip-icon" />
              </div>
            {/snippet}
            {#snippet actions()}
              <Button variant="secondary" size="sm" onclick={handleOpenTemplates}>
                {#snippet iconBefore()}
                  <Sparkles size={14} aria-hidden="true" />
                {/snippet}
                Шаблоны
              </Button>
              <Button variant="secondary" size="sm" onclick={openAddWizard}>
                {#snippet iconBefore()}
                  <LayoutGrid size={14} aria-hidden="true" />
                {/snippet}
                Каталог
              </Button>
              <Button variant="primary" size="sm" onclick={handleAddRule}>
                + Правило
              </Button>
              <div class="panel-win-btns">
                {#if customHeights['rules']}
                  <button
                    type="button"
                    class="panel-win-btn"
                    onclick={() => resetSectionHeight('rules')}
                    title="Сбросить высоту к исходной (двойной клик на нижней границе)"
                    aria-label="Сбросить высоту"
                  >
                    <RotateCcw size={12} />
                  </button>
                {/if}
                <button
                  type="button"
                  class="panel-win-btn"
                  class:active={maximizedSection === 'rules'}
                  onclick={() => toggleMaximize('rules')}
                  title={maximizedSection === 'rules' ? 'Восстановить размер окна' : 'Развернуть во весь экран'}
                  aria-label={maximizedSection === 'rules' ? 'Восстановить' : 'Развернуть'}
                >
                  {#if maximizedSection === 'rules'}
                    <Minimize2 size={13} />
                  {:else}
                    <Maximize2 size={13} />
                  {/if}
                </button>
              </div>
            {/snippet}

          <div class="first-match-bar">
            <span class="fm-tag">FIRST-MATCH-WINS · ЕСЛИ НИЧЕГО НЕ ПОДОШЛО →</span>
            <button
              type="button"
              class="fm-chip-btn"
              onclick={handleEditDefaultFallback}
              title="Нажмите, чтобы изменить действие по умолчанию (MATCH)"
            >
              {#if (fallbackOutbound || '').toUpperCase() === 'DIRECT'}
                <div class="tone-chip tone-chip-compact tone-direct">
                  <Globe size={11} />
                  <span>direct (мимо VPN)</span>
                  <Edit3 size={10} class="edit-icon" />
                </div>
              {:else if (fallbackOutbound || '').toUpperCase() === 'REJECT' || (fallbackOutbound || '').toUpperCase() === 'BLOCK'}
                <div class="tone-chip tone-chip-compact tone-block">
                  <ShieldOff size={11} />
                  <span>Заблокировать</span>
                  <Edit3 size={10} class="edit-icon" />
                </div>
              {:else}
                <div class="tone-chip tone-chip-compact tone-composite">
                  <Zap size={11} />
                  <span>{formatOutbound(fallbackOutbound)}</span>
                  <Edit3 size={10} class="edit-icon" />
                </div>
              {/if}
            </button>
          </div>

          <div class="table-wrap rules-table-scroll">
            <table class="data-table">
              <thead>
                <tr>
                  <th class="col-num">#</th>
                  <th class="col-order">Порядок</th>
                  <th class="col-action">Действие</th>
                  <th class="col-matchers">Условия</th>
                  <th class="col-outbound">Выход</th>
                  <th class="col-actions">Действия</th>
                </tr>
              </thead>
              <tbody>
                {#if $storeSettings?.susaninEnabled && !ruleSearchQuery.trim()}
                  {@const susaninOutbound = $storeSettings.susaninOutbound || 'DIRECT'}
                  {@const susaninCount = runtimeProviders['susanin']?.ruleCount ?? 0}
                  <tr class="susanin-rule-row">
                    <td class="col-num font-mono">⚡</td>
                    <td class="col-order">
                      <Badge variant="success" size="sm">РАДАР</Badge>
                    </td>
                    <td class="col-action">
                      <span class="action-tag">ROUTE</span>
                    </td>
                    <td class="col-matchers">
                      <div class="matcher-group">
                        <span class="m-type">RULE-SET:</span>
                        <span class="m-val font-mono">susanin</span>
                        <Badge variant="muted" size="sm">{susaninCount} IP</Badge>
                      </div>
                    </td>
                    <td class="col-outbound">
                      <div class="tone-chip tone-chip-compact tone-composite">
                        <Zap size={11} />
                        <span>{formatOutbound(susaninOutbound)}</span>
                      </div>
                    </td>
                    <td class="col-actions">
                      <span class="managed-label" title="Динамическое правило Susanin формируется автоматически">
                        Радар Susanin
                      </span>
                    </td>
                  </tr>
                {/if}

                {#if rules.length === 0 && !$storeSettings?.susaninEnabled}
                  <tr>
                    <td colspan="6" class="empty-cell">
                      Нет настроенных правил. Нажмите «+ Правило», чтобы добавить.
                    </td>
                  </tr>
                {:else if filteredRules.length === 0}
                  <tr>
                    <td colspan="6" class="empty-cell">
                      <span>По запросу «{ruleSearchQuery}» ничего не найдено.</span>
                      <button type="button" class="btn-clear-search" onclick={() => (ruleSearchQuery = '')}>Сбросить фильтр</button>
                    </td>
                  </tr>
                {:else}
                  {#each filteredRules as rule, displayIdx (rule.id || displayIdx)}
                    {@const origIdx = rules.indexOf(rule)}
                    {@const isSearching = !!ruleSearchQuery.trim()}
                    {@const isDirect = (rule.outbound || '').toUpperCase() === 'DIRECT'}
                    {@const isReject = (rule.outbound || '').toUpperCase() === 'REJECT' || (rule.outbound || '').toUpperCase() === 'BLOCK'}
                    {@const runtime = runtimeProxies.find((p) => p.name === rule.outbound)}
                    <tr>
                      <td class="col-num font-mono">{origIdx >= 0 ? origIdx : displayIdx}</td>
                      <td class="col-order">
                        <div class="order-btns">
                          <button
                            type="button"
                            class="order-btn"
                            disabled={isSearching || origIdx <= 0}
                            onclick={() => handleMoveRule(origIdx, 'up')}
                            title={isSearching ? 'Сортировка отключена во время поиска' : 'Поднять выше'}
                          >
                            <ChevronUp size={14} />
                          </button>
                          <button
                            type="button"
                            class="order-btn"
                            disabled={isSearching || origIdx < 0 || origIdx >= rules.length - 1}
                            onclick={() => handleMoveRule(origIdx, 'down')}
                            title={isSearching ? 'Сортировка отключена во время поиска' : 'Опустить ниже'}
                          >
                            <ChevronDown size={14} />
                          </button>
                        </div>
                      </td>
                      <td class="col-action">
                        {#if isReject}
                          <Badge variant="error" size="sm">REJECT</Badge>
                        {:else if isDirect}
                          <Badge variant="default" size="sm">DIRECT</Badge>
                        {:else}
                          <Badge variant="accent" size="sm">ROUTE</Badge>
                        {/if}
                      </td>
                      <td class="col-matchers font-mono">
                        <div class="matchers-cell">
                          <span class="matcher-text">{rule.type.toLowerCase()}: {rule.payload}</span>
                          {#if rule.noResolve}
                            <span class="tag-badge">no-resolve</span>
                          {/if}
                        </div>
                      </td>
                      <td class="col-outbound">
                        {#if isDirect}
                          <div class="tone-chip tone-chip-compact tone-direct">
                            <Globe size={11} />
                            <span>direct (мимо VPN)</span>
                          </div>
                        {:else if isReject}
                          <div class="tone-chip tone-chip-compact tone-block">
                            <ShieldOff size={11} />
                            <span>Заблокировать</span>
                          </div>
                        {:else}
                          <div class="tone-chip tone-chip-compact tone-composite">
                            <Zap size={11} />
                            <span>{formatOutbound(rule.outbound)}</span>
                            {#if runtime?.now && runtime.now !== rule.outbound}
                              <span class="live-dot" title={`Активен: ${formatOutbound(runtime.now)}`}>➜ {formatOutbound(runtime.now)}</span>
                            {/if}
                          </div>
                        {/if}
                      </td>
                      <td class="col-actions">
                        <div class="row-actions">
                          <button
                            type="button"
                            class="act-btn"
                            onclick={() => handleEditRule(rule)}
                            title="Редактировать правило"
                          >
                            <Edit3 size={14} />
                          </button>
                          <button
                            type="button"
                            class="act-btn danger"
                            onclick={() => handleDeleteRule(rule.id)}
                            title="Удалить правило"
                          >
                            <Trash2 size={14} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  {/each}
                {/if}
              </tbody>
            </table>
          </div>
        </SidePanel>
  {/snippet}

  {#snippet renderOutbounds()}
    <SidePanel
                title="Outbounds / Группы прокси"
                count={String(groups.length)}
                section="outbounds"
              >
                {#snippet headerBefore()}
                  <div
                    class="block-drag-handle"
                    draggable="true"
                    ondragstart={(e) => handleBlockDragStart(e, 'outbounds')}
                    ondragend={handleBlockDragEnd}
                    title="Зажмите и перетащите блок «Outbounds» мышкой в любое место"
                    role="button"
                    tabindex="0"
                    aria-label="Перетащить блок «Outbounds»"
                  >
                    <GripVertical size={14} class="drag-grip-icon" />
                  </div>
                {/snippet}
                {#snippet actions()}
                  <Button variant="primary" size="sm" onclick={handleAddGroup}>
                    + Группа
                  </Button>
                  <div class="panel-win-btns">
                    {#if customHeights['outbounds']}
                      <button
                        type="button"
                        class="panel-win-btn"
                        onclick={() => resetSectionHeight('outbounds')}
                        title="Сбросить высоту к исходной (двойной клик на нижней границе)"
                        aria-label="Сбросить высоту"
                      >
                        <RotateCcw size={12} />
                      </button>
                    {/if}
                    <button
                      type="button"
                      class="panel-win-btn"
                      class:active={maximizedSection === 'outbounds'}
                      onclick={() => toggleMaximize('outbounds')}
                      title={maximizedSection === 'outbounds' ? 'Восстановить размер окна' : 'Развернуть во весь экран'}
                      aria-label={maximizedSection === 'outbounds' ? 'Восстановить' : 'Развернуть'}
                    >
                      {#if maximizedSection === 'outbounds'}
                        <Minimize2 size={13} />
                      {:else}
                        <Maximize2 size={13} />
                      {/if}
                    </button>
                  </div>
                {/snippet}

                <div class="outbounds-list">
                  {#if groups.length === 0}
                    <div class="empty-cell" style="padding: 16px;">
                      Нет созданных групп прокси. Нажмите «+ Группа», чтобы добавить.
                    </div>
                  {:else}
                    {#each groups as group (group.id || group.name)}
                      <MihomoProxyGroupCard
                        {group}
                        {runtimeProxies}
                        {subscriptions}
                        onEdit={handleEditGroup}
                        onDelete={handleDeleteGroup}
                        {onReload}
                      />
                    {/each}
                  {/if}
                </div>
              </SidePanel>
  {/snippet}

  {#snippet renderRuleSets()}
    <SidePanel
                title="Rule-sets / Провайдеры"
                count={String(ruleProviders.length)}
                section="ruleSets"
              >
                {#snippet headerBefore()}
                  <div
                    class="block-drag-handle"
                    draggable="true"
                    ondragstart={(e) => handleBlockDragStart(e, 'ruleSets')}
                    ondragend={handleBlockDragEnd}
                    title="Зажмите и перетащите блок «Rule-sets» мышкой в любое место"
                    role="button"
                    tabindex="0"
                    aria-label="Перетащить блок «Rule-sets»"
                  >
                    <GripVertical size={14} class="drag-grip-icon" />
                  </div>
                {/snippet}
                {#snippet actions()}
                  <Button variant="secondary" size="sm" onclick={handleOpenProviderCatalog}>
                    {#snippet iconBefore()}
                      <LayoutGrid size={14} aria-hidden="true" />
                    {/snippet}
                    Каталог
                  </Button>
                  <Button variant="primary" size="sm" onclick={handleAddProvider}>
                    + Провайдер
                  </Button>
                  <div class="panel-win-btns">
                    {#if customHeights['ruleSets']}
                      <button
                        type="button"
                        class="panel-win-btn"
                        onclick={() => resetSectionHeight('ruleSets')}
                        title="Сбросить высоту к исходной (двойной клик на нижней границе)"
                        aria-label="Сбросить высоту"
                      >
                        <RotateCcw size={12} />
                      </button>
                    {/if}
                    <button
                      type="button"
                      class="panel-win-btn"
                      class:active={maximizedSection === 'ruleSets'}
                      onclick={() => toggleMaximize('ruleSets')}
                      title={maximizedSection === 'ruleSets' ? 'Восстановить размер окна' : 'Развернуть во весь экран'}
                      aria-label={maximizedSection === 'ruleSets' ? 'Восстановить' : 'Развернуть'}
                    >
                      {#if maximizedSection === 'ruleSets'}
                        <Minimize2 size={13} />
                      {:else}
                        <Maximize2 size={13} />
                      {/if}
                    </button>
                  </div>
                {/snippet}

                <!-- Tabs inside panel -->
                <div class="tabs-sub-bar">
                  <button
                    type="button"
                    class="sub-tab-btn"
                    class:active={providerTab === 'all'}
                    onclick={() => (providerTab = 'all')}
                  >
                    Все <span class="tab-cnt">({ruleProviders.length})</span>
                  </button>
                  <button
                    type="button"
                    class="sub-tab-btn"
                    class:active={providerTab === 'http'}
                    onclick={() => (providerTab = 'http')}
                  >
                    Remote (HTTP) <span class="tab-cnt">({ruleProviders.filter((p) => p.type === 'http').length})</span>
                  </button>
                  <button
                    type="button"
                    class="sub-tab-btn"
                    class:active={providerTab === 'file'}
                    onclick={() => (providerTab = 'file')}
                  >
                    Local (File) <span class="tab-cnt">({ruleProviders.filter((p) => p.type === 'file').length})</span>
                  </button>
                </div>

                <div class="table-wrap">
                  <table class="data-table">
                    <thead>
                      <tr>
                        <th>Тег / Имя</th>
                        <th>Тип</th>
                        <th>Формат / Поведение</th>
                        <th>Источник (URL / Path)</th>
                        <th>Загрузка через</th>
                        <th>Интервал</th>
                        <th class="col-actions">Действия</th>
                      </tr>
                    </thead>
                    <tbody>
                      {#if filteredProviders.length === 0}
                        <tr>
                          <td colspan="7" class="empty-cell">Нет провайдеров правил</td>
                        </tr>
                      {:else}
                        {#each filteredProviders as provider (provider.id || provider.name)}
                          <tr>
                            <td class="font-mono font-bold">
                              <div class="provider-title">
                                <Layers size={13} class="icon-muted" />
                                <span>{provider.name}</span>
                              </div>
                            </td>
                            <td>
                              <Badge variant={provider.type === 'http' ? 'info' : 'default'} size="sm">
                                {provider.type}
                              </Badge>
                            </td>
                            <td class="text-secondary text-sm">
                              {provider.behavior} · {provider.format}
                            </td>
                            <td class="font-mono text-xs text-muted url-cell">
                              {provider.url || provider.path || '—'}
                            </td>
                            <td>
                              {#if provider.type === 'http'}
                                {#if provider.proxy}
                                  <Badge variant="warning" size="sm">
                                    {provider.proxy}
                                  </Badge>
                                {:else}
                                  <span class="text-xs text-muted">DIRECT</span>
                                {/if}
                              {:else}
                                <span class="text-xs text-muted">Локально</span>
                              {/if}
                            </td>
                            <td class="text-sm text-secondary">
                              {provider.interval ? `${provider.interval}с` : '—'}
                            </td>
                            <td class="col-actions">
                              <div class="row-actions">
                                {#if provider.type === 'http'}
                                  <button
                                    type="button"
                                    class="act-btn"
                                    disabled={refreshingProvider === provider.name}
                                    onclick={() => handleRefreshProvider(provider.name)}
                                    title="Обновить правила сейчас"
                                  >
                                    <RefreshCw size={14} class={refreshingProvider === provider.name ? 'spin' : ''} />
                                  </button>
                                {/if}
                                <button
                                  type="button"
                                  class="act-btn"
                                  onclick={() => handleEditProvider(provider)}
                                  title="Редактировать провайдер"
                                >
                                  <Edit3 size={14} />
                                </button>
                                <button
                                  type="button"
                                  class="act-btn danger"
                                  onclick={() => handleDeleteProvider(provider.id)}
                                  title="Удалить провайдер"
                                >
                                  <Trash2 size={14} />
                                </button>
                              </div>
                            </td>
                          </tr>
                        {/each}
                      {/if}
                    </tbody>
                  </table>
                </div>
              </SidePanel>
  {/snippet}

  {#snippet renderDns()}
    <div class="dns-content-wrap">
      <SidePanel
                section="dnsServers"
                title="DNS-серверы и правила"
                count={String($storeDnsServers.length)}
              >
                {#snippet headerBefore()}
                  <div
                    class="block-drag-handle"
                    draggable="true"
                    ondragstart={(e) => handleBlockDragStart(e, 'dns')}
                    ondragend={handleBlockDragEnd}
                    title="Зажмите и перетащите блок «DNS» мышкой в любое место"
                    role="button"
                    tabindex="0"
                    aria-label="Перетащить блок «DNS»"
                  >
                    <GripVertical size={14} class="drag-grip-icon" />
                  </div>
                {/snippet}
                {#snippet actions()}
                  <div style="display: flex; align-items: center; gap: 8px;">
                    <Button variant="secondary" size="sm" onclick={() => (dnsRuleAddOpen = true)}>
                      + Правило
                    </Button>
                    <Button variant="primary" size="sm" onclick={() => (dnsServerAddOpen = true)}>
                      + Сервер
                    </Button>
                    <div class="panel-win-btns">
                      {#if customHeights['dns']}
                        <button
                          type="button"
                          class="panel-win-btn"
                          onclick={() => resetSectionHeight('dns')}
                          title="Сбросить высоту к исходной (двойной клик на нижней границе)"
                          aria-label="Сбросить высоту"
                        >
                          <RotateCcw size={12} />
                        </button>
                      {/if}
                      <button
                        type="button"
                        class="panel-win-btn"
                        class:active={maximizedSection === 'dns'}
                        onclick={() => toggleMaximize('dns')}
                        title={maximizedSection === 'dns' ? 'Восстановить размер окна' : 'Развернуть во весь экран'}
                        aria-label={maximizedSection === 'dns' ? 'Восстановить' : 'Развернуть'}
                      >
                        {#if maximizedSection === 'dns'}
                          <Minimize2 size={13} />
                        {:else}
                          <Maximize2 size={13} />
                        {/if}
                      </button>
                    </div>
                  </div>
                {/snippet}

                <DnsServersCompact
                  servers={$storeDnsServers}
                  rules={$storeDnsRules}
                  outboundOptions={$storeOptions}
                  onEditServer={(tag) => (dnsServerEditTag = tag)}
                  onDeleteServer={handleDeleteDnsServer}
                  onEditRule={(idx) => (dnsRuleEditIdx = idx)}
                  onDeleteRule={handleDeleteDnsRule}
                  onMoveRule={handleMoveDnsRule}
                  onAddRule={() => (dnsRuleAddOpen = true)}
                />
              </SidePanel>
    </div>
  {/snippet}

  {#snippet renderSection(secKey: SectionKey, colIndex: 1 | 2)}
    <div
      id={`section-${secKey}`}
      class="expert-section"
      class:is-dragging={draggedSection === secKey}
      class:has-custom-height={Boolean(customHeights[secKey])}
      class:is-resizing={resizingSection === secKey}
      style={customHeights[secKey] ? `height: ${customHeights[secKey]}px;` : undefined}
      ondragover={(e) => handleBlockDragOver(e, secKey, colIndex)}
      ondragleave={(e) => handleBlockDragLeave(e, secKey)}
      ondrop={(e) => handleBlockDrop(e, secKey, colIndex)}
    >
      {#if dragTarget?.section === secKey && dragTarget.position === 'before'}
        <div class="block-drop-indicator before"></div>
      {/if}

      {#if secKey === 'rules'}
        {@render renderRules()}
      {:else if secKey === 'outbounds'}
        {@render renderOutbounds()}
      {:else if secKey === 'ruleSets'}
        {@render renderRuleSets()}
      {:else if secKey === 'dns'}
        {@render renderDns()}
      {/if}

      {#if dragTarget?.section === secKey && dragTarget.position === 'after'}
        <div class="block-drop-indicator after"></div>
      {/if}

      <!-- Right window resize edge (Windows OS window width) -->
      {#if layoutMode === 'split' && colIndex === 1 && !maximizedSection}
        <div
          class="win-resize-edge-e"
          onpointerdown={(e) => handleStartResize(e, secKey, 'e', colIndex)}
          title="Потяните правую границу окна для изменения ширины (как в Windows OS)"
        ></div>
      {/if}

      <!-- Bottom window resize bar & corner grip (like Windows OS window) -->
      <div
        class="win-resize-bar"
        onpointerdown={(e) => handleStartResize(e, secKey, 's')}
        ondblclick={() => resetSectionHeight(secKey)}
        title="Потяните для изменения высоты окна (двойной клик — сброс)"
        role="separator"
        aria-label="Изменить высоту блока"
      >
        <div class="win-resize-line"></div>
        <div class="win-resize-pill"></div>
        <div
          class="win-resize-corner"
          onpointerdown={(e) => handleStartResize(e, secKey, 'se')}
          title="Потяните угол для изменения размера (как в Windows OS)"
        >
          <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
            <circle cx="2" cy="8" r="1" fill="currentColor" opacity="0.6" />
            <circle cx="5" cy="8" r="1" fill="currentColor" opacity="0.6" />
            <circle cx="8" cy="8" r="1" fill="currentColor" opacity="0.6" />
            <circle cx="5" cy="5" r="1" fill="currentColor" opacity="0.6" />
            <circle cx="8" cy="5" r="1" fill="currentColor" opacity="0.6" />
            <circle cx="8" cy="2" r="1" fill="currentColor" opacity="0.6" />
          </svg>
        </div>
      </div>
    </div>
  {/snippet}

  <div
    bind:this={gridContainerEl}
    class="expert-grid"
    class:layout-split={layoutMode === 'split'}
    class:compact-mode={isCompact}
    class:is-maximized={Boolean(maximizedSection)}
    style={layoutMode === 'split' && !maximizedSection ? `--split-ratio: ${splitRatio}%;` : undefined}
  >
    {#if layoutMode === 'split'}
      <!-- Column 1 (Left): Droppable Column -->
      {#if !maximizedSection || col1Sections.includes(maximizedSection)}
        <div class="expert-col expert-col-1" class:is-full-width={Boolean(maximizedSection)}>
          {#each col1Sections as secKey (secKey)}
            {#if !maximizedSection || maximizedSection === secKey}
              {@render renderSection(secKey, 1)}
            {/if}
          {/each}
          {#if !maximizedSection}
            <div
              class="col-drop-zone"
              class:is-active={draggedSection && dragOverCol === 1}
              ondragover={(e) => handleColDragOver(e, 1)}
              ondragleave={handleColDragLeave}
              ondrop={(e) => handleColDrop(e, 1)}
            >
              {#if draggedSection && dragOverCol === 1}
                <div class="drop-zone-content">
                  <Plus size={14} />
                  <span>Переместить в левую колонку</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}

      <!-- Invisible Column Width Resizer (NO static line) -->
      {#if !maximizedSection}
        <div
          class="expert-col-splitter"
          class:dragging={isDraggingSplitter}
          role="separator"
          tabindex="0"
          aria-orientation="vertical"
          aria-label="Изменить ширину колонок"
          onpointerdown={handleSplitterPointerDown}
          ondblclick={resetSplitter}
          title="Потяните для изменения ширины колонок (двойной клик — сброс 58%)"
        >
          <div class="splitter-hover-glow"></div>
        </div>
      {/if}

      <!-- Column 2 (Right): Droppable Column -->
      {#if !maximizedSection || col2Sections.includes(maximizedSection)}
        <div class="expert-col expert-col-2" class:is-full-width={Boolean(maximizedSection)}>
          {#each col2Sections as secKey (secKey)}
            {#if !maximizedSection || maximizedSection === secKey}
              {@render renderSection(secKey, 2)}
            {/if}
          {/each}
          {#if !maximizedSection}
            <div
              class="col-drop-zone"
              class:is-active={draggedSection && dragOverCol === 2}
              ondragover={(e) => handleColDragOver(e, 2)}
              ondragleave={handleColDragLeave}
              ondrop={(e) => handleColDrop(e, 2)}
            >
              {#if draggedSection && dragOverCol === 2}
                <div class="drop-zone-content">
                  <Plus size={14} />
                  <span>Переместить в правую колонку</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    {:else}
      <!-- 1 Column (Stack layout) -->
      <div class="expert-col expert-col-stack" class:is-full-width={Boolean(maximizedSection)}>
        {#each [...col1Sections, ...col2Sections] as secKey (secKey)}
          {#if !maximizedSection || maximizedSection === secKey}
            {@render renderSection(secKey, 1)}
          {/if}
        {/each}
      </div>
    {/if}
  </div>

  <!-- Modals -->
  {#if dnsServerAddOpen}
    <DNSServerEditModal
      servers={$storeDnsServers}
      outboundOptions={$storeOptions}
      onClose={() => (dnsServerAddOpen = false)}
      onSave={handleDnsServerAddSave}
    />
  {/if}

  {#if dnsServerEditTag !== null && editingDnsServer}
    <DNSServerEditModal
      server={editingDnsServer}
      servers={$storeDnsServers}
      outboundOptions={$storeOptions}
      onClose={() => (dnsServerEditTag = null)}
      onSave={handleDnsServerEditSave}
    />
  {/if}

  {#if dnsRuleAddOpen}
    <DNSRuleEditModal
      servers={$storeDnsServers}
      availableRuleSets={ruleProviders.map(p => ({ tag: p.name, type: 'remote' as const, format: (p.format === 'yaml' ? 'source' : 'binary') as 'source' | 'binary' }))}
      existingRoutingRules={rules}
      onClose={() => (dnsRuleAddOpen = false)}
      onSave={handleDnsRuleAddSave}
    />
  {/if}

  {#if dnsRuleEditIdx !== null && editingDnsRule}
    <DNSRuleEditModal
      rule={editingDnsRule}
      ruleIndex={dnsRuleEditIdx}
      servers={$storeDnsServers}
      availableRuleSets={ruleProviders.map(p => ({ tag: p.name, type: 'remote' as const, format: (p.format === 'yaml' ? 'source' : 'binary') as 'source' | 'binary' }))}
      existingRoutingRules={rules}
      onClose={() => (dnsRuleEditIdx = null)}
      onSave={handleDnsRuleEditSave}
    />
  {/if}
  {#if ruleModalOpen}
    <MihomoRuleEditModal
      open={true}
      rule={editingRule}
      isFallback={editingIsFallback}
      {groups}
      {proxies}
      {subscriptions}
      {ruleProviders}
      onClose={() => {
        ruleModalOpen = false;
        editingRule = null;
        editingIsFallback = false;
      }}
      onSaved={() => {
        ruleModalOpen = false;
        editingRule = null;
        editingIsFallback = false;
        onReload();
      }}
    />
  {/if}

  {#if groupModalOpen}
    <MihomoGroupEditModal
      open={true}
      group={editingGroup}
      {groups}
      {proxies}
      {subscriptions}
      onClose={() => {
        groupModalOpen = false;
        editingGroup = null;
      }}
      onSaved={() => {
        groupModalOpen = false;
        editingGroup = null;
        onReload();
      }}
      onDelete={handleDeleteGroup}
    />
  {/if}

  {#if providerModalOpen}
    <MihomoProviderModal
      open={true}
      provider={editingProvider}
      groups={groups}
      proxies={proxies}
      subscriptions={subscriptions}
      onClose={() => {
        providerModalOpen = false;
        editingProvider = null;
      }}
      onSaved={() => {
        providerModalOpen = false;
        editingProvider = null;
        onReload();
      }}
    />
  {/if}

  {#if templateModalOpen}
    <MihomoTemplateModal
      open={true}
      groups={groups}
      proxies={proxies}
      subscriptions={subscriptions}
      currentRules={rules}
      onClose={() => (templateModalOpen = false)}
      onApplied={() => {
        templateModalOpen = false;
        onReload();
      }}
    />
  {/if}

  {#if providerCatalogOpen}
    <MihomoRuleSetCatalogModal
      open={true}
      groups={groups}
      proxies={proxies}
      subscriptions={subscriptions}
      existingProviders={ruleProviders}
      onClose={() => (providerCatalogOpen = false)}
      onAdded={() => {
        providerCatalogOpen = false;
        onReload();
      }}
    />
  {/if}

  <!-- Delete Confirm Modal -->
  <ConfirmModal
    open={deleteConfirmOpen}
    title={deleteType === 'group' ? 'Удалить группу прокси?' : deleteType === 'provider' ? 'Удалить провайдер правил?' : 'Удалить правило?'}
    message={deleteType === 'group'
      ? 'Вы действительно хотите удалить эту группу прокси Mihomo? Связанные с ней правила могут потребовать обновления.'
      : deleteType === 'provider'
        ? 'Вы действительно хотите удалить этот провайдер правил Mihomo?'
        : 'Вы действительно хотите удалить это правило маршрутизации Mihomo?'}
    confirmLabel="Удалить"
    variant="danger"
    onConfirm={confirmDelete}
    onClose={() => {
      deleteConfirmOpen = false;
      deleteId = null;
      deleteType = null;
    }}
  />
</div>

<style>
  @import '../outboundTone.css';

  .expert-view {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .first-match-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 14px;
    background: var(--bg-tertiary, rgba(255, 255, 255, 0.03));
    border-bottom: 1px solid var(--border);
    font-size: 11px;
    font-family: var(--font-mono);
  }

  .fm-tag {
    color: var(--text-secondary);
    font-weight: 600;
  }

  .fm-chip-btn {
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    border-radius: var(--radius-sm);
    transition: transform 0.15s ease, opacity 0.15s ease;
  }

  .fm-chip-btn:hover {
    opacity: 0.85;
    transform: translateY(-1px);
  }

  .fm-chip-btn :global(.edit-icon) {
    margin-left: 4px;
    opacity: 0.6;
  }

  .table-wrap {
    overflow-x: auto;
  }

  .data-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    text-align: left;
  }

  .data-table th {
    padding: 8px 14px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
    border-bottom: 1px solid var(--border);
    background: transparent;
  }

  .data-table td {
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    color: var(--text-primary);
    vertical-align: middle;
  }

  .data-table tr:last-child td {
    border-bottom: none;
  }

  .data-table tr:hover td {
    background: var(--bg-hover, rgba(255, 255, 255, 0.02));
  }

  .col-num {
    width: 32px;
    text-align: center;
    color: var(--text-muted);
    font-size: 12px;
  }

  .col-order {
    width: 50px;
  }

  .order-btns {
    display: inline-flex;
    gap: 2px;
  }

  .order-btn {
    background: transparent;
    border: none;
    padding: 2px;
    color: var(--text-muted);
    cursor: pointer;
    border-radius: 3px;
  }

  .order-btn:hover:not(:disabled) {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }

  .order-btn:disabled {
    opacity: 0.25;
    cursor: not-allowed;
  }

  .col-action {
    width: 90px;
  }

  .col-matchers {
    min-width: 200px;
  }

  .matchers-cell {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .matcher-text {
    font-size: 12px;
    color: var(--text-primary);
  }

  .tag-badge {
    font-size: 10px;
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px dashed var(--border);
    color: var(--text-muted);
  }

  .col-outbound {
    width: 220px;
  }

  .live-dot {
    font-size: 11px;
    color: var(--text-muted);
    font-weight: 500;
  }

  .col-actions {
    width: 80px;
    text-align: right;
  }

  .row-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    justify-content: flex-end;
  }

  .act-btn {
    background: transparent;
    border: none;
    padding: 4px;
    color: var(--text-muted);
    cursor: pointer;
    border-radius: 4px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition: color var(--t-fast), background var(--t-fast);
  }

  .act-btn:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }

  .act-btn.danger:hover {
    color: var(--color-error, #ef4444);
    background: rgba(239, 68, 68, 0.1);
  }

  .empty-cell {
    padding: 24px 14px;
    text-align: center;
    color: var(--text-muted);
    font-size: 13px;
  }

  /* Sub-tabs bar for Rule Providers */
  .tabs-sub-bar {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 6px 14px;
    background: var(--bg-tertiary, rgba(255, 255, 255, 0.02));
    border-bottom: 1px solid var(--border);
  }

  .sub-tab-btn {
    background: transparent;
    border: none;
    padding: 5px 10px;
    border-radius: var(--radius-sm, 4px);
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    transition: color var(--t-fast), background var(--t-fast);
  }

  .sub-tab-btn:hover {
    color: var(--text-primary);
    background: var(--bg-hover, rgba(255, 255, 255, 0.05));
  }

  .sub-tab-btn.active {
    color: var(--text-primary);
    background: var(--bg-secondary, rgba(255, 255, 255, 0.08));
    font-weight: 600;
  }

  .tab-cnt {
    font-size: 11px;
    color: var(--text-muted);
  }

  .provider-title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .icon-muted {
    color: var(--text-muted);
  }

  .url-cell {
    max-width: 250px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Outbounds compact list */
  .outbounds-list {
    display: flex;
    flex-direction: column;
  }

  .outbound-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    transition: background var(--t-fast);
  }

  .outbound-item:last-child {
    border-bottom: none;
  }

  .outbound-item:hover {
    background: var(--bg-hover, rgba(255, 255, 255, 0.02));
  }

  .outbound-main {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 160px;
  }

  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-success, #22c55e);
  }

  .outbound-titles {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .outbound-name {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary);
  }

  .outbound-sub {
    font-size: 11px;
    color: var(--text-muted);
  }

  .outbound-runtime {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .now-badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;
    color: var(--text-secondary);
  }

  .now-lbl {
    color: var(--text-muted);
  }

  .now-val {
    font-weight: 600;
    color: var(--text-primary);
  }

  .outbound-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .font-mono {
    font-family: var(--font-mono);
  }

  .font-bold {
    font-weight: 600;
  }

  .text-secondary {
    color: var(--text-secondary);
  }

  .text-muted {
    color: var(--text-muted);
  }

  .text-xs {
    font-size: 11px;
  }

  .text-sm {
    font-size: 12px;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .spin {
    animation: spin 1s linear infinite;
  }

  .susanin-rule-row {
    background: rgba(34, 197, 94, 0.05);
    border-bottom: 1px solid rgba(34, 197, 94, 0.2);
  }

  .managed-label {
    font-size: 11px;
    color: var(--text-muted);
    font-style: italic;
  }

  /* Toolbar: Search, Density, Layout */
  .mihomo-view-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-top: 14px;
    margin-bottom: 14px;
    flex-wrap: wrap;
  }

  .toolbar-search {
    position: relative;
    display: flex;
    align-items: center;
    flex: 1;
    min-width: 260px;
    max-width: 520px;
  }

  .toolbar-search :global(.search-icon) {
    position: absolute;
    left: 10px;
    color: var(--text-muted);
    pointer-events: none;
  }

  .toolbar-search .search-input {
    width: 100%;
    padding: 7px 32px 7px 32px;
    font-size: 13px;
    border-radius: var(--radius-sm, 6px);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-primary);
    transition: border-color var(--t-fast), box-shadow var(--t-fast);
  }

  .toolbar-search .search-input:focus {
    outline: none;
    border-color: var(--color-accent, #3b82f6);
    box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.15);
  }

  .search-clear-btn {
    position: absolute;
    right: 8px;
    background: transparent;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    padding: 4px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
  }

  .search-clear-btn:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }

  .toolbar-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .toolbar-toggle-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    font-size: 12px;
    font-weight: 500;
    border-radius: var(--radius-sm, 6px);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-secondary);
    cursor: pointer;
    transition: all var(--t-fast);
  }

  .toolbar-toggle-btn:hover {
    color: var(--text-primary);
    border-color: var(--border-hover, var(--border));
    background: var(--bg-hover, rgba(255, 255, 255, 0.05));
  }

  .toolbar-toggle-btn.active {
    background: rgba(59, 130, 246, 0.12);
    border-color: var(--color-accent, #3b82f6);
    color: var(--color-accent, #3b82f6);
    font-weight: 600;
  }

  .layout-toggle-group {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm, 6px);
    overflow: hidden;
    background: var(--bg-secondary);
  }

  .layout-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 6px 10px;
    font-size: 12px;
    font-weight: 500;
    border: none;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    transition: all var(--t-fast);
  }

  .layout-btn:first-child {
    border-right: 1px solid var(--border);
  }

  .layout-btn:hover {
    color: var(--text-primary);
    background: var(--bg-hover, rgba(255, 255, 255, 0.05));
  }

  .layout-btn.active {
    background: var(--bg-tertiary, rgba(255, 255, 255, 0.1));
    color: var(--text-primary);
    font-weight: 600;
  }

  .btn-clear-search {
    margin-left: 8px;
    background: transparent;
    border: none;
    color: var(--color-accent, #3b82f6);
    text-decoration: underline;
    cursor: pointer;
    font-size: 13px;
  }

  /* Grid Layout: 2-column widescreen & 1-column stack */
  .expert-grid {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .expert-grid.layout-split {
    display: grid;
    grid-template-columns: minmax(280px, calc(var(--split-ratio, 58%) - 8px)) 16px minmax(280px, 1fr);
    gap: 0;
    align-items: start;
  }

  .expert-col-splitter {
    width: 16px;
    margin: 0;
    cursor: col-resize;
    display: flex;
    align-items: center;
    justify-content: center;
    position: relative;
    user-select: none;
    touch-action: none;
    z-index: 15;
    background: transparent;
  }

  .splitter-hover-glow {
    width: 4px;
    height: 60px;
    border-radius: 2px;
    background: transparent;
    transition: background-color 0.15s ease, height 0.15s ease, box-shadow 0.15s ease;
  }

  .expert-col-splitter:hover .splitter-hover-glow,
  .expert-col-splitter.dragging .splitter-hover-glow {
    background: var(--color-accent, #3b82f6);
    height: 90px;
    box-shadow: 0 0 10px rgba(59, 130, 246, 0.7);
  }

  .win-resize-edge-e {
    position: absolute;
    right: -4px;
    top: 0;
    bottom: 0;
    width: 8px;
    cursor: ew-resize;
    z-index: 12;
    touch-action: none;
  }

  .win-resize-edge-e:hover {
    background: rgba(59, 130, 246, 0.2);
  }

  .expert-grid.is-maximized {
    display: block !important;
  }

  .expert-col-main {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: 0;
  }

  .expert-col-side {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: 0;
  }

  .expert-col-main.is-full-width,
  .expert-col-side.is-full-width {
    width: 100% !important;
    max-width: 100% !important;
  }

  .expert-col-splitter {
    width: 16px;
    margin: 0 -2px;
    cursor: col-resize;
    display: flex;
    align-items: center;
    justify-content: center;
    position: relative;
    user-select: none;
    touch-action: none;
    z-index: 10;
    transition: background 0.15s ease;
    border-radius: 4px;
    padding: 0 2px;
    height: 100%;
    min-height: 300px;
  }

  .expert-col-splitter:hover,
  .expert-col-splitter.dragging {
    background: rgba(59, 130, 246, 0.08);
  }

  .splitter-line {
    width: 2px;
    height: 100%;
    min-height: 250px;
    background: var(--border);
    transition: background 0.15s ease, width 0.15s ease;
    border-radius: 1px;
  }

  .expert-col-splitter:hover .splitter-line,
  .expert-col-splitter.dragging .splitter-line {
    background: var(--color-accent, #3b82f6);
    width: 3px;
  }

  .splitter-handle {
    position: sticky;
    top: 40vh;
    width: 12px;
    height: 38px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: 4px;
    display: flex;
    align-items: center;
    justify-content: center;
    box-shadow: 0 1px 4px rgba(0, 0, 0, 0.2);
    z-index: 11;
  }

  .expert-col-splitter:hover .splitter-handle,
  .expert-col-splitter.dragging .splitter-handle {
    border-color: var(--color-accent, #3b82f6);
    background: var(--bg-tertiary);
  }

  .splitter-dots {
    width: 2px;
    height: 16px;
    background: repeating-linear-gradient(
      to bottom,
      var(--text-muted) 0px,
      var(--text-muted) 2px,
      transparent 2px,
      transparent 4px
    );
  }

  .panel-win-btns {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    margin-left: 6px;
  }

  .panel-win-btn {
    background: transparent;
    border: 1px solid transparent;
    border-radius: 4px;
    color: var(--text-muted);
    padding: 3px 5px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition: all 0.15s ease;
  }

  .panel-win-btn:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary, rgba(255, 255, 255, 0.08));
    border-color: var(--border);
  }

  .panel-win-btn.active {
    color: var(--color-accent, #3b82f6);
    background: rgba(59, 130, 246, 0.12);
    border-color: rgba(59, 130, 246, 0.3);
  }

  .expert-section {
    scroll-margin-top: 72px;
  }

  @media (max-width: 1080px) {
    .expert-grid.layout-split {
      display: flex;
      flex-direction: column;
      gap: 16px;
    }

    .expert-col-splitter {
      display: none;
    }
  }

  @media (max-width: 768px) {
    .layout-toggle-group {
      display: none !important;
    }
    .win-resize-edge-e,
    .win-resize-bar,
    .win-resize-corner {
      display: none !important;
    }
    .expert-grid.layout-split {
      display: flex !important;
      flex-direction: column !important;
      gap: 16px !important;
    }
    .expert-col,
    .expert-col-main,
    .expert-col-side,
    .expert-col-1,
    .expert-col-2 {
      width: 100% !important;
      max-width: 100% !important;
    }
    .toolbar-search {
      min-width: 100% !important;
      max-width: 100% !important;
    }
    .rules-table-scroll {
      max-height: 480px;
    }
  }

  /* Sticky Header & Scrollable Table for Rules */
  .rules-table-scroll {
    max-height: calc(100vh - 270px);
    min-height: 240px;
    overflow-y: auto;
    overflow-x: auto;
  }

  .rules-table-scroll thead th {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--bg-secondary, #1e222b);
    box-shadow: 0 1px 0 var(--border);
  }

  /* Compact Mode Density Reduction (~40-50% row height reduction) */
  .compact-mode .data-table th {
    padding: 5px 8px;
    font-size: 10px;
  }

  .compact-mode .data-table td {
    padding: 5px 8px;
    font-size: 12px;
  }

  .compact-mode .first-match-bar {
    padding: 4px 10px;
    font-size: 10px;
  }

  .compact-mode .tone-chip-compact {
    padding: 1px 6px;
    font-size: 10px;
    gap: 4px;
  }

  .compact-mode .order-btn {
    padding: 1px;
  }

  .compact-mode .act-btn {
    padding: 2px;
  }

  .compact-mode .tag-badge {
    font-size: 9px;
    padding: 0 4px;
  }

  .compact-mode .tabs-sub-bar {
    padding: 4px 10px;
  }

  .compact-mode .sub-tab-btn {
    padding: 3px 8px;
    font-size: 11px;
  }

  .compact-mode .empty-cell {
    padding: 14px 10px;
  }

  /* Draggable and Resizable Windows System */
  :global(body.block-dragging-active) {
    user-select: none !important;
    cursor: grabbing !important;
  }

  .block-drag-handle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 3px 4px;
    margin-right: 4px;
    color: var(--text-muted, #64748b);
    border-radius: 4px;
    cursor: grab;
    transition: color 0.15s ease, background-color 0.15s ease;
  }

  .block-drag-handle:hover {
    color: var(--text, #f1f5f9);
    background: var(--bg-hover, rgba(255, 255, 255, 0.08));
  }

  .block-drag-handle:active {
    cursor: grabbing;
  }

  .expert-section {
    position: relative;
    scroll-margin-top: 72px;
    transition: opacity 0.2s ease, transform 0.2s ease;
  }

  .expert-section.is-dragging {
    opacity: 0.35;
    transform: scale(0.985);
  }

  .expert-section.is-resizing {
    user-select: none !important;
  }

  .block-drop-indicator {
    position: absolute;
    left: 0;
    right: 0;
    height: 4px;
    background: var(--color-accent, #3b82f6);
    border-radius: 2px;
    box-shadow: 0 0 10px rgba(59, 130, 246, 0.8);
    z-index: 25;
    pointer-events: none;
    animation: pulse-drop-ind 0.9s infinite alternate ease-in-out;
  }

  .block-drop-indicator.before {
    top: -7px;
  }

  .block-drop-indicator.after {
    bottom: -7px;
  }

  @keyframes pulse-drop-ind {
    from { opacity: 0.7; transform: scaleY(0.9); }
    to { opacity: 1; transform: scaleY(1.4); }
  }

  .col-drop-zone {
    min-height: 44px;
    border: 2px dashed transparent;
    border-radius: var(--radius, 8px);
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.2s ease;
    margin-top: 4px;
  }

  .col-drop-zone.is-active {
    border-color: var(--color-accent, #3b82f6);
    background: rgba(59, 130, 246, 0.08);
  }

  .drop-zone-content {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-weight: 500;
    color: var(--color-accent, #3b82f6);
  }

  /* Windows OS-style bottom resize bar and corner grip */
  .win-resize-bar {
    position: relative;
    height: 14px;
    margin-top: -6px;
    cursor: ns-resize;
    display: flex;
    align-items: center;
    justify-content: center;
    user-select: none;
    touch-action: none;
    z-index: 10;
  }

  .win-resize-line {
    position: absolute;
    left: 12px;
    right: 26px;
    height: 2px;
    background: transparent;
    border-radius: 1px;
    transition: background-color 0.15s ease;
  }

  .win-resize-pill {
    width: 36px;
    height: 4px;
    border-radius: 2px;
    background: rgba(148, 163, 184, 0.25);
    transition: all 0.15s ease;
    z-index: 2;
  }

  .win-resize-bar:hover .win-resize-line,
  .expert-section.is-resizing .win-resize-line {
    background: rgba(59, 130, 246, 0.35);
  }

  .win-resize-bar:hover .win-resize-pill,
  .expert-section.is-resizing .win-resize-pill {
    background: var(--color-accent, #3b82f6);
    width: 52px;
    height: 5px;
    box-shadow: 0 0 8px rgba(59, 130, 246, 0.5);
  }

  .win-resize-corner {
    position: absolute;
    right: 4px;
    bottom: 2px;
    width: 16px;
    height: 16px;
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: nwse-resize;
    color: rgba(148, 163, 184, 0.4);
    transition: color 0.15s ease, transform 0.15s ease;
    z-index: 12;
  }

  .win-resize-corner:hover,
  .expert-section.is-resizing .win-resize-corner {
    color: var(--color-accent, #3b82f6);
    transform: scale(1.15);
  }

  /* Custom height expanding like Windows OS window */
  .expert-section.has-custom-height {
    display: flex;
    flex-direction: column;
  }

  .expert-section.has-custom-height :global(.panel) {
    height: 100%;
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
  }

  .expert-section.has-custom-height :global(.panel > .body) {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .expert-section.has-custom-height :global(.rules-table-scroll),
  .expert-section.has-custom-height :global(.outbounds-list),
  .expert-section.has-custom-height :global(.table-wrap),
  .expert-section.has-custom-height :global(.data-table-scroll),
  .expert-section.has-custom-height :global(.dns-content-wrap) {
    flex: 1 !important;
    min-height: 0 !important;
    max-height: none !important;
    overflow-y: auto !important;
  }

  .expert-col,
  .expert-col-main,
  .expert-col-side,
  .expert-col-1,
  .expert-col-2,
  .expert-col-stack {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
  }

  .expert-col.is-full-width,
  .expert-col-1.is-full-width,
  .expert-col-2.is-full-width,
  .expert-col-stack.is-full-width {
    grid-column: 1 / -1 !important;
    width: 100% !important;
  }

</style>
