<!--
  Источник дизайна: singbox-router/project/parts/RuleSetModal.jsx (RuleSetModal главная функция)
  При правках сверять с JSX напрямую — spacing/colors/layout.
-->

<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { get } from 'svelte/store';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { notifications } from '$lib/stores/notifications';
  import { Search, X as XIcon } from 'lucide-svelte';
  import {
    templatesOpen, templatesSelection, templatesFilter, templatesQuery, templatesOutbound,
    closeTemplatesModal, dismissTemplatesModal, toggleTemplate, clearSelection, setFilter, setQuery, setOutbound,
  } from './templatesStore';
  import {
    buildTemplateList, filterByCategory, countByCategory,
  } from './templatesData';
  import { submitTemplates } from './templatesActions';
  import TemplatesFilterChip from './TemplatesFilterChip.svelte';
  import TemplatesGroup from './TemplatesGroup.svelte';
  import TemplateServiceTile from './TemplateServiceTile.svelte';
  import TemplateRsRow from './TemplateRsRow.svelte';
  import TemplatesFooter from './TemplatesFooter.svelte';
  import { pluralize, RULE_WORDS, TEMPLATE_WORDS } from '$lib/utils/pluralize';

  interface Props {
    mode?: 'submit' | 'collect';
    servicesOnly?: boolean;
    onDone?: () => void;
  }
  let { mode = 'submit', servicesOnly = false, onDone }: Props = $props();

  const presets = singboxRouterStore.presets;
  const ruleSets = singboxRouterStore.ruleSets;
  const outbounds = singboxRouterStore.outbounds;

  let lastLoadedAt = 0;
  $effect(() => {
    if ($templatesOpen) {
      const now = Date.now();
      if (now - lastLoadedAt > 30_000) {
        lastLoadedAt = now;
        void singboxRouterStore.loadAll();
      }
    }
  });

  const allGroups = $derived(buildTemplateList($presets, $ruleSets, $templatesQuery));
  const counts = $derived(countByCategory(allGroups));
  const visibleGroups = $derived(
    servicesOnly
      ? allGroups.filter((g) => g.category === 'services')
      : filterByCategory(allGroups, $templatesFilter),
  );

  let submitting = $state(false);
  let lastFailures = $state<Array<{ id: string; error: string }>>([]);

  async function handleSubmit() {
    submitting = true;
    lastFailures = [];
    try {
      const selectionArr = Array.from(get(templatesSelection));
      const outbound = get(templatesOutbound);
      if (!outbound) return;
      const result = await submitTemplates(selectionArr, outbound, allGroups);
      if (result.failures.length === 0) {
        notifications.success(`Создано ${pluralize(result.successes.length, RULE_WORDS)}`);
        closeTemplatesModal();
        await singboxRouterStore.loadAll();
      } else {
        const succN = result.successes.length;
        const failN = result.failures.length;
        const msg = succN > 0
          ? `Создано ${succN} из ${succN + failN}. Ошибок: ${failN}`
          : `Не удалось создать правила (${failN})`;
        notifications.error(msg);
        lastFailures = result.failures;
        for (const id of result.successes) {
          toggleTemplate(id);
        }
        if (succN > 0) {
          await singboxRouterStore.loadAll();
        }
      }
    } catch (e) {
      notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      submitting = false;
    }
  }

  function handleDone() {
    onDone?.();
    dismissTemplatesModal();
  }

  function handleSelectAll(category: 'services' | 'rulesets') {
    const group = allGroups.find((g) => g.category === category);
    if (!group) return;
    const allSelected = group.items.every((it) => $templatesSelection.has(it.id));
    for (const it of group.items) {
      const isSelected = $templatesSelection.has(it.id);
      if (allSelected && isSelected) toggleTemplate(it.id);
      else if (!allSelected && !isSelected) toggleTemplate(it.id);
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (!$templatesOpen) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      if (mode === 'collect') {
        handleDone();
      } else {
        closeTemplatesModal();
      }
    }
  }

  onMount(() => {
    window.addEventListener('keydown', handleKeydown);
  });

  onDestroy(() => {
    window.removeEventListener('keydown', handleKeydown);
  });

  function failureFor(id: string): string | undefined {
    return lastFailures.find((f) => f.id === id)?.error;
  }
</script>

