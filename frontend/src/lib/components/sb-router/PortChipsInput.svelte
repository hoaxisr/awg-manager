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
  .chips-box {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    padding: 6px 8px;
    border: 1px solid var(--border, #3a4150);
    border-radius: 8px;
    background: var(--bg-input, #1e2430);
  }
  .port-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    background: var(--bg-tertiary, #2d4a63);
    border-radius: 6px;
    padding: 3px 8px;
    font-size: 13px;
  }
  .chip-x {
    background: none;
    border: none;
    color: inherit;
    opacity: 0.6;
    cursor: pointer;
    padding: 0;
    font-size: 12px;
  }
  .chip-x:hover {
    opacity: 1;
  }
  .chip-input {
    flex: 1;
    min-width: 110px;
    border: none;
    background: transparent;
    color: inherit;
    padding: 2px;
    outline: none;
    font-size: 13px;
  }
  .port-error {
    color: var(--error, #e06c5c);
    font-size: 12px;
    margin-top: 6px;
  }
</style>
