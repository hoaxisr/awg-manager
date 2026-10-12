<!--
  Источник дизайна: singbox-router/project/parts/RuleCard.jsx (ActionTile)
-->

<script lang="ts" module>
  export type OutboundTileSize = 'default' | 'compact';
</script>

<script lang="ts">
  import { m } from '$lib/i18n';
  import type { OutboundDisplay } from './types';
  import { displayTone, toneClass } from './outboundTileTone';
  import OutboundToneIcon from './OutboundToneIcon.svelte';
  import OutboundChipLabel from './OutboundChipLabel.svelte';
  import { outboundDisplayTitle } from './outboundLabelFormat';

  interface Props {
    outbound: OutboundDisplay;
    /** default — RuleCard; compact — expert tables (как Badge sm: remote, inline). */
    size?: OutboundTileSize;
  }
  let { outbound, size = 'default' }: Props = $props();

  let tone = $derived(displayTone(outbound));
  let cls = $derived(
    `tone-chip ${toneClass(tone)}${size === 'compact' ? ' tone-chip-compact' : ''}`,
  );
  let iconSize = $derived(size === 'compact' ? 10 : 14);
  let title = $derived(outbound.invalidHint ?? (outbound.kind === 'unknown' ? m.sb_router_outbound_not_found() : undefined));
</script>

{#if outbound.kind === 'block'}
  <div class={cls}>
    <OutboundToneIcon {tone} kind={outbound.kind} size={iconSize} />
    <span>{m.sb_router_wizard_opt_block()}</span>
  </div>
{:else if outbound.kind === 'direct' && tone !== 'invalid'}
  <div class={cls}>
    <OutboundToneIcon {tone} kind={outbound.kind} size={iconSize} />
    <span>{m.sb_router_wizard_opt_direct()}</span>
  </div>
{:else if tone === 'invalid'}
  <div class={cls} title={title}>
    <OutboundToneIcon {tone} kind={outbound.kind} size={iconSize} />
    <OutboundChipLabel
      label={outbound.label}
      metaSuffix={outbound.metaSuffix}
      title={outboundDisplayTitle(outbound)}
    />
  </div>
{:else if outbound.kind === 'via-route'}
  <div class={cls} title={m.sb_router_outbound_via_route()}>
    <OutboundToneIcon {tone} kind={outbound.kind} size={iconSize} />
    <span>{outbound.label}</span>
  </div>
{:else}
  <div class={cls} title={outbound.kind === 'unknown' ? m.sb_router_outbound_not_found() : undefined}>
    <OutboundToneIcon {tone} kind={outbound.kind} size={iconSize} />
    <OutboundChipLabel
      label={outbound.label}
      metaSuffix={outbound.metaSuffix}
      title={outboundDisplayTitle(outbound)}
    />
  </div>
{/if}
