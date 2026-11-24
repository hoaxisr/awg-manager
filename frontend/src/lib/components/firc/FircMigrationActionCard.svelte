<script lang="ts" generics="T extends string">
  // Карточка-радиокнопка выбора действия в мастере миграции.
  import { Badge, type BadgeVariant } from '$lib/components/ui';
  import RichText from '$lib/components/ui/RichText.svelte';
  import type { Snippet } from 'svelte';

  interface Props {
    name: string;
    value: T;
    group: T;
    title: string;
    /** Пояснение; допускает <code>/<strong> (RichText). */
    hint: string;
    disabled?: boolean;
    /** Приглушить карточку (действие недоступно), независимо от занятости. */
    dimmed?: boolean;
    badge?: { variant: BadgeVariant; text: string };
    /** Бейдж, зависящий от состояния, — вместо `badge`. */
    badges?: Snippet;
  }

  let { name, value, group = $bindable(), title, hint, disabled = false, dimmed = false, badge, badges }: Props =
    $props();
</script>

<label class="action-card" class:selected={group === value} class:disabled-card={dimmed}>
  <input type="radio" {name} {value} {disabled} bind:group />
  <div class="action-card-body">
    <div class="action-card-title">
      <span>{title}</span>
      {#if badge}
        <Badge variant={badge.variant} size="sm">{badge.text}</Badge>
      {/if}
      {#if badges}{@render badges()}{/if}
    </div>
    <span class="hint"><RichText text={hint} /></span>
  </div>
</label>

<style>
  .action-card {
    display: flex;
    align-items: flex-start;
    gap: 9px;
    padding: 8px 10px;
    border-radius: 7px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    cursor: pointer;
    transition: border-color 0.12s, background 0.12s;
  }

  .action-card.selected {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 10%, var(--bg-tertiary));
  }

  .action-card.disabled-card {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .action-card input[type='radio'] {
    margin-top: 3px;
    /* Иначе flex сжимает радио пропорционально длине описания карточки. */
    flex-shrink: 0;
  }

  .action-card-body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1 1 auto;
  }

  .action-card-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
    flex-wrap: wrap;
  }
</style>
