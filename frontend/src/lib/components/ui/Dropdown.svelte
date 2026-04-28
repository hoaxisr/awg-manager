<script lang="ts" module>
  import type { Snippet } from 'svelte';

  export interface DropdownOption<T extends string = string> {
    value: T;
    label: string;
    description?: string;
    disabled?: boolean;
    icon?: Snippet;
    group?: string;
  }
</script>

<script lang="ts" generics="T extends string = string">
  import { onDestroy, tick } from 'svelte';

  interface Props {
    value?: T;
    options: DropdownOption<T>[];
    placeholder?: string;
    label?: string;
    hint?: string;
    error?: string;
    disabled?: boolean;
    required?: boolean;
    name?: string;
    id?: string;
    fullWidth?: boolean;
    onchange?: (v: T) => void;
  }

  let {
    value = $bindable('' as T),
    options,
    placeholder = '— выбрать —',
    label,
    hint,
    error,
    disabled = false,
    required = false,
    name,
    id,
    fullWidth = false,
    onchange,
  }: Props = $props();

  const fallbackId = `dropdown-${Math.random().toString(36).slice(2, 8)}`;
  const fieldId = $derived(id ?? fallbackId);

  let open = $state(false);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let panelEl = $state<HTMLDivElement | null>(null);
  let activeIndex = $state(-1);

  const selectedOption = $derived(options.find((o) => o.value === value));

  function openPanel() {
    if (disabled) return;
    open = true;
    // Focus the currently selected option, or the first enabled one.
    const selectedIdx = options.findIndex((o) => o.value === value);
    activeIndex = selectedIdx >= 0 ? selectedIdx : firstEnabledIndex();
  }

  function closePanel() {
    open = false;
    activeIndex = -1;
  }

  function toggle() {
    if (open) closePanel();
    else openPanel();
  }

  function firstEnabledIndex(): number {
    for (let i = 0; i < options.length; i++) {
      if (!options[i].disabled) return i;
    }
    return -1;
  }

  function nextEnabled(from: number, dir: 1 | -1): number {
    if (options.length === 0) return -1;
    let i = from;
    for (let n = 0; n < options.length; n++) {
      i = (i + dir + options.length) % options.length;
      if (!options[i].disabled) return i;
    }
    return from;
  }

  function selectIndex(idx: number) {
    const opt = options[idx];
    if (!opt || opt.disabled) return;
    if (value !== opt.value) {
      value = opt.value;
      onchange?.(opt.value);
    }
    closePanel();
    triggerEl?.focus();
  }

  async function handleTriggerKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      if (!open) {
        openPanel();
        await tick();
        scrollActiveIntoView();
        return;
      }
    }
    if (open) {
      handlePanelKey(e);
    }
  }

  function handlePanelKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      closePanel();
      triggerEl?.focus();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      activeIndex = nextEnabled(activeIndex, 1);
      scrollActiveIntoView();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      activeIndex = nextEnabled(activeIndex, -1);
      scrollActiveIntoView();
    } else if (e.key === 'Home') {
      e.preventDefault();
      activeIndex = firstEnabledIndex();
      scrollActiveIntoView();
    } else if (e.key === 'End') {
      e.preventDefault();
      for (let i = options.length - 1; i >= 0; i--) {
        if (!options[i].disabled) { activeIndex = i; break; }
      }
      scrollActiveIntoView();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (activeIndex >= 0) selectIndex(activeIndex);
    }
  }

  function scrollActiveIntoView() {
    if (!panelEl || activeIndex < 0) return;
    const item = panelEl.querySelector<HTMLElement>(`[data-idx="${activeIndex}"]`);
    item?.scrollIntoView({ block: 'nearest' });
  }

  function handleClickOutside(e: MouseEvent) {
    if (!open) return;
    const t = e.target as Node | null;
    if (t && (triggerEl?.contains(t) || panelEl?.contains(t))) return;
    closePanel();
  }

  $effect(() => {
    if (open) {
      document.addEventListener('mousedown', handleClickOutside);
      return () => document.removeEventListener('mousedown', handleClickOutside);
    }
  });

  onDestroy(() => {
    document.removeEventListener('mousedown', handleClickOutside);
  });
</script>

