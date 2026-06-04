<script lang="ts">
  import { parsePortsString, serializePorts, parseDraftEntries, portKey, type PortEntry } from '$lib/utils/ports';

  interface Props {
    value: string;
    onChange: (next: string) => void;
    inputId?: string;
  }
  let { value, onChange, inputId }: Props = $props();

  // Optimistic local state, not a pure $derived of `value`: onChange persists
  // asynchronously (PUT + store reload), so `value` lags. Mutating chips
  // synchronously on each add/remove lets back-to-back actions see prior
  // changes — without this, two rapid adds both read a stale list and the
  // second overwrites the first. The $effect re-syncs from `value` on external
  // change (initial load, other editors, server reconcile).
  let chips = $state<PortEntry[]>([]);
  $effect(() => {
    chips = parsePortsString(value);
  });

  let draft = $state('');
  let error = $state('');

  function commitDraft() {
    const raw = draft.trim();
    if (!raw) return;
    const r = parseDraftEntries(raw);
    if (!r.ok) {
      error = r.error;
      return;
    }
    // Accept the whole draft or none; drop entries already present (existing
    // chips or duplicates within the same paste).
    const seen = new Set(chips.map(portKey));
    const additions: PortEntry[] = [];
    for (const e of r.entries) {
      const k = portKey(e);
      if (seen.has(k)) continue;
      seen.add(k);
      additions.push(e);
    }
    draft = '';
    error = '';
    if (additions.length === 0) return;
    chips = [...chips, ...additions];
    onChange(serializePorts(chips));
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      commitDraft();
    }
  }

  function remove(c: PortEntry) {
    chips = chips.filter((x) => portKey(x) !== portKey(c));
    onChange(serializePorts(chips));
  }
</script>

<div class="port-chips">
  <div class="chips-box">
    {#each chips as c (portKey(c))}
      <span class="port-chip">
        {c.port}/{c.proto}
        <button type="button" class="chip-x" onclick={() => remove(c)} aria-label="удалить порт">✕</button>
      </span>
    {/each}
    <input
      id={inputId}
      class="chip-input"
      type="text"
      bind:value={draft}
      onkeydown={onKeydown}
      oninput={() => (error = '')}
      placeholder="порт + протокол, напр. 443 TCP"
    />
  </div>
  {#if error}<div class="port-error">{error}</div>{/if}
</div>

<style>
  /* Box mirrors StatusDrawer .inp (radius/bg/border) so the chip field reads
     as a normal input within the drawer. */
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
  /* Removable chip — same shape/vars as sb-router SelectedTemplatesRow. */
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
