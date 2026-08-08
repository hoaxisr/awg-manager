<!--
  Бэкенд применения правил: расхождение запрошенного и фактического режима.
  Пользователь включил галку и вправе считать, что она подействовала, а
  awgm-режим может не подняться (нет бандла под модель, не встали модули ядра) —
  расхождение показываем с причиной. Когда режимы совпали и работает awgm,
  говорим о его единственном заметном свойстве.
-->

<script lang="ts">
  import { TriangleAlert } from 'lucide-svelte';

  type AwgmBackendMode = 'legacy' | 'awgm';

  interface Props {
    /** Запрошенный пользователем режим; absent на legacy/mock-ответах. */
    requested?: AwgmBackendMode;
    /** Фактически работающий режим; absent на legacy/mock-ответах. */
    effective?: AwgmBackendMode;
    /** Причина расхождения; пуста, когда режимы совпали. */
    reason?: string;
  }
  let { requested, effective, reason }: Props = $props();

  // Оба поля обязаны быть известны: без запрошенного режима «расхождения» нет,
  // а есть неполный ответ — молчим, а не показываем догадку.
  let diverged = $derived(
    requested !== undefined && effective !== undefined && requested !== effective,
  );
</script>

{#if diverged}
  <div class="awgm-notice">
    <span class="icon"><TriangleAlert size={14} aria-hidden={true} /></span>
    <div class="text">
      <strong>Запрошен режим {requested}, работает {effective}.</strong>
      {#if reason}<p class="reason">{reason}</p>{/if}
    </div>
  </div>
{:else if effective === 'awgm'}
  <p class="awgm-hint">
    Правила живут в отдельной таблице awgm — перестройка firewall роутером их не стирает.
    Диагностика netfilter показывает штатные таблицы: правил этого режима в ней не видно.
  </p>
{/if}

<style>
  .awgm-notice {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--warning) 10%, var(--bg-tertiary));
    border-left: 3px solid var(--warning);
  }
  .icon {
    margin-top: 2px;
    flex-shrink: 0;
    display: inline-flex;
    color: var(--warning);
  }
  .text {
    flex: 1;
    min-width: 0;
    font-size: 12px;
    color: var(--text-secondary);
    line-height: 1.5;
  }
  .reason {
    margin: 4px 0 0;
    font-size: 11.5px;
    color: var(--text-muted);
    line-height: 1.4;
    word-break: break-word;
  }
  .awgm-hint {
    margin: 0;
    font-size: 11.5px;
    color: var(--text-muted);
    line-height: 1.4;
  }
</style>
