<!--
  Источник дизайна: singbox-router/project/screens/AddRuleFlow.jsx (Step 1 «Описать вручную»)
  Переиспользует общий InlineRuleListEditor (smart-list + парс + geo-пикеры).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { Code } from 'lucide-svelte';
  import InlineRuleListEditor from '$lib/components/routing/singboxRouter/InlineRuleListEditor.svelte';
  import { wizardCustom, updateCustomField } from './addWizardStore';
  import { singboxRouter } from '$lib/stores/singboxRouter';

  interface Props {
    /** В режиме редактирования inline — список сразу развёрнут. */
    expanded?: boolean;
    isMihomo?: boolean;
  }
  let { expanded = false, isMihomo }: Props = $props();

  const routerSettings = singboxRouter.settings;
  const effectiveIsMihomo = $derived(isMihomo ?? ($routerSettings?.routingEngine === 'mihomo'));

  // svelte-ignore state_referenced_locally
  let value = $state($wizardCustom.rulesList);
  $effect(() => { updateCustomField('rulesList', value); });
</script>

<details class="form" open={expanded || undefined}>
  <summary class="summary">
    <Code size={14} color="var(--text-muted)" />
    <span>{m.sb_router_custom_form_title()}</span>
    <span class="meta">{effectiveIsMihomo ? m.sb_router_custom_form_meta_mihomo() : m.sb_router_custom_form_meta()}</span>
  </summary>
  <div class="body">
    <InlineRuleListEditor bind:value isMihomo={effectiveIsMihomo} />
  </div>
</details>

<style>
  .form {
    padding: 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
  }
  .summary {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: 12.5px;
    font-weight: 500;
    list-style: none;
  }
  .summary::-webkit-details-marker { display: none; }
  .meta { color: var(--text-muted); font-weight: 400; }
  .body { margin-top: 12px; }
</style>