<div class="field" class:full-width={fullWidth} class:has-error={!!error}>
  {#if label}
    <label for={fieldId} class="field-label">{label}{#if required}<span class="required">*</span>{/if}</label>
  {/if}
  <div class="control">
    <button
      bind:this={triggerEl}
      id={fieldId}
      type="button"
      class="trigger"
      class:open
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-controls="{fieldId}-listbox"
      {disabled}
      onclick={toggle}
      onkeydown={handleTriggerKey}
    >
      <span class="trigger-content">
        {#if selectedOption}
          {#if selectedOption.icon}
            <span class="trigger-icon">{@render selectedOption.icon()}</span>
          {/if}
          <span class="trigger-label">{selectedOption.label}</span>
        {:else}
          <span class="trigger-placeholder">{placeholder}</span>
        {/if}
      </span>
      <span class="chevron" class:rotated={open} aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>
      </span>
    </button>

    {#if name}
      <input type="hidden" {name} {value} />
    {/if}

    {#if open}
      <div
        bind:this={panelEl}
        id="{fieldId}-listbox"
        role="listbox"
        class="panel"
        tabindex="-1"
        aria-label={label ?? placeholder}
        onkeydown={handlePanelKey}
      >
        {#each options as opt, idx (opt.value)}
          {@const isSelected = opt.value === value}
          {@const isActive = idx === activeIndex}
          {@const showGroup = opt.group && (idx === 0 || options[idx - 1].group !== opt.group)}
          {#if showGroup}
            <div class="group-label" role="presentation">{opt.group}</div>
          {/if}
          <button
            type="button"
            class="option"
            class:selected={isSelected}
            class:active={isActive}
            class:in-group={!!opt.group}
            data-idx={idx}
            role="option"
            aria-selected={isSelected}
            disabled={opt.disabled}
            onmouseenter={() => (activeIndex = idx)}
            onclick={() => selectIndex(idx)}
          >
            {#if opt.icon}
              <span class="option-icon">{@render opt.icon()}</span>
            {/if}
            <span class="option-text">
              <span class="option-label">{opt.label}</span>
              {#if opt.description}
                <span class="option-desc">{opt.description}</span>
              {/if}
            </span>
            {#if isSelected}
              <span class="option-check" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
              </span>
            {/if}
          </button>
        {/each}
        {#if options.length === 0}
          <div class="empty">Нет вариантов</div>
        {/if}
      </div>
    {/if}
  </div>
  {#if error}
    <span class="hint hint-error">{error}</span>
  {:else if hint}
    <span class="hint">{hint}</span>
  {/if}
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .field.full-width { width: 100%; }

  .field-label {
    font-size: 13px;
    color: var(--color-text-secondary);
    font-weight: 500;
  }

  .required {
    color: var(--color-error);
    margin-left: 0.25rem;
  }

  .control {
    position: relative;
  }

  .trigger {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    background: var(--color-bg-primary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    color: var(--color-text-primary);
    font: inherit;
    font-size: 13px;
    padding: 0.4375rem 0.75rem 0.4375rem 0.625rem;
    line-height: 1.4;
    cursor: pointer;
    text-align: left;
    transition: border-color var(--t-fast) ease, background var(--t-fast) ease;
  }

  .trigger:hover:not(:disabled) {
    border-color: var(--color-border-hover);
  }

  .trigger:focus-visible,
  .trigger.open {
    outline: none;
    border-color: var(--color-accent);
  }

  .trigger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .has-error .trigger {
    border-color: var(--color-error);
  }

  .trigger-content {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
    flex: 1;
  }

  .trigger-icon {
    display: inline-flex;
    color: var(--color-text-muted);
    flex-shrink: 0;
  }

  .trigger-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .trigger-placeholder {
    color: var(--color-text-muted);
  }

  .chevron {
    display: inline-flex;
    color: var(--color-text-muted);
    flex-shrink: 0;
    transition: transform var(--t-fast) ease;
  }
  .chevron.rotated {
    transform: rotate(180deg);
  }
  .chevron svg { width: 16px; height: 16px; }

  .panel {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    z-index: 50;
    background: var(--color-bg-secondary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    padding: 4px;
    max-height: 280px;
    overflow-y: auto;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
    display: flex;
    flex-direction: column;
    gap: 1px;
    animation: dropdown-enter 120ms ease-out;
  }

  @keyframes dropdown-enter {
    from { opacity: 0; transform: translateY(-4px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .option {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4375rem 0.5rem;
    background: transparent;
    border: none;
    border-radius: calc(var(--radius-sm) - 2px);
    color: var(--color-text-primary);
    font: inherit;
    font-size: 13px;
    cursor: pointer;
    text-align: left;
    width: 100%;
    transition: background var(--t-fast) ease;
  }

  .option:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .option.active:not(:disabled) {
    background: var(--color-bg-hover);
  }

  .option.selected {
    color: var(--color-accent);
    font-weight: 500;
  }

  .option-icon {
    display: inline-flex;
    color: var(--color-text-muted);
    flex-shrink: 0;
  }
  .option.selected .option-icon { color: var(--color-accent); }

  .option-text {
    display: flex;
    flex-direction: column;
    gap: 1px;
    flex: 1;
    min-width: 0;
  }

  .option-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .option-desc {
    font-size: 11px;
    color: var(--color-text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .option-check {
    display: inline-flex;
    color: var(--color-accent);
    flex-shrink: 0;
  }
  .option-check svg { width: 14px; height: 14px; }

  .group-label {
    padding: 0.375rem 0.5rem 0.125rem;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--color-text-muted);
    font-weight: 600;
  }

  .option.in-group {
    padding-left: 0.875rem;
  }

  .empty {
    padding: 0.625rem 0.5rem;
    font-size: 12px;
    color: var(--color-text-muted);
    text-align: center;
  }

  .hint {
    font-size: 12px;
    color: var(--color-text-muted);
  }
  .hint-error {
    color: var(--color-error);
  }
</style>
