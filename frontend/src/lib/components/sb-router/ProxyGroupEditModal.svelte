<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { X } from 'lucide-svelte';
	import SingboxSettingsModal from '../routing/singboxRouter/SingboxSettingsModal.svelte';
	import type { ProxyGroup } from '$lib/types/sbRouter';
	import { outboundGroupLabel, type OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

  interface Props {
    group?: ProxyGroup;
    outboundOptions: OutboundGroup[];
    onClose: () => void;
    onSave: (g: ProxyGroup) => void;
  }
  let { group, outboundOptions, onClose, onSave }: Props = $props();

  let name = $state('');
  let type = $state<ProxyGroup['type']>('fallback');
  let proxies = $state<string[]>([]);
  let url = $state('http://www.gstatic.com/generate_204');
  let interval = $state(300);

  $effect(() => {
    name = group?.name ?? '';
    type = group?.type ?? 'fallback';
    proxies = [...(group?.proxies ?? [])];
    url = group?.url ?? 'http://www.gstatic.com/generate_204';
    interval = group?.interval ?? 300;
  });

  let memberPicker = $state('');

  const typeOptions = $derived<DropdownOption[]>([
    { value: 'fallback', label: m.sb_router_pg_modal_type_fallback() },
    { value: 'load-balance', label: m.sb_router_pg_modal_type_load_balance() },
    { value: 'url-test', label: m.sb_router_pg_modal_type_url_test() },
    { value: 'select', label: m.sb_router_pg_modal_type_select() },
  ]);

  const outboundDropdownOptions = $derived<DropdownOption[]>([
    { value: '', label: m.sb_router_pg_modal_select_placeholder() },
    ...outboundOptions.flatMap((g) =>
      g.items.map((i) => ({ value: i.value, label: i.label, group: outboundGroupLabel(g.id) })),
    ),
  ]);

  function addMember(val: string) {
    if (!val || proxies.includes(val)) return;
    proxies = [...proxies, val];
    memberPicker = '';
  }

  function removeMember(idx: number) {
    proxies = proxies.filter((_, i) => i !== idx);
  }

  function handleSave() {
    if (!name.trim()) return;
    onSave({
      name: name.trim(),
      type,
      proxies,
      url: type !== 'select' ? url : undefined,
      interval: type !== 'select' ? interval : undefined,
    });
  }

  const isDirty = $derived(
    name !== (group?.name ?? '') ||
    type !== (group?.type ?? 'fallback') ||
    url !== (group?.url ?? 'http://www.gstatic.com/generate_204') ||
    interval !== (group?.interval ?? 300) ||
    JSON.stringify(proxies) !== JSON.stringify(group?.proxies ?? [])
  );
</script>

<SingboxSettingsModal
	title={group ? m.sb_router_pg_modal_title_edit() : m.sb_router_pg_modal_title_new()}
	{onClose}
	hasUnsavedChanges={() => isDirty}
>
	<div class="field">
		<label class="label" for="pg-name">{m.sb_router_pg_modal_name_lbl()}</label>
		<div class="control">
			<input
				id="pg-name"
				class="form-input"
				type="text"
				placeholder={m.sb_router_pg_modal_name_placeholder()}
				bind:value={name}
			/>
		</div>
	</div>

	<div class="field">
		<label class="label" for="pg-type">{m.sb_router_pg_modal_type_lbl()}</label>
		<div class="control">
			<Dropdown
				options={typeOptions}
				bind:value={type}
			/>
		</div>
	</div>

	{#if type !== 'select'}
		<div class="field-row" style="display: flex; gap: 1rem; margin-top: 1rem;">
			<div class="field" style="flex: 2;">
				<label class="label" for="pg-url">{m.sb_router_pg_modal_url_lbl()}</label>
				<div class="control">
					<input
						id="pg-url"
						class="form-input"
						type="text"
						bind:value={url}
					/>
				</div>
			</div>
			<div class="field" style="flex: 1;">
				<label class="label" for="pg-interval">{m.sb_router_pg_modal_interval_lbl()}</label>
				<div class="control">
					<input
						id="pg-interval"
						class="form-input"
						type="number"
						bind:value={interval}
					/>
				</div>
			</div>
		</div>
	{/if}

	<div class="field" style="margin-top: 1rem;">
		<label class="label" for="pg-members">{m.sb_router_pg_modal_members_lbl({ count: proxies.length })}</label>

		{#if proxies.length > 0}
			<div class="members-list" style="display: flex; flex-wrap: wrap; gap: 0.5rem; margin-bottom: 0.5rem; background: var(--sbr-control-bg); padding: 0.75rem; border-radius: var(--sbr-control-radius); border: 1px dashed var(--sbr-control-border);">
				{#each proxies as p, i}
					<div class="member-item" style="display: flex; align-items: center; gap: 0.25rem; background: var(--surface-0); padding: 0.25rem 0.375rem 0.25rem 0.5rem; border-radius: var(--radius-sm); border: 1px solid var(--surface-200); font-size: 0.75rem;">
						<span>{p}</span>
						<button type="button" class="member-remove" onclick={() => removeMember(i)} style="background: none; border: none; color: var(--surface-400); cursor: pointer;">
							<X size={14} />
						</button>
					</div>
				{/each}
			</div>
		{/if}

		<div class="control">
			<Dropdown
				options={outboundDropdownOptions}
				bind:value={memberPicker}
				placeholder={m.sb_router_pg_modal_add_outbound_placeholder()}
				onchange={(v) => addMember(v)}
			/>
		</div>
	</div>

	{#snippet actions()}
		<Button variant="ghost" onclick={onClose}>{m.common_cancel()}</Button>
		<Button
			variant="primary"
			disabled={!name.trim() || !isDirty}
			onclick={handleSave}
		>
			{m.common_save()}
		</Button>
	{/snippet}
</SingboxSettingsModal>

<style>
  .label {
    display: block;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--surface-700);
    margin-bottom: 0.5rem;
  }
  .form-input {
    width: 100%;
    padding: 0.5rem 0.75rem;
    background: var(--surface-100);
    border: 1px solid var(--surface-300);
    border-radius: var(--radius-md);
    color: var(--surface-900);
    font-size: 0.875rem;
  }
  .member-remove:hover {
    color: var(--error-500);
    background: var(--error-50);
  }
</style>
