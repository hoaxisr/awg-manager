<!--
  Источник дизайна: singbox-router/project/screens/RuleDetail.jsx (RuleDetailScreen)
  Главная композиция Trace UI: header (breadcrumb + title + clear),
  input row (domain + port + Проверить), result hero (path stations),
  per-rule list (TraceRuleRow × N).
-->

<script lang="ts">
  import { onMount } from 'svelte';
  import { Globe, Funnel, MonitorSmartphone } from 'lucide-svelte';
  import { Button, SectionLabel } from '$lib/components/ui';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { ArrowDown } from 'lucide-svelte';
  import {
    traceInput,
    traceResult,
    traceLoading,
    traceError,
    dnsResult,
    dnsLoading,
    dnsError,
    closeTrace,
    runTrace,
  } from './traceStore';
  import TracePathStation, { type TracePathTone } from './TracePathStation.svelte';
  import TraceRuleRow from './TraceRuleRow.svelte';
  import TraceDNSStep from './TraceDNSStep.svelte';

  // embedded: панель встроена в модал/контейнер с собственным заголовком
  // (FakeIP-hero «Инспектор маршрутов») — прячем свою крошку «← Назад» и h1,
  // чтобы не дублировать. По умолчанию (страница sb-router) — со своей шапкой.
  let { embedded = false }: { embedded?: boolean } = $props();

  // Auto-run при mount если URL содержал ?q=X (traceStore уже заполнил traceInput.domain).
  onMount(() => {
    let cur = { domain: '' };
    traceInput.subscribe((v) => { cur = v; })();
    if (cur.domain.trim()) {
      void runTrace();
    }
  });

  const rules = singboxRouterStore.rules;

  let input = $derived($traceInput);
  let result = $derived($traceResult);
  let loading = $derived($traceLoading);
  let error = $derived($traceError);
  let allRules = $derived($rules);

  let dns = $derived($dnsResult);
  let dnsBusy = $derived($dnsLoading);
  let dnsErr = $derived($dnsError);

  let canSubmit = $derived(input.domain.trim().length > 0 && !loading);

  let outcomeTone: TracePathTone = $derived.by(() => {
    if (!result) return 'muted';
    if (result.destination === 'reject' || result.destination === 'block') return 'error';
    if (result.matchedRule === -1) return 'muted';
    return 'success';
  });
  let outcomeLabel = $derived.by(() => {
    if (!result) return '';
    if (result.destination === 'reject' || result.destination === 'block') return 'ЗАБЛОКИРОВАНО';
    if (result.matchedRule === -1) return `ПО DEFAULT: ${result.final || 'direct'}`;
    return 'МАРШРУТ НАЙДЕН';
  });

  let matchedRuleAction = $derived.by(() => {
    if (!result || result.matchedRule === -1) return '';
    const m = result.matches.find((x) => x.index === result.matchedRule);
    return m?.action ?? '';
  });

  function handleDomainChange(e: Event) {
    const v = (e.target as HTMLInputElement).value;
    traceInput.update((cur) => ({ ...cur, domain: v }));
  }

  function handleQueryTypeChange(e: Event) {
    const v = (e.target as HTMLSelectElement).value;
    traceInput.update((cur) => ({ ...cur, queryType: v }));
  }

  function handleSourceIPChange(e: Event) {
    const v = (e.target as HTMLInputElement).value;
    traceInput.update((cur) => ({ ...cur, sourceIP: v }));
  }

  function handleSubmit() {
    void runTrace();
  }

  function handleKeyDown(e: KeyboardEvent) {
    if (e.key === 'Enter' && canSubmit) {
      handleSubmit();
    }
  }
</script>

