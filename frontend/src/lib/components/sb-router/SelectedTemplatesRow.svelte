<!--
  Источник дизайна: singbox-router/project/screens/AddRuleFlow.jsx (SelectedTemplatesRow)
  Чипы реально-выбранных шаблонов (services + rulesets) с remove × и «Изменить →»
  для повторного открытия Templates Modal.
-->

<script lang="ts">
  import { X as XIcon } from 'lucide-svelte';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import PresetIcon from '$lib/components/routing/singboxRouter/PresetIcon.svelte';
  import RuleSetTypeBadge from './RuleSetTypeBadge.svelte';
  import { resolveRuleSetDisplayType, type RuleSetDisplayType } from '$lib/utils/ruleSetType';
  import { templatesSelection, toggleTemplate, openTemplatesModal } from './templatesStore';

  const presets = singboxRouterStore.presets;
  const ruleSets = singboxRouterStore.ruleSets;

  interface SelectedItem {
    id: string;
    kind: 'svc' | 'rs';
    label: string;
    iconSlug?: string;
    presetId?: string;
    rsType?: RuleSetDisplayType;
  }

  const items = $derived.by((): SelectedItem[] => {
    const result: SelectedItem[] = [];
    for (const id of $templatesSelection) {
      if (id.startsWith('svc:')) {
        const presetId = id.slice(4);
        const preset = $presets.find((p) => p.id === presetId);
        result.push({
          id, kind: 'svc',
          label: preset?.name ?? presetId,
          iconSlug: preset?.iconSlug,
          presetId,
        });
      } else if (id.startsWith('rs:')) {
        const tag = id.slice(3);
        const rs = $ruleSets.find((r) => r.tag === tag);
        result.push({
          id,
          kind: 'rs',
          label: tag,
          rsType: rs ? resolveRuleSetDisplayType(rs) : 'remote',
        });
      }
    }
    return result;
  });

</script>

{#if items.length > 0}
  <div class="row">
    <header class="head">
      <span class="caption">Выбрано: {items.length}</span>
      <button type="button" class="link" onclick={() => openTemplatesModal()}>Изменить →</button>
    </header>
    <div class="chips">
      {#each items as it (it.id)}
        {#if it.kind === 'svc'}
          <span class="tpl-chip chip-svc">
            <span class="tpl-icon">
              <PresetIcon slug={it.iconSlug ?? it.presetId} label={it.label} size={18} />
            </span>
            <span class="chip-label">{it.label}</span>
            <button type="button" class="chip-x" onclick={() => toggleTemplate(it.id)} aria-label="Убрать">
              <XIcon size={10} />
            </button>
          </span>
        {:else}
          <span class="tpl-chip chip-rs">
            <span class="chip-tag">{it.label}</span>
            {#if it.rsType}
              <RuleSetTypeBadge type={it.rsType} size="xs" />
            {/if}
            <button type="button" class="chip-x" onclick={() => toggleTemplate(it.id)} aria-label="Убрать">
              <XIcon size={10} />
            </button>
          </span>
        {/if}
      {/each}
    </div>
  </div>
{/if}

<style>
  .row {
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    margin-bottom: 12px;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
  }
  .caption {
    font-size: 10.5px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .link {
    background: transparent;
    border: 0;
    color: var(--accent);
    font-size: 11px;
    cursor: pointer;
    font-family: inherit;
    font-weight: 500;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  /* Не .chip: имя занято утилитой Skeleton и app.css — их свойства протекали сюда. */
  .tpl-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 6px 3px 4px;
    border-radius: 999px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
    line-height: calc(1 / 0.75);
    color: var(--color-text-muted);
    white-space: nowrap;
  }
  .chip-rs {
    padding: 3px 6px 3px 10px;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    border-color: var(--accent-line);
  }
  .tpl-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    box-sizing: content-box;
    padding: 0.375rem;
    border-radius: 0.25rem;
    width: 18px;
    height: 18px;
    flex-shrink: 0;
  }
  .chip-label {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
  }
  .chip-tag {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--accent);
    font-weight: 600;
  }
  .chip-x {
    background: transparent;
    border: 0;
    color: var(--text-muted);
    cursor: pointer;
    padding: 2px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .chip-x:hover {
    color: var(--text-primary);
  }
</style>
