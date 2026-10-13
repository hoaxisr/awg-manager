<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { Button, ConfirmModal, SectionLabel } from '$lib/components/ui';
  import { LoadingSpinner } from '$lib/components/layout';
  import { Sparkles, GripVertical, RotateCcw, Maximize2, Minimize2, Columns, LayoutGrid, Plus } from 'lucide-svelte';
  import FlowGraph from '../FlowGraph.svelte';
  import { openAddWizard } from '../addWizardStore';
  import type {
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeRule,
    MihomoNativeRuleProvider,
    MihomoNativeSubscription,
    MihomoRuntimeProxy,
  } from '$lib/types';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import MihomoRuleCard, { type MihomoBeginnerGroupedCard } from './MihomoRuleCard.svelte';
  import MihomoRuleEditModal from './MihomoRuleEditModal.svelte';
  import MihomoGroupEditModal from './MihomoGroupEditModal.svelte';
  import MihomoTemplateModal from './MihomoTemplateModal.svelte';
  import MihomoProxyGroupCard from './MihomoProxyGroupCard.svelte';

  interface Props {
    rules: MihomoNativeRule[];
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    ruleProviders: MihomoNativeRuleProvider[];
    runtimeProxies: MihomoRuntimeProxy[];
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
    loading = false,
    onReload,
  }: Props = $props();

  onMount(() => {
    void subscriptionsStore.refetch();
    loadBeginnerLayout();
  });

  let editModalOpen = $state(false);
  let editingRule = $state<MihomoNativeRule | null>(null);
  let groupModalOpen = $state(false);
  let editingGroup = $state<MihomoNativeGroup | null>(null);
  let deleteConfirmOpen = $state(false);
  let deletingRules = $state<MihomoNativeRule[]>([]);
  let deletingGroup = $state<MihomoNativeGroup | null>(null);

  let templateModalOpen = $state(false);

  function handleOpenTemplates() {
    templateModalOpen = true;
  }

  // Drag & drop state for reordering
  let draggedIndex = $state<number | null>(null);
  let dragOverIndex = $state<number | null>(null);

  function parseServiceMeta(type: string, payload: string = '') {
    const p = (payload || '').toLowerCase();
    const chipKind: 'ip' | 'ruleset' = type === 'GEOIP' ? 'ip' : 'ruleset';

    if (p.includes('youtube')) {
      return {
        serviceKey: 'youtube',
        title: 'YouTube',
        subtitle: m.mihomo_svc_youtube_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: youtube' : m.mihomo_chip_ruleset({ name: 'youtube' }) }],
      };
    }
    if (p.includes('telegram')) {
      return {
        serviceKey: 'telegram',
        title: 'Telegram',
        subtitle: m.mihomo_svc_telegram_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: telegram' : m.mihomo_chip_ruleset({ name: 'telegram' }) }],
      };
    }
    if (p.includes('discord')) {
      return {
        serviceKey: 'discord',
        title: 'Discord',
        subtitle: m.mihomo_svc_discord_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: discord' : m.mihomo_chip_ruleset({ name: 'discord' }) }],
      };
    }
    if (p.includes('instagram')) {
      return {
        serviceKey: 'instagram',
        title: 'Instagram',
        subtitle: m.mihomo_svc_instagram_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: instagram' : m.mihomo_chip_ruleset({ name: 'instagram' }) }],
      };
    }
    if (p.includes('gemini') || p.includes('google-gemini')) {
      return {
        serviceKey: 'gemini',
        title: 'Gemini',
        subtitle: m.mihomo_svc_gemini_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: google-gemini' : m.mihomo_chip_ruleset({ name: 'google-gemini' }) }],
      };
    }
    if (p.includes('anthropic') || p.includes('claude')) {
      return {
        serviceKey: 'anthropic',
        title: 'Claude / Anthropic',
        subtitle: m.mihomo_svc_ai_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: anthropic' : m.mihomo_chip_ruleset({ name: 'anthropic' }) }],
      };
    }
    if (p.includes('openai') || p.includes('chatgpt')) {
      return {
        serviceKey: 'chatgpt',
        title: 'ChatGPT / OpenAI',
        subtitle: m.mihomo_svc_ai_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: openai' : m.mihomo_chip_ruleset({ name: 'openai' }) }],
      };
    }
    if (p.includes('xai') || p.includes('grok')) {
      return {
        serviceKey: 'grok',
        title: 'Grok (xAI)',
        subtitle: m.mihomo_svc_ai_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: xai' : m.mihomo_chip_ruleset({ name: 'xai' }) }],
      };
    }
    if (p.includes('tiktok')) {
      return {
        serviceKey: 'tiktok',
        title: 'TikTok',
        subtitle: m.mihomo_svc_tiktok_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: tiktok' : m.mihomo_chip_ruleset({ name: 'tiktok' }) }],
      };
    }
    if (p.includes('spotify')) {
      return {
        serviceKey: 'spotify',
        title: 'Spotify',
        subtitle: m.mihomo_svc_spotify_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: spotify' : m.mihomo_chip_ruleset({ name: 'spotify' }) }],
      };
    }
    if (p.includes('steam')) {
      return {
        serviceKey: 'steam',
        title: 'Steam',
        subtitle: m.mihomo_svc_steam_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: steam' : m.mihomo_chip_ruleset({ name: 'steam' }) }],
      };
    }
    if (p.includes('netflix')) {
      return {
        serviceKey: 'netflix',
        title: 'Netflix',
        subtitle: m.mihomo_svc_netflix_sub(),
        chips: [{ kind: chipKind, label: type === 'GEOIP' ? 'geoip: netflix' : m.mihomo_chip_ruleset({ name: 'netflix' }) }],
      };
    }
    if (p.includes('category-ru') || p.includes('russian-services')) {
      return {
        serviceKey: 'russian-services',
        title: m.mihomo_svc_ru_title(),
        subtitle: m.mihomo_svc_ru_sub(),
        chips: [{ kind: chipKind, label: 'geosite: category-ru' }],
      };
    }
    if (p.includes('category-media-ru-blocked') || p.includes('rkn') || p.includes('all-blocked') || p.includes('unavailable-in-russia')) {
      return {
        serviceKey: 'rkn',
        title: m.mihomo_svc_rkn_title(),
        subtitle: m.mihomo_svc_rkn_sub(),
        chips: [{ kind: chipKind, label: 'geosite: category-media-ru-blocked' }],
      };
    }
    if (p.includes('category-ads-all') || p.includes('adblock') || p === 'ads') {
      return {
        serviceKey: 'ads',
        title: m.mihomo_svc_ads_title(),
        subtitle: m.mihomo_svc_ads_sub(),
        chips: [{ kind: chipKind, label: 'geosite: category-ads-all' }],
      };
    }

    if (type.startsWith('IP-CIDR') || type === 'GEOIP' || type === 'IP-ASN') {
      return {
        title: payload || type,
        subtitle: m.mihomo_svc_ip_sub(),
        chips: [{ kind: 'ip' as const, label: type === 'GEOIP' ? `geoip: ${payload}` : payload }],
      };
    }

    if (type.startsWith('DOMAIN')) {
      return {
        title: payload || type,
        subtitle: m.mihomo_svc_domain_sub(),
        chips: [{ kind: 'domain' as const, label: payload }],
      };
    }

    if (type === 'GEOSITE') {
      return {
        title: payload || type,
        subtitle: m.mihomo_svc_geosite_sub(),
        chips: [{ kind: 'ruleset' as const, label: m.mihomo_chip_ruleset({ name: payload }) }],
      };
    }

    if (type === 'RULE-SET') {
      return {
        title: payload || type,
        subtitle: m.mihomo_svc_ruleset_sub(),
        chips: [{ kind: 'ruleset' as const, label: `rule-set: ${payload}` }],
      };
    }

    return {
      title: payload || type,
      subtitle: type,
      chips: [{ kind: 'custom' as const, label: `${type}: ${payload}` }],
    };
  }

  function groupRulesForBeginner(ruleList: MihomoNativeRule[]): MihomoBeginnerGroupedCard[] {
    const cards: MihomoBeginnerGroupedCard[] = [];

    for (const rule of ruleList) {
      const parsed = parseServiceMeta(rule.type, rule.payload || '');
      const last = cards[cards.length - 1];

      // Merge into last card if same service and same outbound
      if (
        last &&
        parsed.serviceKey &&
        last.serviceKey === parsed.serviceKey &&
        last.outbound === rule.outbound
      ) {
        last.rules.push(rule);
        for (const chip of parsed.chips) {
          if (!last.chips.some((c) => c.kind === chip.kind && c.label === chip.label)) {
            last.chips.push(chip);
          }
        }
        if (rule.noResolve) last.hasNoResolve = true;
        if (!rule.enabled) last.enabled = false;
      } else {
        cards.push({
          id: rule.id || `${parsed.serviceKey || 'rule'}-${cards.length}`,
          rules: [rule],
          serviceKey: parsed.serviceKey,
          title: parsed.title,
          subtitle: parsed.subtitle,
          chips: [...parsed.chips],
          outbound: rule.outbound,
          enabled: rule.enabled ?? true,
          hasNoResolve: !!rule.noResolve,
        });
      }
    }

    return cards;
  }

  const groupedCards = $derived(groupRulesForBeginner(rules));

  function handleOpenAdd() {
    openAddWizard();
  }

  function handleEditRule(rule: MihomoNativeRule) {
    editingRule = rule;
    editModalOpen = true;
  }

  function handleDeleteCard(rulesToDelete: MihomoNativeRule[]) {
    deletingRules = rulesToDelete;
    deletingGroup = null;
    deleteConfirmOpen = true;
  }

  function handleDeleteGroup(groupId: string) {
    const target = groups.find((g) => g.id === groupId || g.name === groupId);
    if (target) {
      deletingGroup = target;
      deletingRules = [];
      deleteConfirmOpen = true;
    }
  }

  async function confirmDelete() {
    if (deletingGroup) {
      try {
        if (deletingGroup.id) {
          await api.mihomoNativeDeleteGroup(deletingGroup.id, true);
        }
        notifications.success(m.mihomo_group_deleted_success({ name: deletingGroup.name }));
        onReload();
      } catch (e) {
        notifications.error(e instanceof Error ? e.message : m.mihomo_group_delete_error());
      } finally {
        deletingGroup = null;
        deleteConfirmOpen = false;
      }
      return;
    }

    if (deletingRules.length === 0) return;
    try {
      for (let i = 0; i < deletingRules.length; i++) {
        const r = deletingRules[i];
        const isLast = i === deletingRules.length - 1;
        if (r.id) {
          await api.mihomoNativeDeleteRule(r.id, isLast);
        }
      }
      notifications.success(deletingRules.length > 1 ? m.mihomo_rules_deleted_success() : m.mihomo_rule_deleted_success());
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.mihomo_rules_delete_error());
    } finally {
      deletingRules = [];
      deleteConfirmOpen = false;
    }
  }

  async function handleToggleCard(card: MihomoBeginnerGroupedCard) {
    try {
      const nextEnabled = !card.enabled;
      for (let i = 0; i < card.rules.length; i++) {
        const r = card.rules[i];
        const isLast = i === card.rules.length - 1;
        await api.mihomoNativeSaveRule({
          ...r,
          enabled: nextEnabled,
        }, isLast);
      }
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.mihomo_rules_toggle_error());
    }
  }

  // Drag and drop reordering
  function handleDragStart(index: number) {
    draggedIndex = index;
  }

  function handleDragOver(e: DragEvent, index: number) {
    e.preventDefault();
    dragOverIndex = index;
  }

  async function handleDrop(targetCardIndex: number) {
    if (draggedIndex === null || draggedIndex === targetCardIndex) {
      draggedIndex = null;
      dragOverIndex = null;
      return;
    }

    const reorderedCards = [...groupedCards];
    const [moved] = reorderedCards.splice(draggedIndex, 1);
    reorderedCards.splice(targetCardIndex, 0, moved);

    draggedIndex = null;
    dragOverIndex = null;

    try {
      const allReorderedRules = reorderedCards.flatMap((c) => c.rules);
      const ids = allReorderedRules.map((r) => r.id);
      await api.mihomoNativeReorderRules(ids);
      await api.mihomoReload();
      notifications.success(m.mihomo_rules_reordered_success());
      onReload();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.mihomo_rules_reorder_error());
    }
  }

  const uniqueOutbounds = $derived.by(() => {
    const set = new Set<string>();
    for (const r of rules) {
      if (r.outbound && r.outbound !== 'DIRECT' && r.outbound !== 'direct' && r.outbound !== 'REJECT') {
        set.add(r.outbound);
      }
    }
    return Array.from(set);
  });

  // Desktop-window arrangement and resizing system
  type SectionKey = 'rules' | 'groups' | 'flow';

  const STORAGE_KEY_COLUMNS = 'awgm.mihomo.beginner.block-columns-v1';
  const STORAGE_KEY_HEIGHTS = 'awgm.mihomo.beginner.block-heights-v1';
  const STORAGE_KEY_LAYOUT = 'awgm.mihomo.beginner.layout-mode-v1';

  const STORAGE_KEY_SPLIT = 'awgm.mihomo.beginner.col-split-v1';
  let layoutMode = $state<'split' | 'stack'>('split');
  let splitRatio = $state<number>(55);
  let isDraggingSplitter = $state(false);
  let gridContainerEl = $state<HTMLDivElement | null>(null);
  let col1Sections = $state<SectionKey[]>(['rules']);
  let col2Sections = $state<SectionKey[]>(['flow', 'groups']);
  let customHeights = $state<Partial<Record<SectionKey, number>>>({});
  let maximizedSection = $state<SectionKey | null>(null);

  let draggedSection = $state<SectionKey | null>(null);
  let dragTarget = $state<{ section: SectionKey; position: 'before' | 'after' } | null>(null);
  let dragOverCol = $state<1 | 2 | null>(null);

  let resizingSection = $state<SectionKey | null>(null);
  let resizeStartY = 0;
  let resizeStartH = 0;

  function loadBeginnerLayout() {
    try {
      const savedSplit = localStorage.getItem(STORAGE_KEY_SPLIT);
      if (savedSplit) {
        const val = parseFloat(savedSplit);
        if (!isNaN(val) && val >= 25 && val <= 75) splitRatio = val;
      }
      const savedLayout = localStorage.getItem(STORAGE_KEY_LAYOUT);
      if (savedLayout === 'split' || savedLayout === 'stack') {
        layoutMode = savedLayout;
      }
      const savedCols = localStorage.getItem(STORAGE_KEY_COLUMNS);
      if (savedCols) {
        const parsed = JSON.parse(savedCols) as { col1?: SectionKey[]; col2?: SectionKey[] };
        const allKeys: SectionKey[] = ['rules', 'groups', 'flow'];
        if (Array.isArray(parsed.col1) && Array.isArray(parsed.col2)) {
          const present = new Set([...parsed.col1, ...parsed.col2]);
          const c1 = parsed.col1.filter((k) => allKeys.includes(k));
          const c2 = parsed.col2.filter((k) => allKeys.includes(k));
          for (const k of allKeys) {
            if (!present.has(k)) c2.push(k);
          }
          col1Sections = c1;
          col2Sections = c2;
        }
      }
      const savedHeights = localStorage.getItem(STORAGE_KEY_HEIGHTS);
      if (savedHeights) {
        customHeights = JSON.parse(savedHeights);
      }
    } catch {
      // ignore
    }
  }

  function saveBlockLayout() {
    try {
      localStorage.setItem(STORAGE_KEY_COLUMNS, JSON.stringify({ col1: col1Sections, col2: col2Sections }));
    } catch {}
  }

  function saveHeights() {
    try {
      localStorage.setItem(STORAGE_KEY_HEIGHTS, JSON.stringify(customHeights));
    } catch {}
  }

  function setLayoutMode(mode: 'split' | 'stack') {
    layoutMode = mode;
    try {
      localStorage.setItem(STORAGE_KEY_LAYOUT, mode);
    } catch {}
  }

  function toggleMaximize(sec: SectionKey) {
    maximizedSection = maximizedSection === sec ? null : sec;
  }

  function resetSectionHeight(sec: SectionKey) {
    const next = { ...customHeights };
    delete next[sec];
    customHeights = next;
    saveHeights();
  }

  function resetAllLayout() {
    col1Sections = ['rules'];
    col2Sections = ['flow', 'groups'];
    customHeights = {};
    splitRatio = 55;
    layoutMode = 'split';
    maximizedSection = null;
    try {
      localStorage.removeItem(STORAGE_KEY_SPLIT);
      localStorage.removeItem(STORAGE_KEY_LAYOUT);
      localStorage.removeItem(STORAGE_KEY_COLUMNS);
      localStorage.removeItem(STORAGE_KEY_HEIGHTS);
    } catch {}
    notifications.success(m.mihomo_layout_reset_success());
  }

  function handleBlockDragStart(e: DragEvent, secKey: SectionKey) {
    e.stopPropagation();
    draggedSection = secKey;
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', `beginner-section:${secKey}`);
    }
    document.body.classList.add('block-dragging-active');
  }

  function handleBlockDragEnd() {
    draggedSection = null;
    dragTarget = null;
    dragOverCol = null;
    document.body.classList.remove('block-dragging-active');
  }

  function handleBlockDragOver(e: DragEvent, targetKey: SectionKey, colIndex: 1 | 2) {
    if (!draggedSection || draggedSection === targetKey) return;
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';

    const targetEl = e.currentTarget as HTMLElement | null;
    if (!targetEl) return;
    const rect = targetEl.getBoundingClientRect();
    const midY = rect.top + rect.height / 2;
    const position = e.clientY < midY ? 'before' : 'after';

    dragTarget = { section: targetKey, position };
    dragOverCol = colIndex;
  }

  function handleBlockDragLeave(e: DragEvent, targetKey: SectionKey) {
    if (dragTarget?.section === targetKey) {
      const rel = e.relatedTarget as Node | null;
      const cur = e.currentTarget as HTMLElement | null;
      if (cur && rel && cur.contains(rel)) return;
      dragTarget = null;
    }
  }

  function handleBlockDrop(e: DragEvent, targetKey: SectionKey, colIndex: 1 | 2) {
    if (!draggedSection || draggedSection === targetKey) return;
    e.preventDefault();
    e.stopPropagation();

    const moving = draggedSection;
    const pos = dragTarget?.position ?? 'after';

    let c1 = col1Sections.filter((k) => k !== moving);
    let c2 = col2Sections.filter((k) => k !== moving);

    const targetCol = colIndex === 1 ? c1 : c2;
    const idx = targetCol.indexOf(targetKey);
    if (idx !== -1) {
      const insertAt = pos === 'before' ? idx : idx + 1;
      targetCol.splice(insertAt, 0, moving);
    } else {
      targetCol.push(moving);
    }

    col1Sections = c1;
    col2Sections = c2;
    saveBlockLayout();
    handleBlockDragEnd();
  }

  function handleColDragOver(e: DragEvent, colIndex: 1 | 2) {
    if (!draggedSection) return;
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    dragOverCol = colIndex;
  }

  function handleColDragLeave() {
    dragOverCol = null;
  }

  function handleColDrop(e: DragEvent, colIndex: 1 | 2) {
    if (!draggedSection) return;
    e.preventDefault();
    e.stopPropagation();

    const moving = draggedSection;
    let c1 = col1Sections.filter((k) => k !== moving);
    let c2 = col2Sections.filter((k) => k !== moving);

    if (colIndex === 1) {
      c1.push(moving);
    } else {
      c2.push(moving);
    }

    col1Sections = c1;
    col2Sections = c2;
    saveBlockLayout();
    handleBlockDragEnd();
  }


  function handleSplitResize(clientX: number) {
    if (!gridContainerEl) return;
    const rect = gridContainerEl.getBoundingClientRect();
    const relX = clientX - rect.left;
    const pct = Math.min(75, Math.max(25, (relX / rect.width) * 100));
    splitRatio = Math.round(pct * 10) / 10;
    try {
      localStorage.setItem(STORAGE_KEY_SPLIT, String(splitRatio));
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
      if (!isDraggingSplitter) return;
      handleSplitResize(ev.clientX);
    };

    const onPointerUp = (ev: PointerEvent) => {
      isDraggingSplitter = false;
      try {
        target.releasePointerCapture(ev.pointerId);
      } catch {}
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerUp);
    };

    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    window.addEventListener('pointercancel', onPointerUp);
  }

  function resetSplitter() {
    splitRatio = 55;
    try {
      localStorage.setItem(STORAGE_KEY_SPLIT, '55');
    } catch {}
  }

  function handleStartResize(e: PointerEvent, secKey: SectionKey, dir: 's' | 'e' | 'se', colIndex?: 1 | 2) {
    e.preventDefault();
    e.stopPropagation();
    const targetEl = e.currentTarget as HTMLElement | null;
    if (!targetEl) return;
    const handleEl: HTMLElement = targetEl;
    const sectionEl = handleEl.closest('.expert-section') as HTMLElement | null;
    if (!sectionEl) return;

    resizingSection = secKey;
    resizeStartY = e.clientY;
    resizeStartH = sectionEl.getBoundingClientRect().height;

    handleEl.setPointerCapture(e.pointerId);

    function onPointerMove(ev: PointerEvent) {
      if (resizingSection !== secKey) return;
      if (dir === 's' || dir === 'se') {
        const deltaY = ev.clientY - resizeStartY;
        const newHeight = Math.max(180, Math.round(resizeStartH + deltaY));
        customHeights = { ...customHeights, [secKey]: newHeight };
      }
      if ((dir === 'e' || dir === 'se') && layoutMode === 'split' && !maximizedSection && gridContainerEl) {
        handleSplitResize(ev.clientX);
      }
    }

    function onPointerUp(ev: PointerEvent) {
      if (handleEl.hasPointerCapture(ev.pointerId)) {
        handleEl.releasePointerCapture(ev.pointerId);
      }
      handleEl.removeEventListener('pointermove', onPointerMove);
      handleEl.removeEventListener('pointerup', onPointerUp);
      handleEl.removeEventListener('pointercancel', onPointerUp);
      resizingSection = null;
      saveHeights();
    }

    handleEl.addEventListener('pointermove', onPointerMove);
    handleEl.addEventListener('pointerup', onPointerUp);
    handleEl.addEventListener('pointercancel', onPointerUp);
  }

