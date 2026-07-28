<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (DnsServersCompact)
-->

<script lang="ts">
  import type {
    SingboxProxyGroup,
    SingboxRouterDNSServer,
    SingboxRouterDNSRule,
    SingboxRouterOutbound,
    SingboxTunnel,
    Subscription,
  } from '$lib/types';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import { onDestroy } from 'svelte';
  import { Badge, Button } from '$lib/components/ui';
  import { Trash2, Edit3, GripVertical } from 'lucide-svelte';
  import { createReorderDrag } from './reorderDrag.svelte';
  import { dnsServerDetourDisplay, dnsServerSubtitle } from './dnsServerDetourDisplay';
  import OutboundTile from './OutboundTile.svelte';
  import { dnsRuleTarget, type DnsRuleTarget } from './dnsRuleLabel';
  import { dnsMatcherParts, dnsMatcherSummary } from './dnsMatcherParts';
  import { dnsServerDeleteBlockReasons, type DnsServerUsageInput } from './dnsServerUsage';
  import { computeShadowedDnsRuleIndices, isCatchAllDnsRule } from './dnsRuleShadow';
  import { isManagedDnsChainRule } from './dnsChainManaged';

  interface Props {
    servers: SingboxRouterDNSServer[];
    rules: SingboxRouterDNSRule[];
    outbounds?: SingboxRouterOutbound[];
    onEditServer: (tag: string) => void;
    onDeleteServer?: (tag: string) => void;
    onEditRule: (idx: number) => void;
    onDeleteRule?: (idx: number) => void;
    /** Задан — включает перетаскивание правил (оптимистика/откат — на стороне хоста). */
    onMoveRule?: (from: number, to: number) => Promise<void>;
    onAddRule?: () => void;
    addRuleDisabled?: boolean;
    addRuleTitle?: string;
    outboundOptions?: OutboundGroup[];
    subscriptions?: Subscription[] | null;
    proxyGroups?: SingboxProxyGroup[];
    singboxTunnels?: SingboxTunnel[];
    dnsUsage?: Omit<DnsServerUsageInput, 'tag'>;
  }

  let {
    servers,
    rules,
    outbounds = [],
    onEditServer,
    onDeleteServer,
    onEditRule,
    onDeleteRule,
    onMoveRule,
    onAddRule,
    addRuleDisabled = false,
    addRuleTitle,
    outboundOptions = [],
    subscriptions = null,
    proxyGroups = [],
    singboxTunnels = [],
    dnsUsage,
  }: Props = $props();

  const subFor = dnsServerSubtitle;

  // Один проход по DNS-конфигу на список вместо O(servers × конфиг) на строку.
  const serverDeleteReasons = $derived(
    dnsUsage ? dnsServerDeleteBlockReasons(servers, dnsUsage) : null,
  );

  // DNS-правила проверяются сверху вниз (first-match). Правило без условий
  // (catch-all) перехватывает всё, поэтому всё, что ниже него, — мёртвый код.
  // Пересчитывается на каждый drag-reorder (rules — реактивный prop).
  const shadowedRuleIdx = $derived(computeShadowedDnsRuleIndices(rules));

  // Тон бейджа цели: evaluate/respond (sing-box 1.14) отличимы от route/block.
  function targetVariant(kind: DnsRuleTarget['kind']): 'accent' | 'error' | 'info' | 'purple' {
    if (kind === 'block') return 'error';
    if (kind === 'evaluate') return 'info';
    if (kind === 'respond') return 'purple';
    return 'accent';
  }

  // ── Drag-reorder правил (общий движок reorderDrag, как в fakeip/DnsTab) ──
  let ruleRowEls = $state<Array<HTMLElement | null>>([]);
  let rulesEl = $state<HTMLElement | null>(null);

  // Правила пресета бэкенд отклоняет на move (DNS_RULE_MANAGED): ни схватить,
  // ни уронить на их место.
  function isFixedRule(i: number): boolean {
    const r = rules[i];
    return !!r && isManagedDnsChainRule(r);
  }

  const ruleDrag = createReorderDrag({
    getRowElement: (i) => ruleRowEls[i] ?? null,
    count: () => rules.length,
    getPanelEl: () => rulesEl,
    isFixed: isFixedRule,
    onCommit: async (from, to) => {
      await onMoveRule?.(from, to);
    },
  });

  // Клавиатурная альтернатива перетаскиванию: стрелки на грипе двигают правило
  // на соседнюю позицию с теми же гейтами, что и drop.
  function handleGripKeydown(i: number, e: KeyboardEvent): void {
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    if (ruleDrag.busy) return;
    const to = e.key === 'ArrowUp' ? i - 1 : i + 1;
    if (to < 0 || to >= rules.length || isFixedRule(to)) return;
    e.preventDefault();
    void onMoveRule?.(i, to);
  }

  onDestroy(() => ruleDrag.destroy());
