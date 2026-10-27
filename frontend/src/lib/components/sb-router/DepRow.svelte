<!--
  Источник дизайна: singbox-router/project/screens/StatusDrawerView.jsx (DepRow)
  При правках сверять с JSX напрямую.
-->

<script lang="ts">
  import { StatusDot } from '$lib/components/ui';
  import type { DepTone } from './drawerData';

  interface Props {
    tone: DepTone;
    label: string;
    hint?: string;
  }
  let { tone, label, hint }: Props = $props();

  // Маппинг drawerData tones → StatusDot variants
  let dotVariant = $derived(
    tone === 'success' ? 'success' as const :
    tone === 'error'   ? 'error'   as const :
    tone === 'warning' ? 'warning' as const :
    tone === 'info'    ? 'info'    as const :
    'muted' as const
  );
</script>

<div class="dep-row">
  <StatusDot variant={dotVariant} size="sm" />
  <div class="content">
    <div class="dep-label">{label}</div>
    {#if hint}<div class="hint">{hint}</div>{/if}
  </div>
</div>

<style>
  .dep-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
  }
  .content { flex: 1; min-width: 0; }
  .dep-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
    font-family: var(--font-sans);
  }
  .hint {
    font-size: 10.5px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    margin-top: 1px;
  }
</style>
