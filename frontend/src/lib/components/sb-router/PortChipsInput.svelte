<script lang="ts">
  import { parsePortsString, serializePorts, parsePortEntry, type PortEntry } from '$lib/utils/ports';

  interface Props {
    value: string;
    onChange: (next: string) => void;
    inputId?: string;
  }
  let { value, onChange, inputId }: Props = $props();

  let chips = $derived<PortEntry[]>(parsePortsString(value));
  let draft = $state('');
  let error = $state('');

  function keyOf(c: PortEntry): string {
    return `${c.port}/${c.proto}`;
  }

  function addDraft() {
    const raw = draft.trim();
    if (!raw) return;
    const r = parsePortEntry(raw);
    if (!r.ok) {
      error = r.error;
      return;
    }
    if (chips.some((c) => keyOf(c) === keyOf(r.entry))) {
      draft = '';
      error = '';
      return; // duplicate: silently ignore
    }
    onChange(serializePorts([...chips, r.entry]));
    draft = '';
    error = '';
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      addDraft();
    }
  }

  function remove(c: PortEntry) {
    onChange(serializePorts(chips.filter((x) => keyOf(x) !== keyOf(c))));
  }
</script>

<div class="port-chips">
  <div class="chips-box">
    {#each chips as c (keyOf(c))}
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
