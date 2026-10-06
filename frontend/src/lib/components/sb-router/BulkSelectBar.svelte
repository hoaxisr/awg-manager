<!--
  Общий бар массового выделения (F8 — bulk outbound change).
  Фиксирован снизу панели: "N выбрано · [Dropdown] · Применить · Отмена".
  Переиспользуется в RulesPanel (tproxy) и его fakeip-зеркале.
-->

<script lang="ts">
  import { m } from '$lib/i18n';
  import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';

  interface Props {
    count: number;
    options: { value: string; label: string }[];
    applyLabel?: string;
    onapply: (value: string) => void | Promise<void>;
    oncancel: () => void;
    busy?: boolean;
    allowEmpty?: boolean;
  }

  let { count, options, applyLabel, onapply, oncancel, busy = false, allowEmpty = false }: Props = $props();

  let value = $state('');

  $effect(() => {
    if (!options.some((o) => o.value === value)) {
      value = options[0]?.value ?? '';
    }
  });

  async function handleApply() {
    if (busy || (!allowEmpty && !value) || count === 0) return;
    await onapply(value);
  }
</script>

<div class="bulk-select-bar">
  <span class="count">{m.sb_router_bulk_selected({ count })}</span>
  <div class="dropdown-slot">
    <Dropdown bind:value options={options as DropdownOption[]} disabled={busy} fullWidth />
  </div>
  <Button
    variant="primary"
    size="sm"
    disabled={busy || (!allowEmpty && !value) || count === 0}
    loading={busy}
    onclick={handleApply}
  >
    {applyLabel ?? m.common_apply()}
  </Button>
  <Button variant="ghost" size="sm" disabled={busy} onclick={oncancel}>
    {m.common_cancel()}
  </Button>
</div>

<style>
  .bulk-select-bar {
    position: sticky;
    bottom: var(--sp-3, 12px);
    z-index: 5;
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    padding: var(--sp-3) var(--sp-4);
    background: color-mix(in srgb, var(--bg-primary) 92%, transparent);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    backdrop-filter: blur(8px);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
  }

  .count {
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .dropdown-slot {
    flex: 1;
    min-width: 0;
    max-width: 360px;
  }

  @media (max-width: 640px) {
    .bulk-select-bar {
      flex-wrap: wrap;
    }
    .dropdown-slot {
      max-width: none;
      flex-basis: 100%;
      order: 1;
    }
  }
</style>
