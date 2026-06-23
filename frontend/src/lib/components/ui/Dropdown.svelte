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
  import { ChevronDown, Check } from 'lucide-svelte';

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
  let menuUp = $state(false);

  // Computed coords for fixed-position portaled panel.
  let panelTop = $state(0);
  let panelLeft = $state(0);
  let panelWidth = $state(0);

  const selectedOption = $derived(options.find((o) => o.value === value));

  const VIEWPORT_PADDING = 8;
  const PANEL_GAP = 4;
  const ESTIMATED_PANEL_HEIGHT = 280; // matches max-height in CSS

  // Portal action: appends node to document.body so the dropdown panel
  // escapes any ancestor `overflow: hidden|auto` clipping (e.g. modal body).
  function portal(node: HTMLElement, target: HTMLElement = document.body) {
    target.appendChild(node);
    return {
      destroy() {
        if (node.parentNode === target) {
          target.removeChild(node);
        }
      },
    };
  }

  function recomputePlacement() {
    if (!triggerEl) return;
    const rect = triggerEl.getBoundingClientRect();
    panelLeft = rect.left;
    panelWidth = rect.width;

    // Use measured panel height if available, otherwise estimate.
    const measuredH = panelEl?.getBoundingClientRect().height ?? 0;
    const panelHeight = measuredH > 0 ? measuredH : ESTIMATED_PANEL_HEIGHT;

    const spaceBelow = window.innerHeight - rect.bottom - VIEWPORT_PADDING;
    const spaceAbove = rect.top - VIEWPORT_PADDING;

    // Flip up only if there isn't enough room below AND there's more room above.
    const flip = panelHeight > spaceBelow && spaceAbove > spaceBelow;
    menuUp = flip;

    if (flip) {
      panelTop = Math.max(VIEWPORT_PADDING, rect.top - panelHeight - PANEL_GAP);
    } else {
      panelTop = rect.bottom + PANEL_GAP;
    }
  }

  function openPanel() {
    if (disabled) return;
    open = true;
    menuUp = false;
    // Capture initial coords from trigger BEFORE panel is mounted, so
    // the portal renders at a sensible position from frame one.
    if (triggerEl) {
      const rect = triggerEl.getBoundingClientRect();
      panelLeft = rect.left;
      panelWidth = rect.width;
      panelTop = rect.bottom + PANEL_GAP;
    }
    // Focus the currently selected option, or the first enabled one.
    const selectedIdx = options.findIndex((o) => o.value === value);
    activeIndex = selectedIdx >= 0 ? selectedIdx : firstEnabledIndex();
  }

  function closePanel() {
    open = false;
    activeIndex = -1;
    menuUp = false;
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
    // panelEl ref still resolves correctly even when portaled to body.
    if (t && (triggerEl?.contains(t) || panelEl?.contains(t))) return;
    closePanel();
  }

  $effect(() => {
    if (open) {
      document.addEventListener('mousedown', handleClickOutside);
      return () => document.removeEventListener('mousedown', handleClickOutside);
    }
  });

  $effect(() => {
    if (!open || !panelEl) return;
    // Initial placement on the next frame so the panel has been laid out.
    const raf = requestAnimationFrame(recomputePlacement);
    // Recompute when the menu's own size changes (async content load, group expansion).
    const ro = new ResizeObserver(() => recomputePlacement());
    ro.observe(panelEl);
    // Recompute on any ancestor scroll (capture phase catches scroll events
    // from any scroll container, including modal body) and on window resize.
    const onWindowChange = () => recomputePlacement();
    window.addEventListener('resize', onWindowChange);
    window.addEventListener('scroll', onWindowChange, true);
    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      window.removeEventListener('resize', onWindowChange);
      window.removeEventListener('scroll', onWindowChange, true);
    };
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
        <ChevronDown size={16} />
      </span>
    </button>

    {#if name}
      <input type="hidden" {name} {value} />
    {/if}

    {#if open}
      <div
        bind:this={panelEl}
        use:portal
        id="{fieldId}-listbox"
        role="listbox"
        class="dropdown-panel"
        class:menu-up={menuUp}
        tabindex="-1"
        aria-label={label ?? placeholder}
        style="top: {panelTop}px; left: {panelLeft}px; width: {panelWidth}px;"
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
                <Check size={14} strokeWidth={2.5} />
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

  /* Panel is portaled to <body> with position: fixed, so it escapes any
     ancestor overflow clipping (modal body, side drawer, etc). The
     --z-floating tier sits above Modal and SideDrawer — see app.css. */
  :global(.dropdown-panel) {
    position: fixed;
    z-index: var(--z-floating);
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

  :global(.dropdown-panel.menu-up) {
    animation: dropdown-enter-up 120ms ease-out;
  }

  @keyframes dropdown-enter {
    from { opacity: 0; transform: translateY(-4px); }
    to { opacity: 1; transform: translateY(0); }
  }

  @keyframes dropdown-enter-up {
    from { opacity: 0; transform: translateY(4px); }
    to { opacity: 1; transform: translateY(0); }
  }

  :global(.dropdown-panel .option) {
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

  :global(.dropdown-panel .option:disabled) {
    opacity: 0.4;
    cursor: not-allowed;
  }

  :global(.dropdown-panel .option.active:not(:disabled)) {
    background: var(--color-bg-hover);
  }

  :global(.dropdown-panel .option.selected) {
    color: var(--color-accent);
    font-weight: 500;
  }

  :global(.dropdown-panel .option-icon) {
    display: inline-flex;
    color: var(--color-text-muted);
    flex-shrink: 0;
  }
  :global(.dropdown-panel .option.selected .option-icon) { color: var(--color-accent); }

  :global(.dropdown-panel .option-text) {
    display: flex;
    flex-direction: column;
    gap: 1px;
    flex: 1;
    min-width: 0;
  }

  :global(.dropdown-panel .option-label) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  :global(.dropdown-panel .option-desc) {
    font-size: 11px;
    color: var(--color-text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  :global(.dropdown-panel .option-check) {
    display: inline-flex;
    color: var(--color-accent);
    flex-shrink: 0;
  }


  :global(.dropdown-panel .group-label) {
    padding: 0.375rem 0.5rem 0.125rem;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--color-text-muted);
    font-weight: 600;
  }

  :global(.dropdown-panel .option.in-group) {
    padding-left: 0.875rem;
  }

  :global(.dropdown-panel .empty) {
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
