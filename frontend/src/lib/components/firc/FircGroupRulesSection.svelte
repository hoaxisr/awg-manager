<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Badge, Toggle } from '$lib/components/ui';
	import { Plus, Trash2, RefreshCw, FilePen } from 'lucide-svelte';
	import { FIRC_RULE_TYPES, type FircGroup, type FircListRule } from '$lib/types/firc';
	import type { BulkRuleAction } from '$lib/utils/fircBulkRules';
	import { PAGE_SIZE, type GroupListPageState } from '$lib/utils/fircGroups';
	import FircBulkRulesBar from './FircBulkRulesBar.svelte';

	interface Props {
		group: FircGroup;
		otherGroups: { id: string; name: string }[];
		currentPage: number;
		listPage: GroupListPageState | undefined;
		syncing: boolean;
		selectedRuleSet: Set<string>;
		onsetPage: (page: number) => void;
		onsync: () => void;
		onsort: () => void;
		onselectAllRules: () => void;
		onclearRules: () => void;
		ontoggleRuleSelected: (ruleId: string) => void;
		onbulk: (action: BulkRuleAction) => void;
		onaddRule: () => void;
		ondeleteRule: (ruleIndex: number) => void;
		onopenBulkEditor: () => void;
		onloadListPage: (offset: number) => void;
		ontoggleListRule: (rule: FircListRule, checked: boolean) => void;
	}

	let {
		group = $bindable(),
		otherGroups,
		currentPage,
		listPage,
		syncing,
		selectedRuleSet,
		onsetPage,
		onsync,
		onsort,
		onselectAllRules,
		onclearRules,
		ontoggleRuleSelected,
		onbulk,
		onaddRule,
		ondeleteRule,
		onopenBulkEditor,
		onloadListPage,
		ontoggleListRule,
	}: Props = $props();

	let totalRulePages = $derived(Math.max(1, Math.ceil(group.rules.length / PAGE_SIZE)));
	let pagedRules = $derived(
		group.rules.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE),
	);
</script>