</script>

<div class="beginner-view">
  <!-- View Toolbar: Layout Toggle & Reset Windows -->
  <div class="beginner-view-toolbar">
    <div class="toolbar-title-hint">
      <span class="mode-tag">{m.mihomo_beg_mode_tag()}</span>
      <span class="mode-sub">{m.mihomo_beg_mode_sub()}</span>
    </div>
    <div class="toolbar-actions">
      <div class="layout-toggle-group">
        <button
          type="button"
          class="layout-btn"
          class:active={layoutMode === 'split'}
          onclick={() => setLayoutMode('split')}
          title={m.mihomo_win_2col_title()}
        >
          <Columns size={14} />
          <span class="btn-text">{m.mihomo_win_2col_btn()}</span>
        </button>
        <button
          type="button"
          class="layout-btn"
          class:active={layoutMode === 'stack'}
          onclick={() => setLayoutMode('stack')}
          title={m.mihomo_win_1col_title()}
        >
          <LayoutGrid size={14} />
          <span class="btn-text">{m.mihomo_win_1col_btn()}</span>
        </button>
      </div>

      <button
        type="button"
        class="toolbar-toggle-btn"
        onclick={resetAllLayout}
        title={m.mihomo_win_reset_all_title()}
      >
        <RotateCcw size={14} />
        <span>{m.mihomo_win_reset_all_btn()}</span>
      </button>
    </div>
  </div>

  {#snippet renderFlow()}
    <div class="beginner-card-box">
      <div class="section-card-header">
        <div class="section-card-title-wrap">
          <div
            class="block-drag-handle"
            draggable="true"
            ondragstart={(e) => handleBlockDragStart(e, 'flow')}
            ondragend={handleBlockDragEnd}
            title={m.mihomo_beg_flow_drag_title()}
            role="button"
            tabindex="0"
            aria-label={m.mihomo_beg_flow_drag_aria()}
          >
            <GripVertical size={14} class="drag-grip-icon" />
          </div>
          <div>
            <div class="section-card-title">{m.mihomo_beg_flow_title()}</div>
            <div class="section-card-sub">{m.mihomo_beg_flow_sub()}</div>
          </div>
        </div>
        <div class="panel-win-btns">
          {#if customHeights['flow']}
            <button
              type="button"
              class="panel-win-btn"
              onclick={() => resetSectionHeight('flow')}
              title={m.mihomo_win_reset_height_title()}
              aria-label={m.mihomo_win_reset_height_aria()}
            >
              <RotateCcw size={12} />
            </button>
          {/if}
          <button
            type="button"
            class="panel-win-btn"
            class:active={maximizedSection === 'flow'}
            onclick={() => toggleMaximize('flow')}
            title={maximizedSection === 'flow' ? m.mihomo_win_restore_title() : m.mihomo_win_maximize_title()}
            aria-label={maximizedSection === 'flow' ? m.mihomo_win_restore_aria() : m.mihomo_win_maximize_aria()}
          >
            {#if maximizedSection === 'flow'}
              <Minimize2 size={13} />
            {:else}
              <Maximize2 size={13} />
            {/if}
          </button>
        </div>
      </div>
      <div class="flow-inner-scroll">
        <FlowGraph
          isMihomo={true}
          mihomoRulesCount={rules.length}
          mihomoGroupsCount={groups.length}
          mihomoTopGroup={groups[0]?.name || ''}
          mihomoOutbounds={uniqueOutbounds}
        />
      </div>
    </div>
  {/snippet}

  {#snippet renderGroups()}
    <div class="beginner-card-box">
      <div class="section-card-header">
        <div class="section-card-title-wrap">
          <div
            class="block-drag-handle"
            draggable="true"
            ondragstart={(e) => handleBlockDragStart(e, 'groups')}
            ondragend={handleBlockDragEnd}
            title={m.mihomo_beg_groups_drag_title()}
            role="button"
            tabindex="0"
            aria-label={m.mihomo_beg_groups_drag_aria()}
          >
            <GripVertical size={14} class="drag-grip-icon" />
          </div>
          <div>
            <div class="section-card-title">{m.mihomo_beg_groups_title()}</div>
            <div class="section-card-sub">{m.mihomo_beg_groups_sub({ count: groups.length })}</div>
          </div>
        </div>
        <div class="panel-win-btns">
          {#if customHeights['groups']}
            <button
              type="button"
              class="panel-win-btn"
              onclick={() => resetSectionHeight('groups')}
              title={m.mihomo_win_reset_height_title()}
              aria-label={m.mihomo_win_reset_height_aria()}
            >
              <RotateCcw size={12} />
            </button>
          {/if}
          <button
            type="button"
            class="panel-win-btn"
            class:active={maximizedSection === 'groups'}
            onclick={() => toggleMaximize('groups')}
            title={maximizedSection === 'groups' ? m.mihomo_win_restore_title() : m.mihomo_win_maximize_title()}
            aria-label={maximizedSection === 'groups' ? m.mihomo_win_restore_aria() : m.mihomo_win_maximize_aria()}
          >
            {#if maximizedSection === 'groups'}
              <Minimize2 size={13} />
            {:else}
              <Maximize2 size={13} />
            {/if}
          </button>
        </div>
      </div>
      <div class="groups-inner-scroll">
        {#if groups.length === 0}
          <div class="empty-compact">{m.mihomo_beg_groups_empty()}</div>
        {:else}
          <div class="groups-list">
            {#each groups as group (group.id || group.name)}
              <MihomoProxyGroupCard
                {group}
                {runtimeProxies}
                {subscriptions}
                onEdit={(g) => {
                  editingGroup = g;
                  groupModalOpen = true;
                }}
                onDelete={handleDeleteGroup}
                {onReload}
              />
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/snippet}

  {#snippet renderRules()}
    <div class="beginner-card-box">
      <header class="panel-header">
        <div class="section-card-title-wrap">
          <div
            class="block-drag-handle"
            draggable="true"
            ondragstart={(e) => handleBlockDragStart(e, 'rules')}
            ondragend={handleBlockDragEnd}
            title={m.mihomo_beg_rules_drag_title()}
            role="button"
            tabindex="0"
            aria-label={m.mihomo_beg_rules_drag_aria()}
          >
            <GripVertical size={14} class="drag-grip-icon" />
          </div>
          <div class="title-group">
            <h2 class="title">{m.mihomo_beg_rules_title()}</h2>
            <p class="sub">{m.mihomo_beg_rules_sub()}</p>
          </div>
        </div>

        <div class="header-right">
          <div class="counter">
            {m.mihomo_rules_count({ count: groupedCards.length })}
          </div>
          <Button variant="secondary" size="sm" onclick={handleOpenTemplates}>
            {#snippet iconBefore()}
              <Sparkles size={14} aria-hidden="true" />
            {/snippet}
            {m.mihomo_templates_btn()}
          </Button>
          <Button variant="primary" size="sm" onclick={handleOpenAdd}>
            {m.mihomo_add_rule_short_btn()}
          </Button>
          <div class="panel-win-btns">
            {#if customHeights['rules']}
              <button
                type="button"
                class="panel-win-btn"
                onclick={() => resetSectionHeight('rules')}
                title={m.mihomo_win_reset_height_title()}
                aria-label={m.mihomo_win_reset_height_aria()}
              >
                <RotateCcw size={12} />
              </button>
            {/if}
            <button
              type="button"
              class="panel-win-btn"
              class:active={maximizedSection === 'rules'}
              onclick={() => toggleMaximize('rules')}
              title={maximizedSection === 'rules' ? m.mihomo_win_restore_title() : m.mihomo_win_maximize_title()}
              aria-label={maximizedSection === 'rules' ? m.mihomo_win_restore_aria() : m.mihomo_win_maximize_aria()}
            >
              {#if maximizedSection === 'rules'}
                <Minimize2 size={13} />
              {:else}
                <Maximize2 size={13} />
              {/if}
            </button>
          </div>
        </div>
      </header>

      <div class="rules-inner-scroll">
        {#if loading && groupedCards.length === 0}
          <div class="loading-box">
            <LoadingSpinner size="md" />
          </div>
        {:else if groupedCards.length === 0}
          <div class="empty">
            <SectionLabel>{m.mihomo_beg_rules_empty_title()}</SectionLabel>
            <p class="empty-text">
              {m.mihomo_beg_rules_empty_desc()}
            </p>
            <div class="empty-action">
              <Button variant="secondary" size="sm" onclick={handleOpenTemplates}>
                {#snippet iconBefore()}
                  <Sparkles size={14} aria-hidden="true" />
                {/snippet}
                {m.mihomo_templates_btn()}
              </Button>
              <Button variant="primary" size="sm" onclick={handleOpenAdd}>
                {m.mihomo_beg_create_wizard_btn()}
              </Button>
            </div>
          </div>
        {:else}
          <div class="cards" role="list">
            {#each groupedCards as card, i (card.id || i)}
              <div
                class="card-shell"
                class:is-drag-over={dragOverIndex === i}
                draggable="true"
                ondragstart={() => handleDragStart(i)}
                ondragover={(e) => handleDragOver(e, i)}
                ondrop={() => handleDrop(i)}
                role="listitem"
              >
                <MihomoRuleCard
                  {card}
                  index={i}
                  total={groupedCards.length}
                  {runtimeProxies}
                  onEdit={handleEditRule}
                  onDelete={handleDeleteCard}
                  onToggle={handleToggleCard}
                />
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/snippet}

  {#snippet renderSection(secKey: SectionKey, colIndex: 1 | 2)}
    <div
      id={`section-${secKey}`}
      class="expert-section"
      role="region"
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
      {:else if secKey === 'groups'}
        {@render renderGroups()}
      {:else if secKey === 'flow'}
        {@render renderFlow()}
      {/if}

      {#if dragTarget?.section === secKey && dragTarget.position === 'after'}
        <div class="block-drop-indicator after"></div>
      {/if}

      <!-- Right window resize edge (Windows OS window width) -->
      {#if layoutMode === 'split' && colIndex === 1 && !maximizedSection}
        <div
          class="win-resize-edge-e"
          role="separator"
          aria-orientation="vertical"
          onpointerdown={(e) => handleStartResize(e, secKey, 'e', colIndex)}
          title={m.mihomo_win_resize_e_title()}
        ></div>
      {/if}

      <!-- Bottom window resize bar & corner grip (like Windows OS window) -->
      <div
        class="win-resize-bar"
        onpointerdown={(e) => handleStartResize(e, secKey, 's')}
        ondblclick={() => resetSectionHeight(secKey)}
        title={m.mihomo_win_resize_s_title()}
        role="separator"
        aria-label={m.mihomo_win_resize_s_aria()}
      >
        <div class="win-resize-line"></div>
        <div class="win-resize-pill"></div>
        <div
          class="win-resize-corner"
          role="separator"
          onpointerdown={(e) => handleStartResize(e, secKey, 'se')}
          title={m.mihomo_win_resize_se_title()}
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
    class="beginner-grid"
    class:layout-split={layoutMode === 'split'}
    class:is-maximized={Boolean(maximizedSection)}
    style={layoutMode === 'split' && !maximizedSection ? `--split-ratio: ${splitRatio}%;` : undefined}
  >
    {#if layoutMode === 'split'}
      <!-- Column 1 (Left): Droppable Column -->
      {#if !maximizedSection || col1Sections.includes(maximizedSection)}
        <div class="beginner-col beginner-col-1" class:is-full-width={Boolean(maximizedSection)}>
          {#each col1Sections as secKey (secKey)}
            {#if !maximizedSection || maximizedSection === secKey}
              {@render renderSection(secKey, 1)}
            {/if}
          {/each}
          {#if !maximizedSection}
            <div
              class="col-drop-zone"
              role="region"
              class:is-active={draggedSection && dragOverCol === 1}
              ondragover={(e) => handleColDragOver(e, 1)}
              ondragleave={handleColDragLeave}
              ondrop={(e) => handleColDrop(e, 1)}
            >
              {#if draggedSection && dragOverCol === 1}
                <div class="drop-zone-content">
                  <Plus size={14} />
                  <span>{m.mihomo_win_move_left_col()}</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}

      <!-- Invisible Column Width Resizer (NO static line) -->
      {#if !maximizedSection}
        <div
          class="beginner-col-splitter"
          class:dragging={isDraggingSplitter}
          role="separator"
          aria-orientation="vertical"
          aria-label={m.mihomo_win_splitter_aria()}
          onpointerdown={handleSplitterPointerDown}
          ondblclick={resetSplitter}
          title={m.mihomo_win_splitter_55_title()}
        >
          <div class="splitter-hover-glow"></div>
        </div>
      {/if}

      <!-- Column 2 (Right): Droppable Column -->
      {#if !maximizedSection || col2Sections.includes(maximizedSection)}
        <div class="beginner-col beginner-col-2" class:is-full-width={Boolean(maximizedSection)}>
          {#each col2Sections as secKey (secKey)}
            {#if !maximizedSection || maximizedSection === secKey}
              {@render renderSection(secKey, 2)}
            {/if}
          {/each}
          {#if !maximizedSection}
            <div
              class="col-drop-zone"
              role="region"
              class:is-active={draggedSection && dragOverCol === 2}
              ondragover={(e) => handleColDragOver(e, 2)}
              ondragleave={handleColDragLeave}
              ondrop={(e) => handleColDrop(e, 2)}
            >
              {#if draggedSection && dragOverCol === 2}
                <div class="drop-zone-content">
                  <Plus size={14} />
                  <span>{m.mihomo_win_move_right_col()}</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    {:else}
      <!-- 1 Column (Stack layout) -->
      <div class="beginner-col beginner-col-stack" class:is-full-width={Boolean(maximizedSection)}>
        {#each [...col1Sections, ...col2Sections] as secKey (secKey)}
          {#if !maximizedSection || maximizedSection === secKey}
            {@render renderSection(secKey, 1)}
          {/if}
        {/each}
      </div>
    {/if}
  </div>

  <!-- Edit Rule Modal -->
  {#if editModalOpen && editingRule}
    <MihomoRuleEditModal
      open={true}
      rule={editingRule}
      {groups}
      {proxies}
      {subscriptions}
      {ruleProviders}
      onClose={() => {
        editModalOpen = false;
        editingRule = null;
      }}
      onSaved={() => {
        editModalOpen = false;
        editingRule = null;
        onReload();
      }}
    />
  {/if}

  {#if groupModalOpen}
    <MihomoGroupEditModal
      open={groupModalOpen}
      group={editingGroup}
      {groups}
      {proxies}
      {subscriptions}
      {runtimeProxies}
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

  {#if templateModalOpen}
    <MihomoTemplateModal
      open={true}
      {groups}
      {proxies}
      {subscriptions}
      currentRules={rules}
      onClose={() => (templateModalOpen = false)}
      onApplied={() => {
        templateModalOpen = false;
        onReload();
      }}
    />
  {/if}

  <!-- Delete Rule Confirm Modal -->
  <ConfirmModal
    open={deleteConfirmOpen}
    title={deletingGroup ? m.mihomo_confirm_del_group_title() : m.mihomo_confirm_del_rule_title()}
    message={deletingGroup
      ? m.mihomo_confirm_del_group_msg({ name: deletingGroup.name })
      : deletingRules.length > 1
        ? m.mihomo_confirm_del_rules_multi_msg({ count: deletingRules.length })
        : m.mihomo_confirm_del_rule_msg()}
    confirmLabel={m.common_delete()}
    variant="danger"
    onConfirm={confirmDelete}
    onClose={() => {
      deleteConfirmOpen = false;
      deletingRules = [];
      deletingGroup = null;
    }}
  />
</div>

<style>
  .beginner-view {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .panel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 0 4px;
  }

  .title-group {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .title {
    margin: 0;
    font-size: 16px;
    font-weight: 700;
    color: var(--text-primary);
    letter-spacing: -0.01em;
  }

  .sub {
    margin: 0;
    font-size: 12px;
    color: var(--text-secondary);
  }

  .header-right {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .counter {
    font-size: 13px;
    font-family: var(--font-mono);
    color: var(--text-muted);
  }

  .cards {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .card-shell {
    position: relative;
    border-radius: var(--radius-md, 10px);
    transition: transform var(--t-fast, 0.15s);
  }

  .card-shell.is-drag-over {
    border-top: 2px solid var(--accent);
  }

  .loading-box {
    display: flex;
    justify-content: center;
    align-items: center;
    padding: 48px;
  }

  .empty {
    padding: 24px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .empty-text {
    margin: 0;
    font-size: 13px;
    color: var(--text-secondary);
    line-height: 1.4;
  }

  .empty-action {
    margin-top: 6px;
  }

  @media (max-width: 768px) {
    .panel-header {
      flex-direction: column;
      align-items: flex-start;
      gap: 8px;
    }
    .header-right {
      width: 100%;
      justify-content: space-between;
    }
  }

  /* Toolbar: Density, Layout, Reset */
  .beginner-view-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
    flex-wrap: wrap;
  }

  .toolbar-title-hint {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .mode-tag {
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 3px 8px;
    border-radius: 4px;
    background: rgba(59, 130, 246, 0.15);
    color: var(--color-accent, #3b82f6);
  }

  .mode-sub {
    font-size: 12px;
    color: var(--text-muted);
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
    transition: all var(--t-fast, 0.15s);
  }

  .toolbar-toggle-btn:hover {
    color: var(--text-primary);
    border-color: var(--border-hover, var(--border));
    background: var(--bg-hover, rgba(255, 255, 255, 0.05));
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
    transition: all var(--t-fast, 0.15s);
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

  /* Grid Layout: 2-column & 1-column stack */
  .beginner-grid {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .beginner-grid.layout-split {
    display: grid;
    grid-template-columns: minmax(280px, calc(var(--split-ratio, 55%) - 8px)) 16px minmax(280px, 1fr);
    gap: 0;
    align-items: start;
  }

  .beginner-col-splitter {
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

  .beginner-col-splitter:hover .splitter-hover-glow,
  .beginner-col-splitter.dragging .splitter-hover-glow {
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

  .beginner-grid.is-maximized {
    display: block !important;
  }

  .beginner-col,
  .beginner-col-1,
  .beginner-col-2,
  .beginner-col-stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: 0;
  }

  .beginner-col.is-full-width,
  .beginner-col-1.is-full-width,
  .beginner-col-2.is-full-width,
  .beginner-col-stack.is-full-width {
    grid-column: 1 / -1 !important;
    width: 100% !important;
  }

  @media (max-width: 1024px) {
    .beginner-grid.layout-split {
      display: flex;
      flex-direction: column;
      gap: 16px;
    }
    .beginner-col-splitter {
      display: none;
    }
  }

  @media (max-width: 768px) {
    .mode-sub {
      display: none !important;
    }
    .layout-toggle-group {
      display: none !important;
    }
    .win-resize-edge-e,
    .win-resize-bar,
    .win-resize-corner {
      display: none !important;
    }
    .beginner-grid.layout-split {
      display: flex !important;
      flex-direction: column !important;
      gap: 16px !important;
    }
    .beginner-col,
    .beginner-col-1,
    .beginner-col-2 {
      width: 100% !important;
      max-width: 100% !important;
    }
    .beginner-card-box {
      padding: 10px !important;
    }
    .panel-header {
      flex-direction: column;
      align-items: flex-start;
      gap: 8px;
    }
    .header-right {
      width: 100%;
      justify-content: flex-end;
    }
  }

  /* Beginner Card Box */
  .beginner-card-box {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius, 10px);
    padding: 16px;
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }

  .section-card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
    padding-bottom: 8px;
    border-bottom: 1px solid var(--border);
  }

  .section-card-title-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .section-card-title {
    font-size: 15px;
    font-weight: 700;
    color: var(--text-primary);
  }

  .section-card-sub {
    font-size: 11px;
    color: var(--text-muted);
  }

  .empty-compact {
    padding: 24px;
    text-align: center;
    font-size: 13px;
    color: var(--text-muted);
    font-style: italic;
  }

  /* Drag & Drop */
  :global(body.block-dragging-active) {
    user-select: none !important;
    cursor: grabbing !important;
  }

  .block-drag-handle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 3px 4px;
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

  /* Window buttons */
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

  .expert-section.has-custom-height .beginner-card-box {
    height: 100%;
    flex: 1;
    min-height: 0;
  }

  .expert-section.has-custom-height .rules-inner-scroll,
  .expert-section.has-custom-height .groups-inner-scroll,
  .expert-section.has-custom-height .flow-inner-scroll {
    flex: 1 !important;
    min-height: 0 !important;
    max-height: none !important;
    overflow-y: auto !important;
  }

  .rules-inner-scroll,
  .groups-inner-scroll,
  .flow-inner-scroll {
    min-height: 0;
  }

</style>