{#if $templatesOpen}
  <div
    class="overlay"
    role="presentation"
    onclick={(e) => {
      if (e.target !== e.currentTarget) return;
      if (mode === 'collect') handleDone();
      else closeTemplatesModal();
    }}
  >
    <div class="box" role="dialog" aria-modal="true" aria-label="Готовые шаблоны">
      <header class="head">
        <div class="head-text">
          <div class="title">Готовые шаблоны</div>
          <div class="sub">
            Сервисы и сервисные наборы. Выбирайте несколько за раз — каждый создаст своё правило.
          </div>
        </div>
        <button type="button" class="icon-btn" onclick={closeTemplatesModal} aria-label="Закрыть">
          <XIcon size={18} />
        </button>
      </header>

      <div class="search-row">
        <div class="search">
          <Search size={14} color="var(--text-muted)" />
          <input
            type="search"
            placeholder="netflix, geoip, telegram..."
            value={$templatesQuery}
            oninput={(e) => setQuery((e.currentTarget as HTMLInputElement).value)}
          />
          <span class="search-count">{pluralize(counts.all, TEMPLATE_WORDS)}</span>
        </div>
        {#if !servicesOnly}
          <div class="chips">
            <TemplatesFilterChip label="Все" value="all" active={$templatesFilter === 'all'} count={counts.all} onclick={() => setFilter('all')} />
            <TemplatesFilterChip label="Сервисы" value="services" active={$templatesFilter === 'services'} count={counts.services} onclick={() => setFilter('services')} />
            <TemplatesFilterChip label="Наборы доменов и CIDR" value="rulesets" active={$templatesFilter === 'rulesets'} count={counts.rulesets} onclick={() => setFilter('rulesets')} />
          </div>
        {/if}
      </div>

      <div class="body">
        {#if visibleGroups.length === 0}
          <div class="empty">По запросу ничего не нашлось.</div>
        {/if}

        {#each visibleGroups as group (group.category)}
          {@const selectedInGroup = group.items.filter((it) => $templatesSelection.has(it.id)).length}
          <TemplatesGroup
            title={group.title}
            hint={group.hint}
            selectedCount={selectedInGroup}
            totalCount={group.items.length}
            onSelectAll={() => handleSelectAll(group.category)}
          >
            {#if group.category === 'services'}
              <div class="grid">
                {#each group.items as item (item.id)}
                  {#if item.category === 'services'}
                    <div class="cell">
                      <TemplateServiceTile
                        templateId={item.id}
                        presetId={item.presetId}
                        iconSlug={item.iconSlug}
                        name={item.name}
                        sub={item.category_hint}
                        selected={$templatesSelection.has(item.id)}
                        onclick={() => toggleTemplate(item.id)}
                      />
                      {#if failureFor(item.id)}
                        <div class="fail">{failureFor(item.id)}</div>
                      {/if}
                    </div>
                  {/if}
                {/each}
              </div>
            {:else}
              <div class="rs-list">
                {#each group.items as item (item.id)}
                  {#if item.category === 'rulesets'}
                    <div>
                      <TemplateRsRow
                        templateId={item.id}
                        tag={item.tag}
                        type={item.type}
                        selected={$templatesSelection.has(item.id)}
                        onclick={() => toggleTemplate(item.id)}
                      />
                      {#if failureFor(item.id)}
                        <div class="fail">{failureFor(item.id)}</div>
                      {/if}
                    </div>
                  {/if}
                {/each}
              </div>
            {/if}
          </TemplatesGroup>
        {/each}
      </div>

      {#if mode === 'submit'}
        <TemplatesFooter
          selectedIds={Array.from($templatesSelection)}
          outbounds={$outbounds}
          pickedOutbound={$templatesOutbound}
          onPickOutbound={(v) => setOutbound(v)}
          onClear={clearSelection}
          onCancel={closeTemplatesModal}
          onSubmit={handleSubmit}
          {submitting}
        />
      {:else}
        {@const count = $templatesSelection.size}
        <div class="footer-collect">
          <span class="hint">Выбрано: {count}</span>
          <button type="button" class="btn-primary" disabled={count === 0} onclick={handleDone}>
            Готово ({count})
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--sp-5);
  }
  .box {
    width: min(820px, 100%);
    max-height: 92%;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: 0 24px 60px rgba(0, 0, 0, 0.55);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .head {
    padding: var(--sp-4) var(--sp-5);
    border-bottom: 1px solid var(--border);
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--sp-4);
  }
  .title {
    font-size: var(--fs-h5);
    font-weight: 600;
    color: var(--text-primary);
  }
  .sub {
    margin-top: 2px;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .icon-btn {
    background: transparent;
    border: 0;
    color: var(--text-secondary);
    padding: 4px;
    cursor: pointer;
    border-radius: var(--radius-sm);
  }
  .icon-btn:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .search-row {
    padding: var(--sp-3) var(--sp-5);
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: var(--sp-3);
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    border: 1px solid var(--border);
  }
  .search input {
    flex: 1;
    background: transparent;
    border: 0;
    outline: none;
    color: var(--text-primary);
    font-size: 13px;
    font-family: inherit;
  }
  .search-count {
    font-size: 11px;
    color: var(--text-muted);
    font-family: var(--font-mono);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .body {
    flex: 1;
    overflow-y: auto;
    padding: 4px var(--sp-5) var(--sp-4);
  }
  .empty {
    padding: var(--sp-4);
    text-align: center;
    color: var(--text-muted);
    font-size: var(--fs-sm);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 6px;
  }
  @media (max-width: 768px) {
    .grid {
      grid-template-columns: repeat(2, 1fr);
    }
  }
  .rs-list {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 4px;
  }
  .fail {
    font-size: 11px;
    color: var(--color-error, #ff5555);
    margin: 4px 0 0 28px;
  }
  .footer-collect {
    padding: var(--sp-3) var(--sp-5);
    border-top: 1px solid var(--border);
    background: var(--bg-tertiary);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
  }
  .footer-collect .hint {
    font-size: 11.5px;
    color: var(--text-muted);
  }
  .footer-collect .btn-primary {
    padding: 6px 14px;
    border-radius: var(--radius-sm);
    background: var(--accent);
    color: var(--color-accent-contrast, #fff);
    border: 0;
    cursor: pointer;
    font-family: inherit;
    font-size: 12px;
    font-weight: 500;
  }
  .footer-collect .btn-primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  @media (max-width: 768px) {
    .overlay {
      padding: 0 !important;
    }
    .box {
      width: 100% !important;
      max-width: none !important;
      max-height: 100% !important;
      height: 100%;
      border-radius: 0 !important;
      border: 0 !important;
    }
    .search-row {
      padding: var(--sp-3);
    }
    .chips {
      gap: 4px;
      overflow-x: auto;
      flex-wrap: nowrap;
      padding-bottom: 4px;
    }
    .body {
      padding: 4px var(--sp-3) var(--sp-3);
    }
    .footer-collect {
      padding: 10px var(--sp-3);
    }
  }
</style>
