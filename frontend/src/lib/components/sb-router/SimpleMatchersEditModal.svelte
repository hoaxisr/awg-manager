<script lang="ts">
  import Modal from '$lib/components/ui/Modal.svelte';
  import { Button } from '$lib/components/ui';
  import InlineRuleListEditor from '$lib/components/routing/singboxRouter/InlineRuleListEditor.svelte';
  import { isInlineRuleListEmpty, stringifyInlineRuleListForWizard } from '$lib/utils/singboxInlineRules';
  import { notifications } from '$lib/stores/notifications';
  import { ValidationError } from './addWizardActions';
  import { submitMatchersOnlyEdit } from './matchersEditActions';
  import type { RuleCardData } from './types';
  import type { SingboxRouterRuleSet } from '$lib/types';

  interface Props {
    inlineRuleSetTag: string;
    ruleSet: SingboxRouterRuleSet;
    card: RuleCardData;
    sharedRuleSetCount?: number;
    onClose: () => void;
    onSaved: () => void | Promise<void>;
  }

  let {
    inlineRuleSetTag,
    ruleSet,
    card,
    sharedRuleSetCount = 1,
    onClose,
    onSaved,
  }: Props = $props();

  // Снапшот при открытии — обновления стора не должны затирать правки.
  // svelte-ignore state_referenced_locally
  const initialRulesList = ruleSet.type === 'inline'
    ? stringifyInlineRuleListForWizard(ruleSet.rules)
    : '';

  let rulesList = $state(initialRulesList);
  let busy = $state(false);
  let initialSnapshot = $state(initialRulesList);

  const outboundLabel = $derived.by(() => {
    if (card.action === 'block' || card.outbound.kind === 'block') return 'Заблокировать';
    if (card.outbound.kind === 'direct') return 'Напрямую';
    return card.outbound.label;
  });

  const canSave = $derived(!isInlineRuleListEmpty(rulesList));

  function hasUnsavedChanges(): boolean {
    return rulesList !== initialSnapshot;
  }

  async function handleSave() {
    if (!canSave || busy) return;
    busy = true;
    try {
      await submitMatchersOnlyEdit({ rulesList, inlineRuleSetTag });
      notifications.success('Список обновлён');
      await onSaved();
      onClose();
    } catch (e) {
      if (e instanceof ValidationError) {
        notifications.error(e.message);
      } else {
        notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
      }
    } finally {
      busy = false;
    }
  }
</script>

<Modal
  open
  title="Список доменов и адресов"
  size="lg"
  onclose={onClose}
  closeOnBackdrop={false}
  hasUnsavedChanges={hasUnsavedChanges}
>
  <p class="hint">
    Направление трафика: <strong>{outboundLabel}</strong>
    <span class="hint-muted"> · чтобы изменить, нажмите карандаш на карточке</span>
  </p>

  {#if sharedRuleSetCount > 1}
    <div class="warn">
      Этот список используется в {sharedRuleSetCount} правилах — изменения затронут все.
    </div>
  {/if}

  <InlineRuleListEditor bind:value={rulesList} />

  {#snippet actions()}
    <Button variant="ghost" size="md" onclick={onClose} disabled={busy}>Отмена</Button>
    <Button variant="primary" size="md" onclick={handleSave} disabled={!canSave || busy}>
      Сохранить
    </Button>
  {/snippet}
</Modal>

<style>
  .hint {
    margin: 0 0 12px;
    font-size: 13px;
    color: var(--text-secondary);
  }
  .hint-muted {
    color: var(--text-muted);
    font-weight: 400;
  }
  .warn {
    margin-bottom: 12px;
    padding: 8px 12px;
    font-size: 12px;
    color: var(--text-secondary);
    background: rgba(234, 179, 8, 0.1);
    border: 1px solid rgba(234, 179, 8, 0.25);
    border-radius: var(--radius-sm);
  }
</style>