</script>

<!-- Строка DNS-правила: рисуется и в списке, и в плавающем ghost'е. -->
{#snippet ruleRow(r: SingboxRouterDNSRule, i: number, ghost: boolean)}
  {@const tgt = dnsRuleTarget(r)}
  {@const matchers = dnsMatcherParts(r)}
  {@const shadowed = !ghost && shadowedRuleIdx.has(i)}
  {@const managed = isManagedDnsChainRule(r)}
  <div
    class="rule-row"
    class:shadowed
    class:has-grip={!!onMoveRule}
    class:dragging={!ghost && ruleDrag.draggingIndex === i}
    title={managed ? 'Правило DNS-пресета — изменяется через карточку пресета' : undefined}
  >
    {#if onMoveRule}
      {#if managed}
        <span class="grip grip-fixed" aria-hidden="true"></span>
      {:else}
        <button
          type="button"
          class="grip"
          class:is-busy={ruleDrag.busy}
          aria-label={`Перетащить DNS-правило #${i + 1}`}
          title="Перетащить для изменения порядка (или стрелки вверх/вниз)"
          onpointerdown={ruleDrag.busy ? undefined : (e) => ruleDrag.handlePointerDown(i, e)}
          onkeydown={(e) => handleGripKeydown(i, e)}
        >
          <GripVertical size={16} strokeWidth={2} />
        </button>
      {/if}
    {/if}
    <button
      type="button"
      class="rule-content"
      disabled={managed}
      onclick={() => onEditRule(i)}
      title={managed
        ? undefined
        : shadowed
        ? `${dnsMatcherSummary(r)} → ${tgt.label} · перекрыто catch-all правилом выше`
        : `${dnsMatcherSummary(r)} → ${tgt.label}`}
    >
      <span class="rule-match">
        {#if matchers.length === 0}
          <Badge variant="accent" size="xs">
            {r.action === 'evaluate'
              ? 'catch-all · оценивает все запросы'
              : 'catch-all · всё остальное'}
          </Badge>
        {:else}
          {#each matchers as part, pi (part.key + pi)}
            <span class="m-part">
              <span class="m-head">
                {#if pi > 0}<span class="m-sep">· </span>{/if}
                {#if part.key === 'query_type'}
                  <span class="m-key">query_type</span><span class="m-eq">=</span>
                {:else}
                  <span class="m-key">{part.key}</span><span class="m-colon">: </span>
                {/if}
              </span>
              <span class="m-val">{part.value}</span>
            </span>
          {/each}
        {/if}
        {#if shadowed}
          <Badge variant="warning" size="xs">перекрыто catch-all выше</Badge>
        {/if}
        {#if managed}
          <Badge variant="muted" size="xs">пресет</Badge>
        {/if}
      </span>
      <span class="rule-arrow" aria-hidden="true">→</span>
      {#if tgt.kind === 'none'}
        <span class="rule-target none">{tgt.label}</span>
      {:else}
        <span class="rule-target" title={tgt.label}>
          <Badge variant={targetVariant(tgt.kind)} size="sm" mono>{tgt.label}</Badge>
          {#if r.race || r.speculative}
            <span class="rule-flags">
              {#if r.race}<Badge variant="muted" size="xs">race</Badge>{/if}
              {#if r.speculative}<Badge variant="muted" size="xs">spec</Badge>{/if}
            </span>
          {/if}
        </span>
      {/if}
    </button>

    <!-- Правило пресета: бэкенд отклоняет edit/delete/move
         (DNS_RULE_MANAGED) — действий не показываем. -->
    <div class="rule-actions">
      {#if !managed}
        <button
          type="button"
          class="route-action-btn"
          onclick={() => onEditRule(i)}
          aria-label={`Редактировать DNS-правило #${i + 1}`}
          title={`Редактировать DNS-правило #${i + 1}`}
        >
          <Edit3 size={15} />
        </button>

        {#if onDeleteRule}
          <button
            type="button"
            class="route-action-btn danger"
            onclick={() => onDeleteRule(i)}
            aria-label={`Удалить DNS-правило #${i + 1}`}
            title={`Удалить DNS-правило #${i + 1}`}
          >
            <Trash2 size={15} />
          </button>
        {/if}
      {/if}
    </div>
  </div>
{/snippet}

<div class="wrap">
  <div class="servers">
    {#each servers as s (s.tag)}
      {@const deleteReason = serverDeleteReasons?.get(s.tag) ?? null}
      <div class="server-row">
        <span class="dot"></span>
        <button type="button" class="meta-btn" onclick={() => onEditServer(s.tag)}>
          <div class="meta">
            <div class="tag">{s.tag}</div>
            <div class="sub">{subFor(s)}</div>
          </div>
        </button>
        <span class="detour-chip">
          <OutboundTile
            outbound={dnsServerDetourDisplay(
              s,
              outbounds,
              outboundOptions,
              subscriptions,
              proxyGroups,
              singboxTunnels,
            )}
            size="compact"
          />
        </span>
        <div class="server-actions">
          <button
            type="button"
            class="route-action-btn"
            onclick={() => onEditServer(s.tag)}
            aria-label={`Редактировать DNS-сервер ${s.tag}`}
            title={`Редактировать DNS-сервер «${s.tag}»`}
          >
            <Edit3 size={15} />
          </button>
          {#if onDeleteServer}
            <button
              type="button"
              class="route-action-btn danger"
              disabled={deleteReason !== null}
              onclick={() => onDeleteServer(s.tag)}
              aria-label={`Удалить DNS-сервер ${s.tag}`}
              title={deleteReason ?? `Удалить DNS-сервер «${s.tag}»`}
            >
              <Trash2 size={15} />
            </button>
          {/if}
        </div>
      </div>
    {/each}
    {#if servers.length === 0}
      <div class="empty">Нет DNS-серверов.</div>
    {/if}
  </div>

  <div class="rules-cap">
    <span class="rules-cap-label">DNS-правила · {rules.length}</span>
    {#if onAddRule}
      <Button variant="primary" size="sm" onclick={onAddRule} disabled={addRuleDisabled}>+ Правило</Button>
    {/if}
  </div>
  {#if shadowedRuleIdx.size > 0}
    <div class="shadow-note">
      Правила после catch-all (без условий) не проверяются — перенесите их выше или удалите.
    </div>
  {/if}
  {#if rules.length > 0}
    <div class="rules-table">
      <div
        class="rules-rows"
        class:is-dragging={ruleDrag.active}
        style={ruleDrag.cardsMotionStyle()}
        bind:this={rulesEl}
      >
        {#each rules as r, i (i)}
          <div
            class="row-shell"
            class:drag-source-exiting={ruleDrag.isDragSource(i)}
            class:drag-source-collapsed={ruleDrag.sourceCollapsed(i)}
            style={ruleDrag.isDragSource(i) ? ruleDrag.dropIndicatorStyle() : undefined}
            bind:this={ruleRowEls[i]}
          >
            {#if ruleDrag.showsDropBefore(i)}
              <div
                class="drop-indicator"
                class:expanded={ruleDrag.dropBeforeExpanded(i)}
                class:collapsing={ruleDrag.dropBeforeCollapsing(i)}
                style={ruleDrag.dropIndicatorStyle()}
              ></div>
            {/if}
            {@render ruleRow(r, i, false)}
          </div>
        {/each}
        {#if ruleDrag.showsDropAtEnd()}
          <div
            class="drop-indicator drop-indicator-end"
            class:expanded={ruleDrag.dropEndExpanded()}
            class:collapsing={ruleDrag.dropEndCollapsing()}
            style={ruleDrag.dropIndicatorStyle()}
          ></div>
        {/if}
      </div>
    </div>
  {:else}
    <div class="empty">Нет правил</div>
  {/if}
</div>

<!-- Плавающая ghost-карточка: тот же сниппет строки → пиксель-в-пиксель. -->
{#if ruleDrag.ghostVisible && ruleDrag.ghostFromIndex !== null && rules[ruleDrag.ghostFromIndex]}
  <div
    class="drag-ghost"
    style={`top:${ruleDrag.ghostTop}px;left:${ruleDrag.ghostLeft}px;width:${ruleDrag.ghostWidth}px;`}
  >
    {@render ruleRow(rules[ruleDrag.ghostFromIndex], ruleDrag.ghostFromIndex, true)}
  </div>
{/if}

<style>
  .wrap {
    display: flex;
    flex-direction: column;
  }
  .servers {
    display: flex;
    flex-direction: column;
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
  .server-row {
    transition: background-color 0.15s ease;
    display: grid;
    grid-template-columns: 6px minmax(0, 1fr) auto auto;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  }
  .meta-btn {
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .server-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  @media (hover: hover) and (pointer: fine) {
    .server-row:hover,
    .rule-row:hover {
      background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
    }
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }
  .meta {
    flex: 1;
    min-width: 0;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub {
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .detour-chip {
    min-width: 0;
    max-width: 120px;
    overflow: hidden;
  }
  .detour-chip :global(.tone-chip) {
    max-width: 100%;
    min-width: 0;
    overflow: hidden;
  }
  .rules-cap {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 14px;
    background: var(--bg-tertiary);
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .rules-table {
    display: grid;
    min-width: 0;
  }
  .rules-rows {
    display: grid;
    gap: 0.25rem;
    min-width: 0;
  }

  /* ── Drag-reorder: разметка общего движка reorderDrag (ghost + скелетон-слот
     + схлопывание источника), как в fakeip/DnsTab. Инлайновый --card-gap движка
     (6px из route.rules) перебиваем фактическим зазором списка. ── */
  .rules-rows,
  .rules-rows .row-shell,
  .rules-rows .drop-indicator {
    --card-gap: 4px !important;
  }
  .rules-rows.is-dragging {
    user-select: none;
  }
  .row-shell {
    position: relative;
    min-width: 0;
  }
  .row-shell.drag-source-exiting {
    overflow: hidden;
    height: var(--drop-height);
    opacity: 1;
    transition:
      height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      opacity var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
  }
  .row-shell.drag-source-exiting.drag-source-collapsed {
    height: 0;
    max-height: 0;
    opacity: 0;
    margin-bottom: calc(-1 * var(--card-gap, 4px));
  }
  .drop-indicator {
    box-sizing: border-box;
    overflow: hidden;
    border: 1px solid transparent;
    border-radius: 999px;
    background: var(--color-accent, var(--accent));
    box-shadow: 0 0 10px color-mix(in srgb, var(--color-accent, var(--accent)) 45%, transparent);
    opacity: 1;
    pointer-events: none;
    transition:
      height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      background calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      border-color calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      opacity calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
  }
  .drop-indicator:not(.expanded):not(.collapsing) {
    position: absolute;
    top: -3px;
    left: 0;
    right: 0;
    height: 2px;
    margin: 0;
    z-index: 2;
  }
  .drop-indicator.expanded:not(.collapsing) {
    position: static;
    top: auto;
    height: var(--drop-height);
    margin: 0 0 var(--card-gap, 4px);
    border-radius: var(--radius-sm, 6px);
    background: color-mix(in srgb, var(--color-accent, var(--accent)) 6%, transparent);
    border-color: color-mix(in srgb, var(--color-accent, var(--accent)) 55%, transparent);
    border-style: dashed;
    box-shadow: none;
  }
  .drop-indicator.collapsing {
    margin: 0 !important;
    opacity: 0;
    border-color: transparent;
    background: transparent;
    box-shadow: none;
  }
  .drop-indicator.collapsing.expanded {
    position: static;
    height: 0 !important;
  }
  .drop-indicator.collapsing:not(.expanded) {
    position: absolute;
    top: -3px;
    left: 0;
    right: 0;
    height: 2px !important;
    z-index: 2;
  }
  .drop-indicator-end:not(.expanded):not(.collapsing),
  .drop-indicator-end.collapsing:not(.expanded) {
    position: relative;
    top: auto;
    left: auto;
    right: auto;
    height: 2px !important;
    margin: -1px 0 0 !important;
  }
  .drag-ghost {
    position: fixed;
    z-index: 10000;
    pointer-events: none;
    opacity: 0.96;
    filter: drop-shadow(0 14px 24px rgba(0, 0, 0, 0.35));
    background: var(--bg-secondary);
    border: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 55%, var(--border));
    border-radius: var(--radius-sm, 6px);
  }
  .rule-row.dragging {
    opacity: 0.7;
    outline: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 55%, var(--border));
    outline-offset: -1px;
  }
  .grip {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    padding: 0;
    color: var(--text-muted);
    opacity: 0.55;
    cursor: grab;
    touch-action: none;
    border-radius: 4px;
  }
  button.grip:hover {
    color: var(--text-primary);
    opacity: 1;
  }
  button.grip:active {
    cursor: grabbing;
  }
  .grip.is-busy {
    cursor: wait;
    opacity: 0.3;
    pointer-events: none;
  }
  /* managed-строка: место грипа держим, но тащить нечего */
  .grip-fixed {
    cursor: default;
  }

  :global(body.reorder-dragging) {
    user-select: none;
    cursor: grabbing;
  }
  .rule-row {
    transition: background-color 0.15s ease;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    column-gap: 0.5rem;
    min-width: 0;
    background: var(--surface-bg);
    padding: 0.55rem 0.75rem;
    border-radius: 4px;
  }
  .rule-row.has-grip {
    grid-template-columns: 18px minmax(0, 1fr) auto;
  }
  /* Правило, перекрытое catch-all выше: мёртвый код — приглушаем. */
  .rule-row.shadowed {
    opacity: 0.55;
  }
  .shadow-note {
    padding: 6px 14px;
    font-size: 11px;
    line-height: 1.35;
    color: var(--color-warning, #d97706);
    background: color-mix(in srgb, var(--color-warning, #d97706) 10%, transparent);
    border-left: 2px solid var(--color-warning, #d97706);
  }
  .rule-content {
    min-width: 0;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto minmax(4.5rem, max-content);
    align-items: center;
    column-gap: 0.35rem;
    background: transparent;
    border: 0;
    padding: 0;
    color: inherit;
    font-family: var(--font-mono);
    text-align: left;
    cursor: pointer;
  }
  .rule-content:disabled {
    cursor: default;
  }
  .rule-match {
    grid-column: 1;
    min-width: 0;
    color: var(--text);
    font-size: 12px;
    line-height: 1.35;
  }
  .m-part {
    display: inline;
  }
  .m-head {
    white-space: nowrap;
  }
  .m-key {
    color: var(--text-muted);
    font-weight: 600;
  }
  .m-colon,
  .m-eq,
  .m-sep {
    color: var(--text-muted);
  }
  .m-val {
    color: var(--text-secondary);
    overflow-wrap: anywhere;
  }
  .rule-arrow {
    grid-column: 2;
    flex-shrink: 0;
    color: var(--muted-text);
    line-height: 1;
    opacity: 0.85;
  }
  .rule-target {
    grid-column: 3;
    justify-self: end;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 4px;
    max-width: 10rem;
    min-width: 0;
    overflow: hidden;
  }
  /* Ужимается только длинный бейдж цели — прямой ребёнок; race/spec целые. */
  .rule-target > :global(.badge) {
    display: block;
    max-width: 100%;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .rule-flags {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }
  .rule-target.none {
    color: var(--text-muted);
    font-size: 12px;
  }
  .rule-actions {
    display: inline-flex;
    align-items: center;
    justify-content: flex-end;
    gap: 4px;
    flex-shrink: 0;
    white-space: nowrap;
  }
  @media (max-width: 768px) {
    .rules-rows,
    .rules-rows .row-shell,
    .rules-rows .drop-indicator {
      /* строки без зазора → скелетону нечего компенсировать */
      --card-gap: 0px !important;
    }

    .rules-rows {
      gap: 0;
    }

    .rule-row.has-grip {
      grid-template-columns: 18px minmax(0, 1fr) auto;
    }

    .rule-row {
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: start;
      gap: 0.5rem;
      padding: 0.65rem 0.75rem;
      border: 0;
      border-radius: 0;
      border-bottom: 1px solid var(--border);
      background: transparent;
    }

    .rule-row:last-child {
      border-bottom: 0;
    }

    .rule-content {
      grid-template-columns: minmax(0, 1fr);
      grid-template-areas:
        'match'
        'target';
      row-gap: 0.35rem;
    }

    .rule-match {
      grid-area: match;
    }

    .rule-arrow {
      display: none;
    }

    .rule-target {
      grid-area: target;
      grid-column: auto;
      justify-self: start;
      max-width: 100%;
    }

    .rule-actions {
      align-self: start;
    }
  }
</style>
