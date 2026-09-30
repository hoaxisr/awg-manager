<script lang="ts">
  import { onMount } from 'svelte';
  import { Zap, Activity, Edit3, Trash2, CheckCircle2, Shield, Shuffle, ArrowDownUp, Disc, RefreshCw, Layers } from 'lucide-svelte';
  import { Badge } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { pluralize } from '$lib/utils/pluralize';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { singboxDelayHistory } from '$lib/stores/singbox';
  import { runWithConcurrency } from '$lib/utils/runWithConcurrency';
  import { formatOutboundHumanName } from '$lib/utils/outboundHumanName';
  import type { MihomoNativeGroup, MihomoNativeSubscription, MihomoRuntimeProxy } from '$lib/types';
  import MihomoSubQuickContextMenu from './MihomoSubQuickContextMenu.svelte';

  interface Props {
    group: MihomoNativeGroup;
    runtimeProxies?: MihomoRuntimeProxy[];
    subscriptions?: MihomoNativeSubscription[];
    onEdit?: (group: MihomoNativeGroup) => void;
    onDelete?: (id: string) => void;
    onReload?: () => void;
  }

  let {
    group,
    runtimeProxies = [],
    subscriptions = [],
    onEdit,
    onDelete,
    onReload,
  }: Props = $props();

  let testing = $state(false);
  let selecting = $state(false);
  let localDelays = $state<Record<string, number>>({});
  let testingMembers = $state<Record<string, boolean>>({});
  let activeMember = $state<string | null>(null);
  let activeSubRef = $state<string | null>(null);
  let activeSubTrigger = $state<HTMLElement | null>(null);
  let activeSubOpen = $state(false);

  const isLoadBalance = $derived(group.type === 'load-balance');
  const runtime = $derived(runtimeProxies.find((p) => p.name === group.name));
  const currentNow = $derived(
    isLoadBalance ? null : (activeMember || runtime?.now || group.proxies?.[0])
  );

  const nameContext = $derived({
    awgTags: $awgTagsStore.data,
    subscriptions: $subscriptionsStore.data,
    mihomoSubscriptions: subscriptions,
  });

  function formatMemberName(rawName: string): string {
    return formatOutboundHumanName(rawName, nameContext);
  }

  // Combine members from group definition and runtime
  const memberNames = $derived.by(() => {
    const list = [...(group.proxies || [])];
    if (runtime?.all) {
      for (const m of runtime.all) {
        if (!list.includes(m)) list.push(m);
      }
    }
    return list;
  });

  function getMemberDelay(name: string): number | null {
    if (name in localDelays && typeof localDelays[name] === 'number' && localDelays[name] > 0) {
      return localDelays[name];
    }
    const p = runtimeProxies.find((rp) => rp.name === name);
    if (p?.history && p.history.length > 0) {
      const last = p.history[p.history.length - 1];
      if (typeof last?.delay === 'number' && last.delay > 0) {
        return last.delay;
      }
    }
    // If it is a group (e.g. sub-06d59bc1) with an active child node
    if (p?.now) {
      const child = runtimeProxies.find((rp) => rp.name === p.now);
      if (child?.history && child.history.length > 0) {
        const last = child.history[child.history.length - 1];
        if (typeof last?.delay === 'number' && last.delay > 0) {
          return last.delay;
        }
      }
      const sbHist = $singboxDelayHistory.get(p.now) ?? [];
      if (sbHist.length > 0 && typeof sbHist[sbHist.length - 1] === 'number' && sbHist[sbHist.length - 1] > 0) {
        return sbHist[sbHist.length - 1];
      }
    }
    // Check Singbox subscriptions
    const sbSubs = $subscriptionsStore.data ?? [];
    const matchedSub = sbSubs.find((s) =>
      s.id === name || (name.startsWith('sub-') && s.id.startsWith(name.slice(4))) || s.label === name
    );
    if (matchedSub?.activeMember) {
      const child = runtimeProxies.find((rp) => rp.name === matchedSub.activeMember);
      if (child?.history && child.history.length > 0) {
        const last = child.history[child.history.length - 1];
        if (typeof last?.delay === 'number' && last.delay > 0) {
          return last.delay;
        }
      }
      const sbHist = $singboxDelayHistory.get(matchedSub.activeMember) ?? [];
      if (sbHist.length > 0 && typeof sbHist[sbHist.length - 1] === 'number' && sbHist[sbHist.length - 1] > 0) {
        return sbHist[sbHist.length - 1];
      }
    }
    if (name in localDelays && typeof localDelays[name] === 'number') {
      return localDelays[name];
    }
    return null;
  }

  function getDelayTone(delay: number | null): 'good' | 'medium' | 'bad' | 'none' {
    if (delay === null || delay === undefined) return 'none';
    if (delay <= 0) return 'bad';
    if (delay < 200) return 'good';
    if (delay < 600) return 'medium';
    return 'bad';
  }

  async function handleTestAll() {
    if (testing) return;
    testing = true;
    try {
      const testUrl = group.url || 'https://www.gstatic.com/generate_204';
      await runWithConcurrency(memberNames, 3, async (name) => {
        try {
          const delay = await api.mihomoRuntimeDelay(name, testUrl, 3000);
          localDelays = { ...localDelays, [name]: delay };
        } catch {
          localDelays = { ...localDelays, [name]: 0 };
        }
      });
      notifications.success(`Задержки для «${group.name}» обновлены`);
      onReload?.();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка проверки задержки');
    } finally {
      testing = false;
    }
  }

  async function handleTestSingleMember(name: string, e: MouseEvent) {
    e.stopPropagation();
    if (testingMembers[name]) return;
    testingMembers = { ...testingMembers, [name]: true };
    try {
      const testUrl = group.url || 'https://www.gstatic.com/generate_204';
      const delay = await api.mihomoRuntimeDelay(name, testUrl, 3000);
      localDelays = { ...localDelays, [name]: delay };
      if (delay > 0) {
        notifications.success(`Задержка для «${formatMemberName(name)}»: ${delay}ms`);
      } else {
        notifications.warning(`Узел «${formatMemberName(name)}» недоступен (таймаут)`);
      }
    } catch {
      localDelays = { ...localDelays, [name]: 0 };
      notifications.warning(`Узел «${formatMemberName(name)}» недоступен (таймаут)`);
    } finally {
      testingMembers = { ...testingMembers, [name]: false };
    }
  }

  async function handleSelect(memberName: string) {
    if (group.type !== 'select') return;
    if (currentNow === memberName || selecting) return;
    selecting = true;
    activeMember = memberName;
    notifications.success(`Выбран узел «${formatMemberName(memberName)}»`);
    try {
      await api.mihomoRuntimeSelect(group.name, memberName);
      onReload?.();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка переключения узла');
      onReload?.();
    } finally {
      selecting = false;
    }
  }

  function isSubscriptionMember(rawName: string): boolean {
    if (!rawName) return false;
    // 1. Mihomo native subscription groups
    if (
      rawName.startsWith('Mihomo: ') ||
      subscriptions.some((s) =>
        s.name === rawName ||
        s.groupName === rawName ||
        s.providerName === rawName ||
        `Mihomo: ${s.name}` === rawName
      )
    ) {
      return true;
    }
    // 2. Sing-box subscription selector groups (e.g. sub-06d59bc1)
    // Subscription groups have format `sub-XXXXXXXX` (2 parts separated by hyphen).
    // Individual member servers have format `sub-XXXXXXXX-YYYYYYYY` (3+ parts).
    if (rawName.startsWith('sub-') && rawName.split('-').length === 2) {
      return true;
    }

    const sbSubs = $subscriptionsStore.data ?? [];
    const matchedSub = sbSubs.find((s) =>
      (s.selectorTag && s.selectorTag === rawName) ||
      s.id === rawName ||
      s.label === rawName
    );
    if (matchedSub) {
      const isIndividualServer = sbSubs.some((s) =>
        s.members?.some((m) => m.tag === rawName) ||
        s.memberTags?.includes(rawName)
      );
      return !isIndividualServer;
    }

    return false;
  }

  function handleMemberClick(name: string, isSub: boolean, event: MouseEvent) {
    if (isSub) {
      event.stopPropagation();
      const currentTarget = event.currentTarget as HTMLElement;
      if (activeSubRef === name && activeSubOpen) {
        activeSubOpen = false;
        activeSubRef = null;
        activeSubTrigger = null;
      } else {
        activeSubTrigger = currentTarget;
        activeSubRef = name;
        activeSubOpen = true;
      }
      return;
    }
    if (group.type === 'select') {
      void handleSelect(name);
    }
  }

  function getGroupTypeIcon(type: string) {
    switch (type) {
      case 'fallback': return Shield;
      case 'url-test': return Zap;
      case 'load-balance': return ArrowDownUp;
      case 'select':
      default: return Disc;
    }
  }

  function getGroupTypeTooltip(type: string): string {
    switch (type) {
      case 'url-test':
        return 'URL-TEST: Автовыбор самого быстрого узла по пингу. Подходит для веб-серфинга и YouTube.';
      case 'fallback':
        return 'FALLBACK: Отказоустойчивый резерв. Держит основной узел и переключается только при сбое.';
      case 'select':
        return 'SELECT: Ручной выбор. Трафик не переключается автоматически. Максимальная стабильность.';
      case 'load-balance':
        return 'LOAD-BALANCE: Балансировка соединений. Не подходит для YouTube и стриминга (вызывает ошибки 403 и буферизацию).';
      default:
        return type.toUpperCase();
    }
  }

  const TypeIcon = $derived(getGroupTypeIcon(group.type));
