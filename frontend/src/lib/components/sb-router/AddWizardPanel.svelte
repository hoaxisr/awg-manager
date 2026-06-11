<!--
  Источник дизайна: singbox-router/project/screens/AddRuleFlow.jsx (AddRuleFlowScreen)
  Полная композиция wizard'а: header + stepper + 3 шага + actions.
-->

<script lang="ts">
  import { get } from 'svelte/store';
  import { onMount, onDestroy } from 'svelte';
  import {
    ArrowLeft, Info, Check, Zap, Globe, ShieldOff, Plus,
  } from 'lucide-svelte';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { notifications } from '$lib/stores/notifications';
  import { resolveOutboundDisplay } from './adapters';
  import OutboundToneIcon from './OutboundToneIcon.svelte';
  import { displayTone, toneClass } from './outboundTileTone';
  import { Button } from '$lib/components/ui';
  import StepPill from './StepPill.svelte';
  import WizardStep from './WizardStep.svelte';
  import OutboundOption from './OutboundOption.svelte';
  import SelectedTemplatesRow from './SelectedTemplatesRow.svelte';
  import CustomMatcherForm from './CustomMatcherForm.svelte';
  import TemplatesModal from './TemplatesModal.svelte';
  import SbRouterServiceCatalogModal from './SbRouterServiceCatalogModal.svelte';
  import {
    addWizardOpen,
    wizardOutboundCategory, wizardTunnelTags, wizardCustom,
    wizardEditRuleIndex, wizardEditMode, wizardExistingInlineRuleSetTag, wizardWasInlineText,
    closeAddWizard, setOutboundCategory, toggleTunnelTag, resetWizardState,
  } from './addWizardStore';
  import {
    templatesSelection, openTemplatesModal, clearSelection,
  } from './templatesStore';
  import { buildTemplateList } from './templatesData';
  import { submitWizard, submitWizardEdit, ValidationError } from './addWizardActions';
  import { isInlineRuleListEmpty } from '$lib/utils/singboxInlineRules';
  import MobileBottomBar from './MobileBottomBar.svelte';
  import { mode } from './modeStore';
  import { ensureTunnelDnsInfra, syncTunnelDnsRule } from './emptyStateActions';
  import { pluralize, RULE_WORDS, SERVICE_WORDS, SET_WORDS } from '$lib/utils/pluralize';
  import { findScrollContainer } from '$lib/utils/findScrollContainer';
  import { previewTunnelOutboundResolution, formatWizardOutboundPreview } from './wizardCompositeOutbound';

  const outbounds = singboxRouterStore.outbounds;
  const options = singboxRouterStore.options;
  const optionsReady = singboxRouterStore.optionsReady;
  const presets = singboxRouterStore.presets;
  const ruleSets = singboxRouterStore.ruleSets;

  onMount(() => {
    void singboxRouterStore.loadAll();
    window.addEventListener('keydown', handleKeydown);
  });
  onDestroy(() => {
    window.removeEventListener('keydown', handleKeydown);
  });

  function handleKeydown(e: KeyboardEvent) {
    if (!get(addWizardOpen)) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      closeAddWizard();
    }
  }

  const tunnelOutbounds = $derived(
    $options.filter((g) => g.group !== 'Специальные').flatMap((g) => g.items),
  );
  const directTag = $derived(
    $outbounds.find((o) => o.type === 'direct')?.tag ?? 'direct',
  );

  const groups = $derived(buildTemplateList($presets, $ruleSets, ''));

  const isEditMode = $derived($wizardEditRuleIndex !== null);
  const editMode = $derived($wizardEditMode);

  const hasTemplates = $derived($templatesSelection.size > 0);
  const hasCustom = $derived(!isInlineRuleListEmpty($wizardCustom.rulesList));
  const step1Ok = $derived.by(() => {
    if (isEditMode && editMode === 'external') return hasTemplates;
    if (isEditMode && editMode === 'inline') return hasCustom;
    return hasTemplates || hasCustom;
  });
  const step2Ok = $derived.by(() => {
    if ($wizardOutboundCategory === null) return false;
    if ($wizardOutboundCategory === 'tunnel') return $wizardTunnelTags.length > 0;
    return true;
  });
  const canSave = $derived(step1Ok && step2Ok);

  const tunnelOutboundPreview = $derived.by(() => {
    if ($wizardOutboundCategory !== 'tunnel' || $wizardTunnelTags.length === 0) return null;
    return previewTunnelOutboundResolution($wizardTunnelTags, $outbounds);
  });

  const outboundPreviewText = $derived(
    formatWizardOutboundPreview($wizardOutboundCategory, tunnelOutboundPreview, directTag),
  );

  let submitting = $state(false);
  // Бамп для remount CustomMatcherForm после «добавить ещё одно»:
  // визард не уничтожается, поэтому локальный value формы надо сбросить вместе со стором.
  let customResetKey = $state(0);
  let wizardEl = $state<HTMLElement | null>(null);

  const STICKY_HEADER_OFFSET = 72;

  async function scrollWizardToTop(): Promise<void> {
    if (typeof window === 'undefined' || !wizardEl) return;
    (document.activeElement as HTMLElement | null)?.blur?.();

    const anchor = wizardEl.querySelector('.title') ?? wizardEl;
    const container = findScrollContainer(wizardEl);

    if (container) {
      const top =
        container.scrollTop
        + anchor.getBoundingClientRect().top
        - container.getBoundingClientRect().top
        - STICKY_HEADER_OFFSET;
      container.scrollTo({ top: Math.max(0, top), behavior: 'smooth' });
    } else {
      const top = anchor.getBoundingClientRect().top + window.scrollY - STICKY_HEADER_OFFSET;
      window.scrollTo({ top: Math.max(0, top), behavior: 'smooth' });
    }

    await new Promise<void>((resolve) => {
      setTimeout(resolve, 400);
    });
  }

  async function syncDnsAfterSave() {
    if (get(mode) !== 'beginner') return;
    try {
      const cat = get(wizardOutboundCategory);
      const tags = get(wizardTunnelTags);
      if (cat === 'tunnel' && tags.length > 0) await ensureTunnelDnsInfra(tags[0]!);
      await syncTunnelDnsRule();
    } catch (e) {
      notifications.error(`DNS: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  async function doSave(continueAfter: boolean) {
    if (!canSave) return;
    submitting = true;
    try {
      const editIndex = get(wizardEditRuleIndex);
      if (editIndex !== null && get(wizardEditMode)) {
        await submitWizardEdit({
          ruleIndex: editIndex,
          editMode: get(wizardEditMode)!,
          selectedTemplates: Array.from(get(templatesSelection)),
          customFields: get(wizardCustom),
          outboundCategory: get(wizardOutboundCategory)!,
          tunnelTags: get(wizardTunnelTags),
          groups,
          presets: get(presets),
          existingRuleSetTags: get(ruleSets).map((r) => r.tag),
          existingOutbounds: get(outbounds),
          existingInlineRuleSetTag: get(wizardExistingInlineRuleSetTag),
          wasInlineText: get(wizardWasInlineText),
        });
        await syncDnsAfterSave();
        notifications.success('Правило обновлено');
        clearSelection();
        closeAddWizard();
        await singboxRouterStore.loadAll();
        return;
      }

      const result = await submitWizard({
        selectedTemplates: Array.from(get(templatesSelection)),
        customFields: get(wizardCustom),
        outboundCategory: get(wizardOutboundCategory)!,
        tunnelTags: get(wizardTunnelTags),
        groups,
        existingRuleSetTags: get(ruleSets).map((r) => r.tag),
        existingOutbounds: get(outbounds),
      });
      if (result.failures.length === 0) {
        await syncDnsAfterSave();
        const created = result.successes.length;
        if (continueAfter) {
          notifications.success(`Создано ${pluralize(created, RULE_WORDS)}. Можно добавить ещё одно.`);
          clearSelection();
          await scrollWizardToTop();
          resetWizardState();
          customResetKey++;
          await singboxRouterStore.loadAll();
        } else {
          notifications.success(`Создано ${pluralize(created, RULE_WORDS)}`);
          clearSelection();
          closeAddWizard();
          await singboxRouterStore.loadAll();
        }
      } else {
        const sN = result.successes.length;
        const fN = result.failures.length;
        notifications.error(
          sN > 0
            ? `Создано ${sN} из ${sN + fN}. Ошибок: ${fN}`
            : `Не удалось создать (${fN})`,
        );
      }
    } catch (e) {
      if (e instanceof ValidationError) {
        notifications.error(e.message);
      } else {
        notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
      }
    } finally {
      submitting = false;
    }
  }
</script>

{#snippet iconCheck()}<Check size={14} />{/snippet}
{#snippet iconTunnel()}<Zap size={18} />{/snippet}
{#snippet iconDirect()}<Globe size={18} />{/snippet}
{#snippet iconBlock()}<ShieldOff size={18} />{/snippet}

{#if $addWizardOpen}
  <div class="wizard" bind:this={wizardEl}>
    <div class="bc">
      <button type="button" class="bc-back" onclick={closeAddWizard}>
        <ArrowLeft size={12} /> Маршрутизация
      </button>
      <span class="bc-sep">/</span>
      <span class="bc-current">{isEditMode ? 'Редактирование' : 'Новое правило'}</span>
    </div>

    <h1 class="title">{isEditMode ? 'Редактировать правило' : 'Куда направить трафик?'}</h1>
    <p class="sub">
      {#if isEditMode && editMode === 'external'}
        Выберите другой шаблон и куда направить трафик.
      {:else if isEditMode}
        Измените список и куда направить трафик.
      {:else}
        Выберите сервис или опишите свой. Затем — куда его пустить.
      {/if}
    </p>

    <div class="stepper">
      <StepPill n={1} label="Что направить" shortLabel="Что" active={!step1Ok} done={step1Ok} />
      <div class="connector" aria-hidden="true"></div>
      <StepPill n={2} label="Куда" shortLabel="Куда" active={step1Ok && !step2Ok} done={step1Ok && step2Ok} />
      <div class="connector" aria-hidden="true"></div>
      <StepPill n={3} label="Предпросмотр" shortLabel="Проверка" active={step1Ok && step2Ok} done={false} />
    </div>

    <WizardStep
      n={1}
      title="Что направить"
      hint={isEditMode && editMode === 'inline' ? 'список доменов и адресов' : 'выберите шаблон или опишите вручную'}
      active={true}
    >
      {#if !isEditMode || editMode === 'external'}
        <button type="button" class="picker-btn" onclick={() => openTemplatesModal()}>
          <div class="picker-icon"><Plus size={20} /></div>
          <div class="picker-text">
            <div class="picker-title">
              {isEditMode ? 'Заменить шаблон' : 'Выбрать из готовых шаблонов'}
            </div>
            <div class="picker-sub">
              {#if $mode === 'beginner'}
                {pluralize($presets.length, SERVICE_WORDS)}
              {:else}
                {pluralize($presets.length, SERVICE_WORDS)} · {pluralize($ruleSets.length, SET_WORDS)}
              {/if}
            </div>
          </div>
          <div class="picker-chev">›</div>
        </button>

        <SelectedTemplatesRow />
      {/if}

      {#if !isEditMode || editMode === 'inline'}
        {#key customResetKey}
          <CustomMatcherForm expanded={isEditMode && editMode === 'inline'} />
        {/key}
      {/if}
    </WizardStep>

    <WizardStep n={2} title="Куда направить" active={step1Ok}>
      <div class="grid-3">
        <OutboundOption
          icon={iconTunnel}
          label="Через туннель"
          sub="AWG / прокси"
          count="{tunnelOutbounds.length} доступно"
          tone="accent"
          selected={$wizardOutboundCategory === 'tunnel'}
          onclick={() => setOutboundCategory('tunnel')}
        />
        <OutboundOption
          icon={iconDirect}
          label="Напрямую"
          sub="Через интерфейс провайдера"
          count={directTag}
          tone="muted"
          selected={$wizardOutboundCategory === 'direct'}
          onclick={() => setOutboundCategory('direct')}
        />
        <OutboundOption
          icon={iconBlock}
          label="Заблокировать"
          sub="Трафик отбрасывается"
          count="reject"
          tone="error"
          selected={$wizardOutboundCategory === 'block'}
          onclick={() => setOutboundCategory('block')}
        />
      </div>

      {#if $wizardOutboundCategory === 'tunnel'}
        <div class="tunnel-row">
          <div class="tunnel-cap">
            Выбрать туннели
            {#if $wizardTunnelTags.length > 1}
              <span class="tunnel-count">{$wizardTunnelTags.length} выбрано</span>
            {/if}
          </div>
          <p class="tunnel-hint">Можно выбрать несколько — будет использован composite outbound</p>
          {#if tunnelOutbounds.length > 0}
            <div class="tunnel-chips">
              {#each tunnelOutbounds as ob (ob.value)}
                {@const selected = $wizardTunnelTags.includes(ob.value)}
                {@const tunnelDisplay = resolveOutboundDisplay(
                  ob.value,
                  'route',
                  $outbounds,
                  $options,
                  $subscriptionsStore.data,
                  $singboxProxies.data ?? [],
                  $singboxTunnels.data ?? [],
                )}
                {@const tunnelTone = displayTone(tunnelDisplay)}
                <button type="button" class="t-chip" class:selected onclick={() => toggleTunnelTag(ob.value)}>
                  <span class="tone-icon {toneClass(tunnelTone)}">
                    <OutboundToneIcon tone={tunnelTone} kind={tunnelDisplay.kind} size={12} />
                  </span>
                  <span class="tag">{ob.label}</span>
                </button>
              {/each}
            </div>
          {:else if $optionsReady}
            <div class="empty-tunnels">Нет доступных туннелей.</div>
          {/if}
        </div>
      {/if}
    </WizardStep>

    <WizardStep n={3} title="Предпросмотр" active={step1Ok && step2Ok}>
      {#if isEditMode}
        <p class="preview-hint">Изменения применятся к текущему правилу.</p>
      {:else}
        <p class="preview-hint">
          Правила появятся в конце списка. После создания можно перетаскивать.
        </p>
        <div class="preview-info">
          <Info size={14} />
          <span>
            {pluralize($templatesSelection.size + (hasCustom ? 1 : 0), RULE_WORDS)} будет создано.
          </span>
        </div>
      {/if}
      {#if outboundPreviewText}
        <div class="preview-outbound">
          <Info size={14} />
          <span>{outboundPreviewText}</span>
        </div>
      {/if}
    </WizardStep>

    <div class="actions desktop-only">
      <Button variant="ghost" size="md" onclick={closeAddWizard} disabled={submitting}>Отмена</Button>
      <div class="actions-right">
        {#if !isEditMode}
          <Button variant="secondary" size="md" onclick={() => doSave(true)} disabled={!canSave || submitting}>
            + Добавить ещё одно
          </Button>
        {/if}
        <Button variant="primary" size="md" onclick={() => doSave(false)} disabled={!canSave || submitting} iconBefore={iconCheck}>
          {isEditMode ? 'Сохранить изменения' : 'Сохранить'}
        </Button>
      </div>
    </div>

    <MobileBottomBar>
      <Button variant="ghost" size="sm" onclick={closeAddWizard} disabled={submitting}>Отмена</Button>
      <div style="flex:1"></div>
      <Button variant="primary" size="sm" onclick={() => doSave(false)} disabled={!canSave || submitting} iconBefore={iconCheck}>
        Сохранить
      </Button>
    </MobileBottomBar>

    {#if $mode === 'beginner'}
      <SbRouterServiceCatalogModal />
    {:else}
      <TemplatesModal mode="collect" servicesOnly={false} />
    {/if}
  </div>
{/if}

<style>
  .wizard {
    max-width: 720px;
    margin: 0 auto;
    padding: var(--sp-4);
  }
  .bc {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 14px;
  }
  .bc-back {
    background: transparent;
    border: 0;
    color: var(--text-muted);
    font-size: 12px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-family: inherit;
    padding: 0;
  }
  .bc-sep { color: var(--text-muted); }
  .bc-current { font-size: 12px; color: var(--text-secondary); }
  .title { margin: 0 0 4px; font-size: 24px; font-weight: 600; }
  .sub { margin: 0 0 24px; font-size: 14px; color: var(--text-muted); }
  .stepper {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 24px;
    padding: 14px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-size: 12px;
  }
  .connector { flex: 1; height: 1px; background: var(--border); min-width: 16px; }
  .picker-btn {
    display: grid;
    grid-template-columns: 40px 1fr auto;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 12px 14px;
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    border: 1px dashed var(--accent-line);
    color: var(--text-primary);
    cursor: pointer;
    font-family: inherit;
    text-align: left;
    margin-bottom: 14px;
  }
  .picker-icon {
    width: 40px;
    height: 40px;
    border-radius: 8px;
    background: var(--accent-soft);
    color: var(--accent);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .picker-title { font-size: 13.5px; font-weight: 600; }
  .picker-sub { font-size: 11.5px; color: var(--text-muted); margin-top: 2px; }
  .picker-chev { color: var(--text-muted); font-size: 18px; }
  .grid-3 {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 10px;
  }
  @media (max-width: 600px) {
    .grid-3 { grid-template-columns: 1fr; }
  }
  .tunnel-row {
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }
  .tunnel-cap {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    margin-bottom: 4px;
  }
  .tunnel-count {
    font-size: 10px;
    font-weight: 600;
    text-transform: none;
    letter-spacing: 0;
    padding: 2px 6px;
    border-radius: 999px;
    background: var(--accent-soft);
    color: var(--accent);
  }
  .tunnel-hint {
    margin: 0 0 8px;
    font-size: 11.5px;
    color: var(--text-muted);
  }
  .tunnel-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .t-chip {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    cursor: pointer;
    font-family: inherit;
    color: inherit;
  }
  .t-chip.selected {
    background: var(--accent-soft);
    border-color: var(--accent);
  }
  .t-chip .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 500;
  }
  .empty-tunnels {
    font-size: 12px;
    color: var(--text-muted);
    font-style: italic;
  }
  .preview-hint {
    margin: 0 0 8px;
    font-size: 13px;
    color: var(--text-secondary);
  }
  .preview-info,
  .preview-outbound {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    font-size: 12px;
    color: var(--text-muted);
    background: rgba(107, 148, 168, 0.08);
    border-radius: var(--radius-sm);
  }
  .preview-outbound {
    margin-top: 8px;
    color: var(--text-secondary);
    background: var(--accent-soft);
    border: 1px solid color-mix(in srgb, var(--accent) 25%, transparent);
  }
  .actions {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px 0;
  }
  .actions-right {
    display: flex;
    gap: 6px;
  }
  @media (max-width: 768px) {
    .wizard {
      min-width: 0;
      max-width: 100%;
      padding: 0.875rem;
      padding-bottom: calc(0.875rem + 72px);
      overflow: hidden;
    }
    .title {
      font-size: 20px;
    }
    .stepper {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 6px;
      padding: 8px;
      margin-bottom: 16px;
    }
    .connector {
      display: none;
    }
    .actions.desktop-only {
      display: none;
    }
  }
</style>
