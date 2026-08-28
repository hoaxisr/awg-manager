<script lang="ts">
  /**
   * Строка формы «метка — контрол»: одна раскладка на все поля детали.
   *
   * Заведена по итогам разбора вёрстки 2026-08-27: в одной секции «Сеть»
   * уживались три схемы сразу — метка слева в строку, метка сверху над полем и
   * метка с абзацем описания. Ширина метки берётся из `--form-label-w`
   * контейнера, поэтому строки выравниваются между собой без ручных отступов.
   */
  import type { Snippet } from 'svelte';

  interface Props {
    label: string;
    /**
     * id контрола внутри — связывает метку с полем (`<label for>`). У групп со
     * своей `aria-label` (SegmentedControl, ChipMultiSelect) не нужен: там
     * метка остаётся подписью, а не ярлыком поля.
     */
    for?: string;
    /** Пояснение под контролом; занимает колонку контрола, а не всю ширину. */
    hint?: string;
    children: Snippet;
  }

  let { label, for: forId, hint, children }: Props = $props();
</script>

<div class="form-row">
  {#if forId}
    <label class="form-row-label" for={forId}>{label}</label>
  {:else}
    <span class="form-row-label">{label}</span>
  {/if}
  <div class="form-row-control">
    {@render children()}
    {#if hint}<span class="form-row-hint">{hint}</span>{/if}
  </div>
</div>

<style>
  /* Метка держится ВЕРХА строки, а не её середины: у контрола с подсказкой в
     две-три строки центрированная метка уезжала вниз и переставала читаться
     как подпись к первому элементу. */
  .form-row {
    display: grid;
    grid-template-columns: var(--form-label-w, 140px) minmax(0, 1fr);
    gap: 0.75rem;
    align-items: start;
    min-width: 0;
  }

  .form-row-label {
    font-size: 13px;
    color: var(--color-text-secondary);
    line-height: 1.3;
    /* Выравнивание по первой строке контрола: у полей и сегментов высота
       28-30px, у метки — одна строка 17px. */
    padding-top: 0.4375rem;
  }

  .form-row-control {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    min-width: 0;
  }

  .form-row-hint {
    font-size: 12px;
    color: var(--color-text-muted);
  }

  /* Узкий экран: метка встаёт над контролом — на 340px колонка в 140px
     съедала бы поле целиком. */
  @media (max-width: 720px) {
    .form-row {
      grid-template-columns: minmax(0, 1fr);
      gap: 0.25rem;
      align-items: stretch;
    }
  }
</style>