<!-- Expanded Rules Section -->
<div class="group-body">
	<div class="rules-sec-head">
		<div class="rules-sec-title">
			<span>{m.firc_groups_name_label()}</span>
			<input
				type="text"
				class="inp group-name-inp-expanded"
				placeholder={m.firc_groups_name_placeholder()}
				bind:value={group.name}
			/>
			<span>· {m.firc_groups_own_rules_count({ count: group.rules.length })}</span>
			{#if group.rules.length > 1}
				<Button variant="ghost" size="sm" onclick={onsort}>
					{m.firc_groups_sort_az()}
				</Button>
			{/if}
			{#if group.rules.length > 0}
				<Button
					variant="ghost"
					size="sm"
					onclick={() =>
						selectedRuleSet.size === group.rules.length ? onclearRules() : onselectAllRules()}
				>
					{selectedRuleSet.size === group.rules.length
						? m.firc_bulk_deselect_all()
						: m.firc_bulk_select_all({ count: group.rules.length })}
				</Button>
			{/if}
		</div>
		<div class="rules-sec-actions">
			<Button variant="ghost" size="sm" onclick={onopenBulkEditor}>
				{#snippet iconBefore()}<FilePen size={13} />{/snippet}
				{m.firc_bulk_rules_edit_list_btn()}
			</Button>
			<Button variant="secondary" size="sm" onclick={onaddRule}>
				{#snippet iconBefore()}<Plus size={13} />{/snippet}
				{m.firc_groups_add_rule()}
			</Button>
		</div>
	</div>

	{#if selectedRuleSet.size > 0}
		<FircBulkRulesBar
			selectedCount={selectedRuleSet.size}
			totalCount={group.rules.length}
			{otherGroups}
			onSelectAll={onselectAllRules}
			onDeselectAll={onclearRules}
			onEnable={() => onbulk({ kind: 'enable', enable: true })}
			onDisable={() => onbulk({ kind: 'enable', enable: false })}
			onChangeType={(type) => onbulk({ kind: 'setType', type })}
			onMoveToGroup={(targetGroupId) => onbulk({ kind: 'move', targetGroupId })}
			onDelete={() => onbulk({ kind: 'delete' })}
		/>
	{/if}

	{#if group.rules.length === 0}
		<div class="rules-empty">
			{m.firc_groups_rules_empty()}
		</div>
	{:else}
		<div class="rules-table">
			{#each pagedRules as rule, rIdx (rule.id)}
				{@const absIdx = (currentPage - 1) * PAGE_SIZE + rIdx}
				<div
					class="rule-row"
					class:rule-disabled={!rule.enable}
					class:rule-selected={selectedRuleSet.has(rule.id)}
				>
					<input
						type="checkbox"
						class="bulk-check"
						checked={selectedRuleSet.has(rule.id)}
						aria-label={m.firc_bulk_rules_select_aria({
							rule: rule.rule || String(absIdx + 1),
						})}
						onchange={() => ontoggleRuleSelected(rule.id)}
					/>
					<span class="rule-idx font-mono">#{absIdx + 1}</span>
					<select class="inp rule-type-sel" bind:value={rule.type}>
						{#each FIRC_RULE_TYPES as rt}
							<option value={rt.value}>{rt.label}</option>
						{/each}
					</select>
					<input
						type="text"
						class="inp rule-pattern-inp font-mono"
						placeholder={m.firc_groups_rule_placeholder()}
						bind:value={rule.rule}
					/>
					{#if rule.type === 'subnet' || rule.type === 'subnet6'}
						<select
							class="inp rule-proto-sel"
							value={rule.proto ?? ''}
							onchange={(e) => {
								const v = (e.currentTarget as HTMLSelectElement).value;
								rule.proto = v ? (v as 'tcp' | 'udp') : undefined;
								if (!v) rule.ports = undefined;
							}}
						>
							<option value="">{m.firc_groups_proto_any()}</option>
							<option value="tcp">TCP</option>
							<option value="udp">UDP</option>
						</select>
						{#if rule.proto}
							<input
								type="text"
								class="inp rule-ports-inp font-mono"
								placeholder={m.firc_groups_ports_placeholder()}
								bind:value={rule.ports}
							/>
						{/if}
					{/if}
					<input
						type="text"
						class="inp rule-comment-inp"
						placeholder={m.firc_groups_comment_placeholder()}
						bind:value={rule.comment}
					/>
					{#if rule.autolist}
						<Badge
							variant="info"
							size="xs"
							title={rule.autolistByDomain
								? m.firc_groups_autolist_domain_tip()
								: m.firc_groups_autolist_tip()}
						>
							{m.firc_groups_autolist_badge()}
						</Badge>
					{/if}
					<Toggle
						checked={rule.enable}
						onchange={(v) => (rule.enable = v)}
						ariaLabel={m.firc_groups_toggle_rule_aria()}
					/>
					<button
						type="button"
						class="route-action-btn danger"
						title={m.firc_groups_delete_rule_tip()}
						onclick={() => ondeleteRule(absIdx)}
					>
						<Trash2 size={14} />
					</button>
				</div>
			{/each}
		</div>

		{#if totalRulePages > 1}
			<div class="pagination-bar">
				<Button
					variant="ghost"
					size="sm"
					disabled={currentPage <= 1}
					onclick={() => onsetPage(currentPage - 1)}
				>
					{m.common_back()}
				</Button>
				<span class="page-info">
					{m.firc_groups_page_info({ page: currentPage, total: totalRulePages })}
				</span>
				<Button
					variant="ghost"
					size="sm"
					disabled={currentPage >= totalRulePages}
					onclick={() => onsetPage(currentPage + 1)}
				>
					{m.firc_groups_page_next()}
				</Button>
			</div>
		{/if}
	{/if}

	{#if group.list}
		<div class="rules-sec-head mt-3">
			<div class="rules-sec-title">
				<span>
					{m.firc_groups_list_rules_title({ count: listPage?.matched ?? group.list.rulesTotal ?? '' })}
				</span>
			</div>
			<Button
				variant="ghost"
				size="sm"
				disabled={syncing}
				onclick={onsync}
			>
				{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
				{m.firc_groups_sync_list()}
			</Button>
		</div>

		{#if listPage?.loading}
			<div class="rules-empty">{m.firc_groups_list_loading()}</div>
		{:else if !listPage || listPage.rules.length === 0}
			<div class="rules-empty">
				{m.firc_groups_list_empty()}
			</div>
		{:else}
			{@const listPageNum = Math.floor(listPage.offset / PAGE_SIZE) + 1}
			{@const listTotalPages = Math.max(
				1,
				Math.ceil(listPage.matched / PAGE_SIZE),
			)}
			<div class="rules-table">
				{#each listPage.rules as lrule, lIdx (lrule.id)}
					<div class="rule-row list-rule-row" class:rule-disabled={!lrule.enable}>
						<span class="rule-idx font-mono">
							#{listPage.offset + lIdx + 1}
						</span>
						<Badge variant="muted" size="sm" mono>{lrule.type}</Badge>
						<span class="list-rule-pattern font-mono">{lrule.rule}</span>
						<Toggle
							checked={lrule.enable}
							onchange={(v) => ontoggleListRule(lrule, v)}
							ariaLabel={m.firc_groups_toggle_list_rule_aria()}
						/>
					</div>
				{/each}
			</div>

			{#if listTotalPages > 1}
				<div class="pagination-bar">
					<Button
						variant="ghost"
						size="sm"
						disabled={listPageNum <= 1}
						onclick={() => onloadListPage((listPageNum - 2) * PAGE_SIZE)}
					>
						{m.common_back()}
					</Button>
					<span class="page-info">
						{m.firc_groups_page_info({ page: listPageNum, total: listTotalPages })}
					</span>
					<Button
						variant="ghost"
						size="sm"
						disabled={listPageNum >= listTotalPages}
						onclick={() => onloadListPage(listPageNum * PAGE_SIZE)}
					>
						{m.firc_groups_page_next()}
					</Button>
				</div>
			{/if}
		{/if}
	{/if}
</div>

<style>
	.bulk-check {
		width: 15px;
		height: 15px;
		accent-color: var(--color-accent);
		cursor: pointer;
		flex-shrink: 0;
	}

	.group-name-inp-expanded {
		width: 180px;
		font-weight: 600;
	}

	.group-body {
		border-top: 1px solid var(--border);
		background: color-mix(in srgb, var(--bg-primary) 55%, var(--bg-secondary));
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.rules-sec-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 8px;
		min-width: 0;
	}

	.rules-sec-title {
		font-size: 11.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px 10px;
		min-width: 0;
		flex: 1 1 auto;
	}

	.rules-sec-title > span {
		white-space: nowrap;
		flex-shrink: 0;
	}

	.rules-empty {
		padding: 12px;
		font-size: 12px;
		color: var(--text-muted);
		text-align: center;
		border: 1px dashed var(--border);
		border-radius: var(--radius-sm);
	}

	.rules-table {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.rule-row {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 5px 8px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
	}

	.rule-row.rule-disabled {
		opacity: 0.55;
	}

	.rule-row.rule-selected {
		background: color-mix(in srgb, var(--color-accent-tint) 45%, var(--bg-secondary));
		border-color: var(--color-accent-border);
	}

	.rules-sec-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.rule-idx {
		font-size: 11px;
		color: var(--text-muted);
		min-width: 28px;
	}

	.rule-type-sel {
		width: 125px;
		flex-shrink: 0;
	}

	.rule-pattern-inp {
		flex: 2 1 180px;
		min-width: 120px;
	}

	.rule-proto-sel {
		width: 105px;
		flex-shrink: 0;
	}

	.rule-ports-inp {
		width: 120px;
		flex-shrink: 0;
	}

	.rule-comment-inp {
		flex: 1 1 120px;
		min-width: 90px;
	}

	.list-rule-row {
		justify-content: space-between;
	}

	.list-rule-pattern {
		flex: 1;
		font-size: 12.5px;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.pagination-bar {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 12px;
		padding-top: 6px;
	}

	.page-info {
		font-size: 12px;
		color: var(--text-muted);
	}

	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp.group-name-inp-expanded {
		width: 180px !important;
		max-width: 100%;
		flex: 0 1 180px;
		min-width: 120px;
		font-weight: 600;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	.mt-3 {
		margin-top: 12px;
	}

	@media (max-width: 768px) {
		.rule-row {
			flex-wrap: wrap;
		}
	}
</style>
