<!--
  Источник дизайна: singbox-router/project/parts/RuleCard.jsx (MatcherChip)
  При правках сверять с JSX напрямую — не угадывать spacing/typography/layout.
-->

<script lang="ts" module>
  import type { MatcherKind } from './types';

  /** Локализованная подпись для каждой категории матчера. */
  const LABELS: Record<MatcherKind, string> = {
    domain:   'домен',
    ip:       'IP',
    port:     'порт',
    src:      'источник',
    ruleset:  'набор',
    protocol: 'proto',
    private:  'тип',
  };
</script>

<script lang="ts">
  import { Link } from 'lucide-svelte';
  import RuleSetTypeIcon from './RuleSetTypeIcon.svelte';
  import type { RuleSetDisplayType } from '$lib/utils/ruleSetType';

  interface Props {
    kind: MatcherKind;
    label: string;
    /** Если true — значение mono шрифтом (для IP/port/cidr). Подпись слева всегда sans. */
    mono?: boolean;
    /** Иконка типа rule_set перед подписью «набор:» */
    rulesetType?: RuleSetDisplayType;
    /** Клик по чипу — открыть связанный редактор */
    onclick?: () => void;
    /** Подсказка для кликабельного чипа */
    title?: string;
  }
  let { kind, label, mono = false, rulesetType, onclick, title }: Props = $props();

  const isClickable = $derived(typeof onclick === 'function');
</script>

{#snippet chipPrefix()}
  {#if kind === 'domain'}
    <span class="chip-prefix">
      <Link size={10} strokeWidth={2.25} aria-hidden="true" />
      <span class="chip-key">{LABELS.domain}:</span>
    </span>
  {:else if kind === 'ruleset' && rulesetType}
    <span class="chip-prefix">
      <RuleSetTypeIcon type={rulesetType} size={10} />
      <span class="chip-key">{LABELS.ruleset}:</span>
    </span>
  {:else}
    <span class="chip-key">{LABELS[kind]}:</span>
  {/if}
{/snippet}

{#if isClickable}
  <button type="button" class="matcher-chip is-clickable" {title} aria-label={title} {onclick}>
    {@render chipPrefix()}
    <span class="chip-val" class:is-mono={mono}>{label}</span>
  </button>
{:else}
  <span class="matcher-chip">
    {@render chipPrefix()}
    <span class="chip-val" class:is-mono={mono}>{label}</span>
  </span>
{/if}

<style>
  /* Не .chip: имя занято утилитой Skeleton и app.css — их свойства протекали сюда. */
  .matcher-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
    padding: 2px 7px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    white-space: nowrap;
    line-height: 1.4;
    max-width: 100%;
    min-width: 0;
    overflow: hidden;
  }
  button.matcher-chip {
    margin: 0;
    cursor: pointer;
    transition:
      border-color var(--t-fast),
      background var(--t-fast),
      color var(--t-fast);
  }
  button.matcher-chip:hover {
    border-color: var(--border-hover);
    background: color-mix(in srgb, var(--accent) 8%, var(--bg-tertiary));
    color: var(--text-primary);
  }
  button.matcher-chip:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .chip-prefix {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    flex-shrink: 0;
  }
  .chip-key {
    font-size: 10px;
    color: var(--text-muted);
    font-family: var(--font-sans);
    flex-shrink: 0;
  }
  /* Иконки префикса — тот же цвет, что у подписи «домен:» / «набор:» */
  .chip-prefix :global(svg),
  .chip-prefix :global(.icon) {
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .chip-val {
    color: var(--text-primary);
    font-family: var(--font-sans);
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chip-val.is-mono {
    font-family: var(--font-mono);
  }
</style>
