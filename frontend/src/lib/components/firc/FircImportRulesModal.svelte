<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Modal } from '$lib/components/ui';
	import { AlertTriangle, ChevronDown, ChevronUp } from 'lucide-svelte';
	import { FIRC_RULE_TYPES, type FircRuleType } from '$lib/types/firc';
	import { isSubnetRuleType } from '$lib/utils/fircResolve';
	import type {
		FircImportAnalysis,
		FircImportEntry,
		FircImportSelectionResult,
	} from '$lib/utils/fircOverlap';

	interface Props {
		open: boolean;
		targetGroupName: string;
		dnsOnly: boolean;
		text: string;
		forcedType: 'auto' | FircRuleType;
		skipBatchAndSameGroup: boolean;
		skipOtherGroupShadowed: boolean;
		analysis: FircImportAnalysis;
		selection: FircImportSelectionResult;
		onclose: () => void;
		onsubmit: () => void;
	}

	let {
		open,
		targetGroupName,
		dnsOnly,
		text = $bindable(),
		forcedType = $bindable(),
		skipBatchAndSameGroup = $bindable(),
		skipOtherGroupShadowed = $bindable(),
		analysis,
		selection,
		onclose,
		onsubmit,
	}: Props = $props();

	let showConflicts = $state(false);

	$effect(() => {
		if (!open) {
			showConflicts = false;
		}
	});

	let dupInBatchOrGroup = $derived(analysis.batchDuplicateCount + analysis.sameGroupDuplicateCount);
	let upperShadowedEntriesCount = $derived(
		analysis.entries.filter((e) => e.shadowedByUpper).length,
	);
	let otherNoticeCount = $derived(analysis.otherGroupSplitCount + analysis.otherGroupLaterCount);
	let conflictEntries = $derived(analysis.entries.filter((e) => e.status !== 'new'));

	function formatEntryStatus(entry: FircImportEntry): string {
		const upperSuffix =
			entry.shadowedByUpper && entry.conflictGroupName
				? ` · ${m.firc_import_status_shadowed_by({ group: entry.conflictGroupName })}`
				: '';
		switch (entry.status) {
			case 'batch_duplicate':
				return `${m.firc_import_status_batch_dup()}${upperSuffix}`;
			case 'same_group_duplicate':
				return `${m.firc_import_status_same_group()}${upperSuffix}`;
			case 'other_group_shadowed':
				return m.firc_import_status_shadowed_by({ group: entry.conflictGroupName ?? '' });
			case 'other_group_split':
				return m.firc_import_status_split({ group: entry.conflictGroupName ?? '' });
			case 'other_group_later':
				return m.firc_import_status_later({ group: entry.conflictGroupName ?? '' });
			case 'unsupported_dns_only_subnet':
				return m.firc_import_status_unsupported_subnet();
			default:
				return '';
		}
	}
</script>

<!-- Modal 3: Bulk Import Rules -->
<Modal
	{open}
	{onclose}
	title={m.firc_groups_import_rules_title()}
	size="lg"