</script>

<div class="group-card">
  <div class="group-header">
    <div class="group-identity">
      <span class="type-icon-wrapper" class:pulse={testing}>
        <TypeIcon size={14} class="type-icon" />
      </span>
      <span class="group-title" title={group.name}>{group.name}</span>
      <span title={getGroupTypeTooltip(group.type)} class="badge-tooltip-wrapper">
        <Badge variant={group.type === 'load-balance' ? 'warning' : 'accent'} size="sm">{group.type.toUpperCase()}</Badge>
      </span>
      <span class="group-meta">
        {pluralize(memberNames.length, ['узел', 'узла', 'узлов'])}
        {#if group.interval && group.type !== 'select'}
          · {group.interval}с
        {/if}
      </span>
    </div>

    <div class="group-actions">
      <button
        type="button"
        class="compact-btn test-btn"
        disabled={testing}
        onclick={handleTestAll}
        title="Проверить задержки всех узлов"
      >
        <Zap size={12} class={testing ? 'spin' : ''} />
        <span>{testing ? 'Замер...' : 'Тест'}</span>
      </button>

      {#if onEdit}
        <button
          type="button"
          class="compact-btn icon-only"
          onclick={() => onEdit(group)}
          title="Редактировать группу"
        >
          <Edit3 size={13} />
        </button>
      {/if}

      {#if onDelete}
        <button
          type="button"
          class="compact-btn icon-only danger"
          onclick={() => onDelete(group.id || group.name)}
          title="Удалить группу"
        >
          <Trash2 size={13} />
        </button>
      {/if}
    </div>
  </div>

  <div class="members-grid">
    {#each memberNames as name (name)}
      {@const delay = getMemberDelay(name)}
      {@const tone = getDelayTone(delay)}
      {@const isActive = currentNow === name}
      {@const isSelectable = group.type === 'select'}
      {@const isSub = isSubscriptionMember(name)}
      {@const isChecking = !!testingMembers[name]}
      {@const humanLabel = formatMemberName(name)}

      <div
        class="member-chip"
        class:selectable={isSelectable || isSub}
        class:active={isActive}
        class:is-sub={isSub}
        class:menu-open={isSub && activeSubRef === name && activeSubOpen}
        role="button"
        tabindex={0}
        onclick={(e) => handleMemberClick(name, isSub, e)}
        onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && handleMemberClick(name, isSub, e as any)}
        title={isSub ? `Подписка «${humanLabel}» — нажмите для выбора сервера` : isSelectable ? `Нажмите, чтобы выбрать ${humanLabel}` : isLoadBalance ? `Узел балансировки: ${humanLabel}` : isActive ? `Активный узел: ${humanLabel}` : humanLabel}
      >
        <span class="member-dot" class:active={isActive}></span>
        <span class="member-name">{humanLabel}</span>
        {#if isSub}
          <span class="sub-indicator-icon" title="Подписка">
            <Layers size={11} />
          </span>
        {/if}
        <button
          type="button"
          class="delay-btn {tone}"
          class:checking={isChecking}
          title="Замерить скорость узла"
          onclick={(e) => handleTestSingleMember(name, e)}
        >
          <span class="delay-val">{delay !== null ? (delay > 0 ? `${delay}ms` : 'timeout') : '...'}</span>
          <RefreshCw size={9} class="refresh-icon {isChecking ? 'spin' : ''}" />
        </button>
      </div>
    {/each}
  </div>
</div>

{#if activeSubOpen && activeSubRef && activeSubTrigger}
  <MihomoSubQuickContextMenu
    open={activeSubOpen}
    subRef={activeSubRef}
    triggerEl={activeSubTrigger}
    mihomoSubscriptions={subscriptions}
    {runtimeProxies}
    onClose={() => {
      activeSubOpen = false;
      activeSubRef = null;
      activeSubTrigger = null;
    }}
    onUpdated={() => {
      onReload?.();
    }}
  />
{/if}

<style>
  .group-card {
    background: var(--color-bg-secondary, #f8fafc);
    border: 1px solid var(--color-border, #cbd5e1);
    border-radius: var(--radius-sm, 6px);
    padding: 8px 12px;
    margin-bottom: 8px;
    transition: border-color var(--t-fast, 0.15s), background var(--t-fast, 0.15s);
  }

  .group-card:hover {
    border-color: var(--color-border-hover, #94a3b8);
  }

  .group-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 8px;
  }

  .group-identity {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    flex: 1;
  }

  .type-icon-wrapper {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--color-accent, #3b82f6);
  }

  .group-title {
    font-weight: 700;
    font-size: 13px;
    color: var(--color-text-primary, #0f172a);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .group-meta {
    font-size: 11px;
    color: var(--color-text-muted, #64748b);
    margin-left: 2px;
  }

  .group-actions {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }

  .compact-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 22px;
    padding: 0 8px;
    font-size: 11px;
    font-weight: 600;
    border-radius: var(--radius-sm, 4px);
    border: 1px solid var(--color-border, #cbd5e1);
    background: var(--color-bg-tertiary, #ffffff);
    color: var(--color-text-primary, #0f172a);
    cursor: pointer;
    transition: all var(--t-fast, 0.15s);
  }

  .compact-btn:hover:not(:disabled) {
    background: var(--color-bg-hover, #f1f5f9);
    border-color: var(--color-accent, #3b82f6);
    color: var(--color-accent, #3b82f6);
  }

  .compact-btn.icon-only {
    padding: 0 5px;
  }

  .compact-btn.danger:hover:not(:disabled) {
    border-color: var(--color-error, #ef4444);
    color: var(--color-error, #ef4444);
  }

  .members-grid {
    display: flex;
    flex-wrap: wrap;
    gap: 7px;
  }

  .member-chip {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 26px;
    padding: 0 8px;
    font-size: 12px;
    font-weight: 500;
    border-radius: 5px;
    background: var(--color-bg-primary, #ffffff);
    border: 1px solid var(--color-border, #cbd5e1);
    color: var(--color-text-primary, #0f172a);
    cursor: default;
    opacity: 1 !important;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
    transition: all var(--t-fast, 0.15s);
  }

  .member-chip.selectable {
    cursor: pointer;
  }

  .member-chip.selectable:hover {
    border-color: var(--color-accent, #3b82f6);
    background: var(--color-bg-hover, #f1f5f9);
  }

  .member-chip.menu-open {
    border-color: var(--color-accent, #3b82f6);
    box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.2);
    background: var(--color-bg-hover, #f1f5f9);
  }

  .sub-indicator-icon {
    display: inline-flex;
    align-items: center;
    color: var(--color-accent, #3b82f6);
    opacity: 0.85;
    margin-left: -1px;
    margin-right: 1px;
  }

  .member-chip:hover .sub-indicator-icon {
    opacity: 1;
  }

  :global(.dark) .member-chip {
    background: #1e293b;
    border-color: #475569;
    color: #f8fafc;
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
  }

  /* Status indicator dot matching subscription cards */
  .member-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--color-text-muted, #94a3b8);
    flex-shrink: 0;
    transition: all 0.2s ease;
  }

  .member-dot.active {
    background: var(--color-success, #22c55e);
    box-shadow: 0 0 6px var(--color-success-border, rgba(34, 197, 94, 0.6));
    animation: dot-pulse 1.4s ease-in-out infinite;
  }

  @keyframes dot-pulse {
    0%, 100% {
      opacity: 1;
      transform: scale(1);
    }
    50% {
      opacity: 0.35;
      transform: scale(0.85);
    }
  }

  .member-name {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 220px;
    color: inherit;
  }

  /* Interactive single node delay button matching subscription cards */
  .delay-btn {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    padding: 1px 4px;
    border-radius: 3px;
    border: 1px solid transparent;
    background: transparent;
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    font-weight: 500;
    font-variant-numeric: tabular-nums;
    line-height: 1;
    cursor: pointer;
    margin-left: 2px;
    flex-shrink: 0;
    transition: all 0.15s ease;
  }

  .delay-btn:hover {
    background: var(--color-bg-hover, rgba(0, 0, 0, 0.05));
    border-color: var(--color-border, #cbd5e1);
  }

  :global(.dark) .delay-btn:hover {
    background: rgba(255, 255, 255, 0.08);
    border-color: #475569;
  }

  .delay-btn.good {
    color: var(--color-success, #16a34a);
  }

  :global(.dark) .delay-btn.good {
    color: var(--color-success, #4ade80);
  }

  .delay-btn.medium {
    color: var(--color-warning, #d97706);
  }

  :global(.dark) .delay-btn.medium {
    color: var(--color-warning, #fde047);
  }

  .delay-btn.bad {
    color: var(--color-error, #dc2626);
  }

  :global(.dark) .delay-btn.bad {
    color: var(--color-error, #f87171);
  }

  .delay-btn .refresh-icon {
    opacity: 0;
    transition: opacity 0.15s ease;
  }

  .delay-btn:hover .refresh-icon {
    opacity: 0.8;
  }

  .delay-btn.checking .spin {
    animation: spin 1s linear infinite;
  }

  .spin {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
</style>
