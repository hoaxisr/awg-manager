<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (DeviceProxyCompact)
  Full implementation reading DeviceProxyConfig + Runtime + Instances + IPCheck.
-->

<script lang="ts">
  import { onMount } from 'svelte';
  import { Button, Badge } from '$lib/components/ui';
  import { ChevronRight, Trash2 } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import type {
    DeviceProxyRuntime,
    DeviceProxyInstance,
  } from '$lib/types';

  interface ListenChoices {
    lanIP: string;
    bridges: { id: string; label: string; ip: string }[];
    singboxRunning: boolean;
  }

  interface Props {
    onConfigure?: () => void;
    bare?: boolean;
    onSelect?: (in_: DeviceProxyInstance) => void;
    onDelete?: (in_: DeviceProxyInstance) => void;
  }

  let { onConfigure, bare = false, onSelect, onDelete }: Props = $props();

  type RuntimeById = Record<string, DeviceProxyRuntime>;

  let instances = $state<DeviceProxyInstance[]>([]);
  let runtimes = $state<RuntimeById>({});
  let listenChoices = $state<ListenChoices | null>(null);
  let loadError = $state<string | null>(null);

  onMount(async () => {
    try {
      const [ins, choices] = await Promise.all([
        api.listDeviceProxyInstances(),
        api.getDeviceProxyListenChoices().catch(() => null),
      ]);
      instances = ins;
      listenChoices = choices;

      const runtimeEntries = await Promise.all(
        ins.map(async (in_) => {
          const rt = await api.getDeviceProxyInstanceRuntime(in_.id).catch((): DeviceProxyRuntime => ({
            alive: false,
            activeTag: '',
            defaultTag: '',
          }));
          return [in_.id, rt] as const;
        }),
      );
      runtimes = Object.fromEntries(runtimeEntries) as RuntimeById;
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    }
  });

  function runtimeFor(id: string): DeviceProxyRuntime {
    return runtimes[id] ?? { alive: false, activeTag: '', defaultTag: '' };
  }

  function isInstanceActive(in_: DeviceProxyInstance): boolean {
    return in_.enabled && runtimeFor(in_.id).alive;
  }

  function toneFor(in_: DeviceProxyInstance): 'success' | 'error' | 'muted' {
    if (!in_.enabled) return 'muted';
    return runtimeFor(in_.id).alive ? 'success' : 'error';
  }

  function clientListenHost(in_: DeviceProxyInstance): string {
    if (listenChoices) {
      if (in_.listenAll) return listenChoices.lanIP || '0.0.0.0';
      const match = listenChoices.bridges.find((b) => b.id === in_.listenInterface);
      return match?.ip || in_.listenInterface || 'auto';
    }
    return in_.listenAll ? '0.0.0.0' : (in_.listenInterface || 'auto');
  }

  function listenLabelFor(in_: DeviceProxyInstance): string {
    return `${clientListenHost(in_)}:${in_.port}`;
  }

  function outboundLabelFor(in_: DeviceProxyInstance): string {
    const rt = runtimeFor(in_.id);
    return rt.activeTag || rt.defaultTag || in_.selectedOutbound || '—';
  }

  function outboundVariantFor(tag: string): 'accent' | 'muted' {
    return tag === 'direct' || tag === '—' ? 'muted' : 'accent';
  }
</script>

