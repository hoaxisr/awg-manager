<script lang="ts">
  import { normalizeSubnet, parseSubnets, serializeSubnets } from '$lib/utils/subnets';

  interface Props {
    value: string;
    onChange: (next: string) => void;
    inputId?: string;
  }
  let { value, onChange, inputId }: Props = $props();

  // Оптимистичное локальное состояние (см. PortChipsInput): onChange сохраняет
  // асинхронно, value лагает. $effect ресинкает при внешнем изменении.
  let chips = $state<string[]>([]);
  $effect(() => {
    chips = parseSubnets(value);
  });

  let draft = $state('');
  let error = $state('');

  function commitDraft() {
    const raw = draft.trim();
    if (!raw) return;
    const parts = raw.split(/[,\s]+/).filter(Boolean);
    const additions: string[] = [];
    const seen = new Set(chips);
    for (const p of parts) {
      const n = normalizeSubnet(p);
      if (!n) {
        error = `неверный IP/CIDR: ${p}`;
        return;
      }
      if (seen.has(n)) continue;
      seen.add(n);
      additions.push(n);
    }
    draft = '';
    error = '';
    if (additions.length === 0) return;
    chips = [...chips, ...additions];
    onChange(serializeSubnets(chips));
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      commitDraft();
    }
  }

  function remove(c: string) {
    chips = chips.filter((x) => x !== c);
    onChange(serializeSubnets(chips));
  }
</script>

<div class="port-chips">
  <div class="chips-box">
    {#each chips as c (c)}
      <span class="port-chip">
        {c}
        <button type="button" class="chip-x" onclick={() => remove(c)} aria-label="удалить подсеть">✕</button>
      </span>
    {/each}
    <input
      id={inputId}
      class="chip-input"
      type="text"
      bind:value={draft}
      onkeydown={onKeydown}
      oninput={() => (error = '')}
      placeholder="IP или подсеть, напр. 203.0.113.0/24"
    />
  </div>
  {#if error}<div class="port-error">{error}</div>{/if}
</div>

<style>
  .chips-box {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    padding: 5px 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
  }
  .port-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 3px 4px 3px 8px;
    font-size: 12.5px;
    font-family: var(--font-mono);
    color: var(--text-primary);
  }
  .chip-x {
    background: transparent;
    border: 0;
    color: var(--text-muted);
    cursor: pointer;
    padding: 2px;
    display: inline-flex;
    align-items: center;
    transition: color var(--t-fast);
  }
  .chip-x:hover {
    color: var(--text-primary);
  }
  .chip-input {
    flex: 1;
    min-width: 110px;
    border: none;
    background: transparent;
    color: var(--text-primary);
    padding: 2px;
    outline: none;
    font-size: 12.5px;
    font-family: inherit;
  }
  .chip-input::placeholder {
    color: var(--text-muted);
  }
  .port-error {
    color: var(--error);
    font-size: 11.5px;
    margin-top: 6px;
  }
</style>
