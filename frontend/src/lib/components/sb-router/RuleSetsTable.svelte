<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (RuleSetsTable + segment filter)
-->

<script lang="ts">
  import type { SingboxRouterRuleSet } from '$lib/types';
  import { Badge } from '$lib/components/ui';
  import { Edit3, Trash2 } from 'lucide-svelte';

  type RsFilter = 'all' | 'remote' | 'local' | 'inline' | 'dat';

  interface Props {
    ruleSets: SingboxRouterRuleSet[];
    onEdit: (tag: string) => void;
    onDelete: (tag: string) => void;
    bare?: boolean;
  }

  let { ruleSets, onEdit, onDelete, bare = false }: Props = $props();

  let filter = $state<RsFilter>('all');

  const filtered = $derived.by(() => {
    if (filter === 'all') return ruleSets;
    if (filter === 'dat') return ruleSets.filter((rs) => datInfo(rs) !== null);
    return ruleSets.filter((rs) => (rs.type ?? 'remote') === filter);
  });

  function typeVariant(t?: string): 'accent' | 'info' | 'warning' {
    if (t === 'local') return 'info';
    if (t === 'inline') return 'warning';
    return 'accent';
  }

  function datInfo(rs: SingboxRouterRuleSet): { kind: string; tag: string } | null {
    if (rs.type !== 'remote' || !rs.url) return null;
    try {
      const u = new URL(rs.url);
      if (u.pathname !== '/api/singbox/router/rulesets/dat-srs') return null;
      const kind = u.searchParams.get('kind') ?? '';
      const tag = u.searchParams.get('tag') ?? '';
      if ((kind !== 'geosite' && kind !== 'geoip') || !tag) return null;
      return { kind, tag };
    } catch {
      return null;
    }
  }

  function typeLabel(rs: SingboxRouterRuleSet): string {
    return datInfo(rs) ? 'dat' : (rs.type ?? 'remote');
  }

  function sourceFor(rs: SingboxRouterRuleSet): string {
    const dat = datInfo(rs);
    if (dat) return `${dat.kind}:${dat.tag}`;
    if (rs.type === 'remote') return rs.url ?? '—';
    if (rs.type === 'local') return rs.path ?? '—';
    if (rs.type === 'inline') return `${rs.rules?.length ?? 0} rules`;
    return '—';
  }

  function detourFor(rs: SingboxRouterRuleSet): string {
    if (datInfo(rs)) return 'direct';
    return rs.download_detour ?? '—';
  }
</script>

