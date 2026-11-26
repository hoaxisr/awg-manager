<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Badge, Toggle } from '$lib/components/ui';
	import { Plus, Trash2, RefreshCw, FilePen, AlertTriangle } from 'lucide-svelte';
	import { FIRC_RULE_TYPES, type FircGroup, type FircListRule } from '$lib/types/firc';
	import type { BulkRuleAction } from '$lib/utils/fircBulkRules';
	import { PAGE_SIZE, type GroupListPageState } from '$lib/utils/fircGroups';
	import {
		fircListRuleOverlapId,
		fircOwnRuleOverlapId,
		formatFircOverlapTooltip,
		type FircRuleOverlap,
	} from '$lib/utils/fircOverlap';
	import { isDnsOnlyGroup, isSubnetRuleType } from '$lib/utils/fircResolve';
	import FircBulkRulesBar from './FircBulkRulesBar.svelte';

	interface Props {
		group: FircGroup;
		otherGroups: { id: string; name: string }[];
		currentPage: number;
		listPage: GroupListPageState | undefined;
		syncing: boolean;
		selectedRuleSet: Set<string>;
		ruleOverlaps?: ReadonlyMap<string, FircRuleOverlap>;
		shadowedOwnCount?: number;
		highlightedRuleKey?: string | null;
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
		onjumpToRule?: (groupId: string, ruleKey: string, rawRuleId: string, isListRule: boolean) => void;
		onremoveShadowed?: () => void;
	}

	let {
		group = $bindable(),
		otherGroups,
		currentPage,
		listPage,
		syncing,
		selectedRuleSet,
		ruleOverlaps = new Map(),
		shadowedOwnCount = 0,
		highlightedRuleKey = null,
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
		onjumpToRule,
		onremoveShadowed,
	}: Props = $props();

	let dnsOnly = $derived(isDnsOnlyGroup(group));
	let totalRulePages = $derived(Math.max(1, Math.ceil(group.rules.length / PAGE_SIZE)));
	let effectivePage = $derived(Math.min(Math.max(1, currentPage), totalRulePages));
	let pagedRules = $derived(
		group.rules.slice((effectivePage - 1) * PAGE_SIZE, effectivePage * PAGE_SIZE),
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
			{#if shadowedOwnCount > 0}
				<Button
					variant="ghost"
					size="sm"
					title={m.firc_dup_remove_shadowed_tip()}
					onclick={() => onremoveShadowed?.()}
				>
					{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
					{m.firc_dup_remove_shadowed_btn({ count: shadowedOwnCount })}
				</Button>
			{/if}
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

	{#if dnsOnly}
		<p class="dns-only-hint">{m.firc_dns_only_no_subnets()}</p>
	{/if}

	{#if group.rules.length === 0}
		<div class="rules-empty">
			{m.firc_groups_rules_empty()}
		</div>
	{:else}
		<div class="rules-table">
			{#each pagedRules as rule, rIdx (rule.id)}
				{@const absIdx = (effectivePage - 1) * PAGE_SIZE + rIdx}
				{@const ownOverlapKey = fircOwnRuleOverlapId(group.id, rule.id)}
				{@const ruleOverlap = ruleOverlaps.get(ownOverlapKey)}
				<div
					class="rule-row"
					class:rule-disabled={!rule.enable}
					class:rule-selected={selectedRuleSet.has(rule.id)}
					class:rule-shadowed={ruleOverlap?.kind === 'shadowed'}
					class:rule-highlighted={highlightedRuleKey === ownOverlapKey}
					data-firc-rule-key={ownOverlapKey}
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
							<option value={rt.value} disabled={dnsOnly && isSubnetRuleType(rt.value)}>{rt.label}</option>
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
					{#if ruleOverlap}
						{@const ruleDupTip = `${formatFircOverlapTooltip(ruleOverlap)}. ${m.firc_dup_click_to_jump_suffix()}`}
						<button
							type="button"
							class="rule-dup-indicator"
							class:is-split={ruleOverlap.kind === 'split'}
							data-overlap={ruleOverlap.kind}
							title={ruleDupTip}
							aria-label={ruleDupTip}
							onclick={() =>
								onjumpToRule?.(
									ruleOverlap.groupId,
									ruleOverlap.ruleId,
									ruleOverlap.rawRuleId,
									ruleOverlap.isTargetListRule,
								)}
						>
							<AlertTriangle size={14} />
						</button>
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
					disabled={effectivePage <= 1}
					onclick={() => onsetPage(effectivePage - 1)}
				>
					{m.common_back()}
				</Button>
				<span class="page-info">
					{m.firc_groups_page_info({ page: effectivePage, total: totalRulePages })}
				</span>
				<Button
					variant="ghost"
					size="sm"
					disabled={effectivePage >= totalRulePages}
					onclick={() => onsetPage(effectivePage + 1)}
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
					{@const listOverlapKey = fircListRuleOverlapId(group.id, lrule.id)}
					{@const listRuleOverlap = ruleOverlaps.get(listOverlapKey)}
					<div
						class="rule-row list-rule-row"
						class:rule-disabled={!lrule.enable}
						class:rule-shadowed={listRuleOverlap?.kind === 'shadowed'}
						class:rule-highlighted={highlightedRuleKey === listOverlapKey}
						data-firc-rule-key={listOverlapKey}
					>
						<span class="rule-idx font-mono">
							#{listPage.offset + lIdx + 1}
						</span>
						<Badge variant="muted" size="sm" mono>{lrule.type}</Badge>
						<span class="list-rule-pattern font-mono">{lrule.rule}</span>
						{#if listRuleOverlap}
							{@const listDupTip = `${formatFircOverlapTooltip(listRuleOverlap)}. ${m.firc_dup_click_to_jump_suffix()}`}
							<button
								type="button"
								class="rule-dup-indicator"
								class:is-split={listRuleOverlap.kind === 'split'}
								data-overlap={listRuleOverlap.kind}
								title={listDupTip}
								aria-label={listDupTip}
								onclick={() =>
									onjumpToRule?.(
										listRuleOverlap.groupId,
										listRuleOverlap.ruleId,
										listRuleOverlap.rawRuleId,
										listRuleOverlap.isTargetListRule,
									)}
							>
								<AlertTriangle size={14} />
							</button>
						{/if}
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
	.dns-only-hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
	}

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

	.rule-row.rule-shadowed {
		border-color: color-mix(in srgb, var(--color-warning) 35%, var(--border));
	}

	.rule-row.rule-highlighted {
		animation: fircRuleFlash 1.5s ease-out;
		border-color: var(--color-warning);
		box-shadow: 0 0 0 1px var(--color-warning-border);
	}

	@keyframes fircRuleFlash {
		0% {
			background: color-mix(in srgb, var(--color-warning) 26%, var(--bg-secondary));
		}
		100% {
			background: var(--bg-secondary);
		}
	}

	.rule-dup-indicator {
		appearance: none;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 24px;
		height: 24px;
		padding: 0;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		cursor: pointer;
		flex-shrink: 0;
		transition:
			filter 0.12s ease,
			border-color 0.12s ease,
			background-color 0.12s ease;
	}

	.rule-dup-indicator:hover {
		filter: brightness(1.15);
	}

	.rule-dup-indicator.is-split {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
		border-color: var(--border-hover, var(--border));
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