>
	<div class="modal-form">
		{#if targetGroupName}
			<p class="import-target-name">{m.firc_import_target_group({ group: targetGroupName })}</p>
		{/if}
		<p class="hint">
			{m.firc_groups_import_rules_hint()}
		</p>
		{#if dnsOnly}
			<p class="hint">{m.firc_dns_only_no_subnets()}</p>
		{/if}

		<div class="import-type-row">
			<label class="import-type-label" for="firc-import-type">
				{m.firc_import_rule_type_label()}
			</label>
			<select id="firc-import-type" class="inp import-type-sel" bind:value={forcedType}>
				<option value="auto">{m.firc_import_rule_type_auto()}</option>
				{#each FIRC_RULE_TYPES as rt}
					<option value={rt.value} disabled={dnsOnly && isSubnetRuleType(rt.value)}>
						{rt.label}
					</option>
				{/each}
			</select>
		</div>

		<textarea
			class="inp font-mono"
			rows="10"
			placeholder={dnsOnly ? 'example.com\n*.openai.com' : 'example.com\n*.openai.com\n149.154.160.0/20'}
			bind:value={text}
		></textarea>

		{#if analysis.totalValid > 0}
			<div class="import-dedup-box">
				<div class="import-summary-chips">
					<span
						class="import-chip"
						class:chip-ok={selection.selected.length > 0}
						class:chip-muted={selection.selected.length === 0}
					>
						{m.firc_import_chip_to_add({ count: selection.selected.length })}
					</span>
					{#if analysis.unsupportedDnsOnlySubnetCount > 0}
						<span class="import-chip chip-warn">
							<AlertTriangle size={11} />
							{m.firc_import_chip_unsupported_subnet({ count: analysis.unsupportedDnsOnlySubnetCount })}
						</span>
					{/if}
					{#if dupInBatchOrGroup > 0}
						<span class="import-chip chip-warn">
							<AlertTriangle size={11} />
							{m.firc_import_chip_same_or_batch({ count: dupInBatchOrGroup })}
						</span>
					{/if}
					{#if upperShadowedEntriesCount > 0}
						<span class="import-chip chip-warn">
							<AlertTriangle size={11} />
							{m.firc_import_chip_upper_shadowed({ count: upperShadowedEntriesCount })}{#if analysis.shadowedGroupNames.length > 0}{' '}({analysis.shadowedGroupNames.join(', ')}){/if}
						</span>
					{/if}
					{#if otherNoticeCount > 0}
						<span class="import-chip chip-muted">
							{m.firc_import_chip_other_groups({ count: otherNoticeCount })}{#if analysis.otherNoticeGroupNames.length > 0}{' '}({analysis.otherNoticeGroupNames.join(', ')}){/if}
						</span>
					{/if}
				</div>

				{#if dupInBatchOrGroup > 0 || upperShadowedEntriesCount > 0}
					<div class="import-dedup-checks">
						{#if dupInBatchOrGroup > 0}
							<label class="import-check-row">
								<input type="checkbox" bind:checked={skipBatchAndSameGroup} />
								<span>{m.firc_import_skip_same_label()}</span>
							</label>
						{/if}
						{#if upperShadowedEntriesCount > 0}
							<label class="import-check-row">
								<input type="checkbox" bind:checked={skipOtherGroupShadowed} />
								<span>
									{m.firc_import_skip_upper_label({
										groups: analysis.shadowedGroupNames.join(', '),
									})}
								</span>
							</label>
						{/if}
					</div>
				{/if}

				{#if conflictEntries.length > 0}
					<button
						type="button"
						class="import-conflicts-toggle"
						onclick={() => (showConflicts = !showConflicts)}
					>
						{#if showConflicts}
							<ChevronUp size={13} />
							<span>{m.firc_import_hide_conflicts({ count: conflictEntries.length })}</span>
						{:else}
							<ChevronDown size={13} />
							<span>{m.firc_import_show_conflicts({ count: conflictEntries.length })}</span>
						{/if}
					</button>

					{#if showConflicts}
						<div class="import-conflicts-list">
							{#each conflictEntries as entry (`${entry.lineNumber}:${entry.key}`)}
								<div class="import-conflict-row">
									<span class="import-conflict-line font-mono">#{entry.lineNumber}</span>
									<span class="import-conflict-type">{entry.type}</span>
									<span class="import-conflict-rule font-mono">{entry.rule}</span>
									<span
										class="import-conflict-status"
										class:is-warn={entry.status === 'other_group_shadowed' ||
											entry.status === 'same_group_duplicate' ||
											entry.status === 'batch_duplicate' ||
											entry.status === 'unsupported_dns_only_subnet' ||
											Boolean(entry.shadowedByUpper)}
									>
										{formatEntryStatus(entry)}
									</span>
								</div>
							{/each}
						</div>
					{/if}
				{/if}
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={onclose}>
				{m.common_cancel()}
			</Button>
			<Button
				variant="primary"
				size="sm"
				disabled={analysis.totalValid > 0 && selection.selected.length === 0}
				onclick={onsubmit}
			>
				{analysis.totalValid > 0
					? m.firc_import_rules_btn_count({ count: selection.selected.length })
					: m.firc_groups_import_rules_btn()}
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.import-target-name {
		margin: 0;
		font-size: 12.5px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.import-type-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.import-type-label {
		font-size: 12px;
		color: var(--text-secondary);
		font-weight: 500;
	}

	.import-type-sel {
		min-width: 220px;
	}

	.import-dedup-box {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
	}

	.import-summary-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.import-chip {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-size: 11.5px;
		font-weight: 600;
		border: 1px solid var(--border);
	}

	.import-chip.chip-ok {
		color: var(--color-success);
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
	}

	.import-chip.chip-warn {
		color: var(--color-warning);
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.import-chip.chip-muted {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
		border-color: var(--border);
	}

	.import-dedup-checks {
		display: flex;
		flex-direction: column;
		gap: 5px;
	}

	.import-check-row {
		display: flex;
		align-items: flex-start;
		gap: 7px;
		font-size: 12px;
		color: var(--text-secondary);
		cursor: pointer;
		line-height: 1.35;
	}

	.import-check-row input {
		margin-top: 2px;
		accent-color: var(--color-accent);
		cursor: pointer;
		flex-shrink: 0;
	}

	.import-conflicts-toggle {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--accent);
		font-size: 11.5px;
		font-weight: 600;
		font-family: inherit;
		padding: 0;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		cursor: pointer;
		align-self: flex-start;
	}

	.import-conflicts-toggle:hover {
		text-decoration: underline;
	}

	.import-conflicts-list {
		display: flex;
		flex-direction: column;
		gap: 3px;
		max-height: 170px;
		overflow-y: auto;
		padding-right: 2px;
	}

	.import-conflict-row {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 3px 6px;
		border-radius: 4px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		font-size: 11.5px;
		min-width: 0;
	}

	.import-conflict-line {
		color: var(--text-muted);
		font-size: 10.5px;
		flex-shrink: 0;
	}

	.import-conflict-type {
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		padding: 1px 4px;
		border-radius: 3px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		flex-shrink: 0;
	}

	.import-conflict-rule {
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		flex: 1 1 auto;
		min-width: 0;
	}

	.import-conflict-status {
		color: var(--text-muted);
		font-size: 11px;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.import-conflict-status.is-warn {
		color: var(--color-warning);
		font-weight: 500;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}
</style>
