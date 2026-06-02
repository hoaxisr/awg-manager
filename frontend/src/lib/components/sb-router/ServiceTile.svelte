<!--
  Источник дизайна: singbox-router/project/parts/Primitives.jsx (ServiceTile)
  Реализация: тонкая обёртка над $lib/components/routing/singboxRouter/PresetIcon,
  которая рендерит brand SVG (или fallback letter-monogram) по preset.id.
  Не дублируем letter-glyph rendering — у проекта есть унифицированная система.
-->

<script lang="ts" module>
  export type ServiceTileSize = 'sm' | 'md' | 'lg';
</script>

<script lang="ts">
  import PresetIcon from '$lib/components/routing/singboxRouter/PresetIcon.svelte';
  import { resolveIconSlug } from '$lib/utils/resolve-icon-slug';

  interface Props {
    /** Slug — preset iconSlug из каталога или известный brandIcons key. */
    serviceKey: string;
    /** Имя для подписи справа и для letter-fallback. */
    name?: string;
    /** Опциональный sub-text. */
    sub?: string;
    /** Размер тайла. sm=24, md=32 (default), lg=40. */
    size?: ServiceTileSize;
  }

  let { serviceKey, name, sub, size = 'md' }: Props = $props();
  let dim = $derived(size === 'sm' ? 24 : size === 'lg' ? 40 : 32);
  let iconSlug = $derived(
    resolveIconSlug(name ?? '', serviceKey !== 'custom' ? serviceKey : undefined) ?? 'custom',
  );
</script>

<div class="service-tile">
  <PresetIcon slug={iconSlug === 'custom' ? undefined : iconSlug} size={dim} label={name ?? serviceKey} />
  {#if name}
    <div class="text">
      <div class="name">{name}</div>
      {#if sub}<div class="sub">{sub}</div>{/if}
    </div>
  {/if}
</div>

<style>
  .service-tile {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-3);
    min-width: 0;
  }
  .text { min-width: 0; line-height: 1.2; }
  .name {
    font-weight: 600;
    font-size: var(--fs-base);
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    font-size: var(--fs-xs);
    color: var(--text-muted);
    margin-top: 2px;
  }
</style>
