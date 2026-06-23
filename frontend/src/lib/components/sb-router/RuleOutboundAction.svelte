<!--
  Действие правила в простом режиме: обычный outbound-tile или composite
  — один активный туннель + +N с названием группы в попапе.
-->

<script lang="ts">
  import { ArrowRight } from 'lucide-svelte';
  import type { OutboundDisplay } from './types';
  import OutboundTile from './OutboundTile.svelte';
  import OutboundToneIcon from './OutboundToneIcon.svelte';
  import OutboundChipLabel from './OutboundChipLabel.svelte';
  import { outboundDisplayTitle, outboundFullLabel } from './outboundLabelFormat';
  import { displayTone, isCompositeDisplay, toneClass } from './outboundTileTone';

  const TOOLTIP_MAX_ITEMS = 10;

  interface Props {
    outbound: OutboundDisplay;
  }

  let { outbound }: Props = $props();

  const showComposite = $derived(isCompositeDisplay(outbound));
  const overflowCount = $derived(outbound.otherMemberLabels?.length ?? 0);
  const memberTone = $derived(displayTone(outbound));
  const memberChipClass = $derived(`member-chip tone-chip ${toneClass(memberTone)}`);
  const memberChipLabel = $derived(
    outbound.kind === 'subscription'
      ? outbound.label
      : (outbound.activeMemberLabel ?? outbound.label),
  );
  const memberChipMeta = $derived(
    outbound.kind === 'subscription'
      ? outbound.metaSuffix
      : outbound.activeMemberMetaSuffix,
  );
  const memberChipTitle = $derived.by(() => {
    if (outbound.kind === 'subscription' && outbound.activeMemberLabel) {
      const member = outboundFullLabel(
        outbound.activeMemberLabel,
        outbound.activeMemberMetaSuffix,
      );
      return `${outboundDisplayTitle(outbound)} → ${member}`;
    }
    const memberLabel = outbound.activeMemberLabel ?? outbound.label;
    return outboundFullLabel(memberLabel, memberChipMeta);
  });
  const tooltipItems = $derived((outbound.otherMemberLabels ?? []).slice(0, TOOLTIP_MAX_ITEMS));
  const tooltipTruncated = $derived(overflowCount > TOOLTIP_MAX_ITEMS);
</script>

<div class="wrap" class:composite={showComposite}>
  {#if showComposite}
    <ArrowRight class="arrow" size={14} aria-hidden={true} />
    <span class={memberChipClass} title={memberChipTitle}>
      <OutboundToneIcon tone={memberTone} kind={outbound.kind} />
      <OutboundChipLabel
        label={memberChipLabel}
        metaSuffix={memberChipMeta}
      />
    </span>
    {#if overflowCount > 0}
      <span
        class="overflow-tip"
        tabindex="0"
        role="button"
        aria-label={`${outboundDisplayTitle(outbound)}, ещё ${overflowCount} туннелей`}
      >
        <span class="overflow-plus">+{overflowCount}</span>
        <div class="overflow-pop" role="tooltip">
          <div class="overflow-pop-title">{outboundFullLabel(outbound.label, outbound.metaSuffix)} (ещё {overflowCount})</div>
          <ul>
            {#each tooltipItems as label, index (`${label}:${index}`)}
              <li title={outbound.otherMemberTitles?.[index] ?? label}>{label}</li>
            {/each}
            {#if tooltipTruncated}
              <li class="ellipsis" aria-hidden="true">…</li>
            {/if}
          </ul>
        </div>
      </span>
    {/if}
  {:else}
    <ArrowRight class="arrow" size={14} aria-hidden={true} />
    <OutboundTile {outbound} />
  {/if}
</div>

<style>
  .wrap {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-shrink: 0;
    min-width: 0;
    max-width: 100%;
  }

  .wrap.composite {
    flex: 0 1 auto;
    min-width: 0;
    max-width: 100%;
    gap: 6px;
  }

  .wrap :global(.arrow) {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .member-chip {
    flex-shrink: 1;
    min-width: 0;
    max-width: 100%;
  }

  .overflow-tip {
    position: relative;
    display: inline-flex;
    flex-shrink: 0;
    overflow: visible;
    outline: none;
  }

  .overflow-tip:hover,
  .overflow-tip:focus-visible {
    z-index: 20;
  }

  .overflow-plus {
    box-sizing: border-box;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 24px;
    padding: 4px;
    border-radius: 6px;
    border: 1px dotted color-mix(in srgb, var(--text-muted) 40%, transparent);
    background: transparent;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 600;
    line-height: 1;
    white-space: nowrap;
    opacity: 0.85;
    cursor: pointer;
  }

  .overflow-tip:hover .overflow-plus,
  .overflow-tip:focus-visible .overflow-plus {
    opacity: 1;
    border-color: color-mix(in srgb, var(--text-muted) 65%, transparent);
    color: var(--text-secondary);
  }

  .overflow-pop {
    position: absolute;
    right: 0;
    bottom: calc(100% + 8px);
    width: max-content;
    max-width: min(28rem, calc(100vw - 16px));
    opacity: 0;
    visibility: hidden;
    transform: translateY(4px);
    transition:
      opacity 0.15s ease,
      transform 0.15s ease,
      visibility 0.15s ease;
    pointer-events: none;
    padding: 6px 8px;
    font-size: 11px;
    line-height: 1.35;
    color: var(--text-secondary);
    background: color-mix(in srgb, var(--bg-tertiary) 90%, var(--bg-secondary));
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: 0 6px 16px rgba(0, 0, 0, 0.3);
    text-align: left;
  }

  .overflow-tip:hover .overflow-pop,
  .overflow-tip:focus-visible .overflow-pop,
  .overflow-tip:focus-within .overflow-pop {
    opacity: 1;
    visibility: visible;
    transform: translateY(0);
  }

  .overflow-pop-title {
    margin-bottom: 4px;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
  }

  .overflow-pop ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .overflow-pop li {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--text-primary);
    white-space: nowrap;
  }

  .overflow-pop li + li {
    margin-top: 3px;
  }

  .overflow-pop li.ellipsis {
    color: var(--text-muted);
    letter-spacing: 0.12em;
    text-align: center;
  }
</style>
