<!--
  Источник дизайна: singbox-router/project/parts/RuleSetModal.jsx (footer block)
  Отклонение: добавлен outbound picker (в JSX нет — обоснование в spec'е).
-->

<script lang="ts">
  import { m } from '$lib/i18n';
  import type { SingboxRouterOutbound } from '$lib/types';
  import { Badge, Button } from '$lib/components/ui';
  import { Check } from 'lucide-svelte';

  interface Props {
    selectedIds: string[];
    outbounds: SingboxRouterOutbound[];
    pickedOutbound: string | null;
    onPickOutbound: (val: string) => void;
    onClear: () => void;
    onCancel: () => void;
    onSubmit: () => void;
    submitting: boolean;
  }

  let {
    selectedIds, outbounds, pickedOutbound,
    onPickOutbound, onClear, onCancel, onSubmit, submitting,
  }: Props = $props();

  const count = $derived(selectedIds.length);
  const idList = $derived(selectedIds.map((id) => id.replace(/^(svc|rs):/, '')).join(', '));
  const submitDisabled = $derived(count === 0 || !pickedOutbound || submitting);
</script>

{#snippet iconCheck()}<Check size={14} />{/snippet}

<div class="footer" class:hidden={count === 0}>
  <div class="summary">
    <Badge variant="accent" size="md">{m.sb_router_bulk_selected({ count })}</Badge>
    <span class="ids" title={idList}>{idList}</span>
  </div>
  <div class="controls">
    <label class="picker">
      <span class="picker-label">{m.sb_router_templates_via()}</span>
      <select
        class="picker-select"
        value={pickedOutbound ?? ''}
        onchange={(e) => onPickOutbound((e.currentTarget as HTMLSelectElement).value)}
      >
        <option value="" disabled>{m.sb_router_templates_pick_outbound()}</option>
        {#each outbounds as ob (ob.tag)}
          <option value={ob.tag}>{ob.tag}</option>
        {/each}
        <option value="block">⛔ Block</option>
      </select>
    </label>
    <Button variant="ghost" size="sm" onclick={onClear} disabled={submitting}>{m.sb_router_templates_deselect_all()}</Button>
    <Button variant="ghost" size="sm" onclick={onCancel} disabled={submitting}>{m.common_cancel()}</Button>
    <Button
      variant="primary"
      size="sm"
      onclick={onSubmit}
      disabled={submitDisabled}
      iconBefore={iconCheck}
    >
      {m.sb_router_templates_create({ count })}
    </Button>
  </div>
</div>

<style>
  .footer {
    padding: var(--sp-3) var(--sp-5);
    border-top: 1px solid var(--border);
    background: var(--bg-tertiary);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
  }
  .footer.hidden {
    display: none;
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    flex: 1;
  }
  .ids {
    font-size: 11.5px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 360px;
  }
  .controls {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-shrink: 0;
  }
  .picker {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--text-muted);
  }
  .picker-select {
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-family: inherit;
    font-size: 12px;
    min-width: 140px;
  }
</style>
