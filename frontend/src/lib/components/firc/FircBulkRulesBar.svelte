<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { FIRC_RULE_TYPES, type FircRuleType } from '$lib/types/firc';
	import { Power, PowerOff, Trash2 } from 'lucide-svelte';

	interface Props {
		selectedCount: number;
		totalCount: number;
		otherGroups: { id: string; name: string }[];
		onSelectAll: () => void;
		onDeselectAll: () => void;
		onEnable: () => void;
		onDisable: () => void;
		onChangeType: (type: FircRuleType) => void;
		onMoveToGroup: (targetGroupId: string) => void;
		onDelete: () => void;
	}

	let {
		selectedCount,
		totalCount,
		otherGroups,
		onSelectAll,
		onDeselectAll,
		onEnable,
		onDisable,
		onChangeType,
		onMoveToGroup,
		onDelete,
	}: Props = $props();

	function handleTypeChange(e: Event & { currentTarget: HTMLSelectElement }) {
		const raw = e.currentTarget.value;
		const matched = FIRC_RULE_TYPES.find((t) => t.value === raw);
		e.currentTarget.value = '';
		if (matched) onChangeType(matched.value);
	}

	function handleMoveChange(e: Event & { currentTarget: HTMLSelectElement }) {
		const targetId = e.currentTarget.value;
		e.currentTarget.value = '';
		if (targetId) onMoveToGroup(targetId);
	}
</script>

<div class="fb-bar" data-testid="firc-bulk-rules-bar">
	<div class="fb-left">
		<span class="fb-count">{m.firc_bulk_rules_selected({ count: selectedCount })}</span>
		{#if selectedCount < totalCount}
			<button type="button" class="fb-link" onclick={onSelectAll}>
				{m.firc_bulk_select_all({ count: totalCount })}
			</button>
		{/if}
		<button type="button" class="fb-link" onclick={onDeselectAll}>
			{m.firc_bulk_deselect_all()}
		</button>
	</div>

	<div class="fb-actions">
		<Button variant="secondary" size="sm" onclick={onEnable}>
			{#snippet iconBefore()}<Power size={13} />{/snippet}
			{m.firc_bulk_enable()}
		</Button>
		<Button variant="secondary" size="sm" onclick={onDisable}>
			{#snippet iconBefore()}<PowerOff size={13} />{/snippet}
			{m.firc_bulk_disable()}
		</Button>

		<select
			class="fb-select"
			aria-label={m.firc_bulk_rules_change_type()}
			value=""
			onchange={handleTypeChange}
		>
			<option value="" disabled>{m.firc_bulk_rules_change_type()}</option>
			{#each FIRC_RULE_TYPES as rt (rt.value)}
				<option value={rt.value}>{rt.label}</option>
			{/each}
		</select>

		{#if otherGroups.length > 0}
			<select
				class="fb-select"
				aria-label={m.firc_bulk_rules_move_to()}
				value=""
				onchange={handleMoveChange}
			>
				<option value="" disabled>{m.firc_bulk_rules_move_to()}</option>
				{#each otherGroups as og (og.id)}
					<option value={og.id}>{og.name}</option>
				{/each}
			</select>
		{/if}

		<Button variant="outline-danger" size="sm" onclick={onDelete}>
			{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
			{m.firc_bulk_delete()}
		</Button>
	</div>
</div>

<style>
	.fb-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 8px 12px;
		padding: 7px 10px;
		margin-bottom: 8px;
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-accent-tint) 45%, var(--color-bg-secondary));
		border: 1px solid var(--color-accent-border);
	}

	.fb-left {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
	}

	.fb-count {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.fb-link {
		border: 0;
		background: transparent;
		color: var(--color-accent);
		font-size: 11.5px;
		font-weight: 500;
		cursor: pointer;
		padding: 1px 4px;
		border-radius: 4px;
	}

	.fb-link:hover {
		text-decoration: underline;
	}

	.fb-actions {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
	}





	.fb-select {
		height: 28px;
		padding: 0 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 12px;
		font-family: inherit;
		cursor: pointer;
		max-width: 190px;
	}

	.fb-select:focus {
		outline: none;
		border-color: var(--color-accent);
	}
</style>