<div class="wrap">
  <div class="segment-row">
    <div class="seg" role="tablist">
      {#each [{ k: 'all', l: 'Все' }, { k: 'remote', l: 'Remote' }, { k: 'local', l: 'Local' }, { k: 'inline', l: 'Inline' }, { k: 'dat', l: 'Dat' }] as opt (opt.k)}
        <button
          type="button"
          class="chip"
          class:active={filter === opt.k}
          onclick={() => (filter = opt.k as RsFilter)}
        >
          {opt.l}
        </button>
      {/each}
    </div>
  </div>

  <div class="table" class:bare>
    <div class="header">
      <div>Тег</div>
      <div>Тип</div>
      <div>Источник</div>
      <div>Через</div>
      <div class="actions-col">Действия</div>
    </div>
    {#each filtered as rs (rs.tag)}
      <div class="row">
        <div class="tag">{rs.tag}</div>
        <div>
          <Badge variant={typeVariant(typeLabel(rs))} size="sm" mono>{typeLabel(rs)}</Badge>
        </div>
        <div class="source" title={sourceFor(rs)}>{sourceFor(rs)}</div>
        <div class="detour">{detourFor(rs)}</div>
        <div class="actions-col actions">
          <button
            type="button"
            class="route-action-btn"
            title={`Редактировать набор правил «${rs.tag}»`}
            aria-label={`Редактировать набор правил ${rs.tag}`}
            onclick={() => onEdit(rs.tag)}
          >
            <Edit3 size={15} />
          </button>
          <button
            type="button"
            class="route-action-btn danger"
            title={`Удалить набор правил «${rs.tag}»`}
            aria-label={`Удалить набор правил ${rs.tag}`}
            onclick={() => onDelete(rs.tag)}
          >
            <Trash2 size={15} />
          </button>
        </div>
      </div>
    {/each}
    {#if filtered.length === 0}
      <div class="empty">Нет наборов</div>
    {/if}
  </div>
</div>

<style>
  .wrap {
    display: flex;
    flex-direction: column;
    gap: 0;
  }
  .segment-row {
    display: flex;
    width: 100%;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
  }
  .seg {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    width: 100%;
    gap: 0;
    padding: 0;
    background: transparent;
    border: 0;
    border-radius: 0;
  }
  .chip {
    padding: 8px 12px;
    border-radius: 0;
    border: 0;
    border-right: 1px solid var(--border);
    background: transparent;
    color: var(--text-secondary);
    font-size: 12px;
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
  }
  .chip:last-child {
    border-right: 0;
  }
  .chip:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
  }
  .chip.active {
    background: var(--accent-soft);
    color: var(--text-primary);
    box-shadow: inset 0 -2px 0 var(--accent);
  }
  .table {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .header, .row {
    display: grid;
    grid-template-columns: 150px 76px minmax(0, 1fr) 84px 76px;
    padding: 7px 14px;
    align-items: center;
    gap: 8px;
  }
  .header {
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
    padding: 8px 14px;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    font-weight: 500;
  }
  .header > div:nth-child(2),
  .row > div:nth-child(2),
  .header > div:nth-child(3),
  .header > div:nth-child(4) {
    text-align: center;
  }
  .header > div:nth-child(3),
  .row > div:nth-child(3) {
    min-width: 0;
  }
  .row {
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
    font-size: 12.5px;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 600;
  }
  .source {
    min-width: 0;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-muted);
    white-space: normal;
    overflow-wrap: anywhere;
    word-break: break-word;
    line-height: 1.45;
  }
  .detour {
    min-width: 0;
    justify-self: center;
    text-align: center;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--text-secondary);
    white-space: nowrap;
  }
  .actions-col {
    text-align: right;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 4px;
  }

  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
  @media (max-width: 720px) {
    .wrap {
      gap: 0.625rem;
    }

    .segment-row {
      border-bottom: 0;
      background: transparent;
    }

    .seg {
      display: grid;
      grid-template-columns: repeat(5, minmax(0, 1fr));
      width: 100%;
      min-width: 0;
      border: 1px solid var(--border);
      border-radius: 12px;
      overflow: hidden;
      background: var(--bg-tertiary);
    }

    .chip {
      justify-content: center;
      padding-inline: 0.35rem;
      min-width: 0;
    }

    .table {
      border: 0;
      background: transparent;
      overflow: visible;
      display: grid;
      grid-template-columns: 1fr;
      gap: 0.875rem;
    }

    .header {
      display: none;
    }

    .row {
      min-width: 0;
      display: grid;
      grid-template-columns: minmax(0, 1fr) 36px;
      grid-template-areas:
        "tag type"
        "source edit"
        "detour delete";
      align-items: center;
      gap: 0.625rem;
      padding: 0.875rem 1rem;
      border-radius: 14px;
      border: 1px solid var(--border);
      background: var(--bg-secondary);
      overflow: hidden;
    }

    .row > div:nth-child(1) { grid-area: tag; }
    .row > div:nth-child(2) { grid-area: type; justify-self: end; }
    .row > div:nth-child(3) { grid-area: source; }
    .row > div:nth-child(4) { grid-area: detour; }
    .row > div:nth-child(5) { display: contents; }

    .row > div:nth-child(5) .route-action-btn:first-child {
      grid-area: edit;
      justify-self: end;
    }

    .row > div:nth-child(5) .route-action-btn.danger {
      grid-area: delete;
      justify-self: end;
    }

    .tag {
      font-size: 0.95rem;
      line-height: 1.25;
      white-space: normal;
      overflow: visible;
      text-overflow: initial;
      overflow-wrap: anywhere;
      word-break: break-word;
    }

    .source,
    .detour {
      display: block;
      width: 100%;
      min-width: 0;
      font-size: 0.78rem;
      line-height: 1.35;
      white-space: normal;
      overflow: hidden;
      text-overflow: ellipsis;
      overflow-wrap: anywhere;
      word-break: break-word;
      padding: 0.625rem 0.75rem;
      border: 1px solid color-mix(in srgb, var(--border) 72%, transparent);
      border-radius: 10px;
      background: color-mix(in srgb, var(--bg-primary) 45%, transparent);
      justify-self: stretch;
      text-align: left;
    }

    .actions {
      display: contents;
    }

    .actions-col {
      text-align: left;
    }
  }
  /* Bare mode для embed внутри SidePanel — parent даёт chrome */
  .table.bare {
    background: transparent;
    border: 0;
    border-radius: 0;
  }
</style>