<section class="trace">
  {#if !embedded}
    <header class="trace-header">
      <button type="button" class="back-btn" onclick={closeTrace} aria-label="Назад">
        ← Назад
      </button>
      <span class="bread-sep">/</span>
      <span class="bread-current">Куда поедет запрос</span>
    </header>

    <div class="title-row">
      <div class="title-group">
        <h1 class="title">Куда поедет запрос</h1>
        <p class="title-sub">Подставьте домен или IP — увидите, какое правило сработает и через какой туннель.</p>
      </div>
    </div>
  {/if}

  <!-- Input row -->
  <div class="input-card">
    <div class="input-wrap">
      <span class="globe"><Globe size={16} aria-hidden={true} /></span>
      <input
        class="input"
        type="text"
        placeholder="netflix.com или 192.168.1.1"
        value={input.domain}
        oninput={handleDomainChange}
        onkeydown={handleKeyDown}
        autocomplete="off"
        spellcheck={false}
      />
    </div>
    <Button variant="primary" size="md" onclick={handleSubmit} disabled={!canSubmit}>
      {loading ? 'Проверяем…' : 'Проверить'}
    </Button>
  </div>

  <!-- Доп. параметры для DNS-ветки (Step 1): тип запроса + источник (опц.). -->
  <div class="adv-row">
    <label class="adv-field">
      <span class="adv-label">Тип запроса</span>
      <select class="adv-input" value={input.queryType ?? ''} onchange={handleQueryTypeChange}>
        <option value="">любой</option>
        <option value="A">A</option>
        <option value="AAAA">AAAA</option>
        <option value="HTTPS">HTTPS</option>
      </select>
    </label>
    <label class="adv-field">
      <span class="adv-label">Источник (IP клиента) · опц.</span>
      <input
        class="adv-input"
        type="text"
        placeholder="192.168.1.70"
        value={input.sourceIP ?? ''}
        oninput={handleSourceIPChange}
        autocomplete="off"
        spellcheck={false}
      />
    </label>
  </div>

  {#if dnsErr}
    <div class="error-banner">⚠ DNS-инспектор: {dnsErr}</div>
  {/if}

  <!-- Step 1 — DNS-решение (новая ветка fakeip), над route-секцией. -->
  {#if dns}
    <TraceDNSStep result={dns} />
    {#if dns.classification === 'fakeip'}
      <div class="conn">
        <ArrowDown size={14} />
        домен фейкнут → в туннель
      </div>
    {/if}
  {:else if dnsBusy}
    <div class="dns-skel">Проверяем DNS-решение…</div>
  {/if}

  {#if error}
    <div class="error-banner">⚠ {error}</div>
  {/if}

  {#if result}
    {#if dns}
      <div class="step-head">
        <span class="step-no">2</span>
        <span class="step-title">Маршрут (route)</span>
      </div>
    {/if}
    <div class="result-hero tone-{outcomeTone}">
      <div class="outcome">
        <span class="outcome-dot tone-{outcomeTone}"></span>
        <span class="outcome-label">{outcomeLabel}</span>
      </div>

      <div class="path">
        <TracePathStation
          tone="accent"
          kicker="Откуда"
          title={result.input}
          sub={result.inputType === 'ip' ? 'IP' : 'домен'}
        >
          {#snippet icon()}
            <MonitorSmartphone size={16} aria-hidden={true} />
          {/snippet}
        </TracePathStation>

        <svg class="arrow" viewBox="0 0 32 14" width="32" height="14" aria-hidden="true">
          <line x1="0" y1="7" x2="22" y2="7" stroke="var(--text-muted)" stroke-width="2" stroke-dasharray="4 3" />
          <polyline points="20,3 26,7 20,11" fill="none" stroke="var(--text-muted)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>

        <TracePathStation
          tone={outcomeTone}
          kicker={result.matchedRule === -1 ? 'По default' : `Правило #${result.matchedRule}`}
          title={result.matchedRule === -1 ? 'нет матча' : `match ${matchedRuleAction}`}
        >
          {#snippet icon()}
            <Funnel size={16} aria-hidden={true} />
          {/snippet}
        </TracePathStation>

        <svg class="arrow" viewBox="0 0 32 14" width="32" height="14" aria-hidden="true">
          <line x1="0" y1="7" x2="22" y2="7" stroke="var(--text-muted)" stroke-width="2" stroke-dasharray="4 3" />
          <polyline points="20,3 26,7 20,11" fill="none" stroke="var(--text-muted)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>

        <TracePathStation
          tone={outcomeTone}
          kicker="Куда"
          title={result.destination}
          sub={result.matchedRule === -1 ? `final: ${result.final}` : undefined}
        >
          {#snippet icon()}
            <Globe size={16} aria-hidden={true} />
          {/snippet}
        </TracePathStation>
      </div>

      {#if result.note}
        <div class="note">⚠ {result.note}</div>
      {/if}
    </div>

    <div class="rules-section">
      <SectionLabel>Правила ({result.matches.length} из {allRules.length} проверены)</SectionLabel>
      <div class="rules-list">
        {#each result.matches as match (match.index)}
          <TraceRuleRow {match} winner={match.index === result.matchedRule} />
        {/each}
      </div>
    </div>
  {:else if !loading && !error}
    <div class="empty">
      <p>Введите домен или IP и нажмите «Проверить» — увидите подробный разбор маршрута.</p>
    </div>
  {/if}
</section>

<style>
  .trace {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }

  .trace-header {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .back-btn {
    background: transparent;
    border: 0;
    color: var(--text-muted);
    font-size: 12px;
    cursor: pointer;
    padding: 0;
    font-family: inherit;
    transition: color var(--t-fast);
  }
  .back-btn:hover { color: var(--text-primary); }
  .bread-sep { color: var(--text-muted); }
  .bread-current { font-size: 12px; color: var(--text-secondary); }

  .title-row {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
  }
  .title {
    margin: 0 0 4px 0;
    font-size: var(--fs-h3);
    font-weight: 600;
    color: var(--text-primary);
  }
  .title-sub {
    margin: 0;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }

  .input-card {
    padding: 14px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    display: flex;
    gap: 8px;
  }
  .input-wrap {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: 1;
    padding: 10px 14px;
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    border: 1px solid var(--accent-line);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 8%, transparent);
  }
  .globe { display: inline-flex; align-items: center; flex-shrink: 0; color: var(--accent); }
  .input {
    flex: 1;
    background: transparent;
    border: 0;
    outline: none;
    color: var(--text-primary);
    font-size: 15px;
    font-family: var(--font-mono);
  }

  .adv-row {
    display: grid;
    grid-template-columns: 1fr 2fr;
    gap: 8px;
  }
  .adv-field {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .adv-label {
    font-size: 11px;
    color: var(--text-muted);
  }
  .adv-input {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 7px 10px;
    color: var(--text-primary);
    font-size: 13px;
    font-family: var(--font-mono);
    outline: none;
  }
  .adv-input:focus {
    border-color: var(--accent-line);
  }

  .error-banner {
    padding: 10px 14px;
    background: color-mix(in srgb, var(--error) 12%, var(--bg-tertiary));
    border: 1px solid color-mix(in srgb, var(--error) 30%, var(--border));
    border-radius: var(--radius-sm);
    color: var(--error);
    font-size: 13px;
  }

  .conn {
    display: flex;
    align-items: center;
    gap: 8px;
    justify-content: center;
    color: var(--success);
    font-size: 11px;
    margin: -4px 0;
  }

  .dns-skel {
    padding: 14px;
    background: var(--bg-secondary);
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    font-size: 12px;
    color: var(--text-muted);
    text-align: center;
  }

  .step-head {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: -8px;
  }
  .step-no {
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
  .step-title { color: var(--text-primary); font-size: 13px; font-weight: 600; }

  .result-hero {
    padding: 18px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .result-hero.tone-success {
    background: linear-gradient(180deg, color-mix(in srgb, var(--success) 10%, var(--bg-secondary)) 0%, var(--bg-secondary) 100%);
    border-color: color-mix(in srgb, var(--success) 30%, var(--border));
  }
  .result-hero.tone-error {
    background: linear-gradient(180deg, color-mix(in srgb, var(--error) 10%, var(--bg-secondary)) 0%, var(--bg-secondary) 100%);
    border-color: color-mix(in srgb, var(--error) 30%, var(--border));
  }
  .result-hero.tone-muted {
    background: var(--bg-secondary);
  }

  .outcome {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 12px;
  }
  .outcome-dot {
    width: 10px;
    height: 10px;
    border-radius: 999px;
    flex-shrink: 0;
  }
  .outcome-dot.tone-success { background: var(--success); box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 25%, transparent); }
  .outcome-dot.tone-error   { background: var(--error);   box-shadow: 0 0 0 3px color-mix(in srgb, var(--error)   25%, transparent); }
  .outcome-dot.tone-muted   { background: var(--text-muted); }
  .outcome-label {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .result-hero.tone-success .outcome-label { color: var(--success); }
  .result-hero.tone-error   .outcome-label { color: var(--error); }
  .result-hero.tone-muted   .outcome-label { color: var(--text-muted); }

  .path {
    display: grid;
    grid-template-columns: 1fr auto 1fr auto 1fr;
    gap: 14px;
    align-items: center;
  }
  .arrow { flex-shrink: 0; }

  .note {
    margin-top: 12px;
    font-size: 12px;
    color: var(--warning);
  }

  .rules-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .rules-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .empty {
    padding: var(--sp-5);
    background: var(--bg-secondary);
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    text-align: center;
    font-size: 13px;
    color: var(--text-muted);
  }

  @media (max-width: 768px) {
    /* Input card: stack input-wrap + button vertically */
    .input-card {
      flex-direction: column;
    }
    .input-wrap {
      width: 100%;
    }
    .adv-row {
      grid-template-columns: 1fr;
    }

    /* Path stations: collapse 5-col grid to single column */
    .path {
      grid-template-columns: 1fr;
      gap: 4px;
    }
    /* Rotate horizontal arrows to point downward */
    .arrow {
      transform: rotate(90deg);
      justify-self: center;
    }

    /* result-hero padding tighter */
    .result-hero {
      padding: 12px;
    }
  }
</style>
