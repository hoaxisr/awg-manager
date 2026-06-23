<!--
  Источник дизайна: page-inspector-v2.html — Step-1 «DNS-решение» (НОВАЯ ветка fakeip).
  Показывает, какое DNS-правило сматчило домен, какой DNS-сервер отвечает и
  классификацию (fakeip → туннель / real → реальный IP / local → роутер).
  Над route-секцией (Step 2). Lucide-иконки, app theme tokens.
-->

<script lang="ts">
  import { ChevronDown, ChevronRight, ArrowRight, Check } from 'lucide-svelte';
  import type { SingboxRouterInspectDNSResult } from '$lib/types';

  let { result }: { result: SingboxRouterInspectDNSResult } = $props();

  let showRules = $state(false);

  // fakeip = акцент (домен фейкнут → туннель); real = «реальный» upstream;
  // local = резолв на роутере.
  let classLabel = $derived.by(() => {
    switch (result.classification) {
      case 'fakeip': return 'fakeip';
      case 'local': return 'local';
      default: return 'real';
    }
  });

  let ruleLabel = $derived(
    result.matchedRule === -1
      ? `по default → ${result.final}`
      : `DNS-правило #${result.matchedRule + 1}`,
  );
</script>

<div class="card stage">
  <div class="sh">
    <span class="no">1</span>
    <span class="st">DNS-решение</span>
  </div>

  <div class="res">
    <div class="inp">
      <div class="v">{result.input}</div>
      <div class="ty">{result.inputType === 'ip' ? 'IP' : 'домен'}</div>
    </div>
    <div class="arr"><ArrowRight size={14} /></div>
    <div class="dst">
      <span class="srv tone-{result.classification}">{classLabel}</span>
      <div class="m">{ruleLabel}</div>
    </div>
  </div>

  <div class="detail">
    <div class="dh">
      <span class="rn">{result.matchedRule === -1 ? 'нет матча' : `DNS-правило #${result.matchedRule + 1}`}</span>
      <span class="badge tone-{result.classification}">→ {classLabel}</span>
      <span class="ob">сервер: {result.server}</span>
    </div>
  </div>

  {#if result.classification === 'fakeip'}
    <div class="verdict">
      {result.input} получит <b>fake-ip{result.pool ? ` из пула ${result.pool}` : ''}</b>
      → трафик пойдёт в туннель (конкретный адрес выделяется в рантайме).
      Дальше — шаг 2.
    </div>
  {:else}
    <div class="verdict muted">
      {result.input} резолвится в {result.classification === 'local' ? 'адрес на роутере (local)' : 'реальный IP (upstream)'} —
      маршрутизация по домену не применяется, route сработает только по ip_cidr / sniff.
    </div>
  {/if}

  {#if result.note}
    <div class="note">⚠ {result.note}</div>
  {/if}

  {#if result.matches.length > 0}
    <button type="button" class="toggle" onclick={() => (showRules = !showRules)} aria-expanded={showRules}>
      {#if showRules}<ChevronDown size={13} />{:else}<ChevronRight size={13} />{/if}
      разбор DNS-правил ({result.matches.length})
    </button>
    {#if showRules}
      <div class="rules-list">
        {#each result.matches as m (m.index)}
          <div class="rrow" class:is-matched={m.matched} class:is-winner={m.index === result.matchedRule}>
            <span class="ri">#{String(m.index + 1).padStart(2, '0')}</span>
            <div class="rc">
              <div class="rh">
                <span class="rsrv">→ {m.server || '—'}</span>
                {#if m.matched}<Check class="check" size={13} />{/if}
              </div>
              {#if m.conditions && m.conditions.length > 0}
                <div class="conds">
                  {#each m.conditions as cond}<span class="cond-chip">{cond}</span>{/each}
                </div>
              {/if}
              {#if m.reason}<div class="reason">{m.reason}</div>{/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .sh { display: flex; align-items: center; gap: 8px; }
  .no {
    width: 20px;
    height: 20px;
    border-radius: 50%;
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    border: 1px solid var(--accent-line);
    color: var(--accent);
    font-size: 11px;
    font-weight: 700;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
  }
  .st { color: var(--text-primary); font-size: 13px; font-weight: 600; }

  .res {
    display: grid;
    grid-template-columns: 1fr auto auto;
    align-items: center;
    gap: 12px;
  }
  .inp .v { font-size: 15px; color: var(--text-primary); word-break: break-all; font-family: var(--font-mono); }
  .inp .ty { font-size: 11px; color: var(--text-muted); }
  .arr { color: var(--text-muted); display: inline-flex; }
  .dst { text-align: right; display: flex; flex-direction: column; align-items: flex-end; gap: 3px; }
  .dst .m { font-size: 11px; color: var(--text-muted); }

  .srv {
    font-size: 11px;
    border-radius: 5px;
    padding: 2px 8px;
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .srv.tone-fakeip { color: var(--accent); border-color: var(--accent-line); background: color-mix(in srgb, var(--accent) 8%, transparent); }
  .srv.tone-real   { color: var(--info); border-color: color-mix(in srgb, var(--info) 35%, var(--border)); background: color-mix(in srgb, var(--info) 8%, transparent); }
  .srv.tone-local  { color: var(--success); border-color: color-mix(in srgb, var(--success) 35%, var(--border)); background: color-mix(in srgb, var(--success) 8%, transparent); }

  .detail { display: flex; flex-direction: column; gap: 4px; }
  .dh { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; font-size: 12px; }
  .rn { color: var(--text-primary); font-weight: 600; }
  .ob { color: var(--text-muted); font-size: 11px; font-family: var(--font-mono); }
  .badge {
    font-size: 10px;
    font-weight: 700;
    border-radius: 4px;
    padding: 1px 6px;
    border: 1px solid var(--border);
    font-family: var(--font-mono);
  }
  .badge.tone-fakeip { color: var(--accent); border-color: var(--accent-line); background: color-mix(in srgb, var(--accent) 8%, transparent); }
  .badge.tone-real   { color: var(--info); border-color: color-mix(in srgb, var(--info) 35%, var(--border)); }
  .badge.tone-local  { color: var(--success); border-color: color-mix(in srgb, var(--success) 35%, var(--border)); }

  .verdict {
    border-top: 1px solid var(--border);
    padding-top: 10px;
    font-size: 12px;
    line-height: 1.5;
    color: var(--text-secondary);
  }
  .verdict b { color: var(--accent); }
  .verdict.muted { color: var(--text-muted); }

  .note { font-size: 11px; color: var(--warning); }

  .toggle {
    background: transparent;
    border: 0;
    padding: 0;
    font-family: inherit;
    font-size: 11px;
    color: var(--text-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    align-self: flex-start;
    transition: color var(--t-fast);
  }
  .toggle:hover { color: var(--text-primary); }

  .rules-list { display: flex; flex-direction: column; gap: 6px; }
  .rrow {
    display: grid;
    grid-template-columns: 36px 1fr;
    gap: 10px;
    align-items: start;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
  }
  .rrow.is-matched {
    background: color-mix(in srgb, var(--success) 10%, var(--bg-tertiary));
    border-color: color-mix(in srgb, var(--success) 30%, var(--border));
  }
  .rrow.is-winner { border-left: 3px solid var(--success); }
  .ri { font-family: var(--font-mono); font-size: 12px; font-weight: 600; color: var(--text-muted); text-align: right; }
  .rc { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .rh { display: flex; align-items: center; gap: 6px; }
  .rsrv { font-size: 13px; font-weight: 600; color: var(--text-primary); font-family: var(--font-mono); }
  .rrow.is-matched :global(.check) { color: var(--success); }
  .conds { display: flex; flex-wrap: wrap; gap: 4px; }
  .cond-chip {
    display: inline-flex;
    align-items: center;
    padding: 2px 7px;
    border-radius: 4px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-secondary);
    font-family: var(--font-mono);
  }
  .reason { font-size: 11px; color: var(--text-muted); line-height: 1.4; font-style: italic; }

  @media (max-width: 768px) {
    .res { grid-template-columns: 1fr; gap: 6px; }
    .dst { text-align: left; align-items: flex-start; }
    .arr { transform: rotate(90deg); }
  }
</style>