{#snippet icon()}<ChevronRight size={12} />{/snippet}

<div class="panel" class:bare>
  {#if instances.length > 0}
    <div class="proxy-list">
      {#each instances as in_ (in_.id)}
        {@const outboundLabel = outboundLabelFor(in_)}
        <div class="proxy-row">
          <span class="dot" data-tone={toneFor(in_)}></span>
          <button
            type="button"
            class="proxy-click"
            class:clickable={!!onSelect}
            onclick={() => onSelect?.(in_)}
            disabled={!onSelect}
          >
            <div class="proxy-info">
              <div class="proxy-main">
                <span class="ty">mixed</span>
                <span class="mono listen" title={listenLabelFor(in_)}>{listenLabelFor(in_)}</span>
                {#if isInstanceActive(in_)}
                  <Badge variant="success" size="sm" mono>active</Badge>
                {:else}
                  <Badge variant="muted" size="sm" mono>выкл</Badge>
                {/if}
              </div>
              <div class="proxy-sub">
                <span class="arrow">→</span>
                <span class="outbound-wrap">
                  <Badge variant={outboundVariantFor(outboundLabel)} size="sm" mono>{outboundLabel}</Badge>
                </span>
              </div>
            </div>
          </button>
          {#if onDelete && in_.id !== 'default'}
            <button
              type="button"
              class="route-action-btn danger"
              onclick={() => onDelete(in_)}
              aria-label={`Удалить inbound ${in_.name || in_.id}`}
              title={`Удалить inbound «${in_.name || in_.id}»`}
            >
              <Trash2 size={15} />
            </button>
          {/if}
        </div>
      {/each}
    </div>
  {:else if loadError}
    <div class="info">
      <div class="title">Не удалось загрузить</div>
      <div class="sub error">{loadError}</div>
    </div>
  {:else}
    <div class="info">
      <div class="title">Загрузка...</div>
      <div class="sub">device proxy</div>
    </div>
  {/if}
  {#if !bare && onConfigure}
    <Button variant="ghost" size="sm" onclick={onConfigure} iconBefore={icon}>
      Настроить
    </Button>
  {/if}
</div>

<style>
  .panel {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 14px;
    display: flex;
    align-items: flex-start;
    gap: 14px;
  }
  .proxy-list {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .proxy-row {
    display: grid;
    grid-template-columns: 8px minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
  }
  .proxy-click {
    min-width: 0;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    font-family: inherit;
    color: inherit;
    cursor: default;
    display: block;
    padding: 0;
  }
  .proxy-click:disabled {
    opacity: 1;
  }
  .proxy-click.clickable {
    cursor: pointer;
  }
  .proxy-click.clickable:hover {
    background: transparent;
  }
  .ty {
    font-size: 11px;
    color: var(--text-muted);
    font-family: var(--font-mono);
  }
  .arrow {
    color: var(--text-muted);
  }
  .proxy-row + .proxy-row {
    margin-top: 10px;
    padding-top: 10px;
    border-top: 1px solid var(--border);
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }
  .dot[data-tone='success'] {
    background: var(--color-success, #22c55e);
  }
  .dot[data-tone='error'] {
    background: var(--color-error, #dc2626);
  }
  .info, .proxy-info {
    flex: 1;
    min-width: 0;
  }
  .title {
    font-weight: 600;
    font-size: 13px;
    color: var(--text-primary);
  }
  .sub {
    font-size: 12px;
    color: var(--text-muted);
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .proxy-main {
    min-width: 0;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 6px;
  }
  .proxy-sub {
    min-width: 0;
    margin-top: 4px;
    display: grid;
    grid-template-columns: 14px minmax(0, 1fr);
    align-items: center;
    gap: 6px;
  }
  .listen {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .outbound-wrap {
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
  }
  .outbound-wrap :global(.badge) {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub.error {
    color: var(--color-error, #dc2626);
  }
  .mono {
    font-family: var(--font-mono);
    color: var(--text-secondary);
  }

  @media (max-width: 768px) {
    .panel {
      flex-direction: column;
      align-items: stretch;
    }
    .sub {
      gap: 4px;
    }

    .proxy-click,
    .proxy-info,
    .proxy-main,
    .proxy-sub {
      min-width: 0;
      max-width: 100%;
    }
  }
  /* Bare mode для embed внутри SidePanel — без double chrome */
  .panel.bare {
    background: transparent;
    border: 0;
    border-radius: 0;
    padding: 12px 14px;
  }
</style>
